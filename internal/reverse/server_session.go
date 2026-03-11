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
// helloPayload is the JSON payload from the control plane.
func HandleServerSession(conn net.Conn, userHash string, mgr *Manager, helloPayload []byte) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if mgr == nil {
		return fmt.Errorf("reverse manager not configured")
	}

	if len(helloPayload) == 0 || len(helloPayload) > maxHelloBytes {
		return fmt.Errorf("invalid reverse hello size: %d", len(helloPayload))
	}

	var hello helloMessage
	if err := json.Unmarshal(helloPayload, &hello); err != nil {
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
