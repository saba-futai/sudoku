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

import (
	"io"
	"math/rand"
	"net"
	"testing"
	"time"
)

// MockConn implements net.Conn for benchmarking
type MockConn struct {
	readBuf  []byte
	writeBuf []byte
}

func (m *MockConn) Read(b []byte) (n int, err error) {
	if len(m.readBuf) == 0 {
		return 0, io.EOF
	}
	n = copy(b, m.readBuf)
	m.readBuf = m.readBuf[n:]
	return n, nil
}

func (m *MockConn) Write(b []byte) (n int, err error) {
	m.writeBuf = append(m.writeBuf, b...)
	return len(b), nil
}

func (m *MockConn) Close() error                       { return nil }
func (m *MockConn) LocalAddr() net.Addr                { return nil }
func (m *MockConn) RemoteAddr() net.Addr               { return nil }
func (m *MockConn) SetDeadline(t time.Time) error      { return nil }
func (m *MockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *MockConn) SetWriteDeadline(t time.Time) error { return nil }

func BenchmarkSudokuWrite(b *testing.B) {
	key := "benchmark-key"
	table := NewTable(key, "prefer_ascii")
	mock := &MockConn{}
	conn := NewConn(mock, table, 10, 20, false)

	data := make([]byte, 1024)
	rand.Read(data)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mock.writeBuf = mock.writeBuf[:0] // Reset buffer
		conn.Write(data)
	}
}

func BenchmarkSudokuRead(b *testing.B) {
	key := "benchmark-key"
	table := NewTable(key, "prefer_ascii")

	// Pre-generate encoded data
	mock := &MockConn{}
	writerConn := NewConn(mock, table, 10, 20, false)
	data := make([]byte, 1024)
	rand.Read(data)
	writerConn.Write(data)
	encodedData := mock.writeBuf

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// Reset reader state
		mock.readBuf = encodedData
		readerConn := NewConn(mock, table, 10, 20, false)
		buf := make([]byte, 1024)
		io.ReadFull(readerConn, buf)
	}
}

func BenchmarkPackedWrite(b *testing.B) {
	key := "packed-benchmark-key"
	table := NewTable(key, "prefer_ascii")
	mock := &MockConn{}
	conn := NewPackedConn(mock, table, 10, 20)

	data := make([]byte, 63*1024)
	rand.Read(data)

	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mock.writeBuf = mock.writeBuf[:0]
		if _, err := conn.Write(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPackedRead(b *testing.B) {
	key := "packed-benchmark-key"
	table := NewTable(key, "prefer_ascii")

	mock := &MockConn{}
	writerConn := NewPackedConn(mock, table, 10, 20)
	data := make([]byte, 63*1024)
	rand.Read(data)
	if _, err := writerConn.Write(data); err != nil {
		b.Fatal(err)
	}
	encodedData := append([]byte(nil), mock.writeBuf...)

	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mock.readBuf = encodedData
		readerConn := NewPackedConn(mock, table, 10, 20)
		buf := make([]byte, len(data))
		if _, err := io.ReadFull(readerConn, buf); err != nil {
			b.Fatal(err)
		}
	}
}
