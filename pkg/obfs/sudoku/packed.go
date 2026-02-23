package sudoku

import (
	"bufio"
	"io"
	"math/rand"
	"net"
	"sync"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

const (
	// RngBatchSize controls how many random numbers to batch per RNG pull (micro-optimization).
	RngBatchSize = 128
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

	// 读缓冲
	rawBuf      []byte
	pendingData []byte // 解码后尚未被 Read 取走的字节

	// 写缓冲与状态
	writeMu  sync.Mutex
	writeBuf []byte
	bitBuf   uint64 // pending bits (MSB-first)
	bitCount int    // number of valid pending bits in bitBuf

	// 读状态
	readBitBuf uint64 // pending bits (MSB-first)
	readBits   int    // number of valid pending bits in readBitBuf

	// 随机数与填充控制 - 使用整数阈值随机，与 Conn 一致
	rng              *rand.Rand
	paddingThreshold uint64 // 与 Conn 保持一致的随机概率模型
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

// Write encodes bytes into 6-bit groups and writes the corresponding hint bytes.
func (pc *PackedConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	pc.writeMu.Lock()
	defer pc.writeMu.Unlock()

	// 1. 预分配内存，避免 append 导致的多次扩容
	// 预估：原数据 * 1.5 (4/3 + padding 余量)
	needed := len(p)*3/2 + 32
	if cap(pc.writeBuf) < needed {
		pc.writeBuf = make([]byte, 0, needed)
	}
	out := pc.writeBuf[:0]

	i := 0
	n := len(p)

	// 2. 头部对齐处理 (Slow Path)
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

	// 3. 极速批量处理 (Fast Path) - 每次处理 12 字节 → 生成 16 个编码组
	for i+11 < n {
		// 处理 4 组，每组 3 字节
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

	// 4. 处理剩余的 3 字节块
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

	// 5. 尾部处理 (Tail Path) - 处理剩余的 1 或 2 个字节
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

	// 6. 处理残留位
	if pc.bitCount > 0 {
		group := byte(pc.bitBuf << (6 - pc.bitCount))
		pc.bitBuf = 0
		pc.bitCount = 0
		out = pc.appendGroup(out, group&0x3F)
		out = append(out, pc.padMarker)
	}

	// 尾部可能添加 padding
	out = pc.maybeAddPadding(out)

	// 发送数据
	if len(out) > 0 {
		_, err := pc.Conn.Write(out)
		pc.writeBuf = out[:0]
		return len(p), err
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

	// 尾部随机添加 padding
	out = pc.maybeAddPadding(out)

	if len(out) > 0 {
		_, err := pc.Conn.Write(out)
		pc.writeBuf = out[:0]
		return err
	}
	return nil
}

// Read decodes hint bytes back into the original byte stream.
func (pc *PackedConn) Read(p []byte) (int, error) {
	// 1. 优先返回待处理区的数据
	if n, ok := drainPending(p, &pc.pendingData); ok {
		return n, nil
	}

	// 2. 循环读取直到解出数据或出错
	for {
		nr, rErr := pc.reader.Read(pc.rawBuf)
		if nr > 0 {
			// 缓存频繁访问的变量
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

	// 3. 返回解码后的数据 - 优化：避免底层数组泄漏
	n, _ := drainPending(p, &pc.pendingData)
	return n, nil
}

// getPaddingByte 从 Pool 中随机取 Padding 字节
func (pc *PackedConn) getPaddingByte() byte {
	return pc.padPool[pc.rng.Intn(len(pc.padPool))]
}

// encodeGroup 编码 6-bit 组
func (pc *PackedConn) encodeGroup(group byte) byte {
	return pc.table.layout.encodeGroup(group)
}
