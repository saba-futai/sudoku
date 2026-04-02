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
	"strings"
	"syscall"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
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

func RunClientPool(configs []*config.Config, tableSets [][]*sudoku.Table) {
	logx.InstallStd()

	runtimes, err := buildClientRuntimes(configs, tableSets)
	if err != nil {
		logx.Fatalf("Client", "Failed to initialize client nodes: %v", err)
	}
	primary := runtimes[0]
	for _, rt := range runtimes[1:] {
		if rt.Config.LocalPort != primary.Config.LocalPort {
			logx.Infof("Client", "Ignoring local_port=%d on %s; mixed listener stays on first local_port=%d",
				rt.Config.LocalPort, rt.NodeID, primary.Config.LocalPort)
		}
		if rt.Config.ProxyMode != primary.Config.ProxyMode {
			logx.Infof("Client", "Proxy mode on %s is %q, but runtime routing uses first config mode %q",
				rt.NodeID, rt.Config.ProxyMode, primary.Config.ProxyMode)
		}
	}

	resolver, err := dnsutil.NewResolver(dnsutil.RecommendedClientOptions())
	if err != nil {
		logx.Fatalf("Init", "Failed to build DNS resolver: %v", err)
	}

	dialer, err := buildOutboundDialer(runtimes)
	if err != nil {
		logx.Fatalf("Init", "Failed to build outbound dialer: %v", err)
	}

	startReverseClient(primary.Config, &primary.BaseDialer)

	routeMgrs := buildRouteManagers(primary.Config)

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", primary.Config.LocalPort))
	if err != nil {
		logx.Fatalf("Client", "%v", err)
	}

	nodeLabels := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		nodeLabels = append(nodeLabels, rt.NodeID)
	}
	logx.Infof("Client", "Client (Mixed) on :%d -> %s | Mode: %s | Rules: %d",
		primary.Config.LocalPort, strings.Join(nodeLabels, ", "), primary.Config.ProxyMode, len(primary.Config.RuleURLs))

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
	if len(primary.Tables) > 0 {
		primaryTable = primary.Tables[0]
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
		go handleMixedConn(c, primary.Config, primaryTable, routeMgrs, dialer, resolver)
	}
}

func handleMixedConn(c net.Conn, cfg *config.Config, table *sudoku.Table, routeMgrs *routeManagers, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		c.Close()
		return
	}

	pConn := &PeekConn{Conn: c, peeked: buf}

	switch buf[0] {
	case 0x05:
		handleClientSocks5(pConn, cfg, table, routeMgrs, dialer, resolver)
	case 0x04:
		handleClientSocks4(pConn, cfg, table, routeMgrs, dialer, resolver)
	default:
		handleHTTP(pConn, cfg, table, routeMgrs, dialer, resolver)
	}
}
