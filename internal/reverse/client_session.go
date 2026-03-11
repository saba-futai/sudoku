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
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

// ServeClientSession registers routes to the server and serves reverse mux streams until the session ends.
func ServeClientSession(conn net.Conn, clientID string, routes []config.ReverseRoute) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if len(routes) == 0 {
		return fmt.Errorf("no reverse routes")
	}

	clientID = strings.TrimSpace(clientID)

	msg := helloMessage{
		ClientID: clientID,
		Routes:   append([]config.ReverseRoute(nil), routes...),
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxHelloBytes {
		return fmt.Errorf("reverse hello too large: %d", len(raw))
	}
	if err := tunnel.WriteKIPMessage(conn, tunnel.KIPTypeStartRev, raw); err != nil {
		return err
	}

	allowed := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		target := strings.TrimSpace(r.Target)
		if target != "" {
			allowed[target] = struct{}{}
		}
	}

	dialTarget := func(targetAddr string) (net.Conn, error) {
		if _, ok := allowed[targetAddr]; !ok {
			return nil, fmt.Errorf("target not allowed")
		}
		return net.DialTimeout("tcp", targetAddr, 10*time.Second)
	}

	return tunnel.HandleMuxWithDialer(conn, nil, dialTarget)
}
