package apis

import (
	"io"

	"github.com/saba-futai/sudoku/internal/protocol"
)

// ReadTargetAddress parses a single SOCKS5-style target address frame (host:port) from r.
func ReadTargetAddress(r io.Reader) (string, error) {
	addr, _, _, err := protocol.ReadAddress(r)
	return addr, err
}

// WriteTargetAddress writes a single SOCKS5-style target address frame (host:port) to w.
func WriteTargetAddress(w io.Writer, addr string) error {
	return protocol.WriteAddress(w, addr)
}
