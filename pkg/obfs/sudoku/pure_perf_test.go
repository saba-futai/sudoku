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
	"bytes"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"
)

type discardConn struct{}

func (discardConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (discardConn) Write(p []byte) (int, error)      { return len(p), nil }
func (discardConn) Close() error                     { return nil }
func (discardConn) LocalAddr() net.Addr              { return nil }
func (discardConn) RemoteAddr() net.Addr             { return nil }
func (discardConn) SetDeadline(time.Time) error      { return nil }
func (discardConn) SetReadDeadline(time.Time) error  { return nil }
func (discardConn) SetWriteDeadline(time.Time) error { return nil }

func benchmarkPureWriteThroughput(b *testing.B, pMin, pMax int) {
	table := NewTable("pure-throughput-key", "prefer_ascii")
	conn := NewConn(discardConn{}, table, pMin, pMax, false)
	data := make([]byte, 32*1024)
	_, _ = rand.Read(data)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := conn.Write(data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPureWriteThroughput_NoPadding(b *testing.B) {
	benchmarkPureWriteThroughput(b, 0, 0)
}

func BenchmarkPureWriteThroughput_DefaultPadding(b *testing.B) {
	benchmarkPureWriteThroughput(b, 5, 15)
}

func benchmarkPureReadThroughput(b *testing.B, pMin, pMax int) {
	table := NewTable("pure-throughput-key", "prefer_ascii")
	data := make([]byte, 32*1024)
	_, _ = rand.Read(data)

	var encoded bytes.Buffer
	writer := NewConn(writeOnlyConn{Writer: &encoded}, table, pMin, pMax, false)
	if _, err := writer.Write(data); err != nil {
		b.Fatal(err)
	}
	encodedData := append([]byte(nil), encoded.Bytes()...)
	out := make([]byte, len(data))
	reader := NewConn(&cyclicReadConn{data: encodedData}, table, pMin, pMax, false)

	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(reader, out); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPureReadThroughput_NoPadding(b *testing.B) {
	benchmarkPureReadThroughput(b, 0, 0)
}

func BenchmarkPureReadThroughput_DefaultPadding(b *testing.B) {
	benchmarkPureReadThroughput(b, 5, 15)
}

type writeOnlyConn struct {
	io.Writer
}

func (c writeOnlyConn) Read([]byte) (int, error)       { return 0, io.EOF }
func (c writeOnlyConn) Write(p []byte) (int, error)    { return c.Writer.Write(p) }
func (writeOnlyConn) Close() error                     { return nil }
func (writeOnlyConn) LocalAddr() net.Addr              { return nil }
func (writeOnlyConn) RemoteAddr() net.Addr             { return nil }
func (writeOnlyConn) SetDeadline(time.Time) error      { return nil }
func (writeOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (writeOnlyConn) SetWriteDeadline(time.Time) error { return nil }

type cyclicReadConn struct {
	data []byte
	off  int
}

func (c *cyclicReadConn) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := 0
	for n < len(p) {
		copied := copy(p[n:], c.data[c.off:])
		n += copied
		c.off += copied
		if c.off == len(c.data) {
			c.off = 0
		}
	}
	return n, nil
}

func (cyclicReadConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (cyclicReadConn) Close() error                     { return nil }
func (cyclicReadConn) LocalAddr() net.Addr              { return nil }
func (cyclicReadConn) RemoteAddr() net.Addr             { return nil }
func (cyclicReadConn) SetDeadline(time.Time) error      { return nil }
func (cyclicReadConn) SetReadDeadline(time.Time) error  { return nil }
func (cyclicReadConn) SetWriteDeadline(time.Time) error { return nil }
