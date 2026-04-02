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
package geodata

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsCN_HostPortMatchesDomainRules(t *testing.T) {
	m := &Manager{
		domainExact:  map[string]struct{}{"api.bilibili.com": {}},
		domainSuffix: map[string]struct{}{"bilibili.com": {}},
	}

	if ok, _ := m.MatchCN("www.bilibili.com:443", nil); !ok {
		t.Fatalf("expected suffix domain match for host:port")
	}
	if ok, _ := m.MatchCN("api.bilibili.com:443", nil); !ok {
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

func TestParseRule_AcceptsBareDomainEntries(t *testing.T) {
	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}

	parseRule("'ad.qq.com'", state)
	parseRule("+.ads.example.com", state)

	if _, ok := state.exact["ad.qq.com"]; !ok {
		t.Fatalf("expected bare domain entry to be parsed as exact domain")
	}
	if _, ok := state.suffix["ads.example.com"]; !ok {
		t.Fatalf("expected +. bare domain entry to be parsed as suffix domain")
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
	if ok, _ := m.MatchCN("video.example:443", ip); !ok {
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
	if ok, _ := m.MatchCN("36.112.0.1:443", ipv4); ok {
		t.Fatalf("unexpected ipv4 match from ipv6-only rule")
	}
}

func TestDownloadAndParse_UsesRuleDownloadClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload:\n  - DOMAIN-SUFFIX,example.cn\n"))
	}))
	defer srv.Close()

	parsedURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	serverHost, serverPort, err := net.SplitHostPort(parsedURL.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	oldNewRuleDownloadClient := newRuleDownloadClient
	t.Cleanup(func() {
		newRuleDownloadClient = oldNewRuleDownloadClient
	})

	newRuleDownloadClient = func() *http.Client {
		return &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					if addr == net.JoinHostPort("resolver.test", serverPort) {
						addr = net.JoinHostPort(serverHost, serverPort)
					}
					var d net.Dialer
					return d.DialContext(ctx, network, addr)
				},
			},
		}
	}

	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}
	m := &Manager{}
	m.downloadAndParse("http://resolver.test:"+serverPort+"/rules", state)

	if _, ok := state.suffix["example.cn"]; !ok {
		t.Fatalf("expected rules downloaded through injected client")
	}
}

func TestDownloadAndParse_YAMLBareDomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload:\n  - 'ad.qq.com'\n  - '+.ads.example.com'\n"))
	}))
	t.Cleanup(srv.Close)

	state := &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}
	m := &Manager{}
	if !m.downloadAndParse(srv.URL, state) {
		t.Fatalf("expected downloadAndParse to succeed")
	}

	if _, ok := state.exact["ad.qq.com"]; !ok {
		t.Fatalf("expected bare yaml domain to be parsed as exact domain")
	}
	if _, ok := state.suffix["ads.example.com"]; !ok {
		t.Fatalf("expected +. yaml domain to be parsed as suffix domain")
	}
}

func TestUpdate_AppliesEachSourceIncrementally(t *testing.T) {
	secondRelease := make(chan struct{})
	firstServed := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/first"):
			_, _ = w.Write([]byte("payload:\n  - DOMAIN-SUFFIX,first.cn\n"))
			select {
			case firstServed <- struct{}{}:
			default:
			}
		case strings.HasSuffix(r.URL.Path, "/second"):
			<-secondRelease
			_, _ = w.Write([]byte("payload:\n  - DOMAIN-SUFFIX,second.cn\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	m := NewManager([]string{srv.URL + "/first", srv.URL + "/second"})

	done := make(chan struct{})
	go func() {
		m.Update()
		close(done)
	}()

	select {
	case <-firstServed:
	case <-time.After(2 * time.Second):
		t.Fatal("first rule source was not requested")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _ := m.MatchCN("www.first.cn:443", nil); ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ok, _ := m.MatchCN("www.first.cn:443", nil); !ok {
		t.Fatalf("expected first source to be applied before second source completed")
	}
	if ok, _ := m.MatchCN("www.second.cn:443", nil); ok {
		t.Fatalf("second source should not be applied before its download completes")
	}

	close(secondRelease)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("update did not finish")
	}

	if ok, _ := m.MatchCN("www.second.cn:443", nil); !ok {
		t.Fatalf("expected second source to be applied after its download completed")
	}
}
