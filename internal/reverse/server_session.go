package reverse

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path"
	"strings"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

const maxHelloBytes = 64 * 1024

type helloMessage struct {
	ClientID string                `json:"client_id,omitempty"`
	Routes   []config.ReverseRoute `json:"routes,omitempty"`
}

// HandleServerSession handles a reverse client registration connection.
//
// The ReverseMagicByte has already been consumed by the caller.
func HandleServerSession(conn net.Conn, userHash string, mgr *Manager) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if mgr == nil {
		return fmt.Errorf("reverse manager not configured")
	}

	var ver [1]byte
	if _, err := io.ReadFull(conn, ver[:]); err != nil {
		return err
	}
	if ver[0] != tunnel.ReverseVersion {
		return fmt.Errorf("unsupported reverse version: %d", ver[0])
	}

	var lenBuf [4]byte
	if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
		return err
	}
	n := int(binary.BigEndian.Uint32(lenBuf[:]))
	if n <= 0 || n > maxHelloBytes {
		return fmt.Errorf("invalid reverse hello size: %d", n)
	}

	payload := make([]byte, n)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return err
	}

	var hello helloMessage
	if err := json.Unmarshal(payload, &hello); err != nil {
		return fmt.Errorf("invalid reverse hello: %w", err)
	}

	clientID := strings.TrimSpace(hello.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(userHash)
	}
	if clientID == "" {
		clientID = "unknown"
	}

	if len(hello.Routes) == 0 {
		return fmt.Errorf("reverse hello has no routes")
	}

	// Server-side sanitize/validate (don't trust the client).
	normalized := make([]config.ReverseRoute, 0, len(hello.Routes))
	seen := make(map[string]struct{}, len(hello.Routes))
	seenTCP := false
	for _, r := range hello.Routes {
		r.Path = strings.TrimSpace(r.Path)
		r.Target = strings.TrimSpace(r.Target)
		r.HostHeader = strings.TrimSpace(r.HostHeader)

		if r.Path == "" && r.Target == "" {
			continue
		}
		if r.Target == "" {
			return fmt.Errorf("reverse route missing target")
		}

		// Path empty => raw TCP reverse on reverse.listen (no HTTP path prefix).
		// Only one TCP route is supported per server entry.
		if r.Path == "" {
			if seenTCP {
				return fmt.Errorf("reverse route duplicate tcp mapping")
			}
			seenTCP = true
			if _, _, err := net.SplitHostPort(r.Target); err != nil {
				return fmt.Errorf("reverse tcp route invalid target %q: %w", r.Target, err)
			}
			normalized = append(normalized, r)
			continue
		}

		if !strings.HasPrefix(r.Path, "/") {
			r.Path = "/" + r.Path
		}
		r.Path = path.Clean(r.Path)
		if r.Path != "/" {
			r.Path = strings.TrimRight(r.Path, "/")
		}
		if _, ok := seen[r.Path]; ok {
			return fmt.Errorf("reverse route duplicate path: %q", r.Path)
		}
		seen[r.Path] = struct{}{}

		if _, _, err := net.SplitHostPort(r.Target); err != nil {
			return fmt.Errorf("reverse route %q invalid target %q: %w", r.Path, r.Target, err)
		}
		normalized = append(normalized, r)
	}
	hello.Routes = normalized

	// Start mux session: server opens streams, client dials local targets.
	mux, err := tunnel.NewMuxClient(conn)
	if err != nil {
		return fmt.Errorf("start reverse mux session failed: %w", err)
	}

	if err := mgr.RegisterSession(clientID, mux, hello.Routes); err != nil {
		_ = mux.Close()
		return err
	}

	<-mux.Done()
	err = mux.Err()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
