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
	"math/bits"
	"net"
	"testing"
)

func TestCustomLayoutParsingAndPadding(t *testing.T) {
	table, err := NewTableWithCustom("seed-custom", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		t.Fatalf("failed to build table: %v", err)
	}
	if table.IsASCII {
		t.Fatalf("custom table should not be marked ASCII")
	}
	if table.layout == nil || table.layout.hintMask == 0 {
		t.Fatalf("layout mask not initialized")
	}

	for _, b := range table.PaddingPool {
		if table.layout.isHint(b) {
			t.Fatalf("padding byte incorrectly recognized as hint: %08b", b)
		}
		if bits.OnesCount8(b) < 5 {
			t.Fatalf("padding hamming weight too low: %d", bits.OnesCount8(b))
		}
	}
}

func TestCustomLayoutAsciiPriority(t *testing.T) {
	table, err := NewTableWithCustom("seed-custom", "prefer_ascii", "vpxxvpvv")
	if err != nil {
		t.Fatalf("failed to build ascii-preferred table: %v", err)
	}
	if !table.IsASCII {
		t.Fatalf("ascii preference should override custom pattern")
	}
	if table.layout.name != "ascii" {
		t.Fatalf("expected ascii layout, got %s", table.layout.name)
	}
}

func TestCustomLayoutConnRoundTrip(t *testing.T) {
	table, err := NewTableWithCustom("roundtrip", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		t.Fatalf("table creation failed: %v", err)
	}

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	writer := NewConn(c1, table, 0, 0, false)
	reader := NewConn(c2, table, 0, 0, false)

	payload := bytes.Repeat([]byte("sudoku-custom-layout"), 2048)
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write(payload)
		done <- err
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if !bytes.Equal(payload, buf) {
		t.Fatalf("payload mismatch")
	}
}

func TestCustomLayoutPackedRoundTrip(t *testing.T) {
	table, err := NewTableWithCustom("packed-roundtrip", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		t.Fatalf("table creation failed: %v", err)
	}

	c1, c2 := net.Pipe()
	defer c2.Close()

	writer := NewPackedConn(c1, table, 0, 0)
	reader := NewPackedConn(c2, table, 0, 0)

	payload := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01}, 8192)
	done := make(chan error, 1)
	go func() {
		defer c1.Close()

		if _, err := writer.Write(payload); err != nil {
			done <- err
			return
		}
		done <- writer.Flush()
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("drain failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write/flush failed: %v", err)
	}
	if !bytes.Equal(payload, buf) {
		t.Fatalf("payload mismatch")
	}
}

func TestCustomLayoutInvalidPatterns(t *testing.T) {
	if _, err := NewTableWithCustom("seed", "prefer_entropy", "xxxxvvvv"); err == nil {
		t.Fatalf("expected error for invalid pattern")
	}
	if _, err := NewTableWithCustom("seed", "badmode", "xpxvvpvv"); err == nil {
		t.Fatalf("expected error for invalid ascii mode")
	}
}

func TestCustomLayoutPackedStress(t *testing.T) {
	table, err := NewTableWithCustom("stress-key", "prefer_entropy", "vxpvxvvp")
	if err != nil {
		t.Fatalf("table creation failed: %v", err)
	}

	c1, c2 := net.Pipe()
	defer c2.Close()

	writer := NewPackedConn(c1, table, 2, 4)
	reader := NewPackedConn(c2, table, 2, 4)

	payload := bytes.Repeat([]byte{0xFF, 0x00, 0x7F, 0x11, 0x22}, 20000) // ~100KB stress payload
	done := make(chan error, 1)
	go func() {
		defer c1.Close()

		if _, err := writer.Write(payload); err != nil {
			done <- err
			return
		}
		done <- writer.Flush()
	}()

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("drain failed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("write/flush failed: %v", err)
	}
	if !bytes.Equal(payload, buf) {
		t.Fatalf("stress payload mismatch")
	}
}
