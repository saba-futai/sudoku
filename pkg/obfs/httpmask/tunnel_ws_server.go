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
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

func looksLikeWebSocketUpgrade(headers map[string]string) bool {
	if headers == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(headers["upgrade"]), "websocket") {
		return false
	}
	conn := headers["connection"]
	for _, part := range strings.Split(conn, ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}

type hijackRW struct {
	conn  net.Conn
	hdr   http.Header
	wrote bool
}

func (w *hijackRW) Header() http.Header {
	if w.hdr == nil {
		w.hdr = make(http.Header)
	}
	return w.hdr
}

func (w *hijackRW) WriteHeader(statusCode int) {
	if w == nil || w.conn == nil || w.wrote {
		return
	}
	w.wrote = true

	statusText := http.StatusText(statusCode)
	if statusText == "" {
		statusText = "status"
	}
	_, _ = fmt.Fprintf(w.conn, "HTTP/1.1 %d %s\r\n", statusCode, statusText)
	for k, vs := range w.Header() {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		for _, v := range vs {
			_, _ = fmt.Fprintf(w.conn, "%s: %s\r\n", ck, v)
		}
	}
	_, _ = w.conn.Write([]byte("\r\n"))
}

func (w *hijackRW) Write(p []byte) (int, error) {
	if w == nil || w.conn == nil {
		return 0, fmt.Errorf("nil conn")
	}
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.conn.Write(p)
}

func (w *hijackRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if w == nil || w.conn == nil {
		return nil, nil, fmt.Errorf("nil conn")
	}
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func (w *hijackRW) Flush() {}

func buildHTTPRequestFromHeaderBytes(headerBytes []byte, rawConn net.Conn) (*http.Request, error) {
	r, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(headerBytes)))
	if err != nil {
		return nil, err
	}
	if rawConn != nil {
		r.RemoteAddr = rawConn.RemoteAddr().String()
	}
	// Ensure canonical header keys for downstream lookups.
	if r.Header != nil {
		canon := make(http.Header, len(r.Header))
		for k, vs := range r.Header {
			ck := textproto.CanonicalMIMEHeaderKey(k)
			canon[ck] = vs
		}
		r.Header = canon
	}
	return r, nil
}

func (s *TunnelServer) handleWS(rawConn net.Conn, req *httpRequestHeader, headerBytes []byte, buffered []byte) (HandleResult, net.Conn, error) {
	u, err := url.ParseRequestURI(req.target)
	if err != nil {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}

	path, ok := stripPathRoot(s.pathRoot, u.Path)
	if !ok || path != "/ws" {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}
	if strings.ToUpper(strings.TrimSpace(req.method)) != http.MethodGet {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}

	authVal := req.headers["authorization"]
	if authVal == "" {
		authVal = u.Query().Get(tunnelAuthQueryKey)
	}
	if !s.auth.verifyValue(authVal, TunnelModeWS, req.method, path, time.Now()) {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
	}

	earlyPayload, err := parseEarlyDataQuery(u)
	if err != nil {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}
	var prepared *PreparedServerEarlyHandshake
	if len(earlyPayload) > 0 && s.earlyHandshake != nil && s.earlyHandshake.Prepare != nil {
		prepared, err = s.earlyHandshake.Prepare(earlyPayload)
		if err != nil {
			return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusNotFound, "not found")
		}
	}

	// Preserve any bytes read beyond the HTTP header as initial WebSocket stream bytes.
	wsConnRaw := newPreBufferedConn(rawConn, buffered)
	r, err := buildHTTPRequestFromHeaderBytes(headerBytes, rawConn)
	if err != nil {
		return s.rejectOrReply(rawConn, headerBytes, buffered, http.StatusBadRequest, "bad request")
	}

	rw := &hijackRW{conn: wsConnRaw}
	if prepared != nil && len(prepared.ResponsePayload) > 0 {
		rw.Header().Set(tunnelEarlyDataHeader, base64.RawURLEncoding.EncodeToString(prepared.ResponsePayload))
	}
	c, err := websocket.Accept(rw, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept already wrote a response (or the client is not a WS handshake).
		_ = rawConn.Close()
		return HandleDone, nil, nil
	}
	outConn := net.Conn(websocket.NetConn(context.Background(), c, websocket.MessageBinary))
	if prepared != nil && prepared.WrapConn != nil {
		wrapped, err := prepared.WrapConn(outConn)
		if err != nil {
			_ = outConn.Close()
			return HandleDone, nil, nil
		}
		if wrapped != nil {
			outConn = wrapEarlyHandshakeConn(wrapped, prepared.UserHash, wrappedConnUplinkPacked(wrapped))
		}
	}
	return HandleStartTunnel, outConn, nil
}
