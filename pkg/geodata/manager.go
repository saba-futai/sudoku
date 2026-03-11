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
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"gopkg.in/yaml.v3"
)

const (
	// LAN IP ranges in uint32 format
	lanRange1Start = 167772160  // 10.0.0.0
	lanRange1End   = 184549375  // 10.255.255.255
	lanRange2Start = 2886729728 // 172.16.0.0
	lanRange2End   = 2887778303 // 172.31.255.255
	lanRange3Start = 3232235520 // 192.168.0.0
	lanRange3End   = 3232301055 // 192.168.255.255
	lanRange4Start = 2130706432 // 127.0.0.0
	lanRange4End   = 2147483647 // 127.255.255.255
)

// IPRange represents an IP interval [Start, End]
type IPRange struct {
	Start uint32
	End   uint32
}

type IPv6Range struct {
	Start [16]byte
	End   [16]byte
}

type Manager struct {
	ipRanges     []IPRange
	ipv6Ranges   []IPv6Range
	domainExact  map[string]struct{} // Exact match: DOMAIN
	domainSuffix map[string]struct{} // Suffix match: DOMAIN-SUFFIX
	mu           sync.RWMutex
	urls         []string
}

type Match struct {
	Kind  string
	Value string
}

func (m Match) String() string {
	if strings.TrimSpace(m.Value) == "" {
		return m.Kind
	}
	return fmt.Sprintf("%s(%s)", m.Kind, m.Value)
}

// RuleSet is used to parse YAML-formatted payloads.
type RuleSet struct {
	Payload []string `yaml:"payload"`
}

type ruleBuildState struct {
	ipv4   []IPRange
	ipv6   []IPv6Range
	exact  map[string]struct{}
	suffix map[string]struct{}
}

var instance *Manager
var once sync.Once

var newRuleDownloadClient = func() *http.Client {
	return dnsutil.NewOutboundHTTPClient(30*time.Second, dnsutil.RecommendedClientResolver())
}

func NewManager(urls []string) *Manager {
	return &Manager{
		urls:         append([]string(nil), urls...),
		domainExact:  make(map[string]struct{}),
		domainSuffix: make(map[string]struct{}),
	}
}

func newRuleBuildState() *ruleBuildState {
	return &ruleBuildState{
		exact:  make(map[string]struct{}),
		suffix: make(map[string]struct{}),
	}
}

// GetInstance returns the singleton Manager.
func GetInstance(urls []string) *Manager {
	once.Do(func() {
		instance = NewManager(urls)
		go instance.Update()
	})
	return instance
}

func (m *Manager) Update() {
	logx.Infof("GeoData", "Updating rules from %d sources...", len(m.urls))

	state := newRuleBuildState()
	m.applyRuleState(state)

	loaded := 0
	for _, u := range m.urls {
		if !m.downloadAndParse(u, state) {
			continue
		}
		loaded++
		m.applyRuleState(state)
		logx.Infof("GeoData", "Rules Updated (%d/%d): %d IPv4 Ranges, %d IPv6 Ranges, %d Domains, %d Suffixes",
			loaded, len(m.urls), len(m.ipRanges), len(m.ipv6Ranges), len(m.domainExact), len(m.domainSuffix))
	}

	if loaded == 0 {
		logx.Warnf("GeoData", "Rules Updated: no sources loaded successfully")
	}
}

func (m *Manager) applyRuleState(state *ruleBuildState) {
	ipv4 := append([]IPRange(nil), state.ipv4...)
	ipv6 := append([]IPv6Range(nil), state.ipv6...)
	mergedIPv4 := mergeRanges(ipv4)
	mergedIPv6 := mergeIPv6Ranges(ipv6)
	exact := cloneRuleSet(state.exact)
	suffix := cloneRuleSet(state.suffix)

	m.mu.Lock()
	m.ipRanges = mergedIPv4
	m.ipv6Ranges = mergedIPv6
	m.domainExact = exact
	m.domainSuffix = suffix
	m.mu.Unlock()
}

func cloneRuleSet(src map[string]struct{}) map[string]struct{} {
	if len(src) == 0 {
		return make(map[string]struct{})
	}
	dst := make(map[string]struct{}, len(src))
	for k := range src {
		dst[k] = struct{}{}
	}
	return dst
}

func (m *Manager) downloadAndParse(url string, state *ruleBuildState) bool {
	client := newRuleDownloadClient()
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Get(url)
	if err != nil {
		logx.Warnf("GeoData", "Failed to download %s: %v", url, err)
		return false
	}
	defer resp.Body.Close()

	// Read the full response body.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logx.Warnf("GeoData", "Failed to read body from %s: %v", url, err)
		return false
	}

	// 1. Try parsing as YAML.
	var rs RuleSet
	if err := yaml.Unmarshal(body, &rs); err == nil && len(rs.Payload) > 0 {
		for _, rule := range rs.Payload {
			parseRule(rule, state)
		}
		return true
	}

	// 2. Fallback: if YAML parsing failed (e.g. plain-text list), parse line by line.
	// This supports plain-text .list files, while the above path handles unified YAML payloads.
	scanner := bytes.NewBuffer(body)
	for {
		line, err := scanner.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		parseRule(line, state)
		if err == io.EOF {
			break
		}
	}
	return true
}

// parseRule processes a single rule line.
func parseRule(line string, state *ruleBuildState) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return
	}

	// 1. Try parsing Clash format: TYPE,VALUE,...
	// e.g. DOMAIN,baidu.com or IP-CIDR,1.2.3.4/24,no-resolve
	parts := strings.Split(line, ",")
	if len(parts) >= 2 {
		ruleType := strings.TrimSpace(strings.ToUpper(parts[0]))
		ruleValue := strings.TrimSpace(parts[1])

		switch ruleType {
		case "DOMAIN":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.exact[v] = struct{}{}
			}
		case "DOMAIN-SUFFIX":
			if v := normalizeRuleDomain(ruleValue); v != "" {
				state.suffix[v] = struct{}{}
			}
		case "IP-CIDR", "IP-CIDR6":
			parseIPLine(ruleValue, state)
		}
		return
	}

	// 2. Try parsing as plain CIDR or IP.
	parseIPLine(line, state)
}

func parseIPLine(line string, state *ruleBuildState) {
	line = strings.Trim(line, "'\"")
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}

	if _, ipNet, err := net.ParseCIDR(line); err == nil {
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			start := ipToUint32(ip4)
			mask := binary.BigEndian.Uint32(ipNet.Mask)
			end := start | (^mask)
			state.ipv4 = append(state.ipv4, IPRange{Start: start, End: end})
			return
		}
		ip16 := ipNet.IP.To16()
		if ip16 == nil {
			return
		}
		var start [16]byte
		var end [16]byte
		copy(start[:], ip16)
		for i := 0; i < 16; i++ {
			mask := byte(0)
			if i < len(ipNet.Mask) {
				mask = ipNet.Mask[i]
			}
			end[i] = start[i] | (^mask)
		}
		state.ipv6 = append(state.ipv6, IPv6Range{Start: start, End: end})
		return
	}

	ip := net.ParseIP(line)
	if ip == nil {
		return
	}
	if ip4 := ip.To4(); ip4 != nil {
		val := ipToUint32(ip4)
		state.ipv4 = append(state.ipv4, IPRange{Start: val, End: val})
		return
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return
	}
	var v6 [16]byte
	copy(v6[:], ip16)
	state.ipv6 = append(state.ipv6, IPv6Range{Start: v6, End: v6})
}

func normalizeRuleDomain(v string) string {
	v = strings.Trim(v, "'\"")
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.TrimSuffix(v, ".")
	v = strings.TrimPrefix(v, ".")
	return v
}

func normalizeLookupHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

func matchIPv4Range(ranges []IPRange, ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	val := ipToUint32(ip4)
	idx := sort.Search(len(ranges), func(i int) bool { return ranges[i].End >= val })
	return idx < len(ranges) && ranges[idx].Start <= val
}

func matchIPv6Range(ranges []IPv6Range, ip net.IP) bool {
	ip16 := ip.To16()
	if ip16 == nil {
		return false
	}
	var key [16]byte
	copy(key[:], ip16)
	idx := sort.Search(len(ranges), func(i int) bool { return compareIPv6(ranges[i].End, key) >= 0 })
	return idx < len(ranges) && compareIPv6(ranges[idx].Start, key) <= 0
}

func (m *Manager) MatchCN(host string, ip net.IP) (bool, Match) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.isLocalNetwork(ip) {
		return true, Match{Kind: "LOCAL"}
	}

	host = normalizeLookupHost(host)
	hostIP := net.ParseIP(host)
	if host != "" && (hostIP == nil || ip == nil || !hostIP.Equal(ip)) {
		domain := host
		if _, ok := m.domainExact[domain]; ok {
			return true, Match{Kind: "DOMAIN", Value: domain}
		}

		parts := strings.Split(domain, ".")
		for i := 0; i < len(parts); i++ {
			suffix := strings.Join(parts[i:], ".")
			if _, ok := m.domainSuffix[suffix]; ok {
				return true, Match{Kind: "DOMAIN-SUFFIX", Value: suffix}
			}
		}
	}

	if ip != nil {
		if ip.To4() != nil {
			if matchIPv4Range(m.ipRanges, ip) {
				return true, Match{Kind: "IP", Value: ip.String()}
			}
			return false, Match{}
		}
		if matchIPv6Range(m.ipv6Ranges, ip) {
			return true, Match{Kind: "IP", Value: ip.String()}
		}
	}

	return false, Match{}
}

// IsCN checks whether the target matches CN rules (domain first, then IP).
// host can be a domain name or an IP string.
func (m *Manager) IsCN(host string, ip net.IP) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 0. Check if it's a local network address - always treat as "CN" (local)
	if m.isLocalNetwork(ip) {
		return true
	}

	// 1. Domain matching
	host = normalizeLookupHost(host)
	hostIP := net.ParseIP(host)
	if host != "" && (hostIP == nil || ip == nil || !hostIP.Equal(ip)) {
		domain := host

		// Exact match
		if _, ok := m.domainExact[domain]; ok {
			return true
		}

		// Suffix matching
		// Strategy: Check level by level. E.g., www.baidu.com -> check www.baidu.com, baidu.com, com
		parts := strings.Split(domain, ".")
		for i := 0; i < len(parts); i++ {
			suffix := strings.Join(parts[i:], ".")
			if _, ok := m.domainSuffix[suffix]; ok {
				return true
			}
		}
	}

	// 2. IP matching
	if ip != nil {
		if ip.To4() != nil {
			return matchIPv4Range(m.ipRanges, ip)
		}
		return matchIPv6Range(m.ipv6Ranges, ip)
	}

	return false
}

func ipToUint32(ip net.IP) uint32 {
	if len(ip) == 16 {
		return binary.BigEndian.Uint32(ip[12:16])
	}
	return binary.BigEndian.Uint32(ip)
}

func mergeRanges(ranges []IPRange) []IPRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Start < ranges[j].Start
	})
	var result []IPRange
	current := ranges[0]
	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		if current.End >= next.Start-1 {
			if next.End > current.End {
				current.End = next.End
			}
		} else {
			result = append(result, current)
			current = next
		}
	}
	result = append(result, current)
	return result
}

func mergeIPv6Ranges(ranges []IPv6Range) []IPv6Range {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		return compareIPv6(ranges[i].Start, ranges[j].Start) < 0
	})
	result := make([]IPv6Range, 0, len(ranges))
	current := ranges[0]
	for i := 1; i < len(ranges); i++ {
		next := ranges[i]
		if compareIPv6(current.End, next.Start) >= 0 {
			if compareIPv6(next.End, current.End) > 0 {
				current.End = next.End
			}
		} else {
			result = append(result, current)
			current = next
		}
	}
	result = append(result, current)
	return result
}

func compareIPv6(a, b [16]byte) int {
	return bytes.Compare(a[:], b[:])
}

func (m *Manager) isLocalNetwork(ip net.IP) bool {
	if ip == nil {
		return false
	}

	ip4 := ip.To4()
	if ip4 == nil {
		// For IPv6, check if it's loopback or link-local
		return ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}

	val := ipToUint32(ip4)

	// Check against common LAN ranges
	return (val >= lanRange1Start && val <= lanRange1End) || // 10.0.0.0/8
		(val >= lanRange2Start && val <= lanRange2End) || // 172.16.0.0/12
		(val >= lanRange3Start && val <= lanRange3End) || // 192.168.0.0/16
		(val >= lanRange4Start && val <= lanRange4End) || // 127.0.0.0/8
		ip.IsLoopback()
}
