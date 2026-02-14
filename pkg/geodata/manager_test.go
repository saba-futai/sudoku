package geodata

import (
	"net"
	"testing"
)

func TestIsCN_HostPortMatchesDomainRules(t *testing.T) {
	m := &Manager{
		domainExact:  map[string]struct{}{"api.bilibili.com": {}},
		domainSuffix: map[string]struct{}{"bilibili.com": {}},
	}

	if !m.IsCN("www.bilibili.com:443", nil) {
		t.Fatalf("expected suffix domain match for host:port")
	}
	if !m.IsCN("api.bilibili.com:443", nil) {
		t.Fatalf("expected exact domain match for host:port")
	}
}

func TestParseRule_NormalizesDomainEntries(t *testing.T) {
	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}

	parseRule("DOMAIN, API.BiliBili.Com.", state)
	parseRule("DOMAIN-SUFFIX,.BiliBili.Com", state)

	if _, ok := state.exact["api.bilibili.com"]; !ok {
		t.Fatalf("expected normalized exact domain entry")
	}
	if _, ok := state.suffix["bilibili.com"]; !ok {
		t.Fatalf("expected normalized suffix domain entry")
	}
}

func TestIsCN_IPv6RuleMatch(t *testing.T) {
	m := &Manager{
		domainExact:  make(map[string]struct{}),
		domainSuffix: make(map[string]struct{}),
	}
	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}

	parseRule("IP-CIDR6,2400:3200::/32", state)
	m.ipRanges = mergeRanges(state.ipv4)
	m.ipv6Ranges = mergeIPv6Ranges(state.ipv6)

	ip := net.ParseIP("2400:3200::1234")
	if ip == nil {
		t.Fatalf("parse test ipv6 failed")
	}
	if !m.IsCN("video.example:443", ip) {
		t.Fatalf("expected ipv6 rule match")
	}
}

func TestParseRule_IPv6DoesNotPolluteIPv4Ranges(t *testing.T) {
	m := &Manager{
		domainExact:  make(map[string]struct{}),
		domainSuffix: make(map[string]struct{}),
	}
	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}

	parseRule("IP-CIDR6,2400:3200::/32", state)
	m.ipRanges = mergeRanges(state.ipv4)
	m.ipv6Ranges = mergeIPv6Ranges(state.ipv6)

	ipv4 := net.ParseIP("36.112.0.1")
	if ipv4 == nil {
		t.Fatalf("parse test ipv4 failed")
	}
	if m.IsCN("36.112.0.1:443", ipv4) {
		t.Fatalf("unexpected ipv4 match from ipv6-only rule")
	}
}
