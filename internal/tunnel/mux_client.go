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
	"fmt"
	"io"
	"net"

	"github.com/saba-futai/sudoku/internal/protocol"
)

// MuxClient opens multiple target streams over an already-upgraded Sudoku tunnel connection.
//
// The caller owns the lifetime of the underlying conn; calling Close closes the session and the conn.
type MuxClient struct {
	sess *muxSession
}

// NewMuxClient starts the mux session on an already-negotiated mux tunnel connection.
func NewMuxClient(conn net.Conn) (*MuxClient, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil conn")
	}
	return &MuxClient{sess: newMuxSession(conn, nil)}, nil
}

// Dial opens a new logical stream to destAddrStr (host:port).
func (c *MuxClient) Dial(destAddrStr string) (net.Conn, error) {
	if c == nil || c.sess == nil {
		return nil, fmt.Errorf("nil mux client")
	}
	if c.sess.isClosed() {
		return nil, c.sess.closedErr()
	}

	var addrBuf bytes.Buffer
	if err := protocol.WriteAddress(&addrBuf, destAddrStr); err != nil {
		return nil, fmt.Errorf("encode address failed: %w", err)
	}

	streamID := c.sess.nextStreamID()
	st := newMuxStream(c.sess, streamID)
	c.sess.registerStream(st)

	if err := c.sess.sendFrame(muxFrameOpen, streamID, addrBuf.Bytes()); err != nil {
		st.closeNoSend(err)
		c.sess.removeStream(streamID)
		return nil, fmt.Errorf("mux open failed: %w", err)
	}
	return st, nil
}

// Close closes the mux session and the underlying connection.
func (c *MuxClient) Close() error {
	if c == nil || c.sess == nil {
		return nil
	}
	c.sess.closeWithError(io.ErrClosedPipe)
	return nil
}

// Done is closed when the underlying mux session ends.
func (c *MuxClient) Done() <-chan struct{} {
	if c == nil || c.sess == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return c.sess.closed
}

// Err returns the terminal session error when Done is closed.
func (c *MuxClient) Err() error {
	if c == nil || c.sess == nil {
		return io.ErrClosedPipe
	}
	return c.sess.closedErr()
}
