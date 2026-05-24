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
package connutil

import (
	"io"
	"net"
	"sync"
)

// PipeBufferSize stays just below the AEAD record plaintext ceiling, reducing
// record count for large streams without forcing an extra tiny trailing record.
const PipeBufferSize = 63 * 1024

var pipeBufPool = sync.Pool{
	New: func() any {
		return make([]byte, PipeBufferSize)
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
