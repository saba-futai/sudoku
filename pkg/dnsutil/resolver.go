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
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

type lookupIPFunc func(ctx context.Context, network, host string) ([]net.IP, error)

type nameServer interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

type cacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
}

type Resolver struct {
	mu      sync.RWMutex
	cache   map[string]cacheEntry
	ttl     time.Duration
	timeout time.Duration

	disableCache       bool
	disableExpire      bool
	disableBogusFilter bool
	bogusNets          []*net.IPNet
	servers            []nameServer
}

type localNameServer struct {
	lookupFn lookupIPFunc
}

func (s localNameServer) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	if s.lookupFn != nil {
		return s.lookupFn(ctx, network, host)
	}
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

var (
	defaultResolverMu sync.RWMutex
	defaultResolver   = newResolver(defaultCacheTTL, nil)
)

func NewResolver(opts Options) (*Resolver, error) {
	opts = opts.Normalized()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	servers, err := buildNameServers(opts)
	if err != nil {
		return nil, err
	}
	bogusNets, err := parseBogusNets(opts)
	if err != nil {
		return nil, err
	}
	return &Resolver{
		cache:              make(map[string]cacheEntry),
		ttl:                opts.cacheTTL(),
		timeout:            opts.timeout(),
		disableCache:       opts.DisableCache,
		disableExpire:      opts.DisableExpire,
		disableBogusFilter: opts.DisableBogusFilter,
		bogusNets:          bogusNets,
		servers:            servers,
	}, nil
}

func newResolver(ttl time.Duration, fn lookupIPFunc) *Resolver {
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	if fn == nil {
		fn = func(ctx context.Context, network, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, network, host)
		}
	}
	return &Resolver{
		cache:   make(map[string]cacheEntry),
		ttl:     ttl,
		timeout: defaultLookupTimeout,
		servers: []nameServer{localNameServer{lookupFn: fn}},
	}
}

func Default() *Resolver {
	defaultResolverMu.RLock()
	r := defaultResolver
	defaultResolverMu.RUnlock()
	if r == nil {
		return newResolver(defaultCacheTTL, nil)
	}
	return r
}

func ResolveWithCache(ctx context.Context, addr string) (string, error) {
	return Default().Resolve(ctx, addr)
}

func LookupIPsWithCache(ctx context.Context, host string) ([]net.IP, error) {
	return Default().LookupIPs(ctx, host)
}

func (r *Resolver) Resolve(ctx context.Context, addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		return addr, nil
	}
	ips, err := r.LookupIPs(ctx, host)
	if err != nil {
		return "", err
	}
	selected := pickPreferredIP(ips)
	if selected == nil {
		return "", fmt.Errorf("no usable ip found for host %s", host)
	}
	return net.JoinHostPort(selected.String(), port), nil
}

func (r *Resolver) LookupIPs(ctx context.Context, host string) ([]net.IP, error) {
	hostKey, hostIP := normalizeHost(host)
	if hostIP != nil {
		return []net.IP{hostIP}, nil
	}
	if hostKey == "" {
		return nil, fmt.Errorf("empty host")
	}

	now := time.Now()
	cachedIPs, expired := r.lookup(hostKey, now)
	if len(cachedIPs) > 0 && !expired {
		return copyIPs(cachedIPs), nil
	}

	ips, err := r.lookupFresh(ctx, hostKey)
	if err != nil {
		if len(cachedIPs) > 0 && !r.disableExpire {
			return copyIPs(cachedIPs), nil
		}
		return nil, fmt.Errorf("dns lookup failed for %s: %w", hostKey, err)
	}
	ips = r.filterIPs(normalizeIPs(ips))
	if len(ips) == 0 {
		if len(cachedIPs) > 0 && !r.disableExpire {
			return copyIPs(cachedIPs), nil
		}
		return nil, fmt.Errorf("no usable ip found for host %s", hostKey)
	}
	r.store(hostKey, ips, now)
	return copyIPs(ips), nil
}

func (r *Resolver) lookup(hostKey string, now time.Time) ([]net.IP, bool) {
	if r == nil || r.disableCache {
		return nil, false
	}
	r.mu.RLock()
	entry, ok := r.cache[hostKey]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if now.After(entry.expiresAt) {
		return entry.ips, true
	}
	return entry.ips, false
}

func (r *Resolver) store(hostKey string, ips []net.IP, now time.Time) {
	if r == nil || r.disableCache {
		return
	}
	ips = normalizeIPs(ips)
	if len(ips) == 0 {
		return
	}
	r.mu.Lock()
	r.cache[hostKey] = cacheEntry{
		ips:       copyIPs(ips),
		expiresAt: now.Add(r.ttl),
	}
	r.mu.Unlock()
}

func (r *Resolver) lookupFresh(ctx context.Context, host string) ([]net.IP, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok && r != nil && r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	var firstErr error
	servers := r.servers
	if len(servers) == 0 {
		servers = []nameServer{localNameServer{}}
	}
	for _, srv := range servers {
		ips, err := r.lookupOnServer(ctx, srv, host)
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("no ip records found")
	}
	return nil, firstErr
}

func (r *Resolver) lookupOnServer(ctx context.Context, srv nameServer, host string) ([]net.IP, error) {
	type result struct {
		ips []net.IP
		err error
	}

	networks := []string{"ip4"}
	ch := make(chan result, len(networks))

	var wg sync.WaitGroup
	for _, network := range networks {
		network := network
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips, err := srv.LookupIP(ctx, network, host)
			select {
			case ch <- result{ips: ips, err: err}:
			case <-ctx.Done():
			}
		}()
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	var allIPs []net.IP
	var firstErr error
	for res := range ch {
		if res.err == nil && len(res.ips) > 0 {
			allIPs = append(allIPs, res.ips...)
		} else if res.err != nil && firstErr == nil {
			firstErr = res.err
		}
	}
	if len(allIPs) == 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("no ip records found")
		}
		return nil, firstErr
	}
	return r.filterIPs(normalizeIPs(allIPs)), nil
}

func normalizeHost(host string) (string, net.IP) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", nil
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	host = strings.ToLower(host)
	if ip := net.ParseIP(host); ip != nil {
		return host, ip
	}
	return host, nil
}

func normalizeIPs(ips []net.IP) []net.IP {
	if len(ips) == 0 {
		return nil
	}
	out := make([]net.IP, 0, len(ips))
	seen := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			key := ip4.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ip4)
			continue
		}
		if ip16 := ip.To16(); ip16 != nil {
			key := ip16.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, ip16)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func pickPreferredIP(ips []net.IP) net.IP {
	ips = normalizeIPs(append([]net.IP(nil), ips...))
	if len(ips) == 0 {
		return nil
	}
	return ips[0]
}

func copyIPs(ips []net.IP) []net.IP {
	if len(ips) == 0 {
		return nil
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		out = append(out, append(net.IP(nil), ip...))
	}
	return out
}

func buildNameServers(opts Options) ([]nameServer, error) {
	if len(opts.Servers) == 0 {
		return []nameServer{localNameServer{}}, nil
	}
	servers := make([]nameServer, 0, len(opts.Servers))
	for _, srv := range opts.Servers {
		switch normalizeServerType(srv.Type) {
		case "local":
			servers = append(servers, localNameServer{})
		case "https":
			ns, err := newHTTPSNameServer(srv, opts.timeout())
			if err != nil {
				return nil, err
			}
			servers = append(servers, ns)
		case "dot":
			ns, err := newTLSNameServer(srv, opts.timeout())
			if err != nil {
				return nil, err
			}
			servers = append(servers, ns)
		default:
			return nil, fmt.Errorf("unsupported dns server type %q", srv.Type)
		}
	}
	return servers, nil
}

func parseBogusNets(opts Options) ([]*net.IPNet, error) {
	if opts.DisableBogusFilter {
		return nil, nil
	}
	cidrs := []string{"198.18.0.0/15"}
	cidrs = append(cidrs, opts.BogusNets...)
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			return nil, err
		}
		out = append(out, ipNet)
	}
	return out, nil
}

func (r *Resolver) filterIPs(ips []net.IP) []net.IP {
	if r == nil || r.disableBogusFilter || len(r.bogusNets) == 0 {
		return ips
	}
	out := make([]net.IP, 0, len(ips))
	for _, ip := range ips {
		if ip == nil {
			continue
		}
		if isBogusIP(ip, r.bogusNets) {
			continue
		}
		out = append(out, ip)
	}
	return out
}

func isBogusIP(ip net.IP, nets []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	for _, ipNet := range nets {
		if ipNet != nil && ipNet.Contains(ip) {
			return true
		}
	}
	return false
}
