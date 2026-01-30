package apis

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/reverse"
)

// ReverseRoute maps a public path prefix to a client-side TCP target (HTTP service).
type ReverseRoute struct {
	Path        string `json:"path"`
	Target      string `json:"target"`
	StripPrefix *bool  `json:"strip_prefix,omitempty"`
	HostHeader  string `json:"host_header,omitempty"`
}

// ReverseManager is a server-side reverse registry + http.Handler.
//
// A reverse client registers routes over a Sudoku tunnel; this manager then exposes them via HTTP path prefixes.
type ReverseManager struct {
	mgr *reverse.Manager
}

func NewReverseManager() *ReverseManager {
	return &ReverseManager{mgr: reverse.NewManager()}
}

func (m *ReverseManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m == nil || m.mgr == nil {
		http.Error(w, "reverse proxy not configured", http.StatusServiceUnavailable)
		return
	}
	m.mgr.ServeHTTP(w, r)
}

// HandleServerSession handles a reverse client registration connection.
//
// The ReverseMagicByte has already been consumed by the caller (e.g. via ServerHandshakeSessionAuto).
func (m *ReverseManager) HandleServerSession(conn net.Conn, userHash string) error {
	if m == nil || m.mgr == nil {
		return fmt.Errorf("reverse manager is required")
	}
	return reverse.HandleServerSession(conn, userHash, m.mgr)
}

// DialReverseClientSession dials the server and runs a reverse registration session until it ends.
//
// This function returns when the reverse session ends; callers usually want to reconnect with backoff.
func DialReverseClientSession(ctx context.Context, cfg *ProtocolConfig, clientID string, routes []ReverseRoute) error {
	conn, err := DialBase(ctx, cfg)
	if err != nil {
		return err
	}
	err = ServeReverseClientSession(conn, clientID, routes)
	_ = conn.Close()
	return err
}

// ServeReverseClientSession registers routes to the server and serves reverse mux streams until the session ends.
//
// The provided conn must be an upgraded Sudoku tunnel connection (handshake completed).
func ServeReverseClientSession(conn net.Conn, clientID string, routes []ReverseRoute) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if len(routes) == 0 {
		return fmt.Errorf("no reverse routes")
	}

	converted := make([]config.ReverseRoute, 0, len(routes))
	seen := make(map[string]struct{}, len(routes))
	for i, r := range routes {
		r.Path = strings.TrimSpace(r.Path)
		r.Target = strings.TrimSpace(r.Target)
		r.HostHeader = strings.TrimSpace(r.HostHeader)

		if r.Path == "" && r.Target == "" {
			continue
		}
		if r.Path == "" {
			return fmt.Errorf("reverse route[%d]: missing path", i)
		}
		if !strings.HasPrefix(r.Path, "/") {
			r.Path = "/" + r.Path
		}
		r.Path = path.Clean(r.Path)
		if r.Path != "/" {
			r.Path = strings.TrimRight(r.Path, "/")
		}

		if r.Target == "" {
			return fmt.Errorf("reverse route[%d] %q: missing target", i, r.Path)
		}
		if _, _, err := net.SplitHostPort(r.Target); err != nil {
			return fmt.Errorf("reverse route[%d] %q: invalid target %q: %w", i, r.Path, r.Target, err)
		}

		if _, ok := seen[r.Path]; ok {
			return fmt.Errorf("reverse route duplicate path: %q", r.Path)
		}
		seen[r.Path] = struct{}{}

		converted = append(converted, config.ReverseRoute{
			Path:        r.Path,
			Target:      r.Target,
			StripPrefix: r.StripPrefix,
			HostHeader:  r.HostHeader,
		})
	}
	if len(converted) == 0 {
		return fmt.Errorf("no usable reverse routes")
	}

	return reverse.ServeClientSession(conn, clientID, converted)
}
