package connutil

import (
	"bytes"
	"io"
	"net"
	"time"
)

// ReadOnlyConn wraps a bytes.Reader as a net.Conn for probing/parsing code paths.
// Writes always fail with io.ErrClosedPipe.
type ReadOnlyConn struct {
	*bytes.Reader
}

func (c *ReadOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (c *ReadOnlyConn) Close() error                     { return nil }
func (c *ReadOnlyConn) LocalAddr() net.Addr              { return nil }
func (c *ReadOnlyConn) RemoteAddr() net.Addr             { return nil }
func (c *ReadOnlyConn) SetDeadline(time.Time) error      { return nil }
func (c *ReadOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *ReadOnlyConn) SetWriteDeadline(time.Time) error { return nil }
