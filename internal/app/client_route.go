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
	"fmt"
	"net"
	"strings"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/logx"
)

var lookupIPsWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, host string) ([]net.IP, error) {
	if resolver != nil {
		return resolver.LookupIPs(ctx, host)
	}
	return dnsutil.LookupIPsWithCache(ctx, host)
}

var resolveWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, addr string) (string, error) {
	if resolver != nil {
		return resolver.Resolve(ctx, addr)
	}
	return dnsutil.ResolveWithCache(ctx, addr)
}

type routeDecision struct {
	shouldProxy bool
	match       string
	directAddr  string
}

func decideRoute(ctx context.Context, cfg *config.Config, geoMgr *geodata.Manager, destAddr string, destIP net.IP, resolver *dnsutil.Resolver) routeDecision {
	decision := routeDecision{shouldProxy: true, match: "MODE(global)", directAddr: destAddr}
	if cfg == nil {
		decision.match = "CFG(nil)"
		return decision
	}

	switch cfg.ProxyMode {
	case "direct":
		return routeDecision{shouldProxy: false, match: "MODE(direct)", directAddr: destAddr}
	case "global":
		return routeDecision{shouldProxy: true, match: "MODE(global)", directAddr: destAddr}
	case "pac":
		if geoMgr == nil {
			decision.match = "PAC(no-rules)"
			return decision
		}

		if ok, m := geoMgr.MatchCN(destAddr, destIP); ok {
			return routeDecision{shouldProxy: false, match: m.String(), directAddr: destAddr}
		}
		if destIP != nil {
			decision.match = "PAC/NONE"
			return decision
		}

		host, port, err := net.SplitHostPort(destAddr)
		if err != nil {
			decision.match = "PAC/ADDR_INVALID"
			return decision
		}

		if ctx == nil {
			ctx = context.Background()
		}
		ips, err := lookupIPsWithCache(ctx, resolver, host)
		if err != nil || len(ips) == 0 {
			decision.match = "PAC/DNS_FAIL"
			return decision
		}

		for _, ip := range ips {
			if ok, m := geoMgr.MatchCN(destAddr, ip); ok {
				return routeDecision{
					shouldProxy: false,
					match:       "DNS->" + m.String(),
					directAddr:  net.JoinHostPort(ip.String(), port),
				}
			}
		}

		decision.match = "PAC/NONE"
		return decision
	default:
		decision.match = "MODE(unknown)"
		return decision
	}
}

func logRoute(network string, src net.Addr, destAddr string, match string, shouldProxy bool) {
	srcStr := "<unknown>"
	if src != nil {
		srcStr = src.String()
	}
	action := "PROXY"
	if !shouldProxy {
		action = "DIRECT"
	}
	actionText := logx.Bold(logx.Magenta(action))
	if action == "DIRECT" {
		actionText = logx.Bold(logx.Green(action))
	}
	logx.Infof(strings.ToUpper(strings.TrimSpace(network)), "%s --> %s match %s using %s", srcStr, destAddr, logx.Yellow(match), actionText)
}

func resolveUDPAddr(ctx context.Context, addr string, resolver *dnsutil.Resolver) (*net.UDPAddr, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	resolved, err := resolveWithCache(ctx, resolver, addr)
	if err != nil {
		return nil, err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", resolved)
	if err != nil {
		return nil, err
	}
	if udpAddr != nil {
		udpAddr.IP = normalizeIP(udpAddr.IP)
	}
	return udpAddr, nil
}
