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
package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// HandleMuxServer handles a multiplexed tunnel connection after the control plane has selected mux mode.
func HandleMuxServer(conn net.Conn, onConnect func(targetAddr string)) error {
	dial := func(addr string) (net.Conn, error) {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	return HandleMuxWithDialer(conn, onConnect, dial)
}

// HandleMuxWithDialer is like HandleMuxServer but allows the caller to control how targets are dialed.
//
// The dialTarget callback must establish a TCP connection to targetAddr (host:port). It may enforce
// allow-lists (useful for reverse proxy clients).
func HandleMuxWithDialer(conn net.Conn, onConnect func(targetAddr string), dialTarget func(targetAddr string) (net.Conn, error)) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if dialTarget == nil {
		return fmt.Errorf("nil dialTarget")
	}

	sess := newMuxSession(conn, func(stream *muxStream, payload []byte) {
		sess := stream.session
		addr, err := decodeMuxOpenTarget(payload)
		if err != nil {
			sess.sendReset(stream.id, "bad address")
			stream.closeNoSend(err)
			sess.removeStream(stream.id)
			return
		}
		if onConnect != nil {
			onConnect(addr)
		}

		target, err := dialTarget(addr)
		if err != nil {
			sess.sendReset(stream.id, err.Error())
			stream.closeNoSend(err)
			sess.removeStream(stream.id)
			return
		}

		connutil.PipeConn(stream, target)
	})

	<-sess.closed
	err := sess.closedErr()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) {
		return nil
	}
	return err
}

func decodeMuxOpenTarget(payload []byte) (string, error) {
	addr, _, _, err := protocol.ReadAddress(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}
	return addr, nil
}
