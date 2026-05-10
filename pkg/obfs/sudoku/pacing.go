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
	// Each estimated MTU-sized downlink segment gets an independent padding chance.
	defaultInjectMTUBytes = 1460

	// Random low-frequency injection keeps the pattern irregular while limiting average
	// overhead. 1/7 means only a small subset of MTU-sized segments get a spacer packet.
	defaultInjectChanceNumerator   = 1
	defaultInjectChanceDenominator = 7

	// Inter-packet padding must stay small so it behaves like a pacing spacer.
	defaultPaddingPacketMin = 1
	defaultPaddingPacketMax = 128
)

type downlinkPacingConfig struct {
	mtuWireBytes            int
	injectChanceNumerator   int
	injectChanceDenominator int
	paddingPacketMin        int
	paddingPacketMax        int
}

func defaultDownlinkPacingConfig() downlinkPacingConfig {
	return downlinkPacingConfig{
		mtuWireBytes:            defaultInjectMTUBytes,
		injectChanceNumerator:   defaultInjectChanceNumerator,
		injectChanceDenominator: defaultInjectChanceDenominator,
		paddingPacketMin:        defaultPaddingPacketMin,
		paddingPacketMax:        defaultPaddingPacketMax,
	}
}

type downlinkWireSizeEstimator func(plainBytes int) int

type randomPaddingPolicy struct {
	mtuWireBytes int
	numerator    int
	denominator  int
}

func newRandomPaddingPolicy(cfg downlinkPacingConfig) *randomPaddingPolicy {
	return &randomPaddingPolicy{
		mtuWireBytes: maxInt(cfg.mtuWireBytes, 1),
		numerator:    clampInt(cfg.injectChanceNumerator, 0, maxInt(cfg.injectChanceDenominator, 1)),
		denominator:  maxInt(cfg.injectChanceDenominator, 1),
	}
}

// InjectionCount gives each estimated MTU-sized downlink segment an independent chance
// to emit a small spacer packet.
func (p *randomPaddingPolicy) InjectionCount(plainBytes int, estimate downlinkWireSizeEstimator, rng randomSource) int {
	if p == nil || rng == nil || estimate == nil {
		return 0
	}
	opportunities := estimate(plainBytes) / p.mtuWireBytes
	if opportunities <= 0 || p.numerator <= 0 {
		return 0
	}
	if p.numerator >= p.denominator {
		return opportunities
	}
	count := 0
	for i := 0; i < opportunities; i++ {
		if rng.Intn(p.denominator) < p.numerator {
			count++
		}
	}
	return count
}

type interPacketPaddingInjector struct {
	writer       io.Writer
	paddingPool  []byte
	rng          randomSource
	minPacketLen int
	maxPacketLen int
	packetBuf    []byte
}

func newInterPacketPaddingInjector(writer io.Writer, paddingPool []byte, rng randomSource, cfg downlinkPacingConfig) *interPacketPaddingInjector {
	return &interPacketPaddingInjector{
		writer:       writer,
		paddingPool:  append([]byte(nil), paddingPool...),
		rng:          rng,
		minPacketLen: maxInt(cfg.paddingPacketMin, 1),
		maxPacketLen: maxInt(cfg.paddingPacketMax, cfg.paddingPacketMin),
	}
}

// Inject writes a small pure-padding packet directly to the raw transport. Receivers
// safely discard it because these bytes never match Sudoku hints.
func (i *interPacketPaddingInjector) Inject() error {
	if i == nil || len(i.paddingPool) == 0 || i.writer == nil || i.rng == nil {
		return nil
	}

	packetLen := i.pickPacketLen()
	if cap(i.packetBuf) < packetLen {
		i.packetBuf = make([]byte, packetLen)
	}
	packet := i.packetBuf[:0]
	for len(packet) < packetLen {
		packet = append(packet, i.paddingPool[i.rng.Intn(len(i.paddingPool))])
	}
	return connutil.WriteFull(i.writer, packet)
}

func (i *interPacketPaddingInjector) pickPacketLen() int {
	if i.maxPacketLen <= i.minPacketLen {
		return i.minPacketLen
	}
	return i.minPacketLen + i.rng.Intn(i.maxPacketLen-i.minPacketLen+1)
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
	injections := w.paddingPolicy.InjectionCount(n, w.wireSizeEstimator, w.injector.rng)
	for i := 0; i < injections; i++ {
		if err := w.injector.Inject(); err != nil {
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
