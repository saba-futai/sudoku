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
package apis

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
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

func kipUserHashFromKey(key string) [8]byte {
	src := []byte(key)
	if _, err := crypto.RecoverPublicKey(key); err == nil {
		if keyBytes, decErr := hex.DecodeString(key); decErr == nil && len(keyBytes) > 0 {
			src = keyBytes
		}
	}
	sum := sha256.Sum256(src)
	var out [8]byte
	copy(out[:], sum[:8])
	return out
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
	pskC2S, pskS2C := tunnel.DerivePSKDirectionalBases(seed)
	return wrapClientConnWithBases(rawConn, cfg, table, pskC2S, pskS2C)
}

func wrapClientConnWithBases(rawConn net.Conn, cfg *ProtocolConfig, table *sudoku.Table, sendBase, recvBase []byte) (net.Conn, error) {
	obfsConn := buildClientObfsConn(rawConn, cfg, table)
	cConn, err := crypto.NewRecordConn(obfsConn, cfg.AEADMethod, sendBase, recvBase)
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("setup crypto failed: %w", err)
	}
	return cConn, nil
}

func upgradeClientConn(rawConn net.Conn, cfg *ProtocolConfig, table *sudoku.Table, seed string, postHandshake func(net.Conn) error) (net.Conn, error) {
	cConn, err := wrapClientConn(rawConn, cfg, table, seed)
	if err != nil {
		return nil, err
	}

	rc, _ := cConn.(*crypto.RecordConn)
	if rc == nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("unexpected conn type")
	}
	if _, err := tunnel.KIPHandshakeClient(rc, seed, kipUserHashFromKey(cfg.Key), tunnel.KIPFeatAll); err != nil {
		_ = cConn.Close()
		return nil, err
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
		var addrBuf bytes.Buffer
		if err := protocol.WriteAddress(&addrBuf, cfg.TargetAddress); err != nil {
			return fmt.Errorf("encode target address failed: %w", err)
		}
		if err := tunnel.WriteKIPMessage(conn, tunnel.KIPTypeOpenTCP, addrBuf.Bytes()); err != nil {
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
		case "stream", "poll", "auto", "ws":
			table, err := pickClientTable(cfg)
			if err != nil {
				return nil, err
			}
			earlyHandshake, err := tunnel.NewHTTPMaskClientEarlyHandshake(tunnel.EarlyCodecConfig{
				PSK:                seed,
				AEAD:               cfg.AEADMethod,
				EnablePureDownlink: cfg.EnablePureDownlink,
				PaddingMin:         cfg.PaddingMin,
				PaddingMax:         cfg.PaddingMax,
			}, table, kipUserHashFromKey(cfg.Key), tunnel.KIPFeatAll)
			if err != nil {
				return nil, err
			}

			conn, err := httpmask.DialTunnel(ctx, cfg.ServerAddress, httpmask.TunnelDialOptions{
				Mode:           cfg.HTTPMaskMode,
				TLSEnabled:     cfg.HTTPMaskTLSEnabled,
				HostOverride:   cfg.HTTPMaskHost,
				PathRoot:       cfg.HTTPMaskPathRoot,
				AuthKey:        seed,
				EarlyHandshake: earlyHandshake,
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
