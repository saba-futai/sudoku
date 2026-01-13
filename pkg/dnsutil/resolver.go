package dnsutil

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// lookupIPFunc abstracts DNS lookups for easier testing.
type lookupIPFunc func(ctx context.Context, network, host string) ([]net.IP, error)

type cacheEntry struct {
	ip        net.IP
	expiresAt time.Time
}

type resolver struct {
	mu       sync.RWMutex
	cache    map[string]cacheEntry
	ttl      time.Duration
	lookupFn lookupIPFunc
}

func newResolver(ttl time.Duration, fn lookupIPFunc) *resolver {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if fn == nil {
		fn = func(ctx context.Context, network, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, network, host)
		}
	}
	return &resolver{
		cache:    make(map[string]cacheEntry),
		ttl:      ttl,
		lookupFn: fn,
	}
}

var defaultResolver = newResolver(10*time.Minute, nil)

// ResolveWithCache resolves addr (host:port) into ip:port using
// concurrent DNS lookups (IPv4/IPv6) and optimistic caching.
//
// Behavior:
//   - If host is already an IP, returns addr directly.
//   - If a fresh cache entry exists, returns it without DNS queries.
//   - If cache is stale and DNS fails, falls back to stale IP (optimistic cache).
//   - DNS lookups for IPv4/IPv6 are performed concurrently.
func ResolveWithCache(ctx context.Context, addr string) (string, error) {
	return defaultResolver.Resolve(ctx, addr)
}

// Resolve performs the actual resolution logic on a resolver instance.
func (r *resolver) Resolve(ctx context.Context, addr string) (string, error) {
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("invalid address %q: %w", addr, err)
	}

	// If already an IP literal, no DNS is needed.
	if ip := net.ParseIP(host); ip != nil {
		return addr, nil
	}

	now := time.Now()
	cachedIP, expired := r.lookup(host, now)

	// Fresh cache hit.
	if cachedIP != nil && !expired {
		return net.JoinHostPort(cachedIP.String(), port), nil
	}

	// Need DNS resolution (cache miss or expired).
	ips, err := r.lookupConcurrently(ctx, host)
	if err != nil {
		// Optimistic caching: fall back to stale IP if present.
		if cachedIP != nil {
			return net.JoinHostPort(cachedIP.String(), port), nil
		}
		return "", fmt.Errorf("dns lookup failed for %s: %w", host, err)
	}

	// Choose the first IP and update cache.
	selected := firstNonNilIP(ips)
	if selected == nil {
		if cachedIP != nil {
			// Should be rare, but still honor optimistic cache.
			return net.JoinHostPort(cachedIP.String(), port), nil
		}
		return "", fmt.Errorf("no usable ip found for host %s", host)
	}

	r.store(host, selected, now)
	return net.JoinHostPort(selected.String(), port), nil
}

func (r *resolver) lookup(host string, now time.Time) (net.IP, bool) {
	r.mu.RLock()
	entry, ok := r.cache[host]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if now.After(entry.expiresAt) {
		return entry.ip, true
	}
	return entry.ip, false
}

func (r *resolver) store(host string, ip net.IP, now time.Time) {
	if ip == nil {
		return
	}
	r.mu.Lock()
	r.cache[host] = cacheEntry{
		ip:        append(net.IP(nil), ip...), // defensive copy
		expiresAt: now.Add(r.ttl),
	}
	r.mu.Unlock()
}

func (r *resolver) lookupConcurrently(ctx context.Context, host string) ([]net.IP, error) {
	type result struct {
		ips []net.IP
		err error
	}

	networks := []string{"ip4", "ip6"}
	ch := make(chan result, len(networks))

	var wg sync.WaitGroup
	for _, network := range networks {
		network := network
		wg.Add(1)
		go func() {
			defer wg.Done()
			ips, err := r.lookupFn(ctx, network, host)
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

	return allIPs, nil
}

func firstNonNilIP(ips []net.IP) net.IP {
	for _, ip := range ips {
		if ip != nil {
			return ip
		}
	}
	return nil
}
