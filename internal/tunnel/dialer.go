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
	Tables     []*sudoku.Table
	PrivateKey []byte
}

func (d *BaseDialer) pickTable() (byte, *sudoku.Table, error) {
	if len(d.Tables) == 0 {
		return 0, nil, fmt.Errorf("no table configured")
	}
	if len(d.Tables) == 1 {
		return 0, d.Tables[0], nil
	}
	// Use crypto/rand to avoid shared global RNG in concurrent dialing.
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, nil, fmt.Errorf("random table pick failed: %w", err)
	}
	idx := int(b[0]) % len(d.Tables)
	return byte(idx), d.Tables[idx], nil
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

	tableID, table, err := d.pickTable()
	if err != nil {
		rawRemote.Close()
		return nil, err
	}
	return ClientHandshake(rawRemote, d.Config, table, tableID, d.PrivateKey)
}

// ClientHandshake upgrades a raw connection to a Sudoku connection
func ClientHandshake(conn net.Conn, cfg *config.Config, table *sudoku.Table, tableID byte, privateKey []byte) (net.Conn, error) {
	if !cfg.EnablePureDownlink && cfg.AEAD == "none" {
		return nil, fmt.Errorf("enable_pure_downlink=false requires AEAD")
	}

	// 3. Sudoku encapsulation
	obfsConn := buildObfsConnForClient(conn, table, cfg)

	// 4. Encryption
	cConn, err := crypto.NewAEADConn(obfsConn, cfg.Key, cfg.AEAD)
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
	handshake[8] = tableID

	if _, err := cConn.Write(handshake); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("handshake failed: %w", err)
	}

	modeByte := []byte{downlinkModeByte(cfg)}
	if _, err := cConn.Write(modeByte); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("write downlink mode failed: %w", err)
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
