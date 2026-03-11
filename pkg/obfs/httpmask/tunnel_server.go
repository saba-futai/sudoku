/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package httpmask

import (
	"bufio"
	"bytes"
	crand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

type TunnelServerOptions struct {
	Mode string
	// PathRoot is an optional first-level path prefix for all HTTP tunnel endpoints.
	// Example: "aabbcc" => "/aabbcc/session", "/aabbcc/api/v1/upload", ...
	PathRoot string
	// AuthKey enables short-term HMAC auth for HTTP tunnel requests (anti-probing).
	// When set (non-empty), the server requires each request to carry a valid Authorization bearer token.
	AuthKey string
	// AuthSkew controls allowed clock skew / replay window for AuthKey. 0 uses a conservative default.
	AuthSkew time.Duration
	// PassThroughOnReject controls how the server handles "recognized but rejected" tunnel requests
	// (e.g., wrong mode / wrong path / invalid token). When true, the request bytes are replayed back
	// to the caller as HandlePassThrough to allow higher-level fallback handling.
	PassThroughOnReject bool
	// PullReadTimeout controls how long the server long-poll waits for tunnel downlink data before replying.
	PullReadTimeout time.Duration
	// SessionTTL is a best-effort TTL to prevent leaked sessions. 0 uses a conservative default.
	SessionTTL time.Duration
	// EarlyHandshake optionally folds the protocol handshake into the initial HTTP/WS round trip.
	EarlyHandshake *TunnelServerEarlyHandshake
}

type TunnelServer struct {
	mode                TunnelMode
	pathRoot            string
	passThroughOnReject bool
	auth                *tunnelAuth

	pullReadTimeout time.Duration
	sessionTTL      time.Duration
	earlyHandshake  *TunnelServerEarlyHandshake

	mu       sync.Mutex
	sessions map[string]*tunnelSession
}

type tunnelSession struct {
	conn       net.Conn
	lastActive time.Time
}

func NewTunnelServer(opts TunnelServerOptions) *TunnelServer {
	mode := normalizeTunnelMode(opts.Mode)
	if mode == TunnelModeLegacy {
		// Server-side "legacy" means: don't accept stream/poll; only passthrough.
	}
	pathRoot := normalizePathRoot(opts.PathRoot)
	auth := newTunnelAuth(opts.AuthKey, opts.AuthSkew)
	timeout := opts.PullReadTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ttl := opts.SessionTTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &TunnelServer{
		mode:                mode,
		pathRoot:            pathRoot,
		passThroughOnReject: opts.PassThroughOnReject,
		auth:                auth,
		pullReadTimeout:     timeout,
		sessionTTL:          ttl,
		earlyHandshake:      opts.EarlyHandshake,
		sessions:            make(map[string]*tunnelSession),
	}
}

// HandleConn inspects rawConn. If it is an HTTP tunnel request (stream/poll), it is handled here and:
//   - returns HandleStartTunnel + a net.Conn that carries the raw Sudoku stream (stream or poll session pipe)
//   - or returns HandleDone if the HTTP request is a poll control request (push/pull) and no Sudoku handshake should run on this TCP conn
//
// If it is not an HTTP tunnel request (or server mode is legacy), it returns HandlePassThrough with a conn that replays any pre-read bytes.
func (s *TunnelServer) HandleConn(rawConn net.Conn) (HandleResult, net.Conn, error) {
	if rawConn == nil {
		return HandleDone, nil, errors.New("nil conn")
	}

	passThrough := func(prefix []byte) (HandleResult, net.Conn, error) {
		return HandlePassThrough, newPreBufferedConn(rawConn, prefix), nil
	}
	passThroughRejected := func(prefix []byte) (HandleResult, net.Conn, error) {
		return HandlePassThrough, newRejectedPreBufferedConn(rawConn, prefix), nil
	}
	rejectOr404 := func(prefix []byte) (HandleResult, net.Conn, error) {
		if s.passThroughOnReject {
			return passThroughRejected(prefix)
		}
		_ = writeSimpleHTTPResponse(rawConn, http.StatusNotFound, "not found")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	// Small header read deadline to avoid stalling Accept loops. The actual Sudoku handshake has its own deadlines.
	_ = rawConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var first [4]byte
	n, err := io.ReadFull(rawConn, first[:])
	if err != nil {
		_ = rawConn.SetReadDeadline(time.Time{})
		// Even if short-read, preserve bytes for downstream handlers.
		if n > 0 {
			return passThrough(first[:n])
		}
		return HandleDone, nil, err
	}
	pc := newPreBufferedConn(rawConn, first[:])
	br := bufio.NewReader(pc)

	if !LooksLikeHTTPRequestStart(first[:]) {
		_ = rawConn.SetReadDeadline(time.Time{})
		return HandlePassThrough, pc, nil
	}

	req, headerBytes, buffered, err := readHTTPHeader(br)
	_ = rawConn.SetReadDeadline(time.Time{})
	if err != nil {
		// Not a valid HTTP request; hand it back to the legacy path with replay.
		return passThrough(buildInvalidHTTPReplayPrefix(first[:], headerBytes, buffered))
	}

	replayPrefix := buildHTTPReplayPrefix(headerBytes, buffered)

	tunnelHeader := TunnelMode(strings.ToLower(strings.TrimSpace(req.headers["x-sudoku-tunnel"])))
	if looksLikeWebSocketUpgrade(req.headers) {
		tunnelHeader = TunnelModeWS
	}
	if tunnelHeader == "" {
		// Some CDNs / forward proxies may strip unknown headers. When AuthKey is enabled, we can
		// safely infer the intended tunnel mode by verifying the Authorization token against
		// both stream/poll modes and picking the one that matches.
		tunnelHeader = s.inferTunnelModeFromAuth(req)
		if tunnelHeader == "" {
			// Not our tunnel; replay full bytes to legacy handler.
			return passThrough(replayPrefix)
		}
	}

	if s.mode == TunnelModeLegacy {
		return rejectOr404(replayPrefix)
	}

	switch tunnelHeader {
	case TunnelModeStream:
		if s.mode != TunnelModeStream && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handleStream(rawConn, req, headerBytes, buffered)
	case TunnelModePoll:
		if s.mode != TunnelModePoll && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handlePoll(rawConn, req, headerBytes, buffered)
	case TunnelModeWS:
		if s.mode != TunnelModeWS && s.mode != TunnelModeAuto {
			return rejectOr404(replayPrefix)
		}
		return s.handleWS(rawConn, req, headerBytes, buffered)
	default:
		return rejectOr404(replayPrefix)
	}
}

func buildHTTPReplayPrefix(headerBytes, buffered []byte) []byte {
	out := make([]byte, 0, len(headerBytes)+len(buffered))
	out = append(out, headerBytes...)
	out = append(out, buffered...)
	return out
}

func buildInvalidHTTPReplayPrefix(first, headerBytes, buffered []byte) []byte {
	// readHTTPHeader may have consumed some bytes that don't include our initial 4-byte peek
	// (e.g. parse errors / short reads). Preserve a correct replay prefix for downstream handlers.
	out := make([]byte, 0, len(first)+len(headerBytes)+len(buffered))
	if len(headerBytes) == 0 || !bytes.HasPrefix(headerBytes, first) {
		out = append(out, first...)
	}
	out = append(out, headerBytes...)
	out = append(out, buffered...)
	return out
}

func (s *TunnelServer) inferTunnelModeFromAuth(req *httpRequestHeader) TunnelMode {
	if s == nil || s.auth == nil || req == nil {
		return ""
	}
	u, err := url.ParseRequestURI(req.target)
	if err != nil || u == nil {
		return ""
	}
	p, ok := stripPathRoot(s.pathRoot, u.Path)
	if !ok || !s.isAllowedBasePath(p) {
		return ""
	}

	authVal := req.headers["authorization"]
	if authVal == "" {
		authVal = u.Query().Get(tunnelAuthQueryKey)
	}
	now := time.Now()
	streamOK := s.auth.verifyValue(authVal, TunnelModeStream, req.method, p, now)
	pollOK := s.auth.verifyValue(authVal, TunnelModePoll, req.method, p, now)
	switch {
	case streamOK && !pollOK:
		return TunnelModeStream
	case pollOK && !streamOK:
		return TunnelModePoll
	default:
		return ""
	}
}

type httpRequestHeader struct {
	method  string
	target  string // path + query
	proto   string
	headers map[string]string // lower-case keys
}

func readHTTPHeader(r *bufio.Reader) (*httpRequestHeader, []byte, []byte, error) {
	const maxHeaderBytes = 32 * 1024

	var consumed bytes.Buffer
	readLine := func() ([]byte, error) {
		line, err := r.ReadSlice('\n')
		if len(line) > 0 {
			if consumed.Len()+len(line) > maxHeaderBytes {
				return line, fmt.Errorf("http header too large")
			}
			consumed.Write(line)
		}
		return line, err
	}

	// Request line
	line, err := readLine()
	if err != nil {
		return nil, consumed.Bytes(), readAllBuffered(r), err
	}
	lineStr := strings.TrimRight(string(line), "\r\n")
	parts := strings.SplitN(lineStr, " ", 3)
	if len(parts) != 3 {
		return nil, consumed.Bytes(), readAllBuffered(r), fmt.Errorf("invalid request line")
	}
	req := &httpRequestHeader{
		method:  parts[0],
		target:  parts[1],
		proto:   parts[2],
		headers: make(map[string]string),
	}

	// Headers
	for {
		line, err = readLine()
		if err != nil {
			return nil, consumed.Bytes(), readAllBuffered(r), err
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			break
		}
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		// Keep the first value; we only care about a small set.
		if _, exists := req.headers[k]; !exists {
			req.headers[k] = v
		}
	}

	return req, consumed.Bytes(), readAllBuffered(r), nil
}

func readAllBuffered(r *bufio.Reader) []byte {
	n := r.Buffered()
	if n <= 0 {
		return nil
	}
	b, err := r.Peek(n)
	if err != nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, b)
	return out
}

type preBufferedConn struct {
	net.Conn
	buf      []byte
	recorded []byte
	rejected bool
}

func (p *preBufferedConn) CloseWrite() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseWrite(p.Conn)
}

func (p *preBufferedConn) CloseRead() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseRead(p.Conn)
}

func newPreBufferedConn(conn net.Conn, pre []byte) *preBufferedConn {
	cpy := make([]byte, len(pre))
	copy(cpy, pre)
	return &preBufferedConn{Conn: conn, buf: cpy, recorded: cpy}
}

func newRejectedPreBufferedConn(conn net.Conn, pre []byte) *preBufferedConn {
	c := newPreBufferedConn(conn, pre)
	c.rejected = true
	return c
}

func (p *preBufferedConn) IsHTTPMaskRejected() bool { return p.rejected }

func (p *preBufferedConn) GetBufferedAndRecorded() []byte {
	if len(p.recorded) == 0 {
		return nil
	}
	out := make([]byte, len(p.recorded))
	copy(out, p.recorded)
	return out
}

func (p *preBufferedConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

type bodyConn struct {
	net.Conn
	reader io.Reader
	writer io.WriteCloser
	tail   io.Writer
	flush  func() error
}

func (c *bodyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *bodyConn) Write(p []byte) (int, error) {
	n, err := c.writer.Write(p)
	if c.flush != nil {
		_ = c.flush()
	}
	return n, err
}

func (c *bodyConn) CloseWrite() error {
	if c == nil {
		return nil
	}

	var firstErr error
	if c.writer != nil {
		if err := c.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// NewChunkedWriter does not write the final CRLF. Ensure a clean terminator.
		if c.tail != nil {
			_, _ = c.tail.Write([]byte("\r\n"))
		} else if c.Conn != nil {
			_, _ = c.Conn.Write([]byte("\r\n"))
		}
		if c.flush != nil {
			_ = c.flush()
		}
		c.writer = nil
	}

	if c.Conn != nil {
		if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
			if err := cw.CloseWrite(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (c *bodyConn) CloseRead() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

func (c *bodyConn) Close() error {
	var firstErr error
	if c.writer != nil {
		if err := c.writer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		// NewChunkedWriter does not write the final CRLF. Ensure a clean terminator.
		if c.tail != nil {
			_, _ = c.tail.Write([]byte("\r\n"))
		} else {
			_, _ = c.Conn.Write([]byte("\r\n"))
		}
		if c.flush != nil {
			_ = c.flush()
		}
	}
	if err := c.Conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *TunnelServer) rejectOrReply(rawConn net.Conn, headerBytes, buffered []byte, code int, body string) (HandleResult, net.Conn, error) {
	if s.passThroughOnReject {
		prefix := make([]byte, 0, len(headerBytes)+len(buffered))
		prefix = append(prefix, headerBytes...)
		prefix = append(prefix, buffered...)
		return HandlePassThrough, newRejectedPreBufferedConn(rawConn, prefix), nil
	}
	_ = writeSimpleHTTPResponse(rawConn, code, body)
	_ = rawConn.Close()
	return HandleDone, nil, nil
}

func (s *TunnelServer) handleStream(rawConn net.Conn, req *httpRequestHeader, headerBytes []byte, buffered []byte) (HandleResult, net.Conn, error) {
	u, err := url.ParseRequestURI(req.target)
	if err != nil {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}

	// Only accept plausible paths to reduce accidental exposure.
	path, ok := stripPathRoot(s.pathRoot, u.Path)
	if !ok || !s.isAllowedBasePath(path) {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}
	authVal := req.headers["authorization"]
	if authVal == "" {
		authVal = u.Query().Get(tunnelAuthQueryKey)
	}
	if !s.auth.verifyValue(authVal, TunnelModeStream, req.method, path, time.Now()) {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}

	token := u.Query().Get("token")
	closeFlag := u.Query().Get("close") == "1"
	finFlag := u.Query().Get("fin") == "1"

	switch strings.ToUpper(req.method) {
	case http.MethodGet:
		// Stream Split Session: GET /session (no token) => token + start tunnel on a server-side pipe.
		if token == "" && path == "/session" {
			earlyPayload, err := parseEarlyDataQuery(u)
			if err != nil {
				return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
			}
			return s.sessionAuthorize(rawConn, earlyPayload)
		}
		// Stream Split Session: GET /stream?token=... => downlink poll.
		if token != "" && path == "/stream" {
			if s.passThroughOnReject && !s.sessionHas(token) {
				return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
			}
			return s.streamPull(rawConn, token)
		}
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")

	case http.MethodPost:
		// Stream Split Session: POST /api/v1/upload?token=... => uplink push.
		if token != "" && path == "/api/v1/upload" {
			if s.passThroughOnReject && !s.sessionHas(token) {
				return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
			}
			if closeFlag {
				s.sessionClose(token)
				_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
				_ = rawConn.Close()
				return HandleDone, nil, nil
			}
			if finFlag {
				s.sessionCloseWrite(token)
				_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
				_ = rawConn.Close()
				return HandleDone, nil, nil
			}
			bodyReader, err := newRequestBodyReader(newPreBufferedConn(rawConn, buffered), req.headers)
			if err != nil {
				_ = writeSimpleHTTPResponse(rawConn, http.StatusBadRequest, "bad request")
				_ = rawConn.Close()
				return HandleDone, nil, nil
			}
			return s.streamPush(rawConn, token, bodyReader)
		}

		// Stream-One: single full-duplex POST.
		if err := writeTunnelResponseHeader(rawConn); err != nil {
			_ = rawConn.Close()
			return HandleDone, nil, err
		}

		bodyReader, err := newRequestBodyReader(newPreBufferedConn(rawConn, buffered), req.headers)
		if err != nil {
			_ = rawConn.Close()
			return HandleDone, nil, err
		}

		bw := bufio.NewWriterSize(rawConn, 32*1024)
		chunked := httputil.NewChunkedWriter(bw)
		stream := &bodyConn{
			Conn:   rawConn,
			reader: bodyReader,
			writer: chunked,
			tail:   bw,
			flush:  bw.Flush,
		}
		return HandleStartTunnel, stream, nil

	default:
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}
}

func (s *TunnelServer) isAllowedBasePath(path string) bool {
	for _, p := range paths {
		if path == p {
			return true
		}
	}
	return false
}

func newRequestBodyReader(conn net.Conn, headers map[string]string) (io.Reader, error) {
	br := bufio.NewReaderSize(conn, 32*1024)

	te := strings.ToLower(headers["transfer-encoding"])
	if strings.Contains(te, "chunked") {
		return httputil.NewChunkedReader(br), nil
	}
	if clStr := headers["content-length"]; clStr != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(clStr), 10, 64)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid content-length")
		}
		return io.LimitReader(br, n), nil
	}
	return br, nil
}

func writeTunnelResponseHeader(w io.Writer) error {
	_, err := io.WriteString(w,
		"HTTP/1.1 200 OK\r\n"+
			"Content-Type: application/octet-stream\r\n"+
			"Transfer-Encoding: chunked\r\n"+
			"Cache-Control: no-store\r\n"+
			"Pragma: no-cache\r\n"+
			"Connection: keep-alive\r\n"+
			"X-Accel-Buffering: no\r\n"+
			"\r\n")
	return err
}

func writeSimpleHTTPResponse(w io.Writer, code int, body string) error {
	if body == "" {
		body = http.StatusText(code)
	}
	body = strings.TrimRight(body, "\r\n")
	_, err := io.WriteString(w,
		fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			code, http.StatusText(code), len(body), body))
	return err
}

func writeTokenHTTPResponse(w io.Writer, token string, earlyPayload []byte) error {
	token = strings.TrimRight(token, "\r\n")
	body := "token=" + token
	if len(earlyPayload) > 0 {
		body += "\ned=" + base64.RawURLEncoding.EncodeToString(earlyPayload)
	}
	// Use application/octet-stream to avoid CDN auto-compression (e.g. brotli) breaking clients that expect a plain token string.
	_, err := io.WriteString(w,
		fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nCache-Control: no-store\r\nPragma: no-cache\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			len(body), body))
	return err
}

func (s *TunnelServer) handlePoll(rawConn net.Conn, req *httpRequestHeader, headerBytes []byte, buffered []byte) (HandleResult, net.Conn, error) {
	u, err := url.ParseRequestURI(req.target)
	if err != nil {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}

	path, ok := stripPathRoot(s.pathRoot, u.Path)
	if !ok || !s.isAllowedBasePath(path) {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}
	authVal := req.headers["authorization"]
	if authVal == "" {
		authVal = u.Query().Get(tunnelAuthQueryKey)
	}
	if !s.auth.verifyValue(authVal, TunnelModePoll, req.method, path, time.Now()) {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}

	token := u.Query().Get("token")
	closeFlag := u.Query().Get("close") == "1"
	finFlag := u.Query().Get("fin") == "1"
	switch strings.ToUpper(req.method) {
	case http.MethodGet:
		if token == "" && path == "/session" {
			earlyPayload, err := parseEarlyDataQuery(u)
			if err != nil {
				return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
			}
			return s.sessionAuthorize(rawConn, earlyPayload)
		}
		if path == "/stream" {
			if s.passThroughOnReject && !s.sessionHas(token) {
				return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
			}
			return s.pollPull(rawConn, token)
		}
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	case http.MethodPost:
		if path != "/api/v1/upload" {
			return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
		}
		if token == "" {
			return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "missing token")
		}
		if s.passThroughOnReject && !s.sessionHas(token) {
			return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
		}
		if closeFlag {
			s.sessionClose(token)
			_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
			_ = rawConn.Close()
			return HandleDone, nil, nil
		}
		if finFlag {
			s.sessionCloseWrite(token)
			_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
			_ = rawConn.Close()
			return HandleDone, nil, nil
		}
		bodyReader, err := newRequestBodyReader(newPreBufferedConn(rawConn, buffered), req.headers)
		if err != nil {
			return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
		}
		return s.pollPush(rawConn, token, bodyReader)
	default:
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}
}

func (s *TunnelServer) sessionAuthorize(rawConn net.Conn, earlyPayload []byte) (HandleResult, net.Conn, error) {
	token, err := newSessionToken()
	if err != nil {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusInternalServerError, "internal error")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	c1, c2 := newHalfPipe()
	outConn := net.Conn(c1)
	var responsePayload []byte
	var userHash string
	if len(earlyPayload) > 0 && s.earlyHandshake != nil && s.earlyHandshake.Prepare != nil {
		prepared, err := s.earlyHandshake.Prepare(earlyPayload)
		if err != nil {
			_ = c1.Close()
			_ = c2.Close()
			return s.rejectOrReply(rawConn, nil, nil, http.StatusNotFound, "not found")
		}
		responsePayload = prepared.ResponsePayload
		userHash = prepared.UserHash
		if prepared.WrapConn != nil {
			wrapped, err := prepared.WrapConn(c1)
			if err != nil {
				_ = c1.Close()
				_ = c2.Close()
				_ = writeSimpleHTTPResponse(rawConn, http.StatusInternalServerError, "internal error")
				_ = rawConn.Close()
				return HandleDone, nil, nil
			}
			if wrapped != nil {
				outConn = wrapEarlyHandshakeConn(wrapped, userHash)
			}
		}
	}

	s.mu.Lock()
	s.sessions[token] = &tunnelSession{conn: c2, lastActive: time.Now()}
	s.mu.Unlock()

	go s.reapLater(token)

	_ = writeTokenHTTPResponse(rawConn, token, responsePayload)
	_ = rawConn.Close()
	return HandleStartTunnel, outConn, nil
}

func newSessionToken() (string, error) {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func (s *TunnelServer) reapLater(token string) {
	ttl := s.sessionTTL
	if ttl <= 0 {
		return
	}

	timer := time.NewTimer(ttl)
	defer timer.Stop()

	for {
		<-timer.C

		s.mu.Lock()
		sess, ok := s.sessions[token]
		if !ok {
			s.mu.Unlock()
			return
		}
		idle := time.Since(sess.lastActive)
		if idle >= ttl {
			delete(s.sessions, token)
			s.mu.Unlock()
			_ = sess.conn.Close()
			return
		}
		next := ttl - idle
		s.mu.Unlock()

		// Avoid a tight loop under high-frequency activity; we only need best-effort cleanup.
		if next < 50*time.Millisecond {
			next = 50 * time.Millisecond
		}
		timer.Reset(next)
	}
}

func (s *TunnelServer) sessionHas(token string) bool {
	s.mu.Lock()
	_, ok := s.sessions[token]
	s.mu.Unlock()
	return ok
}

func (s *TunnelServer) sessionGet(token string) (*tunnelSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil, false
	}
	sess.lastActive = time.Now()
	return sess, true
}

func (s *TunnelServer) sessionClose(token string) {
	s.mu.Lock()
	sess, ok := s.sessions[token]
	if ok {
		delete(s.sessions, token)
	}
	s.mu.Unlock()
	if ok {
		_ = sess.conn.Close()
	}
}

func (s *TunnelServer) sessionCloseWrite(token string) {
	sess, ok := s.sessionGet(token)
	if !ok || sess == nil || sess.conn == nil {
		return
	}
	if cw, ok := sess.conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = sess.conn.Close()
}

func (s *TunnelServer) pollPush(rawConn net.Conn, token string, body io.Reader) (HandleResult, net.Conn, error) {
	sess, ok := s.sessionGet(token)
	if !ok {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusForbidden, "forbidden")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	payload, err := io.ReadAll(io.LimitReader(body, 1<<20)) // 1MiB per request cap
	if err != nil {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusBadRequest, "bad request")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	lines := bytes.Split(payload, []byte{'\n'})
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		decoded := make([]byte, base64.StdEncoding.DecodedLen(len(line)))
		n, decErr := base64.StdEncoding.Decode(decoded, line)
		if decErr != nil {
			_ = writeSimpleHTTPResponse(rawConn, http.StatusBadRequest, "bad request")
			_ = rawConn.Close()
			return HandleDone, nil, nil
		}
		if n == 0 {
			continue
		}
		_ = sess.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		_, werr := sess.conn.Write(decoded[:n])
		_ = sess.conn.SetWriteDeadline(time.Time{})
		if werr != nil {
			s.sessionClose(token)
			_ = writeSimpleHTTPResponse(rawConn, http.StatusGone, "gone")
			_ = rawConn.Close()
			return HandleDone, nil, nil
		}
	}

	_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
	_ = rawConn.Close()
	return HandleDone, nil, nil
}

func (s *TunnelServer) streamPush(rawConn net.Conn, token string, body io.Reader) (HandleResult, net.Conn, error) {
	sess, ok := s.sessionGet(token)
	if !ok {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusForbidden, "forbidden")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	const maxUploadBytes = 1 << 20
	payload, err := io.ReadAll(io.LimitReader(body, maxUploadBytes+1))
	if err != nil {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusBadRequest, "bad request")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}
	if len(payload) > maxUploadBytes {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusRequestEntityTooLarge, "too large")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	if len(payload) > 0 {
		_ = sess.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
		werr := connutil.WriteFull(sess.conn, payload)
		_ = sess.conn.SetWriteDeadline(time.Time{})
		if werr != nil {
			s.sessionClose(token)
			_ = writeSimpleHTTPResponse(rawConn, http.StatusGone, "gone")
			_ = rawConn.Close()
			return HandleDone, nil, nil
		}
	}

	_ = writeSimpleHTTPResponse(rawConn, http.StatusOK, "")
	_ = rawConn.Close()
	return HandleDone, nil, nil
}

func (s *TunnelServer) streamPull(rawConn net.Conn, token string) (HandleResult, net.Conn, error) {
	return s.sessionPull(rawConn, token, false, func(w io.Writer, p []byte) error {
		return connutil.WriteFull(w, p)
	})
}

func (s *TunnelServer) pollPull(rawConn net.Conn, token string) (HandleResult, net.Conn, error) {
	enc := make([]byte, base64.StdEncoding.EncodedLen(32*1024))
	return s.sessionPull(rawConn, token, true, func(w io.Writer, p []byte) error {
		if cap(enc) < base64.StdEncoding.EncodedLen(len(p)) {
			enc = make([]byte, base64.StdEncoding.EncodedLen(len(p)))
		}
		line := enc[:base64.StdEncoding.EncodedLen(len(p))]
		base64.StdEncoding.Encode(line, p)
		if err := connutil.WriteFull(w, line); err != nil {
			return err
		}
		return connutil.WriteFull(w, []byte{'\n'})
	})
}

func (s *TunnelServer) sessionPull(rawConn net.Conn, token string, keepalive bool, writePayload func(io.Writer, []byte) error) (HandleResult, net.Conn, error) {
	sess, ok := s.sessionGet(token)
	if !ok {
		_ = writeSimpleHTTPResponse(rawConn, http.StatusForbidden, "forbidden")
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}

	if err := writeTunnelResponseHeader(rawConn); err != nil {
		_ = rawConn.Close()
		return HandleDone, nil, err
	}

	bw := bufio.NewWriterSize(rawConn, 32*1024)
	cw := httputil.NewChunkedWriter(bw)
	defer func() {
		_ = cw.Close()
		_, _ = bw.WriteString("\r\n")
		_ = bw.Flush()
		_ = rawConn.Close()
	}()

	buf := make([]byte, 32*1024)
	for {
		_ = sess.conn.SetReadDeadline(time.Now().Add(s.pullReadTimeout))
		n, err := sess.conn.Read(buf)
		if n > 0 {
			_ = writePayload(cw, buf[:n])
			_ = bw.Flush()
		}
		if err == nil {
			continue
		}

		if errors.Is(err, os.ErrDeadlineExceeded) {
			if keepalive {
				_, _ = cw.Write([]byte("\n"))
				_ = bw.Flush()
			}
			return HandleDone, nil, nil
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
			return HandleDone, nil, nil
		}
		s.sessionClose(token)
		return HandleDone, nil, nil
	}
}
