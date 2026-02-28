package connutil

import (
	"io"
	"net"
	"sync"
)

var pipeBufPool = sync.Pool{
	New: func() any {
		return make([]byte, 32*1024)
	},
}

// PipeConn copies data bidirectionally between a and b, then closes both.
//
// It attempts half-close when supported:
//   - after copying src->dst: CloseWrite(dst) and CloseRead(src)
func PipeConn(a, b net.Conn) {
	if a == nil || b == nil {
		if a != nil {
			_ = a.Close()
		}
		if b != nil {
			_ = b.Close()
		}
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		copyOneWay(a, b)
		_ = TryCloseWrite(a)
		_ = TryCloseRead(b)
	}()

	go func() {
		defer wg.Done()
		copyOneWay(b, a)
		_ = TryCloseWrite(b)
		_ = TryCloseRead(a)
	}()

	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

func copyOneWay(dst io.Writer, src io.Reader) {
	buf := pipeBufPool.Get().([]byte)
	defer pipeBufPool.Put(buf)
	_, _ = io.CopyBuffer(dst, src, buf)
}
