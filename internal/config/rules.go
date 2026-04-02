package config

import "strings"

const (
	RejectRulePrefix          = "!"
	rejectRulePrefixFullWidth = "！"
)

func splitRuleURLs(ruleURLs []string) (direct []string, reject []string) {
	for _, raw := range ruleURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if url, ok := trimRejectRulePrefix(raw); ok {
			reject = append(reject, url)
			continue
		}
		direct = append(direct, raw)
	}
	return direct, reject
}

func trimRejectRulePrefix(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, RejectRulePrefix):
		raw = strings.TrimSpace(strings.TrimPrefix(raw, RejectRulePrefix))
	case strings.HasPrefix(raw, rejectRulePrefixFullWidth):
		raw = strings.TrimSpace(strings.TrimPrefix(raw, rejectRulePrefixFullWidth))
	default:
		return "", false
	}
	if raw == "" {
		return "", false
	}
	return raw, true
}

func RuntimeRuleURLs(proxyMode string, ruleURLs []string) (direct []string, reject []string) {
	direct, reject = splitRuleURLs(ruleURLs)
	switch normalizeProxyMode(proxyMode) {
	case "direct", "global":
		reject = appendImplicitRejectRule(reject)
	}
	return direct, reject
}

func appendImplicitRejectRule(reject []string) []string {
	if len(reject) == 0 {
		return append(reject, DefaultImplicitRejectRuleURL)
	}
	return reject
}
