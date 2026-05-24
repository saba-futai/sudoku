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
	"bytes"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

const (
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

	recorder   *bytes.Buffer
	recording  atomic.Bool
	recordLock sync.Mutex

	// Read buffer
	rawBuf      []byte
	pendingData pendingBuffer // Decoded bytes not yet consumed by Read

	// Write buffer and state
	writeMu  sync.Mutex
	writeBuf []byte
	bitBuf   uint64 // pending bits (MSB-first)
	bitCount int    // number of valid pending bits in bitBuf

	// Read state
	readBitBuf uint64 // pending bits (MSB-first)
	readBits   int    // number of valid pending bits in readBitBuf

	// RNG and padding control — uses integer-threshold random, consistent with Conn
	rng              *sudokuRand
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
	return NewPackedConnWithRecord(c, table, pMin, pMax, false)
}

func NewPackedConnWithRecord(c net.Conn, table *Table, pMin, pMax int, record bool) *PackedConn {
	localRng := newSeededRand()

	pc := &PackedConn{
		Conn:             c,
		table:            table,
		reader:           bufio.NewReaderSize(c, PackedIOBufferSize),
		rawBuf:           make([]byte, PackedDecodeBufferSize),
		pendingData:      newPendingBuffer(4096),
		writeBuf:         make([]byte, 0, 4096),
		rng:              localRng,
		paddingThreshold: pickPaddingThreshold(localRng, pMin, pMax),
	}

	if table != nil && table.layout != nil {
		pc.padMarker = table.layout.padMarker
		for _, b := range table.PaddingPool {
			if b != pc.padMarker {
				pc.padPool = append(pc.padPool, b)
			}
		}
	}
	if len(pc.padPool) == 0 {
		pc.padPool = append(pc.padPool, pc.padMarker)
	}
	if record {
		pc.recorder = new(bytes.Buffer)
		pc.recording.Store(true)
	}
	return pc
}

func (pc *PackedConn) StopRecording() {
	if pc == nil {
		return
	}
	pc.recordLock.Lock()
	pc.recording.Store(false)
	pc.recorder = nil
	pc.recordLock.Unlock()
}

func (pc *PackedConn) GetBufferedAndRecorded() []byte {
	if pc == nil {
		return nil
	}

	pc.recordLock.Lock()
	defer pc.recordLock.Unlock()

	var recorded []byte
	if pc.recorder != nil {
		recorded = pc.recorder.Bytes()
	}
	if pc.reader == nil {
		return recorded
	}

	buffered := pc.reader.Buffered()
	if buffered > 0 {
		peeked, _ := pc.reader.Peek(buffered)
		full := make([]byte, len(recorded)+len(peeked))
		copy(full, recorded)
		copy(full[len(recorded):], peeked)
		return full
	}
	return recorded
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
			out = appendPackedGroup(out, pc.table.layout, pc.rng, pc.paddingThreshold, pc.padPool, group)
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

func appendPackedGroup(out []byte, layout *byteLayout, rng *sudokuRand, paddingThreshold uint64, padPool []byte, group byte) []byte {
	if paddingThreshold != 0 {
		u := rng.Uint32()
		if uint64(u) < paddingThreshold {
			out = append(out, padPool[fastIntnFromUint32(rng.Uint32(), len(padPool))])
		}
	}
	return append(out, layout.encodeGroup[group&0x3F])
}

func maybeAppendPackedPadding(out []byte, rng *sudokuRand, paddingThreshold uint64, padPool []byte) []byte {
	if paddingThreshold != 0 {
		u := rng.Uint32()
		if uint64(u) < paddingThreshold {
			out = append(out, padPool[fastIntnFromUint32(rng.Uint32(), len(padPool))])
		}
	}
	return out
}

// Write encodes bytes into 6-bit groups and writes the corresponding hint bytes.
func (pc *PackedConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if pc == nil || pc.Conn == nil || pc.table == nil || pc.table.layout == nil || pc.rng == nil || len(pc.padPool) == 0 {
		return 0, io.ErrClosedPipe
	}

	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	// 1. Pre-allocate memory to avoid repeated append expansions.
	needed := len(p)*3/2 + 32
	if pc.paddingThreshold == 0 {
		needed = ((len(p)+2)/3)*4 + 32
	}
	if cap(pc.writeBuf) < needed {
		pc.writeBuf = make([]byte, 0, needed)
	}
	out := pc.writeBuf[:0]
	layout := pc.table.layout
	rng := pc.rng
	paddingThreshold := pc.paddingThreshold
	padPool := pc.padPool

	prefixN := 0
	out, prefixN = pc.writeProtectedPrefix(out, p)

	i := 0
	n := len(p)
	if prefixN > 0 {
		i = prefixN
	}

	// 2. Header alignment (Slow Path)
	for pc.bitCount > 0 && i < n {
		out = maybeAppendPackedPadding(out, rng, paddingThreshold, padPool)
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
			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, group)
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

			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g1)
			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g2)
			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g3)
			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g4)
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

		out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g1)
		out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g2)
		out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g3)
		out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, g4)
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
			out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, group)
		}
	}

	// 6. Flush residual bits
	if pc.bitCount > 0 {
		group := byte(pc.bitBuf << (6 - pc.bitCount))
		pc.bitBuf = 0
		pc.bitCount = 0
		out = appendPackedGroup(out, layout, rng, paddingThreshold, padPool, group)
		out = append(out, pc.padMarker)
	}

	// Possibly append trailing padding
	out = maybeAppendPackedPadding(out, rng, paddingThreshold, padPool)

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
	if pc == nil || pc.Conn == nil || pc.table == nil || pc.table.layout == nil || pc.rng == nil || len(pc.padPool) == 0 {
		return io.ErrClosedPipe
	}

	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	out := pc.writeBuf[:0]
	if pc.bitCount > 0 {
		group := byte(pc.bitBuf << (6 - pc.bitCount))
		pc.bitBuf = 0
		pc.bitCount = 0

		out = append(out, pc.table.layout.groupByte(group&0x3F))
		out = append(out, pc.padMarker)
	}

	// Possibly append trailing padding
	out = maybeAppendPackedPadding(out, pc.rng, pc.paddingThreshold, pc.padPool)

	if len(out) > 0 {
		pc.writeBuf = out[:0]
		return connutil.WriteFull(pc.Conn, out)
	}
	return nil
}

// Read decodes hint bytes back into the original byte stream.
func (pc *PackedConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if pc == nil || pc.Conn == nil || pc.reader == nil || len(pc.rawBuf) == 0 || pc.table == nil || pc.table.layout == nil {
		return 0, io.ErrClosedPipe
	}

	// 1. Return pending decoded data first
	if n, ok := drainPending(p, &pc.pendingData); ok {
		return n, nil
	}

	// 2. Keep reading until data is decoded or an error occurs
	outN := 0
	for {
		nr, rErr := pc.reader.Read(pc.rawBuf[:packedReadSize(len(p)-outN, len(pc.rawBuf))])
		if nr > 0 {
			if pc.recording.Load() {
				pc.recordLock.Lock()
				if pc.recording.Load() && pc.recorder != nil {
					pc.recorder.Write(pc.rawBuf[:nr])
				}
				pc.recordLock.Unlock()
			}

			// Cache frequently accessed variables
			rBuf := pc.readBitBuf
			rBits := pc.readBits
			padMarker := pc.padMarker
			layout := pc.table.layout

			chunk := pc.rawBuf[:nr]
			for i := 0; i < len(chunk); {
				if rBits == 0 && outN+3 <= len(p) && i+3 < len(chunk) &&
					layout.hintTable[chunk[i]] && layout.hintTable[chunk[i+1]] &&
					layout.hintTable[chunk[i+2]] && layout.hintTable[chunk[i+3]] {
					g1 := layout.decodeGroup[chunk[i]]
					g2 := layout.decodeGroup[chunk[i+1]]
					g3 := layout.decodeGroup[chunk[i+2]]
					g4 := layout.decodeGroup[chunk[i+3]]
					p[outN] = (g1 << 2) | (g2 >> 4)
					p[outN+1] = (g2 << 4) | (g3 >> 2)
					p[outN+2] = (g3 << 6) | g4
					outN += 3
					i += 4
					continue
				}

				b := chunk[i]
				i++
				if !layout.hintTable[b] {
					if b == padMarker {
						rBuf = 0
						rBits = 0
					}
					continue
				}

				group, ok := layout.decodePackedGroup(b)
				if !ok {
					return 0, ErrInvalidSudokuMapMiss
				}

				rBuf = (rBuf << 6) | uint64(group)
				rBits += 6

				if rBits >= 8 {
					rBits -= 8
					val := byte(rBuf >> rBits)
					outN = appendDecodedByte(p, outN, &pc.pendingData, val)
					if rBits == 0 {
						rBuf = 0
					} else {
						rBuf &= (uint64(1) << rBits) - 1
					}
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
			if outN > 0 {
				return outN, nil
			}
			if n, ok := drainPending(p, &pc.pendingData); ok {
				return n, nil
			}
			return 0, rErr
		}

		if outN > 0 {
			return outN, nil
		}
	}
}

// getPaddingByte picks a random padding byte from the pool.
func (pc *PackedConn) getPaddingByte() byte {
	return pc.padPool[pc.rng.Intn(len(pc.padPool))]
}

func packedReadSize(decodedRemaining, maxRaw int) int {
	if maxRaw <= minDecodeReadSize || decodedRemaining <= 0 {
		return maxRaw
	}
	if decodedRemaining > (maxRaw-minDecodeReadSize)/2 {
		return maxRaw
	}

	// Packed mode emits 4 wire hint bytes per 3 payload bytes, with optional
	// padding and a protected prefix. Avoid decoding an entire 64 KiB raw buffer
	// when the caller only needs an AEAD length/header-sized read.
	need := decodedRemaining*2 + minDecodeReadSize
	return need
}
