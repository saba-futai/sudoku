package sudoku

import (
	crypto_rand "crypto/rand"
	"encoding/binary"
	"math/rand"
	"time"
)

func newSeededRand() *rand.Rand {
	seed := time.Now().UnixNano()
	var seedBytes [8]byte
	if _, err := crypto_rand.Read(seedBytes[:]); err == nil {
		seed = int64(binary.BigEndian.Uint64(seedBytes[:]))
	}
	return rand.New(rand.NewSource(seed))
}
