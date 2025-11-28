// pkg/obfs/sudoku/conn.go
package sudoku

import (
	"bufio"
	"bytes"
	crypto_rand "crypto/rand"
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"sync"
)

const IOBufferSize = 32 * 1024

var (
	// bufferPool is a pool of 32KB buffers for IO operations
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, IOBufferSize)
		},
	}
	// outBufferPool is a pool for write buffers, starting small but can grow
	outBufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 0, 4096)
		},
	}
)

type Conn struct {
	net.Conn
	table      *Table
	reader     *bufio.Reader
	recorder   *bytes.Buffer
	recording  bool
	recordLock sync.Mutex

	rawBuf      []byte
	rawBufMu    sync.Mutex
	pendingData []byte
	hintBuf     []byte

	rng         *rand.Rand
	paddingRate float32
}

func NewConn(c net.Conn, table *Table, pMin, pMax int, record bool) *Conn {
	var seedBytes [8]byte
	if _, err := crypto_rand.Read(seedBytes[:]); err != nil {
		binary.BigEndian.PutUint64(seedBytes[:], uint64(rand.Int63()))
	}
	seed := int64(binary.BigEndian.Uint64(seedBytes[:]))
	localRng := rand.New(rand.NewSource(seed))

	min := float32(pMin) / 100.0
	rng := float32(pMax-pMin) / 100.0
	rate := min + localRng.Float32()*rng

	sc := &Conn{
		Conn:        c,
		table:       table,
		reader:      bufio.NewReaderSize(c, IOBufferSize),
		rawBuf:      bufferPool.Get().([]byte),
		pendingData: make([]byte, 0, 4096),
		hintBuf:     make([]byte, 0, 4),
		rng:         localRng,
		paddingRate: rate,
	}
	if record {
		sc.recorder = new(bytes.Buffer)
		sc.recording = true
	}
	return sc
}

// Close closes the connection and returns buffers to the pool
func (sc *Conn) Close() error {
	sc.rawBufMu.Lock()
	if sc.rawBuf != nil {
		bufferPool.Put(sc.rawBuf)
		sc.rawBuf = nil
	}
	sc.rawBufMu.Unlock()
	return sc.Conn.Close()
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

	// Estimate capacity: 1 byte -> 4 hints + padding.
	// Conservative estimate: 6x expansion
	out := outBufferPool.Get().([]byte)
	out = out[:0] // Reset
	defer func() {
		if cap(out) <= 64*1024 { // Only put back if not too huge
			outBufferPool.Put(out)
		}
	}()

	pads := sc.table.PaddingPool
	padLen := len(pads)

	for _, b := range p {
		if sc.rng.Float32() < sc.paddingRate {
			out = append(out, pads[sc.rng.Intn(padLen)])
		}

		puzzles := sc.table.EncodeTable[b]
		if len(puzzles) == 0 {
			// Should not happen if table is initialized correctly
			continue
		}
		puzzle := puzzles[sc.rng.Intn(len(puzzles))]

		// Shuffle hints
		perm := []int{0, 1, 2, 3}
		sc.rng.Shuffle(4, func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })

		for _, idx := range perm {
			if sc.rng.Float32() < sc.paddingRate {
				out = append(out, pads[sc.rng.Intn(padLen)])
			}
			out = append(out, puzzle[idx])
		}
	}

	if sc.rng.Float32() < sc.paddingRate {
		out = append(out, pads[sc.rng.Intn(padLen)])
	}

	_, err = sc.Conn.Write(out)
	return len(p), err
}

func (sc *Conn) Read(p []byte) (n int, err error) {
	if len(sc.pendingData) > 0 {
		n = copy(p, sc.pendingData)
		if n == len(sc.pendingData) {
			sc.pendingData = sc.pendingData[:0]
		} else {
			sc.pendingData = sc.pendingData[n:]
		}
		return n, nil
	}

	for {
		if len(sc.pendingData) > 0 {
			break
		}

		sc.rawBufMu.Lock()

		buf := sc.rawBuf

		sc.rawBufMu.Unlock()
		if buf == nil {
			return 0, io.ErrClosedPipe
		}
		nr, rErr := sc.reader.Read(buf)
		if nr > 0 {
			chunk := buf[:nr]
			sc.recordLock.Lock()
			if sc.recording {
				sc.recorder.Write(chunk)
			}
			sc.recordLock.Unlock()

			for _, b := range chunk {
				isPadding := false

				if sc.table.IsASCII {
					// === ASCII Mode ===
					// Padding: 001xxxxx (Bit 6 is 0) -> (b & 0x40) == 0
					// Hint:    01vvpppp (Bit 6 is 1) -> (b & 0x40) != 0
					if (b & 0x40) == 0 {
						isPadding = true
					}
				} else {
					// === Entropy Mode ===
					// Padding: 0x80... or 0x10... -> (b & 0x90) != 0
					if (b & 0x90) != 0 {
						isPadding = true
					}
				}

				if isPadding {
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
			if rErr == io.EOF && len(sc.pendingData) > 0 {
				break
			}
			return 0, rErr
		}
		if len(sc.pendingData) > 0 {
			break
		}
	}

	n = copy(p, sc.pendingData)
	if n == len(sc.pendingData) {
		sc.pendingData = sc.pendingData[:0]
	} else {
		sc.pendingData = sc.pendingData[n:]
	}
	return n, nil
}
