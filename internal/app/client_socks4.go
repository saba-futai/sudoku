package app

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func handleClientSocks4(conn net.Conn, cfg *config.Config, _ *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	buf := make([]byte, 8)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}

	if buf[0] != 0x04 || buf[1] != 0x01 { // only CONNECT
		return
	}

	port := binary.BigEndian.Uint16(buf[2:4])
	ipBytes := buf[4:8]

	if _, err := readString(conn); err != nil {
		return
	}

	var destAddrStr string
	var destIP net.IP

	// SOCKS4a: 0.0.0.x => domain string after userid.
	if ipBytes[0] == 0 && ipBytes[1] == 0 && ipBytes[2] == 0 && ipBytes[3] != 0 {
		domain, err := readString(conn)
		if err != nil {
			return
		}
		destAddrStr = fmt.Sprintf("%s:%d", domain, port)
	} else {
		destIP = net.IP(ipBytes)
		destAddrStr = fmt.Sprintf("%s:%d", destIP.String(), port)
	}

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), destAddrStr, destIP, cfg, geoMgr, dialer)
	if !success {
		_, _ = conn.Write([]byte{0x00, 0x5B, 0, 0, 0, 0, 0, 0})
		return
	}

	_, _ = conn.Write([]byte{0x00, 0x5A, 0, 0, 0, 0, 0, 0})
	connutil.PipeConn(conn, targetConn)
}

func readString(r io.Reader) (string, error) {
	var buf []byte
	var b [1]byte
	for {
		if _, err := r.Read(b[:]); err != nil {
			return "", err
		}
		if b[0] == 0 {
			break
		}
		buf = append(buf, b[0])
	}
	return string(buf), nil
}
