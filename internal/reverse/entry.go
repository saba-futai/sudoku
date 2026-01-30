package reverse

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
)

// ServeEntry serves both:
//   - HTTP reverse proxy requests (path-based routing), and
//   - raw TCP reverse forwarding (requires a route with Path=="")
//
// on the same TCP listen address. Incoming connections are sniffed by the first 4 bytes: if they look
// like a supported HTTP/1.x request method prefix, the connection is treated as HTTP; otherwise it is
// treated as raw TCP.
func ServeEntry(listenAddr string, mgr *Manager) error {
	if mgr == nil {
		return fmt.Errorf("reverse manager is required")
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	httpLn := newConnChanListener(ln.Addr(), 256)
	httpSrv := &http.Server{
		Handler:           mgr,
		ReadHeaderTimeout: 5 * time.Second,
	}

	httpErrCh := make(chan error, 1)
	go func() { httpErrCh <- httpSrv.Serve(httpLn) }()
	defer func() {
		_ = httpLn.Close()
		_ = httpSrv.Close()
		<-httpErrCh
	}()

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		go func(raw net.Conn) {
			if raw == nil {
				return
			}

			var peek [4]byte
			_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
			n, err := io.ReadFull(raw, peek[:])
			_ = raw.SetReadDeadline(time.Time{})
			if err != nil {
				// For raw TCP forwarding, the protocol might be "server-first" (client sends nothing).
				// If we time out while sniffing, fall back to TCP when possible.
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					if n > 0 {
						mgr.ServeTCP(tunnel.NewPreBufferedConn(raw, peek[:n]))
						return
					}
					mgr.ServeTCP(raw)
					return
				}
				_ = raw.Close()
				return
			}

			conn := tunnel.NewPreBufferedConn(raw, peek[:n])
			if httpmask.LooksLikeHTTPRequestStart(peek[:]) {
				if !httpLn.enqueue(conn) {
					_ = conn.Close()
				}
				return
			}

			mgr.ServeTCP(conn)
		}(c)
	}
}

type connChanListener struct {
	addr   net.Addr
	ch     chan net.Conn
	closed chan struct{}

	once sync.Once
}

func newConnChanListener(addr net.Addr, buffer int) *connChanListener {
	if buffer <= 0 {
		buffer = 1
	}
	return &connChanListener{
		addr:   addr,
		ch:     make(chan net.Conn, buffer),
		closed: make(chan struct{}),
	}
}

func (l *connChanListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.ch:
		return c, nil
	}
}

func (l *connChanListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *connChanListener) Addr() net.Addr { return l.addr }

func (l *connChanListener) enqueue(c net.Conn) bool {
	select {
	case <-l.closed:
		return false
	default:
	}
	select {
	case l.ch <- c:
		return true
	case <-l.closed:
		return false
	}
}
