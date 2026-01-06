package tunnel

import (
	"fmt"
	"net"

	"github.com/saba-futai/sudoku/internal/protocol"
)

type AdaptiveDialer struct {
	BaseDialer
}

func (d *AdaptiveDialer) Dial(destAddrStr string) (net.Conn, error) {
	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}

	if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}
	return cConn, nil
}

func (d *AdaptiveDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}
