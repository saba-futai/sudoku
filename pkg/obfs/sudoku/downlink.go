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

// DownlinkWriter encodes payload bytes with the selected downlink codec.
type DownlinkWriter struct {
	dataWriter io.Writer
	writeMu    sync.Mutex
}

func NewDownlinkWriter(raw io.Writer, table *Table, pMin, pMax int) *DownlinkWriter {
	localRng := newSeededRand()
	return newDownlinkWriter(newSudokuDataWriter(raw, table, localRng, pMin, pMax))
}

func NewPackedDownlinkWriter(raw net.Conn, table *Table, pMin, pMax int) (*PackedConn, *DownlinkWriter) {
	packed := NewPackedConn(raw, table, pMin, pMax)
	return packed, newDownlinkWriter(packed)
}

func NewServerDownlinkWriter(raw net.Conn, table *Table, pMin, pMax int, pure bool) (io.Writer, []func() error) {
	if pure {
		return NewDownlinkWriter(raw, table, pMin, pMax), nil
	}

	packed, writer := NewPackedDownlinkWriter(raw, table, pMin, pMax)
	return writer, []func() error{packed.Flush}
}

func newDownlinkWriter(dataWriter io.Writer) *DownlinkWriter {
	return &DownlinkWriter{
		dataWriter: dataWriter,
	}
}

func (w *DownlinkWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if w == nil || w.dataWriter == nil {
		return 0, io.ErrClosedPipe
	}

	w.writeMu.Lock()
	defer w.writeMu.Unlock()

	return w.dataWriter.Write(p)
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
	if w == nil || w.writer == nil {
		return 0, io.ErrClosedPipe
	}
	if w.table == nil || w.table.layout == nil || w.rng == nil {
		return 0, io.ErrClosedPipe
	}
	w.writeBuf = encodeSudokuPayload(w.writeBuf[:0], w.table, w.rng, w.paddingThreshold, p)
	return len(p), connutil.WriteFull(w.writer, w.writeBuf)
}

func encodeSudokuPayload(dst []byte, table *Table, rng randomSource, paddingThreshold uint64, p []byte) []byte {
	if len(p) == 0 {
		return dst[:0]
	}
	if paddingThreshold == 0 {
		return encodeSudokuPayloadNoPadding(dst, table, rng, p)
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

func encodeSudokuPayloadNoPadding(dst []byte, table *Table, rng randomSource, p []byte) []byte {
	outCapacity := len(p) * 4
	if cap(dst) < outCapacity {
		dst = make([]byte, 0, outCapacity)
	}
	out := dst[:0]

	for _, b := range p {
		puzzles := table.EncodeTable[b]
		puzzle := puzzles[rng.Intn(len(puzzles))]
		perm := perm4[rng.Intn(len(perm4))]
		out = append(out, puzzle[perm[0]], puzzle[perm[1]], puzzle[perm[2]], puzzle[perm[3]])
	}
	return out
}
