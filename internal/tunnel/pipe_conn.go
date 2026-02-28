package tunnel

import (
	"net"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

// PipeConn copies data bidirectionally between a and b, then closes both.
func PipeConn(a, b net.Conn) {
	connutil.PipeConn(a, b)
}
