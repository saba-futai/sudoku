package app

import (
	"io"
	"net"
	"sync"
)

type closeWriter interface {
	CloseWrite() error
}

type closeReader interface {
	CloseRead() error
}

// copyBufferPool reuses buffers for bidirectional piping to reduce GC churn.
var copyBufferPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, 32*1024)
	},
}

func pipeConn(a, b net.Conn) {
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

func copyOneWay(dst io.Writer, src io.Reader) {
	buf := copyBufferPool.Get().([]byte)
	defer copyBufferPool.Put(buf)
	_, _ = io.CopyBuffer(dst, src, buf)
}
