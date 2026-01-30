package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
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

// DialBase establishes a Sudoku tunnel connection to the configured server (and optional chain hops),
// performing the handshake but not requesting any target address.
func (d *BaseDialer) DialBase() (net.Conn, error) {
	return d.dialBase()
}

func (d *BaseDialer) dialHTTPMaskTunnel(dialCtx context.Context, upgrade func(net.Conn) (net.Conn, error)) (net.Conn, error) {
	if d.Config == nil {
		return nil, fmt.Errorf("missing config")
	}
	opts := httpmask.TunnelDialOptions{
		Mode:         d.Config.HTTPMask.Mode,
		TLSEnabled:   d.Config.HTTPMask.TLS,
		HostOverride: d.Config.HTTPMask.Host,
		PathRoot:     d.Config.HTTPMask.PathRoot,
		AuthKey:      d.Config.Key,
		Upgrade:      upgrade,
		Multiplex:    d.Config.HTTPMask.Multiplex,
	}
	return httpmask.DialTunnel(dialCtx, d.Config.ServerAddress, opts)
}

func (d *BaseDialer) pickTable() (*sudoku.Table, error) {
	if len(d.Tables) == 0 {
		return nil, fmt.Errorf("no table configured")
	}
	if len(d.Tables) == 1 {
		return d.Tables[0], nil
	}
	// Use crypto/rand to avoid shared global RNG in concurrent dialing.
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("random table pick failed: %w", err)
	}
	idx := int(b[0]) % len(d.Tables)
	return d.Tables[idx], nil
}

func (d *BaseDialer) dialBase() (net.Conn, error) {
	if d.Config == nil {
		return nil, fmt.Errorf("missing config")
	}

	chainHops := []string(nil)
	if d.Config.Chain != nil && len(d.Config.Chain.Hops) > 0 {
		chainHops = d.Config.Chain.Hops
	}

	var baseConn net.Conn

	// HTTP tunnel (CDN-friendly) modes. The returned conn already strips HTTP headers.
	if d.Config.HTTPMaskTunnelEnabled() {
		dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		table, err := d.pickTable()
		if err != nil {
			return nil, err
		}
		conn, err := d.dialHTTPMaskTunnel(dialCtx, func(raw net.Conn) (net.Conn, error) {
			return ClientHandshake(raw, d.Config, table, d.PrivateKey)
		})
		if err != nil {
			return nil, fmt.Errorf("dial http tunnel failed: %w", err)
		}
		baseConn = conn
	} else {
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
		if !d.Config.HTTPMask.Disable {
			// Legacy HTTP mask (not CDN-compatible): write a fake HTTP/1.1 header then switch to raw stream.
			if err := httpmask.WriteRandomRequestHeaderWithPathRoot(rawRemote, d.Config.ServerAddress, d.Config.HTTPMask.PathRoot); err != nil {
				rawRemote.Close()
				return nil, fmt.Errorf("write http mask failed: %w", err)
			}
		}

		table, err := d.pickTable()
		if err != nil {
			rawRemote.Close()
			return nil, err
		}
		baseConn, err = ClientHandshake(rawRemote, d.Config, table, d.PrivateKey)
		if err != nil {
			return nil, err
		}
	}

	if len(chainHops) == 0 {
		return baseConn, nil
	}
	return d.chainUpgrade(baseConn, chainHops)
}

func (d *BaseDialer) dialTarget(destAddrStr string) (net.Conn, error) {
	if strings.TrimSpace(destAddrStr) == "" {
		return nil, fmt.Errorf("empty target address")
	}

	if d.Config != nil && d.Config.Chain != nil && len(d.Config.Chain.Hops) > 0 {
		cConn, err := d.dialBase()
		if err != nil {
			return nil, err
		}
		if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
			_ = cConn.Close()
			return nil, fmt.Errorf("write address failed: %w", err)
		}
		return cConn, nil
	}

	if d.Config.HTTPMaskTunnelEnabled() {
		dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		table, err := d.pickTable()
		if err != nil {
			return nil, err
		}

		conn, err := d.dialHTTPMaskTunnel(dialCtx, func(raw net.Conn) (net.Conn, error) {
			cConn, err := ClientHandshake(raw, d.Config, table, d.PrivateKey)
			if err != nil {
				return nil, err
			}
			if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
				_ = cConn.Close()
				return nil, fmt.Errorf("write address failed: %w", err)
			}
			return cConn, nil
		})
		if err != nil {
			return nil, fmt.Errorf("dial http tunnel failed: %w", err)
		}
		return conn, nil
	}

	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}
	return cConn, nil
}

func (d *BaseDialer) chainUpgrade(baseConn net.Conn, hops []string) (net.Conn, error) {
	conn := baseConn
	for _, hopAddr := range hops {
		if strings.TrimSpace(hopAddr) == "" {
			continue
		}

		// Ask the current hop to connect to the next hop, then immediately start the next handshake.
		if err := protocol.WriteAddress(conn, hopAddr); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("chain write hop address failed: %w", err)
		}

		// Inner hops always use direct Sudoku handshake over the already-established TCP stream.
		// We keep the legacy HTTP mask header optional for compatibility with servers that expect it.
		if d.Config != nil && !d.Config.HTTPMask.Disable {
			if err := httpmask.WriteRandomRequestHeaderWithPathRoot(conn, hopAddr, d.Config.HTTPMask.PathRoot); err != nil {
				_ = conn.Close()
				return nil, fmt.Errorf("chain write http mask failed: %w", err)
			}
		}

		table, err := d.pickTable()
		if err != nil {
			_ = conn.Close()
			return nil, err
		}

		nextConn, err := ClientHandshake(conn, d.Config, table, d.PrivateKey)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("chain handshake failed: %w", err)
		}
		conn = nextConn
	}

	return conn, nil
}

// ClientHandshake upgrades a raw connection to a Sudoku connection
func ClientHandshake(conn net.Conn, cfg *config.Config, table *sudoku.Table, privateKey []byte) (net.Conn, error) {
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
	return d.dialTarget(destAddrStr)
}

// DialUDPOverTCP establishes a UoT-capable tunnel for UDP proxying.
func (d *StandardDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}
