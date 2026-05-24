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
	"net"
	"testing"
)

func TestDownlinkWriter_DoesNotInjectInterPacketPadding(t *testing.T) {
	table := NewTable("downlink-no-interpacket", "prefer_ascii")

	var raw bytes.Buffer
	writer := newDownlinkWriter(newSudokuDataWriter(&raw, table, newSudokuRand(1), 0, 0))
	payload := bytes.Repeat([]byte("a"), 256)

	n, err := writer.Write(payload)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("write len mismatch: got %d want %d", n, len(payload))
	}

	if got, want := raw.Len(), len(payload)*4; got != want {
		t.Fatalf("wire len = %d, want %d", got, want)
	}
	if extra := countNonHintBytes(raw.Bytes(), table); extra != 0 {
		t.Fatalf("unexpected non-hint bytes: %d", extra)
	}
}

func TestDownlinkWriter_RoundTripCompatibility(t *testing.T) {
	table := NewTable("downlink-roundtrip", "prefer_entropy")

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	writer := NewDownlinkWriter(c1, table, 0, 0)
	reader := NewConn(c2, table, 0, 0, false)
	payload := bytes.Repeat([]byte("compat-downlink-"), 8)

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		_ = c1.Close()
		writeErr <- err
	}()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch")
	}
}

func TestPackedDownlinkWriter_RoundTripCompatibility(t *testing.T) {
	table := NewTable("packed-downlink-roundtrip", "prefer_entropy")

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	packed, writer := NewPackedDownlinkWriter(c1, table, 0, 0)
	reader := NewPackedConn(c2, table, 0, 0)
	payload := bytes.Repeat([]byte("packed-downlink-compat"), 96)

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		if err == nil {
			err = packed.Flush()
		}
		_ = c1.Close()
		writeErr <- err
	}()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("packed roundtrip mismatch")
	}
}

func TestPackedConn_SmallReadDoesNotOverDecode(t *testing.T) {
	table := NewTable("packed-small-read", "prefer_ascii")

	mock := &MockConn{}
	writer := NewPackedConn(mock, table, 0, 0)
	payload := bytes.Repeat([]byte("x"), 63*1024)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}

	reader := NewPackedConn(&MockConn{readBuf: mock.writeBuf}, table, 0, 0)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		t.Fatalf("small read: %v", err)
	}

	if pending := reader.pendingData.available(); pending > minDecodeReadSize {
		t.Fatalf("small read decoded too much pending data: %d", pending)
	}
}

func countNonHintBytes(data []byte, table *Table) int {
	count := 0
	for _, b := range data {
		if !table.layout.isHint(b) {
			count++
		}
	}
	return count
}
