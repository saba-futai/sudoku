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
package app

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/logx"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

type PeekConn struct {
	net.Conn
	peeked []byte
}

func (c *PeekConn) CloseWrite() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseWrite(c.Conn)
}

func (c *PeekConn) CloseRead() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseRead(c.Conn)
}

func (c *PeekConn) Read(p []byte) (n int, err error) {
	if len(c.peeked) > 0 {
		n = copy(p, c.peeked)
		c.peeked = c.peeked[n:]
		return n, nil
	}
	if c.Conn == nil {
		return 0, io.EOF
	}
	return c.Conn.Read(p)
}

func normalizeClientKey(cfg *config.Config) ([]byte, bool, error) {
	pubKeyPoint, err := crypto.RecoverPublicKey(cfg.Key)
	if err != nil {
		return nil, false, nil
	}

	privateKeyBytes, err := hex.DecodeString(cfg.Key)
	if err != nil {
		return nil, false, fmt.Errorf("decode key: %w", err)
	}

	cfg.Key = crypto.EncodePoint(pubKeyPoint)
	return privateKeyBytes, true, nil
}

func RunClient(cfg *config.Config, tables []*sudoku.Table) {
	logx.InstallStd()
	var dialer tunnel.Dialer

	privateKeyBytes, changed, err := normalizeClientKey(cfg)
	if err != nil {
		logx.Fatalf("Client", "Failed to process key: %v", err)
	}
	if changed {
		logx.Infof("Init", "Derived Public Key: %s", cfg.Key)
	}

	if tables == nil || len(tables) == 0 || changed {
		var err error
		tables, err = BuildTables(cfg)
		if err != nil {
			logx.Fatalf("Init", "Failed to build table(s): %v", err)
		}
	}
	resolver, err := dnsutil.NewResolver(dnsutil.RecommendedClientOptions())
	if err != nil {
		logx.Fatalf("Init", "Failed to build DNS resolver: %v", err)
	}

	baseDialer := tunnel.BaseDialer{
		Config:     cfg,
		Tables:     tables,
		PrivateKey: privateKeyBytes,
	}

	if cfg.HTTPMaskSessionMuxEnabled() {
		dialer = &tunnel.MuxDialer{BaseDialer: baseDialer}
		logx.Infof("Init", "Enabled HTTPMask session mux (single tunnel, multi-target)")
	} else {
		dialer = &tunnel.StandardDialer{
			BaseDialer: baseDialer,
		}
	}

	startReverseClient(cfg, &baseDialer)

	var geoMgr *geodata.Manager
	if cfg.ProxyMode == "pac" {
		geoMgr = geodata.GetInstance(cfg.RuleURLs)
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		logx.Fatalf("Client", "%v", err)
	}
	logx.Infof("Client", "Client (Mixed) on :%d -> %s | Mode: %s | Rules: %d",
		cfg.LocalPort, cfg.ServerAddress, cfg.ProxyMode, len(cfg.RuleURLs))

	// Graceful shutdown on SIGINT / SIGTERM.
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logx.Infof("Client", "Shutting down...")
		close(stopCh)
		_ = l.Close()
	}()

	var primaryTable *sudoku.Table
	if len(tables) > 0 {
		primaryTable = tables[0]
	}
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-stopCh:
				return
			default:
				continue
			}
		}
		go handleMixedConn(c, cfg, primaryTable, geoMgr, dialer, resolver)
	}
}

func handleMixedConn(c net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		c.Close()
		return
	}

	pConn := &PeekConn{Conn: c, peeked: buf}

	switch buf[0] {
	case 0x05:
		handleClientSocks5(pConn, cfg, table, geoMgr, dialer, resolver)
	case 0x04:
		handleClientSocks4(pConn, cfg, table, geoMgr, dialer, resolver)
	default:
		handleHTTP(pConn, cfg, table, geoMgr, dialer, resolver)
	}
}
