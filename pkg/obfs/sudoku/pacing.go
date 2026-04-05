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
	"net"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

const (
	// Only payloads that are already large enough to occupy multiple packets on the wire
	// are eligible for extra inter-packet padding. This keeps short replies/control frames
	// out of the path without requiring a separate burst detector.
	defaultInjectMinWireBytes = 2 * 1460

	// Random low-frequency injection keeps the pattern irregular while limiting average
	// overhead. 1/8 means only a small subset of eligible writes get a spacer packet.
	defaultInjectChanceNumerator   = 1
	defaultInjectChanceDenominator = 8

	// Inter-packet padding must stay small so it behaves like a pacing spacer rather
	// than a second payload burst. The exact length is adapted from the payload size.
	defaultPaddingPacketMin = 24
	defaultPaddingPacketMax = 96
	defaultPaddingJitter    = 12
)

type downlinkPacingConfig struct {
	minInjectWireBytes      int
	injectChanceNumerator   int
	injectChanceDenominator int
	paddingPacketMin        int
	paddingPacketMax        int
	paddingJitter           int
}

func defaultDownlinkPacingConfig() downlinkPacingConfig {
	return downlinkPacingConfig{
		minInjectWireBytes:      defaultInjectMinWireBytes,
		injectChanceNumerator:   defaultInjectChanceNumerator,
		injectChanceDenominator: defaultInjectChanceDenominator,
		paddingPacketMin:        defaultPaddingPacketMin,
		paddingPacketMax:        defaultPaddingPacketMax,
		paddingJitter:           defaultPaddingJitter,
	}
}

type downlinkWireSizeEstimator func(plainBytes int) int

type randomPaddingPolicy struct {
	minWireBytes int
	numerator    int
	denominator  int
}

func newRandomPaddingPolicy(cfg downlinkPacingConfig) *randomPaddingPolicy {
	return &randomPaddingPolicy{
		minWireBytes: maxInt(cfg.minInjectWireBytes, 1),
		numerator:    clampInt(cfg.injectChanceNumerator, 0, maxInt(cfg.injectChanceDenominator, 1)),
		denominator:  maxInt(cfg.injectChanceDenominator, 1),
	}
}

// ShouldInject applies a lightweight size gate plus random probability. This keeps the
// feature stochastic without needing a separate sustained-burst state machine.
func (p *randomPaddingPolicy) ShouldInject(plainBytes int, estimate downlinkWireSizeEstimator, rng randomSource) bool {
	if p == nil || rng == nil || estimate == nil {
		return false
	}
	if estimate(plainBytes) < p.minWireBytes {
		return false
	}
	if p.numerator <= 0 {
		return false
	}
	if p.numerator >= p.denominator {
		return true
	}
	return rng.Intn(p.denominator) < p.numerator
}

type interPacketPaddingInjector struct {
	writer       io.Writer
	paddingPool  []byte
	rng          randomSource
	minPacketLen int
	maxPacketLen int
	jitter       int
	packetBuf    []byte
}

func newInterPacketPaddingInjector(writer io.Writer, paddingPool []byte, rng randomSource, cfg downlinkPacingConfig) *interPacketPaddingInjector {
	return &interPacketPaddingInjector{
		writer:       writer,
		paddingPool:  append([]byte(nil), paddingPool...),
		rng:          rng,
		minPacketLen: maxInt(cfg.paddingPacketMin, 1),
		maxPacketLen: maxInt(cfg.paddingPacketMax, cfg.paddingPacketMin),
		jitter:       maxInt(cfg.paddingJitter, 0),
	}
}

// Inject writes a pure-padding packet directly to the raw transport. Receivers that do
// not implement pacing still discard it safely because the bytes never match Sudoku hints.
func (i *interPacketPaddingInjector) Inject(plainBytes int) error {
	if i == nil || len(i.paddingPool) == 0 || i.writer == nil || i.rng == nil {
		return nil
	}

	packetLen := i.pickPacketLen(plainBytes)
	if cap(i.packetBuf) < packetLen {
		i.packetBuf = make([]byte, packetLen)
	}
	packet := i.packetBuf[:packetLen]
	for idx := range packet {
		packet[idx] = i.paddingPool[i.rng.Intn(len(i.paddingPool))]
	}
	return connutil.WriteFull(i.writer, packet)
}

func (i *interPacketPaddingInjector) pickPacketLen(plainBytes int) int {
	if i.maxPacketLen <= i.minPacketLen {
		return i.minPacketLen
	}

	// Scale the spacer size from the payload, then keep it inside a narrow range so the
	// pacing packet stays lightweight even during very large downloads.
	base := minEncodedSudokuBytes(plainBytes) / 192
	base = clampInt(base, i.minPacketLen, i.maxPacketLen)
	low := clampInt(base-i.jitter, i.minPacketLen, i.maxPacketLen)
	high := clampInt(base+i.jitter, low, i.maxPacketLen)
	if high == low {
		return low
	}
	return low + i.rng.Intn(high-low+1)
}

// DownlinkPacingWriter encodes payload bytes with the underlying downlink codec and
// occasionally injects a tiny pure-padding spacer packet.
type DownlinkPacingWriter struct {
	dataWriter        io.Writer
	wireSizeEstimator downlinkWireSizeEstimator
	paddingPolicy     *randomPaddingPolicy
	injector          *interPacketPaddingInjector

	writeMu sync.Mutex
}

func NewDownlinkPacingWriter(raw io.Writer, table *Table, pMin, pMax int) *DownlinkPacingWriter {
	localRng := newSeededRand()
	return newDownlinkPacingWriterWithConfig(
		newSudokuDataWriter(raw, table, localRng, pMin, pMax),
		raw,
		table.PaddingPool,
		minEncodedSudokuBytes,
		localRng,
		defaultDownlinkPacingConfig(),
	)
}

func NewPackedDownlinkPacingWriter(raw net.Conn, table *Table, pMin, pMax int) (*PackedConn, *DownlinkPacingWriter) {
	packed := NewPackedConn(raw, table, pMin, pMax)
	return packed, newDownlinkPacingWriterWithConfig(
		packed,
		raw,
		packedInterPacketPaddingPool(table),
		minEncodedPackedBytes,
		newSeededRand(),
		defaultDownlinkPacingConfig(),
	)
}

func NewServerDownlinkWriter(raw net.Conn, table *Table, pMin, pMax int, pure bool) (io.Writer, []func() error) {
	if pure {
		return NewDownlinkPacingWriter(raw, table, pMin, pMax), nil
	}

	packed, writer := NewPackedDownlinkPacingWriter(raw, table, pMin, pMax)
	return writer, []func() error{packed.Flush}
}

func newDownlinkPacingWriterWithConfig(
	dataWriter io.Writer,
	raw io.Writer,
	paddingPool []byte,
	estimate downlinkWireSizeEstimator,
	rng randomSource,
	cfg downlinkPacingConfig,
) *DownlinkPacingWriter {
	return &DownlinkPacingWriter{
		dataWriter:        dataWriter,
		wireSizeEstimator: estimate,
		paddingPolicy:     newRandomPaddingPolicy(cfg),
		injector:          newInterPacketPaddingInjector(raw, paddingPool, rng, cfg),
	}
}

func (w *DownlinkPacingWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	n, err := w.dataWriter.Write(p)
	if err != nil {
		return n, err
	}
	if w.paddingPolicy.ShouldInject(n, w.wireSizeEstimator, w.injector.rng) {
		if err := w.injector.Inject(n); err != nil {
			return n, err
		}
	}
	return n, nil
}

type sudokuDataWriter struct {
	writer           io.Writer
	table            *Table
	rng              randomSource
	paddingThreshold uint64
	writeBuf         []byte
}

func newSudokuDataWriter(writer io.Writer, table *Table, rng randomSource, pMin, pMax int) *sudokuDataWriter {
	return &sudokuDataWriter{
		writer:           writer,
		table:            table,
		rng:              rng,
		paddingThreshold: pickPaddingThreshold(rng, pMin, pMax),
		writeBuf:         make([]byte, 0, 4096),
	}
}

func (w *sudokuDataWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.writeBuf = encodeSudokuPayload(w.writeBuf[:0], w.table, w.rng, w.paddingThreshold, p)
	return len(p), connutil.WriteFull(w.writer, w.writeBuf)
}

func encodeSudokuPayload(dst []byte, table *Table, rng randomSource, paddingThreshold uint64, p []byte) []byte {
	if len(p) == 0 {
		return dst[:0]
	}

	outCapacity := len(p)*6 + 1
	if cap(dst) < outCapacity {
		dst = make([]byte, 0, outCapacity)
	}
	out := dst[:0]
	pads := table.PaddingPool
	padLen := len(pads)

	for _, b := range p {
		if shouldPad(rng, paddingThreshold) {
			out = append(out, pads[rng.Intn(padLen)])
		}

		puzzles := table.EncodeTable[b]
		puzzle := puzzles[rng.Intn(len(puzzles))]

		perm := perm4[rng.Intn(len(perm4))]
		for _, idx := range perm {
			if shouldPad(rng, paddingThreshold) {
				out = append(out, pads[rng.Intn(padLen)])
			}
			out = append(out, puzzle[idx])
		}
	}

	if shouldPad(rng, paddingThreshold) {
		out = append(out, pads[rng.Intn(padLen)])
	}
	return out
}

func minEncodedSudokuBytes(plainBytes int) int {
	if plainBytes <= 0 {
		return 0
	}
	return plainBytes * 4
}

func minEncodedPackedBytes(plainBytes int) int {
	if plainBytes <= 0 {
		return 0
	}
	// Packed downlink has a 4/3 core expansion plus a short protected prefix/tail.
	return ((plainBytes + 2) / 3 * 4) + 16
}

func packedInterPacketPaddingPool(table *Table) []byte {
	if table == nil || table.layout == nil {
		return nil
	}
	pool := make([]byte, 0, len(table.PaddingPool))
	for _, b := range table.PaddingPool {
		if b != table.layout.padMarker {
			pool = append(pool, b)
		}
	}
	if len(pool) == 0 && table.layout.padMarker != 0 {
		pool = append(pool, table.layout.padMarker)
	}
	return pool
}

func clampInt(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
