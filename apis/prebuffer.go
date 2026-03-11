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
	"net"

	"github.com/saba-futai/sudoku/internal/tunnel"
)

// NewPreBufferedConn returns a net.Conn that replays preRead before reading from conn.
//
// This is useful when you need to peek some bytes (e.g. to detect an HTTP tunnel header or probe tables)
// and still keep the stream consumable by the next parser.
func NewPreBufferedConn(conn net.Conn, preRead []byte) net.Conn {
	return tunnel.NewPreBufferedConn(conn, preRead)
}
