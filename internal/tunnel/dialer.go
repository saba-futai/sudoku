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
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
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

// DialBase establishes a Sudoku tunnel connection to the configured server,
// performing the handshake but not requesting any target address.
func (d *BaseDialer) DialBase() (net.Conn, error) {
	return d.dialBase()
}

func (d *BaseDialer) dialHTTPMaskTunnel(dialCtx context.Context, table *sudoku.Table, upgrade func(net.Conn) (net.Conn, error)) (net.Conn, error) {
	if d.Config == nil {
		return nil, fmt.Errorf("missing config")
	}
	earlyHandshake, err := NewHTTPMaskClientEarlyHandshake(EarlyCodecConfig{
		PSK:                d.Config.Key,
		AEAD:               d.Config.AEAD,
		EnablePureDownlink: d.Config.EnablePureDownlink,
		PaddingMin:         d.Config.PaddingMin,
		PaddingMax:         d.Config.PaddingMax,
	}, table, kipUserHashFromPrivateKey(d.PrivateKey, d.Config.Key), KIPFeatAll)
	if err != nil {
		return nil, err
	}
	opts := httpmask.TunnelDialOptions{
		Mode:           d.Config.HTTPMask.Mode,
		TLSEnabled:     d.Config.HTTPMask.TLS,
		HostOverride:   d.Config.HTTPMask.Host,
		PathRoot:       d.Config.HTTPMask.PathRoot,
		AuthKey:        d.Config.Key,
		EarlyHandshake: earlyHandshake,
		Upgrade: func(raw net.Conn) (net.Conn, error) {
			return upgrade(raw)
		},
		Multiplex: d.Config.HTTPMask.Multiplex,
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

	var baseConn net.Conn

	// HTTP tunnel (CDN-friendly) modes. The returned conn already strips HTTP headers.
	if d.Config.HTTPMaskTunnelEnabled() {
		dialCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		table, err := d.pickTable()
		if err != nil {
			return nil, err
		}
		conn, err := d.dialHTTPMaskTunnel(dialCtx, table, func(raw net.Conn) (net.Conn, error) {
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
		rawRemote, err := dnsutil.OutboundDialer(5*time.Second).Dial("tcp", serverAddr)
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

	return baseConn, nil
}

func (d *BaseDialer) dialTarget(destAddrStr string) (net.Conn, error) {
	if strings.TrimSpace(destAddrStr) == "" {
		return nil, fmt.Errorf("empty target address")
	}
	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}
	if err := writeKIPOpenTCP(cConn, destAddrStr); err != nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}
	return cConn, nil
}

func (d *BaseDialer) dialUoT() (net.Conn, error) {
	conn, err := d.dialBase()
	if err != nil {
		return nil, err
	}
	if err := WriteKIPMessage(conn, KIPTypeStartUoT, nil); err != nil {
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
