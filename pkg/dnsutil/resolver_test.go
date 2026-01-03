package dnsutil

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResolve_IPLiteralBypassDNS(t *testing.T) {
	r := newResolver(1*time.Minute, func(ctx context.Context, network, host string) ([]net.IP, error) {
		t.Fatalf("DNS should not be called for IP literal")
		return nil, nil
	})

	addr, err := r.Resolve(context.Background(), "1.2.3.4:80")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "1.2.3.4:80" {
		t.Fatalf("unexpected addr: %s", addr)
	}
}

func TestResolve_CacheHitAvoidsDNS(t *testing.T) {
	var calls atomic.Int64
	lookup := func(ctx context.Context, network, host string) ([]net.IP, error) {
		calls.Add(1)
		return []net.IP{net.ParseIP("1.2.3.4")}, nil
	}

	r := newResolver(100*time.Millisecond, lookup)
	ctx := context.Background()

	addr1, err := r.Resolve(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if addr1 != "1.2.3.4:80" {
		t.Fatalf("unexpected addr1: %s", addr1)
	}

	addr2, err := r.Resolve(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if addr2 != addr1 {
		t.Fatalf("cache mismatch: %s vs %s", addr1, addr2)
	}

	if calls.Load() == 0 {
		t.Fatalf("expected at least one DNS call")
	}
}

func TestResolve_OptimisticCacheOnFailure(t *testing.T) {
	ip := net.ParseIP("1.2.3.4")
	if ip == nil {
		t.Fatalf("failed to parse test IP")
	}

	var mu sync.Mutex
	fail := false

	lookup := func(ctx context.Context, network, host string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		if fail {
			return nil, fmt.Errorf("dns failure")
		}
		if network == "ip4" {
			return []net.IP{ip}, nil
		}
		// Simulate missing IPv6 record.
		return nil, fmt.Errorf("no ipv6")
	}

	r := newResolver(20*time.Millisecond, lookup)
	ctx := context.Background()

	addr1, err := r.Resolve(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("initial resolve failed: %v", err)
	}
	expected := "1.2.3.4:80"
	if addr1 != expected {
		t.Fatalf("unexpected addr1: %s", addr1)
	}

	// Expire the cache entry.
	time.Sleep(30 * time.Millisecond)

	// Force DNS failure; resolver should still return cached IP.
	mu.Lock()
	fail = true
	mu.Unlock()

	addr2, err := r.Resolve(ctx, "example.com:80")
	if err != nil {
		t.Fatalf("resolve with failing DNS should still succeed via optimistic cache: %v", err)
	}
	if addr2 != expected {
		t.Fatalf("unexpected addr2 with optimistic cache: %s", addr2)
	}
}

func TestResolve_InvalidAddress(t *testing.T) {
	r := newResolver(1*time.Minute, nil)
	if _, err := r.Resolve(context.Background(), "bad-address"); err == nil {
		t.Fatalf("expected error for invalid address")
	}
}
