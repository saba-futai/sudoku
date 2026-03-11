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
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

var (
	ErrInvalidSudokuMapMiss = errors.New("INVALID_SUDOKU_MAP_MISS")
)

type Table struct {
	EncodeTable [256][][4]byte
	DecodeMap   map[uint32]byte
	PaddingPool []byte
	IsASCII     bool // Marks the current encoding mode
	layout      *byteLayout
}

// NewTable initializes the obfuscation tables with built-in layouts.
// Equivalent to calling NewTableWithCustom(key, mode, "").
func NewTable(key string, mode string) *Table {
	t, err := NewTableWithCustom(key, mode, "")
	if err != nil {
		logx.Errorf("Init", "Failed to build table: %v", err)
		return nil
	}
	return t
}

// NewTableWithCustom initializes obfuscation tables using either predefined or custom layouts.
// mode: "prefer_ascii" or "prefer_entropy". If a custom pattern is provided, ASCII mode still takes precedence.
// The customPattern must contain 8 characters with exactly 2 x, 2 p, and 4 v (case-insensitive).
func NewTableWithCustom(key string, mode string, customPattern string) (*Table, error) {
	start := time.Now()

	layout, err := resolveLayout(mode, customPattern)
	if err != nil {
		return nil, err
	}

	t := &Table{
		DecodeMap: make(map[uint32]byte),
		IsASCII:   layout.name == "ascii",
		layout:    layout,
	}
	t.PaddingPool = append(t.PaddingPool, layout.paddingPool...)

	// Generate Sudoku grids
	grids := allGrids()
	h := sha256.New()
	h.Write([]byte(key))
	seed := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
	rng := rand.New(rand.NewSource(seed))

	shuffledGrids := make([]Grid, len(grids))
	copy(shuffledGrids, grids)
	rng.Shuffle(len(shuffledGrids), func(i, j int) {
		shuffledGrids[i], shuffledGrids[j] = shuffledGrids[j], shuffledGrids[i]
	})

	// Build encoding/decoding maps
	for byteVal := 0; byteVal < 256; byteVal++ {
		targetGrid := shuffledGrids[byteVal]
		for _, positions := range hintPositions {
			var rawParts [4]hintPart
			for i, pos := range positions {
				val := targetGrid[pos] // 1..4
				rawParts[i] = hintPart{val: val, pos: pos}
			}
			if !hasUniqueMatch(grids, rawParts) {
				continue
			}
			var currentHints [4]byte
			for i, p := range rawParts {
				currentHints[i] = t.layout.encodeHint(p.val-1, p.pos)
			}
			t.EncodeTable[byteVal] = append(t.EncodeTable[byteVal], currentHints)
			key := packHintsToKey(currentHints)
			t.DecodeMap[key] = byte(byteVal)
		}
	}
	logx.Infof("Init", "Sudoku Tables initialized (%s) in %v", layout.name, time.Since(start))
	return t, nil
}

func packHintsToKey(hints [4]byte) uint32 {
	// Sorting network for 4 elements (Bubble sort unrolled)
	// Swap if a > b
	if hints[0] > hints[1] {
		hints[0], hints[1] = hints[1], hints[0]
	}
	if hints[2] > hints[3] {
		hints[2], hints[3] = hints[3], hints[2]
	}
	if hints[0] > hints[2] {
		hints[0], hints[2] = hints[2], hints[0]
	}
	if hints[1] > hints[3] {
		hints[1], hints[3] = hints[3], hints[1]
	}
	if hints[1] > hints[2] {
		hints[1], hints[2] = hints[2], hints[1]
	}

	return uint32(hints[0])<<24 | uint32(hints[1])<<16 | uint32(hints[2])<<8 | uint32(hints[3])
}
