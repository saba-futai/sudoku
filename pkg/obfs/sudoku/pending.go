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
package sudoku

type pendingBuffer struct {
	data []byte
	off  int
}

func newPendingBuffer(capacity int) pendingBuffer {
	return pendingBuffer{data: make([]byte, 0, capacity)}
}

func (p *pendingBuffer) available() int {
	if p == nil {
		return 0
	}
	return len(p.data) - p.off
}

func (p *pendingBuffer) reset() {
	if p == nil {
		return
	}
	p.data = p.data[:0]
	p.off = 0
}

func (p *pendingBuffer) ensureAppendCapacity(extra int) {
	if p == nil || extra <= 0 {
		return
	}
	if p.off == 0 {
		return
	}
	if cap(p.data)-len(p.data) >= extra {
		return
	}

	unread := len(p.data) - p.off
	copy(p.data[:unread], p.data[p.off:])
	p.data = p.data[:unread]
	p.off = 0
}

func (p *pendingBuffer) appendByte(b byte) {
	p.ensureAppendCapacity(1)
	p.data = append(p.data, b)
}

// drainPending copies buffered decoded bytes into dst.
// It returns (n, true) when pending data existed, otherwise (0, false).
func drainPending(dst []byte, pending *pendingBuffer) (int, bool) {
	if pending == nil || pending.available() == 0 {
		return 0, false
	}

	n := copy(dst, pending.data[pending.off:])
	pending.off += n
	if pending.off == len(pending.data) {
		pending.reset()
	}
	return n, true
}
