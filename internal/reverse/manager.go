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

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

type Manager struct {
	mu sync.RWMutex

	routes   map[string]*routeEntry
	sessions map[*tunnel.MuxClient]*session
}

type session struct {
	clientID string
	mux      *tunnel.MuxClient
	prefixes []string
}

type routeEntry struct {
	prefix      string
	target      string
	stripPrefix bool
	hostHeader  string
	proxy       *httputil.ReverseProxy
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
	for _, r := range routes {
		prefix := strings.TrimSpace(r.Path)
		if prefix == "" {
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
		if prefix == "" || target == "" {
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
		for _, p := range sess.prefixes {
			if ent := m.routes[p]; ent != nil {
				delete(m.routes, p)
			}
		}
	}
	m.mu.Unlock()
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
