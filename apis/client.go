/*
Copyright (C) 2025 by ふたい <contact me via issue>

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
package apis

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func canonicalCryptoSeedKey(key string) string {
	if recoveredFromKey, err := crypto.RecoverPublicKey(key); err == nil {
		return crypto.EncodePoint(recoveredFromKey)
	}
	return key
}

func buildHandshakePayload(key string) [16]byte {
	var payload [16]byte
	binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().Unix()))
	src := []byte(key)
	if _, err := crypto.RecoverPublicKey(key); err == nil {
		if keyBytes, decErr := hex.DecodeString(key); decErr == nil && len(keyBytes) > 0 {
			src = keyBytes
		}
	}
	hash := sha256.Sum256(src)
	copy(payload[8:], hash[:8])
	return payload
}

func pickClientTable(cfg *ProtocolConfig) (*sudoku.Table, error) {
	candidates := cfg.tableCandidates()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no table configured")
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, fmt.Errorf("random table pick failed: %w", err)
	}
	idx := int(b[0]) % len(candidates)
	return candidates[idx], nil
}

func wrapClientConn(rawConn net.Conn, cfg *ProtocolConfig, table *sudoku.Table, seed string) (net.Conn, error) {
	obfsConn := buildClientObfsConn(rawConn, cfg, table)
	if strings.TrimSpace(seed) == "" {
		seed = cfg.Key
	}
	cConn, err := crypto.NewAEADConn(obfsConn, seed, cfg.AEADMethod)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("setup crypto failed: %w", err)
	}
	return cConn, nil
}

func upgradeClientConn(rawConn net.Conn, cfg *ProtocolConfig, table *sudoku.Table, seed string, postHandshake func(net.Conn) error) (net.Conn, error) {
	cConn, err := wrapClientConn(rawConn, cfg, table, seed)
	if err != nil {
		return nil, err
	}

	handshake := buildHandshakePayload(cfg.Key)
	if _, err := cConn.Write(handshake[:]); err != nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("send handshake failed: %w", err)
	}
	if _, err := cConn.Write([]byte{downlinkMode(cfg)}); err != nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("send downlink mode failed: %w", err)
	}

	if postHandshake != nil {
		if err := postHandshake(cConn); err != nil {
			_ = cConn.Close()
			return nil, err
		}
	}

	return cConn, nil
}

// Dial opens a Sudoku tunnel to cfg.ServerAddress and requests cfg.TargetAddress.
func Dial(ctx context.Context, cfg *ProtocolConfig) (net.Conn, error) {
	baseConn, err := establishBaseConn(ctx, cfg, func(c *ProtocolConfig) error { return c.ValidateClient() }, func(conn net.Conn) error {
		if err := protocol.WriteAddress(conn, cfg.TargetAddress); err != nil {
			return fmt.Errorf("send target address failed: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return baseConn, nil
}

// DialBase opens a Sudoku tunnel to cfg.ServerAddress and completes the handshake, but does not send a target address.
//
// This is useful for higher-level protocols built on top of the tunnel (e.g. mux sessions, reverse proxy sessions).
func DialBase(ctx context.Context, cfg *ProtocolConfig) (net.Conn, error) {
	return establishBaseConn(ctx, cfg, validateBaseClientConfig, nil)
}

func establishBaseConn(ctx context.Context, cfg *ProtocolConfig, validate func(*ProtocolConfig) error, postHandshake func(net.Conn) error) (net.Conn, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	seed := canonicalCryptoSeedKey(cfg.Key)

	// CDN-capable HTTP tunnel modes.
	var baseConn net.Conn
	if !cfg.DisableHTTPMask {
		switch strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMode)) {
		case "stream", "poll", "auto":
			table, err := pickClientTable(cfg)
			if err != nil {
				return nil, err
			}

			conn, err := httpmask.DialTunnel(ctx, cfg.ServerAddress, httpmask.TunnelDialOptions{
				Mode:         cfg.HTTPMaskMode,
				TLSEnabled:   cfg.HTTPMaskTLSEnabled,
				HostOverride: cfg.HTTPMaskHost,
				PathRoot:     cfg.HTTPMaskPathRoot,
				AuthKey:      seed,
				Upgrade: func(rawConn net.Conn) (net.Conn, error) {
					return upgradeClientConn(rawConn, cfg, table, seed, nil)
				},
				Multiplex: cfg.HTTPMaskMultiplex,
			})
			if err != nil {
				return nil, fmt.Errorf("dial http tunnel failed: %w", err)
			}
			baseConn = conn
		}
	}
	if baseConn != nil {
		if len(cfg.ChainHops) > 0 {
			chained, err := chainUpgradeConn(baseConn, cfg, seed)
			if err != nil {
				_ = baseConn.Close()
				return nil, err
			}
			baseConn = chained
		}

		if postHandshake != nil {
			if err := postHandshake(baseConn); err != nil {
				_ = baseConn.Close()
				return nil, err
			}
		}
		return baseConn, nil
	}

	resolvedAddr, err := dnsutil.ResolveWithCache(ctx, cfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve server address failed: %w", err)
	}

	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "tcp", resolvedAddr)
	if err != nil {
		return nil, fmt.Errorf("dial tcp failed: %w", err)
	}

	success := false
	defer func() {
		if !success {
			rawConn.Close()
		}
	}()

	if !cfg.DisableHTTPMask {
		if err := httpmask.WriteRandomRequestHeaderWithPathRoot(rawConn, cfg.ServerAddress, cfg.HTTPMaskPathRoot); err != nil {
			return nil, fmt.Errorf("write http mask failed: %w", err)
		}
	}

	table, err := pickClientTable(cfg)
	if err != nil {
		return nil, err
	}

	cConn, err := upgradeClientConn(rawConn, cfg, table, seed, nil)
	if err != nil {
		return nil, err
	}

	if len(cfg.ChainHops) > 0 {
		chained, err := chainUpgradeConn(cConn, cfg, seed)
		if err != nil {
			_ = cConn.Close()
			return nil, err
		}
		cConn = chained
	}

	if postHandshake != nil {
		if err := postHandshake(cConn); err != nil {
			_ = cConn.Close()
			return nil, err
		}
	}

	success = true
	return cConn, nil
}

func validateBaseClientConfig(cfg *ProtocolConfig) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if cfg.ServerAddress == "" {
		return fmt.Errorf("ServerAddress cannot be empty")
	}
	return cfg.Validate()
}

func chainUpgradeConn(conn net.Conn, cfg *ProtocolConfig, seed string) (net.Conn, error) {
	cur := conn
	for _, hopAddr := range cfg.ChainHops {
		hopAddr = strings.TrimSpace(hopAddr)
		if hopAddr == "" {
			return nil, fmt.Errorf("empty chain hop")
		}

		if err := protocol.WriteAddress(cur, hopAddr); err != nil {
			return nil, fmt.Errorf("chain write hop address failed: %w", err)
		}
		if !cfg.DisableHTTPMask {
			if err := httpmask.WriteRandomRequestHeaderWithPathRoot(cur, hopAddr, cfg.HTTPMaskPathRoot); err != nil {
				return nil, fmt.Errorf("chain write http mask failed: %w", err)
			}
		}

		table, err := pickClientTable(cfg)
		if err != nil {
			return nil, err
		}

		next, err := upgradeClientConn(cur, cfg, table, seed, nil)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
