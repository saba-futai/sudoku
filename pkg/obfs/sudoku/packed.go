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
	"bufio"
	"io"
	"math/rand"
	"net"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

const (
	// RngBatchSize controls how many random numbers to batch per RNG pull (micro-optimization).
	RngBatchSize = 128

	packedProtectedPrefixBytes = 14
)

// PackedConn is a bandwidth-optimized downlink codec.
//
// It encodes ciphertext bits into 6-bit groups, then maps them to Sudoku "hint" bytes with optional padding
// to keep the traffic profile consistent with the classic Sudoku codec:
//   - Write: batch 12 bytes -> 16 groups (fast path), with the same padding probability model as Conn
//   - Read: decode groups back to bytes, avoiding slice aliasing leaks
type PackedConn struct {
	net.Conn
	table  *Table
	reader *bufio.Reader

	// Read buffer
	rawBuf      []byte
	pendingData []byte // Decoded bytes not yet consumed by Read

	// Write buffer and state
	writeMu  sync.Mutex
	writeBuf []byte
	bitBuf   uint64 // pending bits (MSB-first)
	bitCount int    // number of valid pending bits in bitBuf

	// Read state
	readBitBuf uint64 // pending bits (MSB-first)
	readBits   int    // number of valid pending bits in readBitBuf

	// RNG and padding control — uses integer-threshold random, consistent with Conn
	rng              *rand.Rand
	paddingThreshold uint64 // Same probability model as Conn
	padMarker        byte
	padPool          []byte
}

func (pc *PackedConn) CloseWrite() error {
	if pc == nil {
		return nil
	}
	return connutil.TryCloseWrite(pc.Conn)
}

func (pc *PackedConn) CloseRead() error {
	if pc == nil {
		return nil
	}
	return connutil.TryCloseRead(pc.Conn)
}

func NewPackedConn(c net.Conn, table *Table, pMin, pMax int) *PackedConn {
	localRng := newSeededRand()

	pc := &PackedConn{
		Conn:             c,
		table:            table,
		reader:           bufio.NewReaderSize(c, IOBufferSize),
		rawBuf:           make([]byte, IOBufferSize),
		pendingData:      make([]byte, 0, 4096),
		writeBuf:         make([]byte, 0, 4096),
		rng:              localRng,
		paddingThreshold: pickPaddingThreshold(localRng, pMin, pMax),
	}

	pc.padMarker = table.layout.padMarker
	for _, b := range table.PaddingPool {
		if b != pc.padMarker {
			pc.padPool = append(pc.padPool, b)
		}
	}
	if len(pc.padPool) == 0 {
		pc.padPool = append(pc.padPool, pc.padMarker)
	}
	return pc
}

// maybeAddPadding inserts a padding byte with the same probability model as Conn.
func (pc *PackedConn) maybeAddPadding(out []byte) []byte {
	if shouldPad(pc.rng, pc.paddingThreshold) {
		out = append(out, pc.getPaddingByte())
	}
	return out
}

func (pc *PackedConn) appendGroup(out []byte, group byte) []byte {
	out = pc.maybeAddPadding(out)
	return append(out, pc.encodeGroup(group))
}

func (pc *PackedConn) appendForcedPadding(out []byte) []byte {
	return append(out, pc.getPaddingByte())
}

func (pc *PackedConn) nextProtectedPrefixGap() int {
	return 1 + pc.rng.Intn(2)
}

func (pc *PackedConn) writeProtectedPrefix(out []byte, p []byte) ([]byte, int) {
	if len(p) == 0 {
		return out, 0
	}

	limit := len(p)
	if limit > packedProtectedPrefixBytes {
		limit = packedProtectedPrefixBytes
	}

	for padCount := 0; padCount < 1+pc.rng.Intn(2); padCount++ {
		out = pc.appendForcedPadding(out)
	}

	gap := pc.nextProtectedPrefixGap()
	effective := 0
	for i := 0; i < limit; i++ {
		pc.bitBuf = (pc.bitBuf << 8) | uint64(p[i])
		pc.bitCount += 8
		for pc.bitCount >= 6 {
			pc.bitCount -= 6
			group := byte(pc.bitBuf >> pc.bitCount)
			if pc.bitCount == 0 {
				pc.bitBuf = 0
			} else {
				pc.bitBuf &= (1 << pc.bitCount) - 1
			}
			out = pc.appendGroup(out, group&0x3F)
		}

		effective++
		if effective >= gap {
			out = pc.appendForcedPadding(out)
			effective = 0
			gap = pc.nextProtectedPrefixGap()
		}
	}

	return out, limit
}

// Write encodes bytes into 6-bit groups and writes the corresponding hint bytes.
func (pc *PackedConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	// 1. Pre-allocate memory to avoid repeated append expansions.
	// Estimate: original data * 1.5 (4/3 + padding margin)
	needed := len(p)*3/2 + 32
	if cap(pc.writeBuf) < needed {
		pc.writeBuf = make([]byte, 0, needed)
	}
	out := pc.writeBuf[:0]

	prefixN := 0
	out, prefixN = pc.writeProtectedPrefix(out, p)

	i := 0
	n := len(p)
	if prefixN > 0 {
		i = prefixN
	}

	// 2. Header alignment (Slow Path)
	for pc.bitCount > 0 && i < n {
		out = pc.maybeAddPadding(out)
		b := p[i]
		i++
		pc.bitBuf = (pc.bitBuf << 8) | uint64(b)
		pc.bitCount += 8
		for pc.bitCount >= 6 {
			pc.bitCount -= 6
			group := byte(pc.bitBuf >> pc.bitCount)
			if pc.bitCount == 0 {
				pc.bitBuf = 0
			} else {
				pc.bitBuf &= (1 << pc.bitCount) - 1
			}
			out = pc.appendGroup(out, group&0x3F)
		}
	}

	// 3. Fast batch processing (Fast Path) — process 12 bytes at a time → 16 encoded groups
	for i+11 < n {
		// Process 4 batches, each of 3 bytes
		for batch := 0; batch < 4; batch++ {
			b1, b2, b3 := p[i], p[i+1], p[i+2]
			i += 3

			g1 := (b1 >> 2) & 0x3F
			g2 := ((b1 & 0x03) << 4) | ((b2 >> 4) & 0x0F)
			g3 := ((b2 & 0x0F) << 2) | ((b3 >> 6) & 0x03)
			g4 := b3 & 0x3F

			out = pc.appendGroup(out, g1)
			out = pc.appendGroup(out, g2)
			out = pc.appendGroup(out, g3)
			out = pc.appendGroup(out, g4)
		}
	}

	// 4. Process remaining 3-byte blocks
	for i+2 < n {
		b1, b2, b3 := p[i], p[i+1], p[i+2]
		i += 3

		g1 := (b1 >> 2) & 0x3F
		g2 := ((b1 & 0x03) << 4) | ((b2 >> 4) & 0x0F)
		g3 := ((b2 & 0x0F) << 2) | ((b3 >> 6) & 0x03)
		g4 := b3 & 0x3F

		out = pc.appendGroup(out, g1)
		out = pc.appendGroup(out, g2)
		out = pc.appendGroup(out, g3)
		out = pc.appendGroup(out, g4)
	}

	// 5. Tail processing (Tail Path) — handle remaining 1 or 2 bytes
	for ; i < n; i++ {
		b := p[i]
		pc.bitBuf = (pc.bitBuf << 8) | uint64(b)
		pc.bitCount += 8
		for pc.bitCount >= 6 {
			pc.bitCount -= 6
			group := byte(pc.bitBuf >> pc.bitCount)
			if pc.bitCount == 0 {
				pc.bitBuf = 0
			} else {
				pc.bitBuf &= (1 << pc.bitCount) - 1
			}
			out = pc.appendGroup(out, group&0x3F)
		}
	}

	// 6. Flush residual bits
	if pc.bitCount > 0 {
		group := byte(pc.bitBuf << (6 - pc.bitCount))
		pc.bitBuf = 0
		pc.bitCount = 0
		out = pc.appendGroup(out, group&0x3F)
		out = append(out, pc.padMarker)
	}

	// Possibly append trailing padding
	out = pc.maybeAddPadding(out)

	// Send data
	if len(out) > 0 {
		pc.writeBuf = out[:0]
		return len(p), connutil.WriteFull(pc.Conn, out)
	}
	pc.writeBuf = out[:0]
	return len(p), nil
}

// Flush writes any residual bits left by partial writes.
func (pc *PackedConn) Flush() error {
	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	out := pc.writeBuf[:0]
	if pc.bitCount > 0 {
		group := byte(pc.bitBuf << (6 - pc.bitCount))
		pc.bitBuf = 0
		pc.bitCount = 0

		out = append(out, pc.encodeGroup(group&0x3F))
		out = append(out, pc.padMarker)
	}

	// Possibly append trailing padding
	out = pc.maybeAddPadding(out)

	if len(out) > 0 {
		pc.writeBuf = out[:0]
		return connutil.WriteFull(pc.Conn, out)
	}
	return nil
}

// Read decodes hint bytes back into the original byte stream.
func (pc *PackedConn) Read(p []byte) (int, error) {
	// 1. Return pending decoded data first
	if n, ok := drainPending(p, &pc.pendingData); ok {
		return n, nil
	}

	// 2. Keep reading until data is decoded or an error occurs
	for {
		nr, rErr := pc.reader.Read(pc.rawBuf)
		if nr > 0 {
			// Cache frequently accessed variables
			rBuf := pc.readBitBuf
			rBits := pc.readBits
			padMarker := pc.padMarker
			layout := pc.table.layout

			for _, b := range pc.rawBuf[:nr] {
				if !layout.isHint(b) {
					if b == padMarker {
						rBuf = 0
						rBits = 0
					}
					continue
				}

				group, ok := layout.decodeGroup(b)
				if !ok {
					return 0, ErrInvalidSudokuMapMiss
				}

				rBuf = (rBuf << 6) | uint64(group)
				rBits += 6

				if rBits >= 8 {
					rBits -= 8
					val := byte(rBuf >> rBits)
					pc.pendingData = append(pc.pendingData, val)
				}
			}

			pc.readBitBuf = rBuf
			pc.readBits = rBits
		}

		if rErr != nil {
			if rErr == io.EOF {
				pc.readBitBuf = 0
				pc.readBits = 0
			}
			if len(pc.pendingData) > 0 {
				break
			}
			return 0, rErr
		}

		if len(pc.pendingData) > 0 {
			break
		}
	}

	// 3. Return decoded data — avoid underlying array leaks
	n, _ := drainPending(p, &pc.pendingData)
	return n, nil
}

// getPaddingByte picks a random padding byte from the pool.
func (pc *PackedConn) getPaddingByte() byte {
	return pc.padPool[pc.rng.Intn(len(pc.padPool))]
}

// encodeGroup encodes a 6-bit group.
func (pc *PackedConn) encodeGroup(group byte) byte {
	return pc.table.layout.encodeGroup(group)
}
