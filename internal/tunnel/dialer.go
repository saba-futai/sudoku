package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/hybrid"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// Dialer abstracts the logic for establishing a connection to the server.
type Dialer interface {
	Dial(destAddrStr string) (net.Conn, error)
}

// BaseDialer contains common logic for Sudoku connections.
type BaseDialer struct {
	Config     *config.Config
	Table      *sudoku.Table
	PrivateKey []byte
}

func (d *BaseDialer) dialBase() (net.Conn, error) {
	// Resolve server address with DNS concurrency and optimistic cache.
	resolveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverAddr, err := dnsutil.ResolveWithCache(resolveCtx, d.Config.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve server address failed: %w", err)
	}

	// 1. Establish base TCP connection
	rawRemote, err := net.DialTimeout("tcp", serverAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("dial server failed: %w", err)
	}

	// 2. Send HTTP mask
	if !d.Config.DisableHTTPMask {
		if err := httpmask.WriteRandomRequestHeader(rawRemote, d.Config.ServerAddress); err != nil {
			rawRemote.Close()
			return nil, fmt.Errorf("write http mask failed: %w", err)
		}
	}

	return ClientHandshake(rawRemote, d.Config, d.Table, d.PrivateKey)
}

// ClientHandshake upgrades a raw connection to a Sudoku connection
func ClientHandshake(conn net.Conn, cfg *config.Config, table *sudoku.Table, privateKey []byte) (net.Conn, error) {
	// 3. Sudoku encapsulation
	sConn := sudoku.NewConn(conn, table, cfg.PaddingMin, cfg.PaddingMax, false)

	// 4. Encryption
	cConn, err := crypto.NewAEADConn(sConn, cfg.Key, cfg.AEAD)
	if err != nil {

		return nil, fmt.Errorf("crypto setup failed: %w", err)
	}

	// 5. Handshake
	handshake := make([]byte, 16)
	binary.BigEndian.PutUint64(handshake[:8], uint64(time.Now().Unix()))

	if len(privateKey) > 0 {
		// Use deterministic nonce from Private Key
		hash := sha256.Sum256(privateKey)
		copy(handshake[8:], hash[:8])
	} else {
		// Fallback to random if no private key (legacy/server mode)
		if _, err := rand.Read(handshake[8:]); err != nil {
			return nil, fmt.Errorf("generate nonce failed: %w", err)
		}
	}

	if _, err := cConn.Write(handshake); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	return cConn, nil
}

func (d *BaseDialer) dialUoT() (net.Conn, error) {
	conn, err := d.dialBase()
	if err != nil {
		return nil, err
	}
	if err := WriteUoTPreface(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("uot preface failed: %w", err)
	}
	return conn, nil
}

// StandardDialer implements Dialer for standard Sudoku mode.
type StandardDialer struct {
	BaseDialer
}

func (d *StandardDialer) Dial(destAddrStr string) (net.Conn, error) {
	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}

	// Standard Mode: Write destination address directly
	if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}

	return cConn, nil
}

// DialUDPOverTCP establishes a UoT-capable tunnel for UDP proxying.
func (d *StandardDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}

// HybridDialer implements Dialer for Split (Mieru) mode.
type HybridDialer struct {
	BaseDialer
	Manager *hybrid.Manager
}

func (d *HybridDialer) Dial(destAddrStr string) (net.Conn, error) {
	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}

	// Split Mode Logic
	splitUUID := hybrid.GenerateUUID()

	// Send Split Flag (0xFF) + UUID
	magic := []byte{0xFF}
	uuidBytes := []byte(splitUUID)
	lenByte := byte(len(uuidBytes))

	if _, err := cConn.Write(magic); err != nil {
		cConn.Close()
		return nil, err
	}
	if _, err := cConn.Write([]byte{lenByte}); err != nil {
		cConn.Close()
		return nil, err
	}
	if _, err := cConn.Write(uuidBytes); err != nil {
		cConn.Close()
		return nil, err
	}

	// Establish Mieru Downlink
	mConn, err := d.Manager.DialMieruForDownlink(splitUUID)
	if err != nil {
		cConn.Close()
		return nil, fmt.Errorf("dial mieru failed: %w", err)
	}

	// Combine connections
	hybridConn := &hybrid.SplitConn{
		Conn:   cConn,
		Writer: cConn,
		Reader: mConn,
		CloseFn: func() error {
			e1 := cConn.Close()
			e2 := mConn.Close()
			if e1 != nil {
				return e1
			}
			return e2
		},
	}

	// Write destination address through Sudoku uplink
	if err := protocol.WriteAddress(hybridConn, destAddrStr); err != nil {
		hybridConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}

	return hybridConn, nil
}

// DialUDPOverTCP establishes a UoT tunnel when in hybrid (Mieru) mode.
func (d *HybridDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}
