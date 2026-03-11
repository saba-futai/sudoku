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
	"context"
	"fmt"
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
)

// DialUDPOverTCP bootstraps a UDP-over-TCP tunnel using the standard Dial flow.
func DialUDPOverTCP(ctx context.Context, cfg *ProtocolConfig) (net.Conn, error) {
	conn, err := establishBaseConn(ctx, cfg, validateBaseClientConfig, nil)
	if err != nil {
		return nil, err
	}
	if err := tunnel.WriteKIPMessage(conn, tunnel.KIPTypeStartUoT, nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write uot preface: %w", err)
	}
	return conn, nil
}

// HandleUoT runs the UDP-over-TCP loop on an upgraded tunnel connection.
func HandleUoT(conn net.Conn) error {
	return tunnel.HandleUoTServer(conn)
}

func WriteUoTDatagram(w io.Writer, addr string, payload []byte) error {
	return tunnel.WriteUoTDatagram(w, addr, payload)
}

func ReadUoTDatagram(r io.Reader) (string, []byte, error) {
	return tunnel.ReadUoTDatagram(r)
}
