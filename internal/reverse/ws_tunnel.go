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
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

const sudokuTCPSubprotocol = "sudoku-tcp-v1"

func serveSudokuTCPTunnel(w http.ResponseWriter, r *http.Request, mux *tunnel.MuxClient, target string) {
	if w == nil || r == nil || mux == nil || strings.TrimSpace(target) == "" {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{sudokuTCPSubprotocol},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept already wrote the response.
		return
	}
	if c.Subprotocol() != sudokuTCPSubprotocol {
		_ = c.Close(websocket.StatusPolicyViolation, "subprotocol required")
		return
	}

	up, err := mux.Dial(target)
	if err != nil {
		_ = c.Close(websocket.StatusInternalError, "dial failed")
		return
	}

	wsConn := websocket.NetConn(r.Context(), c, websocket.MessageBinary)
	tunnel.PipeConn(wsConn, up)
}

func websocketClientOffersSubprotocol(r *http.Request, want string) bool {
	if r == nil {
		return false
	}
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == want {
				return true
			}
		}
	}
	return false
}

func isWebSocketUpgrade(r *http.Request) bool {
	if r == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Upgrade")), "websocket") {
		return false
	}
	return headerHasToken(r.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, key, token string) bool {
	if h == nil {
		return false
	}
	token = strings.ToLower(strings.TrimSpace(token))
	if token == "" {
		return false
	}
	for _, v := range h.Values(key) {
		for _, part := range strings.Split(v, ",") {
			if strings.ToLower(strings.TrimSpace(part)) == token {
				return true
			}
		}
	}
	return false
}
