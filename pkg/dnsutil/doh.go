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
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type httpsNameServer struct {
	host      string
	port      string
	path      string
	serverURL string
	bootstrap []string
	headers   map[string]string
	next      uint32
	client    *http.Client
	fallback  lookupIPFunc
}

func newHTTPSNameServer(srv ServerOptions, timeout time.Duration) (*httpsNameServer, error) {
	host, port, path, err := parseHTTPSEndpoint(srv.Address, srv.Path)
	if err != nil {
		return nil, err
	}
	bootstrap := normalizeBootstrapAddrs(srv.Bootstrap)
	ns := &httpsNameServer{
		host:      host,
		port:      port,
		path:      path,
		serverURL: "https://" + net.JoinHostPort(host, port) + path,
		bootstrap: bootstrap,
		headers:   cloneHeaders(srv.Headers),
		fallback: func(ctx context.Context, network, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, network, host)
		},
	}
	ns.client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			DisableCompression:    true,
			ResponseHeaderTimeout: timeout,
			TLSHandshakeTimeout:   minDuration(timeout, 10*time.Second),
			TLSClientConfig: &tls.Config{
				ServerName: trimPortForHost(host),
				MinVersion: tls.VersionTLS12,
			},
			DialContext: ns.dialContext,
		},
	}
	return ns, nil
}

func (s *httpsNameServer) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := OutboundDialer(0)
	if len(s.bootstrap) == 0 {
		return d.DialContext(ctx, network, net.JoinHostPort(s.host, s.port))
	}
	idx := atomic.AddUint32(&s.next, 1)
	ip := s.bootstrap[int(idx-1)%len(s.bootstrap)]
	return d.DialContext(ctx, network, net.JoinHostPort(ip, s.port))
}

func (s *httpsNameServer) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
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
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.serverURL, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("doh http %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseDNSAnswerIPs(body, qtype)
}

func buildDNSQuery(host string, qtype dnsmessage.Type) ([]byte, error) {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}
	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{RecursionDesired: true})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(dnsmessage.Question{
		Name:  name,
		Type:  qtype,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func parseDNSAnswerIPs(resp []byte, qtype dnsmessage.Type) ([]net.IP, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(resp)
	if err != nil {
		return nil, err
	}
	if header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("dns rcode=%s", header.RCode)
	}
	if err := parser.SkipAllQuestions(); err != nil {
		return nil, err
	}
	var ips []net.IP
	for {
		h, err := parser.AnswerHeader()
		if errors.Is(err, dnsmessage.ErrSectionDone) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch h.Type {
		case dnsmessage.TypeA:
			body, err := parser.AResource()
			if err != nil {
				return nil, err
			}
			if qtype == dnsmessage.TypeA {
				ips = append(ips, net.IP(body.A[:]))
			}
		case dnsmessage.TypeAAAA:
			body, err := parser.AAAAResource()
			if err != nil {
				return nil, err
			}
			if qtype == dnsmessage.TypeAAAA {
				ips = append(ips, net.IP(body.AAAA[:]))
			}
		default:
			if err := parser.SkipAnswer(); err != nil {
				return nil, err
			}
		}
	}
	return ips, nil
}

func cloneHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || v == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func trimPortForHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil && strings.TrimSpace(h) != "" {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

func isLikelyLocalHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]")))
	if host == "" {
		return false
	}
	if !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".localhost", ".localdomain", ".home.arpa", ".lan"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func minDuration(a time.Duration, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	if a < b {
		return a
	}
	return b
}
