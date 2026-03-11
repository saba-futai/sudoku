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
package apis

import (
	"bytes"
	"fmt"
	"net"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

// SessionKind describes the payload type carried by a Sudoku tunnel connection after the handshake.
//
// The first control-plane message selects the session mode.
type SessionKind uint8

const (
	// SessionForward means the client will send a target address and then proxy TCP streams.
	SessionForward SessionKind = iota
	// SessionUoT means the tunnel carries UDP-over-TCP frames.
	SessionUoT
	// SessionMux means the tunnel carries a multiplexed stream session (single tunnel, multi-target).
	SessionMux
	// SessionReverse means the tunnel is used to register reverse proxy routes (server exposes client services).
	SessionReverse
)

// ServerHandshakeSessionAutoWithUserHash upgrades the connection and auto-detects the session kind.
//
// Returns:
//   - conn: the upgraded tunnel connection. The control-plane message is always consumed.
//   - session: the detected session kind
//   - targetAddr: valid only when session==SessionForward
//   - userHash: per-user handshake identifier (if available)
//   - sessionPayload: valid only when session==SessionReverse (the JSON registration payload)
func ServerHandshakeSessionAutoWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (conn net.Conn, session SessionKind, targetAddr string, userHash string, sessionPayload []byte, err error) {
	conn, userHash, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, SessionForward, "", "", nil, err
	}

	for {
		msg, err := tunnel.ReadKIPMessage(conn)
		if err != nil {
			_ = conn.Close()
			return nil, SessionForward, "", "", nil, fail(fmt.Errorf("read session message failed: %w", err))
		}
		if msg.Type == tunnel.KIPTypeKeepAlive {
			continue
		}
		switch msg.Type {
		case tunnel.KIPTypeStartUoT:
			return conn, SessionUoT, "", userHash, nil, nil
		case tunnel.KIPTypeStartMux:
			return conn, SessionMux, "", userHash, nil, nil
		case tunnel.KIPTypeStartRev:
			return conn, SessionReverse, "", userHash, msg.Payload, nil
		case tunnel.KIPTypeOpenTCP:
			addr, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
			if err != nil {
				_ = conn.Close()
				return nil, SessionForward, "", "", nil, fail(fmt.Errorf("decode target address failed: %w", err))
			}
			return conn, SessionForward, addr, userHash, nil, nil
		default:
			_ = conn.Close()
			return nil, SessionForward, "", "", nil, fail(fmt.Errorf("unknown session message: %d", msg.Type))
		}
	}
}

// ServerHandshakeSessionAuto is like ServerHandshakeSessionAutoWithUserHash but omits the user hash.
func ServerHandshakeSessionAuto(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, SessionKind, string, error) {
	conn, session, targetAddr, _, _, err := ServerHandshakeSessionAutoWithUserHash(rawConn, cfg)
	return conn, session, targetAddr, err
}
