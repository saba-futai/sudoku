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
package sudoku

import (
	"io"
	"net"
	"testing"
)

func TestConnWrite_Empty(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	table := NewTable("edge-key", "prefer_entropy")
	conn := NewConn(c1, table, 0, 0, false)
	if n, err := conn.Write(nil); err != nil || n != 0 {
		t.Fatalf("Write(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := conn.Write([]byte{}); err != nil || n != 0 {
		t.Fatalf("Write(empty) = (%d, %v), want (0, nil)", n, err)
	}
}

func TestConnInvalidStateReturnsError(t *testing.T) {
	var classic *Conn
	if n, err := classic.Write([]byte("x")); n != 0 || err != io.ErrClosedPipe {
		t.Fatalf("nil Conn Write = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	if n, err := classic.Read(make([]byte, 1)); n != 0 || err != io.ErrClosedPipe {
		t.Fatalf("nil Conn Read = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	classic.StopRecording()
	if got := classic.GetBufferedAndRecorded(); got != nil {
		t.Fatalf("nil Conn recorded = %v, want nil", got)
	}
}

func TestPackedConnInvalidStateReturnsError(t *testing.T) {
	var packed *PackedConn
	if n, err := packed.Write([]byte("x")); n != 0 || err != io.ErrClosedPipe {
		t.Fatalf("nil PackedConn Write = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	if err := packed.Flush(); err != io.ErrClosedPipe {
		t.Fatalf("nil PackedConn Flush = %v, want io.ErrClosedPipe", err)
	}
	if n, err := packed.Read(make([]byte, 1)); n != 0 || err != io.ErrClosedPipe {
		t.Fatalf("nil PackedConn Read = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
	packed.StopRecording()
	if got := packed.GetBufferedAndRecorded(); got != nil {
		t.Fatalf("nil PackedConn recorded = %v, want nil", got)
	}

	table := NewTable("invalid-state", "prefer_ascii")
	malformed := &PackedConn{
		Conn:             &MockConn{},
		table:            table,
		rng:              newSudokuRand(1),
		paddingThreshold: probOne,
	}
	if n, err := malformed.Write([]byte("x")); n != 0 || err != io.ErrClosedPipe {
		t.Fatalf("malformed PackedConn Write = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
}
