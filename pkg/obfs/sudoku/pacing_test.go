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

func TestRandomPaddingPolicy_SizeGateAndProbability(t *testing.T) {
	cfg := defaultDownlinkPacingConfig()
	cfg.minInjectWireBytes = 16
	cfg.injectChanceNumerator = 1
	cfg.injectChanceDenominator = 1

	policy := newRandomPaddingPolicy(cfg)
	rng := newSudokuRand(1)
	if policy.ShouldInject(3, minEncodedSudokuBytes, rng) {
		t.Fatalf("small write should not trigger pacing")
	}
	if !policy.ShouldInject(4, minEncodedSudokuBytes, rng) {
		t.Fatalf("eligible large write should inject when probability is forced to 100%%")
	}
}

func TestDownlinkPacingWriter_SkipsSmallTraffic(t *testing.T) {
	table := NewTable("pacing-small", "prefer_ascii")
	cfg := defaultDownlinkPacingConfig()
	cfg.minInjectWireBytes = 128
	cfg.injectChanceNumerator = 1
	cfg.injectChanceDenominator = 1

	var raw bytes.Buffer
	writer := newTestSudokuPacingWriter(&raw, table, cfg)
	payload := []byte("small-response")

	n, err := writer.Write(payload)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if n != len(payload) {
		t.Fatalf("write len mismatch: got %d want %d", n, len(payload))
	}

	if got, want := raw.Len(), len(payload)*4; got != want {
		t.Fatalf("small traffic should not add pacing bytes: got %d want %d", got, want)
	}
	if extra := countNonHintBytes(raw.Bytes(), table); extra != 0 {
		t.Fatalf("small traffic unexpectedly injected %d padding bytes", extra)
	}
}

func TestDownlinkPacingWriter_InjectsPurePaddingOnEligibleWrite(t *testing.T) {
	table := NewTable("pacing-burst", "prefer_ascii")
	cfg := defaultDownlinkPacingConfig()
	cfg.minInjectWireBytes = 32
	cfg.injectChanceNumerator = 1
	cfg.injectChanceDenominator = 1
	cfg.paddingPacketMin = 5
	cfg.paddingPacketMax = 5
	cfg.paddingJitter = 0

	var raw bytes.Buffer
	writer := newTestSudokuPacingWriter(&raw, table, cfg)
	chunk := bytes.Repeat([]byte("a"), 8)

	if _, err := writer.Write(chunk); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	expectedDataBytes := len(chunk) * 4
	if got, want := raw.Len(), expectedDataBytes+5; got != want {
		t.Fatalf("unexpected wire len: got %d want %d", got, want)
	}
	if extra := countNonHintBytes(raw.Bytes(), table); extra != 5 {
		t.Fatalf("expected exactly one 5-byte padding packet, got %d extra bytes", extra)
	}
}

func TestDownlinkPacingWriter_RoundTripCompatibility(t *testing.T) {
	table := NewTable("pacing-roundtrip", "prefer_entropy")
	cfg := defaultDownlinkPacingConfig()
	cfg.minInjectWireBytes = 4
	cfg.injectChanceNumerator = 1
	cfg.injectChanceDenominator = 1
	cfg.paddingPacketMin = 3
	cfg.paddingPacketMax = 3
	cfg.paddingJitter = 0

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	writer := newTestSudokuPacingWriter(c1, table, cfg)
	reader := NewConn(c2, table, 0, 0, false)
	payload := bytes.Repeat([]byte("compat-padding-"), 128)

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
		t.Fatalf("roundtrip mismatch with pacing padding injected")
	}
}

func TestPackedDownlinkPacingWriter_RoundTripCompatibility(t *testing.T) {
	table := NewTable("packed-roundtrip", "prefer_entropy")
	cfg := defaultDownlinkPacingConfig()
	cfg.minInjectWireBytes = 4
	cfg.injectChanceNumerator = 1
	cfg.injectChanceDenominator = 1
	cfg.paddingPacketMin = 4
	cfg.paddingPacketMax = 4
	cfg.paddingJitter = 0

	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	packed, writer := newTestPackedPacingWriter(c1, table, cfg)
	reader := NewPackedConn(c2, table, 0, 0)
	payload := bytes.Repeat([]byte("packed-padding-compat"), 96)

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
		t.Fatalf("packed roundtrip mismatch with pacing padding injected")
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

func newTestSudokuPacingWriter(raw io.Writer, table *Table, cfg downlinkPacingConfig) *DownlinkPacingWriter {
	return newDownlinkPacingWriterWithConfig(
		newSudokuDataWriter(raw, table, newSudokuRand(1), 0, 0),
		raw,
		table.PaddingPool,
		minEncodedSudokuBytes,
		newSudokuRand(2),
		cfg,
	)
}

func newTestPackedPacingWriter(raw net.Conn, table *Table, cfg downlinkPacingConfig) (*PackedConn, *DownlinkPacingWriter) {
	packed := NewPackedConn(raw, table, 0, 0)
	return packed, newDownlinkPacingWriterWithConfig(
		packed,
		raw,
		packedInterPacketPaddingPool(table),
		minEncodedPackedBytes,
		newSudokuRand(3),
		cfg,
	)
}
