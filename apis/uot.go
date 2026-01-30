package apis

import (
	"context"
	"fmt"
	"io"
	"net"

	"github.com/saba-futai/sudoku/internal/tunnel"
)

// DialUDPOverTCP bootstraps a UDP-over-TCP tunnel using the standard Dial flow.
func DialUDPOverTCP(ctx context.Context, cfg *ProtocolConfig) (net.Conn, error) {
	conn, err := establishBaseConn(ctx, cfg, validateBaseClientConfig, nil)
	if err != nil {
		return nil, err
	}
	if err := tunnel.WriteUoTPreface(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write uot preface: %w", err)
	}
	return conn, nil
}

// DetectUoT peeks the first payload byte and returns a conn that can be used normally
// (with the byte re-inserted) when the stream is not a UoT session.
func DetectUoT(conn net.Conn) (bool, net.Conn, error) {
	first := []byte{0}
	if _, err := io.ReadFull(conn, first); err != nil {
		return false, conn, err
	}
	if first[0] != tunnel.UoTMagicByte {
		return false, &prebufferConn{Conn: conn, buf: first}, nil
	}
	return true, conn, nil
}

// HandleUoT runs the UDP-over-TCP loop on an upgraded tunnel connection.
func HandleUoT(conn net.Conn) error {
	return tunnel.HandleUoTServer(conn)
}

func WriteUoTDatagram(w io.Writer, addr string, payload []byte) error {
	return tunnel.WriteUoTDatagram(w, addr, payload)
}

func ReadUoTDatagram(r io.Reader) (string, []byte, error) {
	return tunnel.ReadUoTDatagram(r)
}

// prebufferConn replays buffered bytes before reading from the underlying connection.
type prebufferConn struct {
	net.Conn
	buf []byte
}

func (p *prebufferConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
