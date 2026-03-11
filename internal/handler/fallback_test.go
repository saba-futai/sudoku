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
package handler

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

type recordedConn struct {
	net.Conn
	data []byte
}

func (r *recordedConn) GetBufferedAndRecorded() []byte {
	return r.data
}

func TestHandleSuspiciousFallback(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	got := make(chan []byte, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		all, _ := io.ReadAll(conn)
		got <- all
	}()

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	cfg := &config.Config{
		FallbackAddr: l.Addr().String(),
	}

	wrapper := &recordedConn{Conn: serverSide, data: []byte("bad")}

	go HandleSuspicious(wrapper, serverSide, cfg)

	// Write extra data that should also be forwarded
	if _, err := clientSide.Write([]byte("tail")); err != nil {
		t.Fatalf("write: %v", err)
	}
	clientSide.Close()

	select {
	case data := <-got:
		if string(data) != "badtail" {
			t.Fatalf("unexpected fallback data: %q", string(data))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("fallback did not receive data")
	}
}
