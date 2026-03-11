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
	"math/rand"
	"net"
	"sync"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

const IOBufferSize = 32 * 1024

var perm4 = [24][4]byte{
	{0, 1, 2, 3},
	{0, 1, 3, 2},
	{0, 2, 1, 3},
	{0, 2, 3, 1},
	{0, 3, 1, 2},
	{0, 3, 2, 1},
	{1, 0, 2, 3},
	{1, 0, 3, 2},
	{1, 2, 0, 3},
	{1, 2, 3, 0},
	{1, 3, 0, 2},
	{1, 3, 2, 0},
	{2, 0, 1, 3},
	{2, 0, 3, 1},
	{2, 1, 0, 3},
	{2, 1, 3, 0},
	{2, 3, 0, 1},
	{2, 3, 1, 0},
	{3, 0, 1, 2},
	{3, 0, 2, 1},
	{3, 1, 0, 2},
	{3, 1, 2, 0},
	{3, 2, 0, 1},
	{3, 2, 1, 0},
}

type Conn struct {
	net.Conn
	table      *Table
	reader     *bufio.Reader
	recorder   *bytes.Buffer
	recording  bool
	recordLock sync.Mutex

	rawBuf      []byte
	pendingData []byte
	hintBuf     []byte

	rng              *rand.Rand
	paddingThreshold uint64
}

func (sc *Conn) CloseWrite() error {
	if sc == nil {
		return nil
	}
	return connutil.TryCloseWrite(sc.Conn)
}

func (sc *Conn) CloseRead() error {
	if sc == nil {
		return nil
	}
	return connutil.TryCloseRead(sc.Conn)
}

func NewConn(c net.Conn, table *Table, pMin, pMax int, record bool) *Conn {
	localRng := newSeededRand()

	sc := &Conn{
		Conn:             c,
		table:            table,
		reader:           bufio.NewReaderSize(c, IOBufferSize),
		rawBuf:           make([]byte, IOBufferSize),
		pendingData:      make([]byte, 0, 4096),
		hintBuf:          make([]byte, 0, 4),
		rng:              localRng,
		paddingThreshold: pickPaddingThreshold(localRng, pMin, pMax),
	}
	if record {
		sc.recorder = new(bytes.Buffer)
		sc.recording = true
	}
	return sc
}

func (sc *Conn) StopRecording() {
	sc.recordLock.Lock()
	sc.recording = false
	sc.recorder = nil
	sc.recordLock.Unlock()
}

func (sc *Conn) GetBufferedAndRecorded() []byte {
	if sc == nil {
		return nil
	}

	sc.recordLock.Lock()
	defer sc.recordLock.Unlock()

	var recorded []byte
	if sc.recorder != nil {
		recorded = sc.recorder.Bytes()
	}

	buffered := sc.reader.Buffered()
	if buffered > 0 {
		peeked, _ := sc.reader.Peek(buffered)
		full := make([]byte, len(recorded)+len(peeked))
		copy(full, recorded)
		copy(full[len(recorded):], peeked)
		return full
	}
	return recorded
}

func (sc *Conn) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	outCapacity := len(p) * 6
	out := make([]byte, 0, outCapacity)
	pads := sc.table.PaddingPool
	padLen := len(pads)

	for _, b := range p {
		if shouldPad(sc.rng, sc.paddingThreshold) {
			out = append(out, pads[sc.rng.Intn(padLen)])
		}

		puzzles := sc.table.EncodeTable[b]
		puzzle := puzzles[sc.rng.Intn(len(puzzles))]

		perm := perm4[sc.rng.Intn(len(perm4))]
		for _, idx := range perm {
			if shouldPad(sc.rng, sc.paddingThreshold) {
				out = append(out, pads[sc.rng.Intn(padLen)])
			}
			out = append(out, puzzle[idx])
		}
	}

	if shouldPad(sc.rng, sc.paddingThreshold) {
		out = append(out, pads[sc.rng.Intn(padLen)])
	}

	return len(p), connutil.WriteFull(sc.Conn, out)
}

func (sc *Conn) Read(p []byte) (n int, err error) {
	if n, ok := drainPending(p, &sc.pendingData); ok {
		return n, nil
	}

	for {
		if len(sc.pendingData) > 0 {
			break
		}

		nr, rErr := sc.reader.Read(sc.rawBuf)
		if nr > 0 {
			chunk := sc.rawBuf[:nr]
			sc.recordLock.Lock()
			if sc.recording {
				sc.recorder.Write(chunk)
			}
			sc.recordLock.Unlock()

			for _, b := range chunk {
				if !sc.table.layout.isHint(b) {
					continue
				}

				sc.hintBuf = append(sc.hintBuf, b)
				if len(sc.hintBuf) == 4 {
					key := packHintsToKey([4]byte{sc.hintBuf[0], sc.hintBuf[1], sc.hintBuf[2], sc.hintBuf[3]})
					val, ok := sc.table.DecodeMap[key]
					if !ok {
						return 0, ErrInvalidSudokuMapMiss
					}
					sc.pendingData = append(sc.pendingData, val)
					sc.hintBuf = sc.hintBuf[:0]
				}
			}
		}

		if rErr != nil {
			return 0, rErr
		}
		if len(sc.pendingData) > 0 {
			break
		}
	}

	n, _ = drainPending(p, &sc.pendingData)
	return n, nil
}
