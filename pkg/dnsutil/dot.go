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
package dnsutil

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

var newDOTTLSConfig = func(serverName string) *tls.Config {
	return &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}
}

type tlsNameServer struct {
	host      string
	port      string
	bootstrap []string
	next      uint32
	fallback  lookupIPFunc
	timeout   time.Duration
}

func newTLSNameServer(srv ServerOptions, timeout time.Duration) (*tlsNameServer, error) {
	host, port, err := parseTLSEndpoint(srv.Address)
	if err != nil {
		return nil, err
	}
	return &tlsNameServer{
		host:      host,
		port:      port,
		bootstrap: normalizeBootstrapAddrs(srv.Bootstrap),
		timeout:   timeout,
		fallback: func(ctx context.Context, network, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, network, host)
		},
	}, nil
}

func (s *tlsNameServer) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, fmt.Errorf("empty host")
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return []net.IP{ip}, nil
	}
	if isLikelyLocalHostname(host) {
		return s.fallback(ctx, network, host)
	}

	qtype := dnsmessage.TypeA
	switch network {
	case "ip4":
		qtype = dnsmessage.TypeA
	case "ip6":
		qtype = dnsmessage.TypeAAAA
	default:
		return s.fallback(ctx, network, host)
	}

	query, err := buildDNSQuery(host, qtype)
	if err != nil {
		return nil, err
	}
	return s.lookupQuery(ctx, query, qtype)
}

func (s *tlsNameServer) lookupQuery(ctx context.Context, query []byte, qtype dnsmessage.Type) ([]net.IP, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := s.dialTLS(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else if s.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}

	var prefix [2]byte
	binary.BigEndian.PutUint16(prefix[:], uint16(len(query)))
	if _, err := conn.Write(prefix[:]); err != nil {
		return nil, err
	}
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}

	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return nil, err
	}
	respLen := int(binary.BigEndian.Uint16(prefix[:]))
	if respLen <= 0 {
		return nil, fmt.Errorf("empty dot response")
	}
	resp := make([]byte, respLen)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	return parseDNSAnswerIPs(resp, qtype)
}

func (s *tlsNameServer) dialTLS(ctx context.Context) (*tls.Conn, error) {
	targetHost := s.host
	if len(s.bootstrap) > 0 {
		idx := atomic.AddUint32(&s.next, 1)
		targetHost = s.bootstrap[int(idx-1)%len(s.bootstrap)]
	}
	raw, err := OutboundDialer(0).DialContext(ctx, "tcp", net.JoinHostPort(targetHost, s.port))
	if err != nil {
		return nil, err
	}

	cfg := newDOTTLSConfig(trimPortForHost(s.host))
	if cfg == nil {
		cfg = &tls.Config{
			ServerName: trimPortForHost(s.host),
			MinVersion: tls.VersionTLS12,
		}
	}
	tlsConn := tls.Client(raw, cfg)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return tlsConn, nil
}

func parseTLSEndpoint(address string) (host string, port string, err error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", "", fmt.Errorf("empty tls dns address")
	}
	host, port = splitHostPortDefault(address, "853")
	if strings.TrimSpace(host) == "" {
		return "", "", fmt.Errorf("tls dns server missing host")
	}
	return host, port, nil
}
