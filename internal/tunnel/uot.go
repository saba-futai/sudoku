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
package tunnel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

const (
	maxUoTPayload = 64 * 1024
)

// UoTDialer extends Dialer with the ability to bootstrap a UDP-over-TCP tunnel.
type UoTDialer interface {
	Dialer
	DialUDPOverTCP() (net.Conn, error)
}

// WriteUoTDatagram sends a single UDP datagram frame over the reliable tunnel.
func WriteUoTDatagram(w io.Writer, addr string, payload []byte) error {
	addrBuf := &bytes.Buffer{}
	if err := protocol.WriteAddress(addrBuf, addr); err != nil {
		return fmt.Errorf("encode address: %w", err)
	}

	if addrBuf.Len() > int(^uint16(0)) {
		return fmt.Errorf("address too long: %d", addrBuf.Len())
	}
	if len(payload) > int(^uint16(0)) {
		return fmt.Errorf("payload too large: %d", len(payload))
	}

	var header [4]byte
	binary.BigEndian.PutUint16(header[:2], uint16(addrBuf.Len()))
	binary.BigEndian.PutUint16(header[2:], uint16(len(payload)))
	if err := connutil.WriteFull(w, header[:]); err != nil {
		return err
	}
	if err := connutil.WriteFull(w, addrBuf.Bytes()); err != nil {
		return err
	}
	return connutil.WriteFull(w, payload)
}

// ReadUoTDatagram parses a single UDP datagram frame from the reliable tunnel.
func ReadUoTDatagram(r io.Reader) (string, []byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", nil, err
	}

	addrLen := int(binary.BigEndian.Uint16(header[:2]))
	payloadLen := int(binary.BigEndian.Uint16(header[2:]))

	if addrLen <= 0 || addrLen > maxUoTPayload {
		return "", nil, fmt.Errorf("invalid address length: %d", addrLen)
	}
	if payloadLen < 0 || payloadLen > maxUoTPayload {
		return "", nil, fmt.Errorf("invalid payload length: %d", payloadLen)
	}

	addrBuf := make([]byte, addrLen)
	if _, err := io.ReadFull(r, addrBuf); err != nil {
		return "", nil, err
	}

	var addr string
	if parsed, _, _, err := protocol.ReadAddress(bytes.NewReader(addrBuf)); err != nil {
		return "", nil, fmt.Errorf("decode address: %w", err)
	} else {
		addr = parsed
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		return "", nil, err
	}

	return addr, payload, nil
}

// HandleUoTServer bridges UDP packets over the already-upgraded tunnel connection.
func HandleUoTServer(conn net.Conn) error {
	pConn, err := net.ListenPacket("udp", "")
	if err != nil {
		return fmt.Errorf("listen udp for uot: %w", err)
	}

	errCh := make(chan error, 1)
	var once sync.Once

	closeAll := func(err error) {
		once.Do(func() {
			_ = conn.Close()
			_ = pConn.Close()
			errCh <- err
		})
	}

	go func() {
		buf := make([]byte, maxUoTPayload)
		for {
			n, addr, err := pConn.ReadFrom(buf)
			if err != nil {
				closeAll(err)
				return
			}
			if err := WriteUoTDatagram(conn, addr.String(), buf[:n]); err != nil {
				closeAll(err)
				return
			}
		}
	}()

	go func() {
		for {
			addrStr, payload, err := ReadUoTDatagram(conn)
			if err != nil {
				closeAll(err)
				return
			}
			udpAddr, err := net.ResolveUDPAddr("udp", addrStr)
			if err != nil {
				// Skip invalid destinations instead of failing the whole session.
				continue
			}
			if _, err := pConn.WriteTo(payload, udpAddr); err != nil {
				closeAll(err)
				return
			}
		}
	}()

	return <-errCh
}
