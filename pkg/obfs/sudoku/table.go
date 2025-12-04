// pkg/obfs/sudoku/table.go
package sudoku

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/rand"
)

var (
	ErrInvalidSudokuMapMiss = errors.New("INVALID_SUDOKU_MAP_MISS")
)

type Table struct {
	EncodeTable [256][][4]byte
	DecodeMap   map[uint32]byte
	PaddingPool []byte
	IsASCII     bool // 标记当前模式
}

// NewTable initializes the obfuscation tables
// mode: "prefer_ascii" or "prefer_entropy"
func NewTable(key string, mode string) *Table {
	t := &Table{
		DecodeMap: make(map[uint32]byte),
		IsASCII:   mode == "prefer_ascii",
	}

	// 初始化填充池和编码逻辑
	if t.IsASCII {
		// === ASCII Mode (0x20 - 0x7F) ===
		// Payload (Hint): 01vvpppp (Bit 6 is 1) -> 0x40 | ...
		// Padding: 001xxxxx (Bit 6 is 0) -> 0x20 | rand(0-31)
		// Range: Padding [32, 63], Payload [64, 127]

		t.PaddingPool = make([]byte, 0, 32)
		for i := 0; i < 32; i++ {
			// 001xxxxx -> 0x20 + i
			t.PaddingPool = append(t.PaddingPool, byte(0x20+i))
		}
	} else {
		// === Entropy Mode (Legacy) ===
		// Padding: 0x80 (10000xxx) & 0x10 (00010xxx)
		// Payload: Avoids bits 7 and 4 being strictly defined like padding
		t.PaddingPool = make([]byte, 0, 16)
		for i := 0; i < 8; i++ {
			t.PaddingPool = append(t.PaddingPool, byte(0x80+i))
			t.PaddingPool = append(t.PaddingPool, byte(0x10+i))
		}
	}

	// 生成数独网格 (逻辑不变)
	allGrids := GenerateAllGrids()
	h := sha256.New()
	h.Write([]byte(key))
	seed := int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]))
	rng := rand.New(rand.NewSource(seed))

	shuffledGrids := make([]Grid, 288)
	copy(shuffledGrids, allGrids)
	rng.Shuffle(len(shuffledGrids), func(i, j int) {
		shuffledGrids[i], shuffledGrids[j] = shuffledGrids[j], shuffledGrids[i]
	})

	// 预计算组合
	var combinations [][]int
	var combine func(int, int, []int)
	combine = func(s, k int, c []int) {
		if k == 0 {
			tmp := make([]int, len(c))
			copy(tmp, c)
			combinations = append(combinations, tmp)
			return
		}
		for i := s; i <= 16-k; i++ {
			c = append(c, i)
			combine(i+1, k-1, c)
			c = c[:len(c)-1]
		}
	}
	combine(0, 4, []int{})

	// 构建映射表
	for byteVal := 0; byteVal < 256; byteVal++ {
		targetGrid := shuffledGrids[byteVal]
		for _, positions := range combinations {
			var currentHints [4]byte

			// 1. 计算抽象提示 (Abstract Hints)
			// 我们先计算出 val 和 pos，后面再根据模式编码成 byte
			var rawParts [4]struct{ val, pos byte }

			for i, pos := range positions {
				val := targetGrid[pos] // 1..4
				rawParts[i] = struct{ val, pos byte }{val, uint8(pos)}
			}

			// 检查唯一性 (数独逻辑)
			matchCount := 0
			for _, g := range allGrids {
				match := true
				for _, p := range rawParts {
					if g[p.pos] != p.val {
						match = false
						break
					}
				}
				if match {
					matchCount++
					if matchCount > 1 {
						break
					}
				}
			}

			if matchCount == 1 {
				// 唯一确定，生成最终编码字节
				for i, p := range rawParts {
					if t.IsASCII {
						// ASCII Encoding: 01vvpppp
						// vv = val-1 (0..3), pppp = pos (0..15)
						// 0x40 | (vv << 4) | pppp
						currentHints[i] = 0x40 | ((p.val - 1) << 4) | (p.pos & 0x0F)
					} else {
						// Entropy Encoding (Legacy)
						// 0vv0pppp
						// 格式: ((val-1) << 5) | pos
						currentHints[i] = ((p.val - 1) << 5) | (p.pos & 0x0F)
					}
				}

				t.EncodeTable[byteVal] = append(t.EncodeTable[byteVal], currentHints)
				// 生成解码键 (需要对 Hints 进行排序以忽略传输顺序)
				key := packHintsToKey(currentHints)
				t.DecodeMap[key] = byte(byteVal)
			}
		}
	}
	return t
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
