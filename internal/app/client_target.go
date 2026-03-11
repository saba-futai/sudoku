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
	"context"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/logx"
)

var directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
	d := dnsutil.OutboundDialer(timeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return d.DialContext(ctx, network, addr)
}

func dialTarget(network string, src net.Addr, destAddrStr string, destIP net.IP, cfg *config.Config, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) (net.Conn, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	decision := decideRoute(ctx, cfg, geoMgr, destAddrStr, destIP, resolver)
	cancel()

	logRoute(network, src, destAddrStr, decision.match, decision.shouldProxy)

	if decision.shouldProxy {
		conn, err := dialer.Dial(destAddrStr)
		if err != nil {
			logx.Warnf("Proxy", "Dial Failed: %v", err)
			return nil, false
		}
		return conn, true
	}

	directAddr := strings.TrimSpace(decision.directAddr)
	if directAddr == "" {
		directAddr = destAddrStr
	}
	if resolvedAddr, err := resolveDirectAddr(resolver, directAddr); err == nil && strings.TrimSpace(resolvedAddr) != "" {
		directAddr = resolvedAddr
	}

	dConn, err := directDial("tcp", directAddr, 5*time.Second)
	if err != nil && strings.TrimSpace(destAddrStr) != "" && directAddr != destAddrStr {
		if resolvedDest, rerr := resolveDirectAddr(resolver, destAddrStr); rerr == nil && strings.TrimSpace(resolvedDest) != "" {
			destAddrStr = resolvedDest
		}
		dConn, err = directDial("tcp", destAddrStr, 5*time.Second)
	}
	if err != nil {
		logx.Warnf("Direct", "Dial Failed: %v", err)
		return nil, false
	}
	return dConn, true
}

func resolveDirectAddr(resolver *dnsutil.Resolver, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return addr, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return resolveWithCache(ctx, resolver, addr)
}
