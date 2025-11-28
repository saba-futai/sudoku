// pkg/obfs/sudoku/conn.go
package sudoku

import (
	"bufio"
	"bytes"
	crypto_rand "crypto/rand"
	"encoding/binary"
	"errors"
	"math/rand"
	"net"
	"sync"
)

const IOBufferSize = 32 * 1024

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
		rawBuf:      make([]byte, IOBufferSize),
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
		if sc.rng.Float32() < sc.paddingRate {
			out = append(out, pads[sc.rng.Intn(padLen)])
		}

		puzzles := sc.table.EncodeTable[b]
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

		nr, rErr := sc.reader.Read(sc.rawBuf)
		if nr > 0 {
			chunk := sc.rawBuf[:nr]
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
						return 0, errors.New("INVALID_SUDOKU_MAP_MISS")
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

	n = copy(p, sc.pendingData)
	if n == len(sc.pendingData) {
		sc.pendingData = sc.pendingData[:0]
	} else {
		sc.pendingData = sc.pendingData[n:]
	}
	return n, nil
}
