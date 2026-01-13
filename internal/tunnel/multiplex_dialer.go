package tunnel

import (
	"net"
)

type AdaptiveDialer struct {
	BaseDialer
}

func (d *AdaptiveDialer) Dial(destAddrStr string) (net.Conn, error) {
	return d.dialTarget(destAddrStr)
}

func (d *AdaptiveDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}
