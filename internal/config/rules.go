package config

import "strings"

const (
	RejectRulePrefix          = "!"
	rejectRulePrefixFullWidth = "！"
	rejectRulePrefixQuestion  = "?"
	ProxyRulePrefixDash       = "-"
	ProxyRulePrefixUnderscore = "_"
)

func splitRuleURLs(ruleURLs []string) (direct []string, proxy []string, reject []string) {
	for _, raw := range ruleURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if url, ok := trimRejectRulePrefix(raw); ok {
			reject = append(reject, url)
			continue
		}
		if url, ok := trimProxyRulePrefix(raw); ok {
			proxy = append(proxy, url)
			continue
		}
		direct = append(direct, raw)
	}
	return direct, proxy, reject
}

func trimRejectRulePrefix(raw string) (string, bool) {
	return trimRulePrefix(raw, RejectRulePrefix, rejectRulePrefixFullWidth, rejectRulePrefixQuestion)
}

func trimProxyRulePrefix(raw string) (string, bool) {
	return trimRulePrefix(raw, ProxyRulePrefixDash, ProxyRulePrefixUnderscore)
}

func trimRulePrefix(raw string, prefixes ...string) (string, bool) {
	raw = strings.TrimSpace(raw)
	for _, prefix := range prefixes {
		if strings.HasPrefix(raw, prefix) {
			raw = strings.TrimSpace(strings.TrimPrefix(raw, prefix))
			return raw, raw != ""
		}
	}
	return "", false
}

func RuntimeRuleURLs(proxyMode string, ruleURLs []string) (direct []string, proxy []string, reject []string) {
	direct, proxy, reject = splitRuleURLs(ruleURLs)
	switch normalizeProxyMode(proxyMode) {
	case "direct", "global":
		reject = appendImplicitRejectRule(reject)
	}
	return direct, proxy, reject
}

func appendImplicitRejectRule(reject []string) []string {
	if len(reject) == 0 {
		return append(reject, DefaultImplicitRejectRuleURL)
	}
	return reject
}
