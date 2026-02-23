package config

// DefaultPACRuleURLs returns the recommended PAC rule sources.
//
// The list is intentionally small and CDN-friendly, and should cover both IPv4 and IPv6 CN traffic.
func DefaultPACRuleURLs() []string {
	return []string{
		"https://gh-proxy.org/https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/BiliBili/BiliBili.list",
		"https://gh-proxy.org/https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/WeChat/WeChat.list",
		"https://gh-proxy.org/https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/ChinaMaxNoIP/ChinaMaxNoIP.list",
		"https://gh-proxy.org/https://raw.githubusercontent.com/fernvenue/chn-cidr-list/master/ipv4.yaml",
		"https://gh-proxy.org/https://raw.githubusercontent.com/fernvenue/chn-cidr-list/master/ipv6.yaml",
	}
}
