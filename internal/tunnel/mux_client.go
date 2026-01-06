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

// NewMuxClient writes the mux preface and starts the client-side mux session.
func NewMuxClient(conn net.Conn) (*MuxClient, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil conn")
	}
	if err := WriteMuxPreface(conn); err != nil {
		return nil, err
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
