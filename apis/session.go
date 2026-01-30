package apis

import (
	"fmt"
	"io"
	"net"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

// SessionKind describes the payload type carried by a Sudoku tunnel connection after the handshake.
//
// The first payload byte selects the session mode:
//   - UoT:     [UoTMagicByte][version]...
//   - Mux:     [MuxMagicByte][version]...
//   - Reverse: [ReverseMagicByte][version]...
//   - Forward: SOCKS5-style target address
type SessionKind uint8

const (
	// SessionForward means the client will send a target address and then proxy TCP streams.
	SessionForward SessionKind = iota
	// SessionUoT means the tunnel carries UDP-over-TCP frames.
	SessionUoT
	// SessionMux means the tunnel carries a multiplexed stream session (single tunnel, multi-target).
	SessionMux
	// SessionReverse means the tunnel is used to register reverse proxy routes (server exposes client services).
	SessionReverse
)

// ServerHandshakeSessionAutoWithUserHash upgrades the connection and auto-detects the session kind.
//
// Returns:
//   - conn: the upgraded tunnel connection. For SessionUoT/SessionMux/SessionReverse, the magic byte is consumed.
//   - session: the detected session kind
//   - targetAddr: valid only when session==SessionForward
//   - userHash: per-user handshake identifier (if available)
func ServerHandshakeSessionAutoWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (conn net.Conn, session SessionKind, targetAddr string, userHash string, err error) {
	conn, userHash, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, SessionForward, "", "", err
	}

	first := []byte{0}
	if _, err := io.ReadFull(conn, first); err != nil {
		_ = conn.Close()
		return nil, SessionForward, "", "", fail(fmt.Errorf("read session preface failed: %w", err))
	}

	switch first[0] {
	case tunnel.UoTMagicByte:
		return conn, SessionUoT, "", userHash, nil
	case tunnel.MuxMagicByte:
		return conn, SessionMux, "", userHash, nil
	case tunnel.ReverseMagicByte:
		return conn, SessionReverse, "", userHash, nil
	default:
		// Put back the first byte for address decoding.
		tuned := &prebufferConn{Conn: conn, buf: first}
		addr, _, _, err := protocol.ReadAddress(tuned)
		if err != nil {
			_ = tuned.Close()
			return nil, SessionForward, "", "", fail(fmt.Errorf("read target address failed: %w", err))
		}
		return tuned, SessionForward, addr, userHash, nil
	}
}

// ServerHandshakeSessionAuto is like ServerHandshakeSessionAutoWithUserHash but omits the user hash.
func ServerHandshakeSessionAuto(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, SessionKind, string, error) {
	conn, session, targetAddr, _, err := ServerHandshakeSessionAutoWithUserHash(rawConn, cfg)
	return conn, session, targetAddr, err
}
