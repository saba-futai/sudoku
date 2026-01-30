package sudoku

import (
	"io"
	"net"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

// DirectionalConn wires separate reader/writer streams onto a single net.Conn.
// It is useful for asymmetric obfuscation (e.g. packed downlink, sudoku uplink).
type DirectionalConn struct {
	net.Conn
	reader  io.Reader
	writer  io.Writer
	closers []func() error
}

func NewDirectionalConn(base net.Conn, reader io.Reader, writer io.Writer, closers ...func() error) *DirectionalConn {
	return &DirectionalConn{
		Conn:    base,
		reader:  reader,
		writer:  writer,
		closers: closers,
	}
}

func (c *DirectionalConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *DirectionalConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

func (c *DirectionalConn) CloseRead() error {
	if err := connutil.TryCloseRead(c.reader); err != nil {
		return err
	}
	return connutil.TryCloseRead(c.Conn)
}

func (c *DirectionalConn) CloseWrite() error {
	firstErr := connutil.RunClosers(c.closers...)
	if err := connutil.TryCloseWrite(c.writer); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := connutil.TryCloseWrite(c.Conn); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (c *DirectionalConn) Close() error {
	firstErr := connutil.RunClosers(c.closers...)
	if err := c.Conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
