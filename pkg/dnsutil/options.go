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
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	defaultCacheTTL      = 10 * time.Minute
	defaultLookupTimeout = 5 * time.Second
	defaultHTTPSPath     = "/dns-query"
)

type Options struct {
	DisableCache       bool            `json:"disable_cache"`
	DisableExpire      bool            `json:"disable_expire"`
	CacheTTLSeconds    int             `json:"cache_ttl_seconds"`
	TimeoutMs          int             `json:"timeout_ms"`
	DisableBogusFilter bool            `json:"disable_bogus_filter"`
	BogusNets          []string        `json:"bogus_nets"`
	Servers            []ServerOptions `json:"servers"`
}

type ServerOptions struct {
	Type      string            `json:"type"`
	Address   string            `json:"address"`
	Path      string            `json:"path"`
	Bootstrap []string          `json:"bootstrap"`
	Headers   map[string]string `json:"headers"`
}

// RecommendedClientOptions is the built-in target-DNS policy used by the mixed proxy client.
func RecommendedClientOptions() Options {
	return Options{
		TimeoutMs: 3000,
		Servers: []ServerOptions{
			{
				Type:      "https",
				Address:   "dns.alidns.com",
				Path:      "/dns-query",
				Bootstrap: []string{"223.5.5.5", "223.6.6.6"},
			},
			{
				Type:      "https",
				Address:   "doh.pub",
				Path:      "/dns-query",
				Bootstrap: []string{"119.29.29.29", "119.28.28.28"},
			},
			{
				Type: "local",
			},
		},
	}
}

func (o Options) Normalized() Options {
	if o.CacheTTLSeconds < 0 {
		o.CacheTTLSeconds = 0
	}
	if o.TimeoutMs < 0 {
		o.TimeoutMs = 0
	}
	if len(o.BogusNets) > 0 {
		out := o.BogusNets[:0]
		for _, cidr := range o.BogusNets {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				out = append(out, cidr)
			}
		}
		o.BogusNets = out
	}
	if len(o.Servers) > 0 {
		out := o.Servers[:0]
		for _, srv := range o.Servers {
			srv.Type = normalizeServerType(srv.Type)
			srv.Address = strings.TrimSpace(srv.Address)
			srv.Path = normalizeHTTPSPath(srv.Path)
			srv.Bootstrap = normalizeBootstrapAddrs(srv.Bootstrap)
			if len(srv.Headers) > 0 {
				headers := make(map[string]string, len(srv.Headers))
				for k, v := range srv.Headers {
					k = strings.TrimSpace(k)
					v = strings.TrimSpace(v)
					if k == "" || v == "" {
						continue
					}
					headers[k] = v
				}
				srv.Headers = headers
			}
			if srv.Type == "https" || srv.Address != "" || len(srv.Bootstrap) > 0 {
				out = append(out, srv)
			}
		}
		o.Servers = out
	}
	return o
}

func (o Options) Validate() error {
	o = o.Normalized()
	for _, cidr := range o.BogusNets {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid dns bogus_nets entry %q: %w", cidr, err)
		}
	}
	for i, srv := range o.Servers {
		if err := validateServerOptions(srv); err != nil {
			return fmt.Errorf("invalid dns server[%d]: %w", i, err)
		}
	}
	return nil
}

func (o Options) cacheTTL() time.Duration {
	if o.CacheTTLSeconds > 0 {
		return time.Duration(o.CacheTTLSeconds) * time.Second
	}
	return defaultCacheTTL
}

func (o Options) timeout() time.Duration {
	if o.TimeoutMs > 0 {
		return time.Duration(o.TimeoutMs) * time.Millisecond
	}
	return defaultLookupTimeout
}

func normalizeServerType(serverType string) string {
	switch strings.ToLower(strings.TrimSpace(serverType)) {
	case "", "local", "system":
		return "local"
	case "https", "doh":
		return "https"
	case "tls", "dot":
		return "dot"
	default:
		return strings.ToLower(strings.TrimSpace(serverType))
	}
}

func normalizeHTTPSPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultHTTPSPath
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func validateServerOptions(srv ServerOptions) error {
	switch normalizeServerType(srv.Type) {
	case "local":
		return nil
	case "https":
		if strings.TrimSpace(srv.Address) == "" {
			return fmt.Errorf("https server requires address")
		}
		if _, _, _, err := parseHTTPSEndpoint(srv.Address, srv.Path); err != nil {
			return err
		}
		return validateBootstrapAddrs(srv.Bootstrap)
	case "dot":
		if strings.TrimSpace(srv.Address) == "" {
			return fmt.Errorf("dot server requires address")
		}
		if _, _, err := parseTLSEndpoint(srv.Address); err != nil {
			return err
		}
		return validateBootstrapAddrs(srv.Bootstrap)
	default:
		return fmt.Errorf("unsupported dns server type %q", srv.Type)
	}
}

func normalizeBootstrapAddrs(addrs []string) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := addrs[:0]
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr != "" {
			out = append(out, addr)
		}
	}
	return out
}

func validateBootstrapAddrs(addrs []string) error {
	for _, addr := range addrs {
		if parsed := net.ParseIP(strings.TrimSpace(addr)); parsed == nil {
			return fmt.Errorf("invalid bootstrap ip %q", addr)
		}
	}
	return nil
}

func parseHTTPSEndpoint(address string, path string) (host string, port string, reqPath string, err error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", "", "", fmt.Errorf("empty https dns address")
	}
	path = normalizeHTTPSPath(path)
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil {
			return "", "", "", fmt.Errorf("parse https dns address: %w", err)
		}
		if !strings.EqualFold(strings.TrimSpace(u.Scheme), "https") {
			return "", "", "", fmt.Errorf("https dns server requires https scheme")
		}
		if strings.TrimSpace(u.Host) == "" {
			return "", "", "", fmt.Errorf("https dns server missing host")
		}
		host, port = splitHostPortDefault(u.Host, "443")
		reqPath = normalizeHTTPSPath(u.Path)
		if reqPath == "/" {
			reqPath = path
		}
		return host, port, reqPath, nil
	}
	host, port = splitHostPortDefault(address, "443")
	if strings.TrimSpace(host) == "" {
		return "", "", "", fmt.Errorf("https dns server missing host")
	}
	return host, port, path, nil
}

func splitHostPortDefault(address string, defaultPort string) (string, string) {
	address = strings.TrimSpace(address)
	if host, port, err := net.SplitHostPort(address); err == nil {
		return host, port
	}
	if ip := net.ParseIP(strings.Trim(address, "[]")); ip != nil {
		return strings.Trim(address, "[]"), defaultPort
	}
	return address, defaultPort
}
