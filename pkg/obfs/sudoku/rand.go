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
	crypto_rand "crypto/rand"
	"encoding/binary"
	"time"
)

type randomSource interface {
	Uint32() uint32
	Uint64() uint64
	Intn(n int) int
}

type sudokuRand struct {
	state uint64
}

func newSeededRand() *sudokuRand {
	seed := time.Now().UnixNano()
	var seedBytes [8]byte
	if _, err := crypto_rand.Read(seedBytes[:]); err == nil {
		seed = int64(binary.BigEndian.Uint64(seedBytes[:]))
	}
	return newSudokuRand(seed)
}

func newSudokuRand(seed int64) *sudokuRand {
	state := uint64(seed)
	if state == 0 {
		state = 0x9e3779b97f4a7c15
	}
	return &sudokuRand{state: state}
}

func (r *sudokuRand) Uint64() uint64 {
	if r == nil {
		return 0
	}
	r.state += 0x9e3779b97f4a7c15
	z := r.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *sudokuRand) Uint32() uint32 {
	return uint32(r.Uint64() >> 32)
}

func (r *sudokuRand) Intn(n int) int {
	if n <= 1 {
		return 0
	}
	return int((uint64(r.Uint32()) * uint64(n)) >> 32)
}
