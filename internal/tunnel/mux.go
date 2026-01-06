package tunnel

import (
	"fmt"
	"io"
)

const (
	// MuxMagicByte marks a Sudoku tunnel connection that carries multiple target streams over a single tunnel.
	// It must not collide with protocol.AddrType* or UoTMagicByte.
	MuxMagicByte byte = 0xED
	muxVersion   byte = 0x01
)

func WriteMuxPreface(w io.Writer) error {
	if w == nil {
		return fmt.Errorf("nil writer")
	}
	_, err := w.Write([]byte{MuxMagicByte, muxVersion})
	return err
}

func readMuxPreface(r io.Reader) error {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return err
	}
	if b[0] != MuxMagicByte {
		return fmt.Errorf("invalid mux magic: %d", b[0])
	}
	if b[1] != muxVersion {
		return fmt.Errorf("unsupported mux version: %d", b[1])
	}
	return nil
}
