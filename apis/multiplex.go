package apis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/pkg/multiplex"
)

const (
	MultiplexMagicByte byte = multiplex.MagicByte
	MultiplexVersion   byte = multiplex.Version
)

// DialMultiplex establishes a long-lived tunnel connection and upgrades it into a multiplex session.
//
// Each logical stream can then be used to dial a different target without paying the full (HTTP mask + Sudoku)
// connection RTT again.
//
// Server side: after finishing the normal Sudoku handshake, read 1 byte; if it equals multiplex.MagicByte,
// call AcceptMultiplexServer to start accepting streams.
func DialMultiplex(ctx context.Context, cfg *ProtocolConfig) (*MultiplexClient, error) {
	baseConn, err := establishBaseConn(ctx, cfg, func(c *ProtocolConfig) error {
		if c == nil {
			return fmt.Errorf("config is required")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if strings.TrimSpace(c.ServerAddress) == "" {
			return fmt.Errorf("ServerAddress cannot be empty")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	success := false
	defer func() {
		if !success {
			_ = baseConn.Close()
		}
	}()

	if err := multiplex.WritePreface(baseConn); err != nil {
		return nil, fmt.Errorf("write multiplex preface failed: %w", err)
	}

	sess, err := multiplex.NewClientSession(baseConn)
	if err != nil {
		return nil, fmt.Errorf("start multiplex session failed: %w", err)
	}

	success = true
	return &MultiplexClient{sess: sess}, nil
}

type MultiplexClient struct {
	sess *multiplex.Session
}

// Dial opens a new logical stream, writes the target address, and returns the stream as net.Conn.
func (c *MultiplexClient) Dial(ctx context.Context, targetAddress string) (net.Conn, error) {
	if c == nil || c.sess == nil || c.sess.IsClosed() {
		return nil, fmt.Errorf("multiplex session is closed")
	}
	if strings.TrimSpace(targetAddress) == "" {
		return nil, fmt.Errorf("TargetAddress cannot be empty")
	}

	stream, err := c.sess.OpenStream()
	if err != nil {
		return nil, err
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetWriteDeadline(deadline)
		defer stream.SetWriteDeadline(time.Time{})
	}
	if err := WriteTargetAddress(stream, targetAddress); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("send target address failed: %w", err)
	}

	return stream, nil
}

func (c *MultiplexClient) Close() error {
	if c == nil || c.sess == nil {
		return nil
	}
	return c.sess.Close()
}

// AcceptMultiplexServer upgrades a server-side, already-handshaked Sudoku connection into a multiplex session.
//
// The caller must have already read and matched the initial magic byte (multiplex.MagicByte); this function
// consumes the multiplex version byte and starts the session.
func AcceptMultiplexServer(conn net.Conn) (*MultiplexServer, error) {
	if conn == nil {
		return nil, fmt.Errorf("nil conn")
	}
	v, err := multiplex.ReadVersion(conn)
	if err != nil {
		return nil, err
	}
	if err := multiplex.ValidateVersion(v); err != nil {
		return nil, err
	}
	sess, err := multiplex.NewServerSession(conn)
	if err != nil {
		return nil, err
	}
	return &MultiplexServer{sess: sess}, nil
}

// MultiplexServer wraps a multiplex session created from a handshaked Sudoku tunnel connection.
type MultiplexServer struct {
	sess *multiplex.Session
}

func (s *MultiplexServer) AcceptStream() (net.Conn, error) {
	if s == nil || s.sess == nil {
		return nil, fmt.Errorf("nil session")
	}
	return s.sess.AcceptStream()
}

// AcceptTCP accepts a multiplex stream and reads the target address preface, returning the stream
// positioned at application data.
func (s *MultiplexServer) AcceptTCP() (net.Conn, string, error) {
	stream, err := s.AcceptStream()
	if err != nil {
		return nil, "", err
	}

	target, err := ReadTargetAddress(stream)
	if err != nil {
		_ = stream.Close()
		return nil, "", err
	}
	return stream, target, nil
}

func (s *MultiplexServer) Close() error {
	if s == nil || s.sess == nil {
		return nil
	}
	return s.sess.Close()
}

func (s *MultiplexServer) IsClosed() bool {
	if s == nil || s.sess == nil {
		return true
	}
	return s.sess.IsClosed()
}
