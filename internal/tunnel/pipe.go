package tunnel

import (
	"io"
	"net"
	"sync"
)

func pipeConn(a, b net.Conn) {
	var once sync.Once
	closeBoth := func() {
		_ = a.Close()
		_ = b.Close()
	}

	go func() {
		copyOneWay(a, b)
		once.Do(closeBoth)
	}()

	copyOneWay(b, a)
	once.Do(closeBoth)
}

func copyOneWay(dst io.Writer, src io.Reader) {
	buf := bufferPool.Get().([]byte)
	defer bufferPool.Put(buf)
	_, _ = io.CopyBuffer(dst, src, buf)
}
