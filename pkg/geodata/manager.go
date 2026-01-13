package geodata

import (
	"bytes"
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

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

// IPRange 表示一个 IP 区间 [Start, End]
type IPRange struct {
	Start uint32
	End   uint32
}

type Manager struct {
	ipRanges     []IPRange
	domainExact  map[string]struct{} // 精确匹配 DOMAIN
	domainSuffix map[string]struct{} // 后缀匹配 DOMAIN-SUFFIX
	mu           sync.RWMutex
	urls         []string
}

// RuleSet 用于解析 YAML 格式的 payload
type RuleSet struct {
	Payload []string `yaml:"payload"`
}

var instance *Manager
var once sync.Once

// GetInstance 单例模式
func GetInstance(urls []string) *Manager {
	once.Do(func() {
		instance = &Manager{
			urls:         urls,
			domainExact:  make(map[string]struct{}),
			domainSuffix: make(map[string]struct{}),
		}
		go instance.Update()
	})
	return instance
}

func (m *Manager) Update() {
	log.Printf("[GeoData] Updating rules from %d sources...", len(m.urls))

	var tempRanges []IPRange
	tempExact := make(map[string]struct{})
	tempSuffix := make(map[string]struct{})

	for _, u := range m.urls {
		m.downloadAndParse(u, &tempRanges, tempExact, tempSuffix)
	}

	// 优化 IP 区间
	mergedIPs := mergeRanges(tempRanges)

	m.mu.Lock()
	m.ipRanges = mergedIPs
	m.domainExact = tempExact
	m.domainSuffix = tempSuffix
	m.mu.Unlock()

	log.Printf("[GeoData] Rules Updated: %d IP Ranges, %d Domains, %d Suffixes",
		len(mergedIPs), len(tempExact), len(tempSuffix))
}

func (m *Manager) downloadAndParse(url string, ipRanges *[]IPRange, exact, suffix map[string]struct{}) {
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf("[GeoData] Failed to download %s: %v", url, err)
		return
	}
	defer resp.Body.Close()

	// 读取全部内容
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[GeoData] Failed to read body from %s: %v", url, err)
		return
	}

	// 1. 尝试作为 YAML 解析
	var rs RuleSet
	if err := yaml.Unmarshal(body, &rs); err == nil && len(rs.Payload) > 0 {
		for _, rule := range rs.Payload {
			m.parseRule(rule, ipRanges, exact, suffix)
		}
		return
	}

	// 2. 兼容模式：如果 YAML 解析失败（例如是纯文本列表），则按行解析
	// 这能兼容一些纯文本的 .list 文件，同时通过上面的逻辑支持统一的 YAML payload
	scanner := bytes.NewBuffer(body)
	for {
		line, err := scanner.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		m.parseRule(line, ipRanges, exact, suffix)
		if err == io.EOF {
			break
		}
	}
}

// parseRule 统一处理单行规则字符串
func (m *Manager) parseRule(line string, ipRanges *[]IPRange, exact, suffix map[string]struct{}) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
		return
	}

	// 1. 尝试解析 Clash 格式: TYPE,VALUE,...
	// 格式如: DOMAIN,baidu.com 或 IP-CIDR,1.2.3.4/24,no-resolve
	parts := strings.Split(line, ",")
	if len(parts) >= 2 {
		ruleType := strings.TrimSpace(strings.ToUpper(parts[0]))
		ruleValue := strings.TrimSpace(parts[1])

		switch ruleType {
		case "DOMAIN":
			exact[ruleValue] = struct{}{}
		case "DOMAIN-SUFFIX":
			suffix[ruleValue] = struct{}{}
		case "IP-CIDR", "IP-CIDR6":
			// 处理 IP-CIDR,1.2.3.4/24
			parseIPLine(ruleValue, ipRanges)
		}
		return
	}

	// 2. 尝试解析纯 CIDR 或 IP
	parseIPLine(line, ipRanges)
}

func parseIPLine(line string, list *[]IPRange) {
	// 移除可能的引号
	line = strings.Trim(line, "'\"")

	_, ipNet, err := net.ParseCIDR(line)
	if err != nil {
		// 尝试作为单 IP
		ip := net.ParseIP(line)
		if ip != nil {
			val := ipToUint32(ip)
			*list = append(*list, IPRange{Start: val, End: val})
		}
		return
	}
	start := ipToUint32(ipNet.IP)
	mask := binary.BigEndian.Uint32(ipNet.Mask)
	end := start | (^mask)
	*list = append(*list, IPRange{Start: start, End: end})
}

// IsCN 检查目标是否匹配 CN 规则 (域名优先，其次 IP)
// host 可以是域名或 IP 字符串
func (m *Manager) IsCN(host string, ip net.IP) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// 0. Check if it's a local network address - always treat as "CN" (local)
	if m.isLocalNetwork(ip) {
		return true
	}

	// 1. Domain matching
	if ip == nil || (len(host) > 0 && host != ip.String()) {
		// This is a domain
		domain := strings.TrimSuffix(host, ".") // Remove trailing dot

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
		ip4 := ip.To4()
		if ip4 == nil {
			return false // IPv6 not supported for direct connection rules, default proxy
		}
		val := ipToUint32(ip4)

		idx := sort.Search(len(m.ipRanges), func(i int) bool {
			return m.ipRanges[i].End >= val
		})

		if idx < len(m.ipRanges) && m.ipRanges[idx].Start <= val {
			return true
		}
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
