package httpmask

import (
	"io"
	"net"
	"sync"
	"time"
)

// queuedConn provides a net.Conn-like interface backed by internal channels.
//
// It is used by HTTP tunnel modes (stream/poll) to bridge HTTP request/response bodies to
// a byte-stream API while preserving CloseWrite semantics.
type queuedConn struct {
	rxc    chan []byte
	closed chan struct{}

	writeCh chan []byte
	// writeClosed is closed by CloseWrite to stop accepting new payloads.
	// When closed, Write returns io.ErrClosedPipe, but Read is unaffected.
	writeClosed chan struct{}

	mu         sync.Mutex
	readBuf    []byte
	closeErr   error
	localAddr  net.Addr
	remoteAddr net.Addr
}

func (c *queuedConn) CloseWrite() error {
	if c == nil || c.writeClosed == nil {
		return nil
	}
	c.mu.Lock()
	if !isClosedPipeChan(c.writeClosed) {
		close(c.writeClosed)
	}
	c.mu.Unlock()
	return nil
}

func (c *queuedConn) closeWithError(err error) error {
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil
	default:
		if err == nil {
			err = io.ErrClosedPipe
		}
		if c.closeErr == nil {
			c.closeErr = err
		}
		close(c.closed)
	}
	c.mu.Unlock()
	return nil
}

func (c *queuedConn) closedErr() error {
	c.mu.Lock()
	err := c.closeErr
	c.mu.Unlock()
	if err == nil {
		return io.ErrClosedPipe
	}
	return err
}

func (c *queuedConn) Read(b []byte) (n int, err error) {
	if len(c.readBuf) == 0 {
		select {
		case c.readBuf = <-c.rxc:
		case <-c.closed:
			return 0, c.closedErr()
		}
	}
	n = copy(b, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *queuedConn) Write(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return 0, c.closedErr()
	default:
	}
	if c.writeClosed != nil {
		select {
		case <-c.writeClosed:
			c.mu.Unlock()
			return 0, io.ErrClosedPipe
		default:
		}
	}
	c.mu.Unlock()

	payload := make([]byte, len(b))
	copy(payload, b)
	if c.writeClosed == nil {
		select {
		case c.writeCh <- payload:
			return len(b), nil
		case <-c.closed:
			return 0, c.closedErr()
		}
	}
	select {
	case c.writeCh <- payload:
		return len(b), nil
	case <-c.closed:
		return 0, c.closedErr()
	case <-c.writeClosed:
		return 0, io.ErrClosedPipe
	}
}

func (c *queuedConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *queuedConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *queuedConn) SetDeadline(time.Time) error      { return nil }
func (c *queuedConn) SetReadDeadline(time.Time) error  { return nil }
func (c *queuedConn) SetWriteDeadline(time.Time) error { return nil }
