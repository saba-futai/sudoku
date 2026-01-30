package reverse

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

type Manager struct {
	mu sync.RWMutex

	routes   map[string]*routeEntry
	sessions map[*tunnel.MuxClient]*session
	tcp      *tcpEntry
}

type session struct {
	clientID string
	mux      *tunnel.MuxClient
	prefixes []string
	tcp      bool
}

type routeEntry struct {
	prefix      string
	target      string
	stripPrefix bool
	hostHeader  string
	proxy       *httputil.ReverseProxy
}

type tcpEntry struct {
	clientID string
	target   string
	mux      *tunnel.MuxClient
}

func NewManager() *Manager {
	return &Manager{
		routes:   make(map[string]*routeEntry),
		sessions: make(map[*tunnel.MuxClient]*session),
	}
}

// RegisterSession registers a reverse client session and its routes.
//
// On conflict (same path prefix already registered), it returns an error.
func (m *Manager) RegisterSession(clientID string, mux *tunnel.MuxClient, routes []config.ReverseRoute) error {
	if m == nil {
		return fmt.Errorf("nil manager")
	}
	if mux == nil {
		return fmt.Errorf("nil mux client")
	}
	if len(routes) == 0 {
		return fmt.Errorf("no reverse routes")
	}

	sess := &session{
		clientID: clientID,
		mux:      mux,
	}

	m.mu.Lock()
	if _, ok := m.sessions[mux]; ok {
		m.mu.Unlock()
		return fmt.Errorf("reverse session already registered")
	}
	seenTCP := false
	for _, r := range routes {
		prefix := strings.TrimSpace(r.Path)
		target := strings.TrimSpace(r.Target)
		if prefix == "" {
			if target == "" {
				continue
			}
			// Path empty => raw TCP reverse on reverse.listen (no HTTP path prefix).
			if seenTCP {
				m.mu.Unlock()
				return fmt.Errorf("reverse tcp route already set in this session")
			}
			seenTCP = true
			if m.tcp != nil {
				m.mu.Unlock()
				return fmt.Errorf("reverse tcp route already registered")
			}
			continue
		}
		if _, ok := m.routes[prefix]; ok {
			m.mu.Unlock()
			return fmt.Errorf("reverse path already registered: %q", prefix)
		}
	}

	for _, r := range routes {
		prefix := strings.TrimSpace(r.Path)
		target := strings.TrimSpace(r.Target)
		if prefix == "" {
			if target == "" {
				continue
			}
			m.tcp = &tcpEntry{
				clientID: clientID,
				target:   target,
				mux:      mux,
			}
			sess.tcp = true
			continue
		}
		if target == "" {
			continue
		}
		strip := true
		if r.StripPrefix != nil {
			strip = *r.StripPrefix
		}
		hostHeader := strings.TrimSpace(r.HostHeader)

		entry := &routeEntry{
			prefix:      prefix,
			target:      target,
			stripPrefix: strip,
			hostHeader:  hostHeader,
		}
		entry.proxy = newRouteProxy(prefix, target, strip, hostHeader, mux)
		m.routes[prefix] = entry
		sess.prefixes = append(sess.prefixes, prefix)
	}
	m.sessions[mux] = sess
	m.mu.Unlock()

	go func() {
		<-mux.Done()
		m.UnregisterSession(mux)
		_ = mux.Close()
	}()

	return nil
}

func (m *Manager) UnregisterSession(mux *tunnel.MuxClient) {
	if m == nil || mux == nil {
		return
	}

	m.mu.Lock()
	sess := m.sessions[mux]
	if sess != nil {
		delete(m.sessions, mux)
		if sess.tcp && m.tcp != nil && m.tcp.mux == mux {
			m.tcp = nil
		}
		for _, p := range sess.prefixes {
			if ent := m.routes[p]; ent != nil {
				delete(m.routes, p)
			}
		}
	}
	m.mu.Unlock()
}

// ServeTCP handles a raw TCP connection by forwarding it through the reverse session.
//
// It requires a reverse route with an empty path (Path=="") to be registered.
func (m *Manager) ServeTCP(conn net.Conn) {
	if conn == nil {
		return
	}

	m.mu.RLock()
	ent := m.tcp
	m.mu.RUnlock()

	if ent == nil || ent.mux == nil || strings.TrimSpace(ent.target) == "" {
		_ = conn.Close()
		return
	}

	up, err := ent.mux.Dial(ent.target)
	if err != nil {
		_ = conn.Close()
		return
	}

	tunnel.PipeConn(conn, up)
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "reverse proxy not configured", http.StatusServiceUnavailable)
		return
	}
	if r == nil || r.URL == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	path := r.URL.Path

	m.mu.RLock()
	entry := m.matchLocked(path)
	m.mu.RUnlock()

	if entry == nil || entry.proxy == nil {
		http.NotFound(w, r)
		return
	}
	entry.proxy.ServeHTTP(w, r)
}

func (m *Manager) matchLocked(reqPath string) *routeEntry {
	var (
		best     *routeEntry
		bestSize int
	)
	for prefix, entry := range m.routes {
		if entry == nil {
			continue
		}
		if !pathPrefixMatch(reqPath, prefix) {
			continue
		}
		if len(prefix) > bestSize {
			best = entry
			bestSize = len(prefix)
		}
	}
	return best
}

func pathPrefixMatch(path, prefix string) bool {
	if prefix == "" {
		return false
	}
	if prefix == "/" {
		return true
	}
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	return path[len(prefix)] == '/'
}

func newRouteProxy(prefix, target string, stripPrefix bool, hostHeader string, mux *tunnel.MuxClient) *httputil.ReverseProxy {
	targetURL := &url.URL{Scheme: "http", Host: "reverse.internal"}
	rp := httputil.NewSingleHostReverseProxy(targetURL)

	// Each reverse request uses a fresh mux stream; do not let net/http attempt to keep idle conns.
	rp.Transport = &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		DisableKeepAlives:  true,
		ForceAttemptHTTP2:  false,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			return mux.Dial(target)
		},
	}

	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)

		if stripPrefix {
			req.URL.Path = stripPathPrefix(req.URL.Path, prefix)
			req.URL.RawPath = ""
		}
		if hostHeader != "" {
			req.Host = hostHeader
		}
		if prefix != "" && prefix != "/" {
			req.Header.Set("X-Forwarded-Prefix", prefix)
		}
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		// When we strip the prefix for upstream routing, we need to re-add it for browsers:
		// - absolute redirects (Location: /foo)
		// - cookie paths (Path=/)
		// - root-absolute asset URLs in HTML/CSS/JS ("/assets/...", url(/assets/...))
		if stripPrefix && prefix != "" && prefix != "/" {
			rewriteLocation(resp, prefix)
			rewriteSetCookiePath(resp, prefix)
			if err := rewriteTextBody(resp, prefix); err != nil {
				return err
			}
		}
		return nil
	}

	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// Avoid leaking internal details; the server logs should carry the rest.
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	// Reasonable default for streaming responses (SSE, long polls, etc.).
	rp.FlushInterval = 50 * time.Millisecond

	return rp
}

func stripPathPrefix(reqPath, prefix string) string {
	if prefix == "" || prefix == "/" {
		if reqPath == "" {
			return "/"
		}
		return reqPath
	}
	if reqPath == prefix {
		return "/"
	}
	if strings.HasPrefix(reqPath, prefix+"/") {
		out := strings.TrimPrefix(reqPath, prefix)
		if out == "" {
			return "/"
		}
		return out
	}
	// Not a match; keep as-is.
	if reqPath == "" {
		return "/"
	}
	return reqPath
}

func rewriteLocation(resp *http.Response, prefix string) {
	if resp == nil || resp.Header == nil || prefix == "" || prefix == "/" {
		return
	}
	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if loc == "" {
		return
	}
	// Only rewrite root-absolute redirects.
	if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") {
		return
	}
	if strings.HasPrefix(loc, prefix+"/") || loc == prefix {
		return
	}
	resp.Header.Set("Location", prefix+loc)
}

func rewriteSetCookiePath(resp *http.Response, prefix string) {
	if resp == nil || resp.Header == nil || prefix == "" || prefix == "/" {
		return
	}
	values := resp.Header.Values("Set-Cookie")
	if len(values) == 0 {
		return
	}

	out := make([]string, 0, len(values))
	for _, v := range values {
		parts := strings.Split(v, ";")
		for i := range parts {
			part := strings.TrimSpace(parts[i])
			if part == "" {
				continue
			}
			if len(part) < 5 {
				continue
			}
			// Case-insensitive "Path="
			if strings.EqualFold(part[:5], "Path=") {
				pathVal := strings.TrimSpace(part[5:])
				if strings.HasPrefix(pathVal, "/") && !strings.HasPrefix(pathVal, "//") {
					if strings.HasPrefix(pathVal, prefix+"/") || pathVal == prefix {
						continue
					}
					parts[i] = "Path=" + prefix + pathVal
				}
			}
		}
		out = append(out, strings.Join(parts, ";"))
	}

	resp.Header.Del("Set-Cookie")
	for _, v := range out {
		resp.Header.Add("Set-Cookie", v)
	}
}

func rewriteTextBody(resp *http.Response, prefix string) error {
	if resp == nil || resp.Body == nil || resp.Header == nil || prefix == "" || prefix == "/" {
		return nil
	}

	const maxBody = 8 << 20
	if resp.ContentLength > maxBody {
		return nil
	}

	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if ct == "" {
		return nil
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	if !isRewritableContentType(ct) {
		return nil
	}
	if ct == "text/event-stream" {
		// Never buffer/modify SSE.
		return nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		// Too large to buffer; restore consumed bytes and stream the rest.
		resp.Body = multiReadCloser{
			Reader: io.MultiReader(bytes.NewReader(raw), resp.Body),
			Closer: resp.Body,
		}
		return nil
	}
	_ = resp.Body.Close()

	rewritten := rewriteRootAbsolutePaths(raw, prefix)
	if !bytes.Equal(raw, rewritten) {
		resp.Body = io.NopCloser(bytes.NewReader(rewritten))
		resp.ContentLength = int64(len(rewritten))
		resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		resp.Header.Del("Transfer-Encoding")
		return nil
	}

	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	resp.Header.Del("Transfer-Encoding")
	return nil
}

type multiReadCloser struct {
	io.Reader
	io.Closer
}

func isRewritableContentType(ct string) bool {
	switch {
	case strings.HasPrefix(ct, "text/"):
		// Covers text/html, text/css, text/javascript, etc.
		return true
	case ct == "application/javascript":
		return true
	case ct == "application/x-javascript":
		return true
	case ct == "application/json":
		return true
	case ct == "application/manifest+json":
		return true
	case ct == "image/svg+xml":
		return true
	default:
		return false
	}
}

func rewriteRootAbsolutePaths(in []byte, prefix string) []byte {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return in
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" || prefix == "/" {
		return in
	}
	p := []byte(prefix)

	var (
		out  bytes.Buffer
		last int
	)
	out.Grow(len(in) + len(in)/16)

	for i := 0; i < len(in); i++ {
		if in[i] != '/' {
			continue
		}
		if !isRootPathContext(in, i) {
			continue
		}
		if i+1 < len(in) && in[i+1] == '/' {
			// Protocol-relative URL ("//example.com/...").
			continue
		}
		if bytes.HasPrefix(in[i:], p) {
			continue
		}
		out.Write(in[last:i])
		out.Write(p)
		last = i
	}

	if last == 0 {
		return in
	}
	out.Write(in[last:])
	return out.Bytes()
}

func isRootPathContext(b []byte, slashIndex int) bool {
	if slashIndex <= 0 {
		return false
	}
	prev := b[slashIndex-1]
	switch prev {
	case '"', '\'':
		return true
	default:
	}

	// CSS url(/...) (without quotes).
	k := slashIndex - 1
	for k >= 0 && isSpace(b[k]) {
		k--
	}
	if k < 0 || b[k] != '(' {
		return false
	}
	k--
	for k >= 0 && isSpace(b[k]) {
		k--
	}
	if k < 2 {
		return false
	}
	return lowerASCII(b[k-2]) == 'u' && lowerASCII(b[k-1]) == 'r' && lowerASCII(b[k]) == 'l'
}

func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
