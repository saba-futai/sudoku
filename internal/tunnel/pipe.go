package tunnel

import (
	"io"
	"net"
	"sync"
)

func pipeConn(a, b net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	type closeReader interface {
		CloseRead() error
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		copyOneWay(a, b)
		if cw, ok := a.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		if cr, ok := b.(closeReader); ok {
			_ = cr.CloseRead()
		}
	}()

	go func() {
		defer wg.Done()
		copyOneWay(b, a)
		if cw, ok := b.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		if cr, ok := a.(closeReader); ok {
			_ = cr.CloseRead()
		}
	}()

	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

// PipeConn copies data bidirectionally between a and b, then closes both.
func PipeConn(a, b net.Conn) {
	pipeConn(a, b)
}

func copyOneWay(dst io.Writer, src io.Reader) {
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)
	_, _ = io.CopyBuffer(dst, src, buf)
}
