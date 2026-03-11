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
	"io"

	"github.com/saba-futai/sudoku/internal/protocol"
)

// ReadTargetAddress parses a single SOCKS5-style target address frame (host:port) from r.
func ReadTargetAddress(r io.Reader) (string, error) {
	addr, _, _, err := protocol.ReadAddress(r)
	return addr, err
}

// WriteTargetAddress writes a single SOCKS5-style target address frame (host:port) to w.
func WriteTargetAddress(w io.Writer, addr string) error {
	return protocol.WriteAddress(w, addr)
}
