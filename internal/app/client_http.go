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
	"bufio"
	"net"
	"net/http"
	"strings"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func handleHTTP(conn net.Conn, cfg *config.Config, _ *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
	defer conn.Close()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	host := ensureHostPort(req.Host, req.Method)
	destIP := net.ParseIP(hostOnly(host))

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), host, destIP, cfg, geoMgr, dialer, resolver)
	if !success {
		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	if req.Method == http.MethodConnect {
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		connutil.PipeConn(conn, targetConn)
		return
	}

	req.RequestURI = ""
	if req.URL != nil && req.URL.Scheme != "" {
		req.URL.Scheme = ""
		req.URL.Host = ""
	}

	if err := req.Write(targetConn); err != nil {
		_ = targetConn.Close()

		retryable := req.Body == nil || req.Body == http.NoBody
		if retryable && req.ContentLength <= 0 {
			if targetConn2, ok := dialTarget("TCP", conn.RemoteAddr(), host, destIP, cfg, geoMgr, dialer, resolver); ok {
				if err2 := req.Write(targetConn2); err2 == nil {
					connutil.PipeConn(conn, targetConn2)
					return
				}
				_ = targetConn2.Close()
			}
		}

		_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	connutil.PipeConn(conn, targetConn)
}

func defaultPortForMethod(method string) string {
	if method == http.MethodConnect {
		return "443"
	}
	return "80"
}

func ensureHostPort(host string, method string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}

	port := defaultPortForMethod(method)

	trimmed := host
	if strings.HasPrefix(trimmed, "[") {
		trimmed = strings.TrimPrefix(trimmed, "[")
	}
	if strings.HasSuffix(trimmed, "]") {
		trimmed = strings.TrimSuffix(trimmed, "]")
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return net.JoinHostPort(ip.String(), port)
	}
	if ip := net.ParseIP(host); ip != nil {
		return net.JoinHostPort(ip.String(), port)
	}
	return net.JoinHostPort(host, port)
}

func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
}
