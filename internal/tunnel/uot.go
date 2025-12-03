package tunnel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/saba-futai/sudoku/internal/protocol"
)

const (
	// UoTMagicByte marks a Sudoku tunnel connection that carries UDP-over-TCP traffic.
	UoTMagicByte byte = 0xEE
	uotVersion        = 0x01

	maxUoTPayload = 64 * 1024
)

// UoTDialer extends Dialer with the ability to bootstrap a UDP-over-TCP tunnel.
type UoTDialer interface {
	Dialer
	DialUDPOverTCP() (net.Conn, error)
}

// WriteUoTPreface writes the UDP-over-TCP marker and version.
func WriteUoTPreface(w io.Writer) error {
	_, err := w.Write([]byte{UoTMagicByte, uotVersion})
	return err
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

	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[:2], uint16(addrBuf.Len()))
	binary.BigEndian.PutUint16(header[2:], uint16(len(payload)))

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(addrBuf.Bytes()); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
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
	versionBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, versionBuf); err != nil {
		return fmt.Errorf("read uot version: %w", err)
	}
	if versionBuf[0] != uotVersion {
		return fmt.Errorf("unsupported uot version: %d", versionBuf[0])
	}

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
