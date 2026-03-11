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
package reverse

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
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
	mux         *tunnel.MuxClient
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
			mux:         mux,
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
	if entry == nil {
		entry = m.matchByRefererLocked(r)
	}
	if entry == nil {
		entry = m.matchByCookieLocked(r)
	}
	m.mu.RUnlock()

	if entry == nil || entry.proxy == nil {
		http.NotFound(w, r)
		return
	}

	// If the request path doesn't contain a reverse prefix but a Referer does (common for
	// root-absolute assets/API calls like "/static/app.js" or "/api/foo"), rewrite it into the
	// correct subpath so routing works without mutating response bodies.
	if entry.prefix != "" && entry.prefix != "/" && pathPrefixMatch(path, entry.prefix) == false {
		r.URL.Path = entry.prefix + path
		r.URL.RawPath = ""
		path = r.URL.Path
	}

	// /<prefix> (no trailing slash) is reserved for Sudoku's TCP-over-WS tunnel when the client
	// explicitly negotiates the "sudoku-tcp-v1" subprotocol. Any other WS upgrade should fall
	// through to the normal reverse proxy (and must not be redirected).
	if path == entry.prefix && isWebSocketUpgrade(r) {
		if entry.mux != nil && websocketClientOffersSubprotocol(r, sudokuTCPSubprotocol) {
			serveSudokuTCPTunnel(w, r, entry.mux, entry.target)
			return
		}
	} else if entry.prefix != "" && entry.prefix != "/" && path == entry.prefix && r.Method != "" {
		// Ensure the route root is treated as a directory. Many apps use relative asset URLs
		// (e.g. "static/app.js"); without a trailing slash, browsers resolve them to "/static/...".
		// Redirecting keeps the app working under a subpath like "/gitea/" or "/netdata/".
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			u := *r.URL
			u.Path = path + "/"
			http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
			return
		default:
		}
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

func (m *Manager) matchByRefererLocked(r *http.Request) *routeEntry {
	if r == nil {
		return nil
	}
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref == "" {
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil {
		return nil
	}
	refPath := u.EscapedPath()
	if refPath == "" {
		refPath = u.Path
	}
	if refPath == "" {
		refPath = "/"
	}
	return m.matchLocked(refPath)
}

const reversePrefixCookieName = "sudoku_rev_prefix"

func (m *Manager) matchByCookieLocked(r *http.Request) *routeEntry {
	if r == nil {
		return nil
	}
	c, err := r.Cookie(reversePrefixCookieName)
	if err != nil {
		return nil
	}
	prefix, err := url.PathUnescape(strings.TrimSpace(c.Value))
	if err != nil {
		return nil
	}
	prefix = normalizePrefix(prefix)
	if prefix == "" {
		return nil
	}
	return m.matchLocked(prefix)
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
		if stripPrefix && prefix != "" && prefix != "/" {
			// Many web apps gzip/br their HTML/JS/CSS when the client sends Accept-Encoding.
			// Subpath support requires response rewriting, so force identity encoding upstream.
			req.Header.Del("Accept-Encoding")
		}
	}

	rp.ModifyResponse = func(resp *http.Response) error {
		// When we strip the prefix for upstream routing, we need to re-add it for browsers:
		// - absolute redirects (Location: /foo)
		// - cookie paths (Path=/)
		// - root-absolute asset URLs in HTML/CSS/SVG/JSON ("/assets/...", url(/assets/...))
		if stripPrefix && prefix != "" && prefix != "/" {
			rewriteLocation(resp, prefix)
			rewriteSetCookiePath(resp, prefix)
			rewriteReversePrefixCookie(resp, prefix)
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

	// Root-absolute redirect.
	if strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, "//") {
		u, err := url.Parse(loc)
		if err != nil || u == nil || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
			return
		}
		if pathPrefixMatch(u.Path, prefix) {
			return
		}
		u.Path = prefix + u.Path
		u.RawPath = ""
		resp.Header.Set("Location", u.String())
		return
	}

	// Absolute redirect to the same host.
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		u, err := url.Parse(loc)
		if err != nil || u.Host == "" || u.Path == "" || !strings.HasPrefix(u.Path, "/") {
			return
		}
		if strings.HasPrefix(u.Path, prefix+"/") || u.Path == prefix {
			return
		}
		if resp.Request == nil || !sameHTTPHost(resp.Request.Host, u.Host, u.Scheme) {
			return
		}
		u.Path = prefix + u.Path
		u.RawPath = ""
		resp.Header.Set("Location", u.String())
		return
	}
}

func sameHTTPHost(reqHost, locHost, scheme string) bool {
	reqHost = strings.TrimSpace(reqHost)
	locHost = strings.TrimSpace(locHost)
	if reqHost == "" || locHost == "" {
		return false
	}

	reqURL := &url.URL{Host: reqHost}
	locURL := &url.URL{Host: locHost}

	if !strings.EqualFold(reqURL.Hostname(), locURL.Hostname()) {
		return false
	}

	reqPort := reqURL.Port()
	if reqPort == "" {
		// If the request Host has no port, accept same-host redirects regardless of default port.
		return true
	}

	locPort := locURL.Port()
	if locPort == "" {
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "https":
			locPort = "443"
		case "http":
			locPort = "80"
		default:
			return false
		}
	}
	return reqPort == locPort
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

func rewriteReversePrefixCookie(resp *http.Response, prefix string) {
	if resp == nil || resp.Header == nil {
		return
	}
	p := normalizePrefix(prefix)
	if p == "" {
		return
	}
	c := (&http.Cookie{
		Name:     reversePrefixCookieName,
		Value:    url.PathEscape(p),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}).String()
	resp.Header.Add("Set-Cookie", c)
}
