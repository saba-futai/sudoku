/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package app

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func handleClientSocks4(conn net.Conn, cfg *config.Config, _ *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
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

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), destAddrStr, destIP, cfg, geoMgr, dialer, resolver)
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
