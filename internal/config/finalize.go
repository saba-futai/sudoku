package config

import (
	"fmt"
	"strings"
)

func normalizeLower(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func normalizeHTTPMaskMode(mode string) string {
	switch normalizeLower(mode) {
	case "", "legacy":
		return "legacy"
	case "stream":
		return "stream"
	case "poll":
		return "poll"
	case "auto":
		return "auto"
	default:
		return "legacy"
	}
}

func normalizeHTTPMaskMultiplex(disableHTTPMask bool, mode string) string {
	if disableHTTPMask {
		return "off"
	}
	switch normalizeLower(mode) {
	case "", "off":
		return "off"
	case "auto":
		return "auto"
	case "on":
		return "on"
	default:
		return "off"
	}
}

func normalizeSuspiciousAction(action string) string {
	switch normalizeLower(action) {
	case "", "fallback":
		return "fallback"
	case "silent":
		return "silent"
	default:
		return "fallback"
	}
}

func normalizeProxyMode(mode string) string {
	switch normalizeLower(mode) {
	case "", "global":
		return "global"
	case "direct":
		return "direct"
	case "pac":
		return "pac"
	default:
		return "global"
	}
}

// Finalize normalizes and validates cross-field settings after loading/unmarshalling.
func (c *Config) Finalize() error {
	if c == nil {
		return fmt.Errorf("nil config")
	}

	c.Mode = normalizeLower(c.Mode)
	c.ServerAddress = strings.TrimSpace(c.ServerAddress)
	c.FallbackAddr = strings.TrimSpace(c.FallbackAddr)
	c.Key = strings.TrimSpace(c.Key)
	c.AEAD = normalizeLower(c.AEAD)
	c.CustomTable = strings.TrimSpace(c.CustomTable)

	if len(c.CustomTables) > 0 {
		out := c.CustomTables[:0]
		for _, v := range c.CustomTables {
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		}
		c.CustomTables = out
	}

	if len(c.RuleURLs) > 0 {
		out := c.RuleURLs[:0]
		for _, v := range c.RuleURLs {
			v = strings.TrimSpace(v)
			if v != "" {
				out = append(out, v)
			}
		}
		c.RuleURLs = out
	}

	if strings.TrimSpace(c.Transport) == "" {
		c.Transport = "tcp"
	} else {
		c.Transport = normalizeLower(c.Transport)
	}

	if strings.TrimSpace(c.ASCII) == "" {
		c.ASCII = "prefer_entropy"
	} else {
		c.ASCII = normalizeLower(c.ASCII)
	}

	c.HTTPMaskMode = normalizeHTTPMaskMode(c.HTTPMaskMode)
	c.HTTPMaskMultiplex = normalizeHTTPMaskMultiplex(c.DisableHTTPMask, c.HTTPMaskMultiplex)
	c.SuspiciousAction = normalizeSuspiciousAction(c.SuspiciousAction)
	c.HTTPMaskHost = strings.TrimSpace(c.HTTPMaskHost)
	c.HTTPMaskPathRoot = strings.TrimSpace(c.HTTPMaskPathRoot)

	// Proxy mode:
	// - rule_urls: ["global"] / ["direct"] acts as a keyword override and clears RuleURLs.
	// - any other non-empty rule_urls means PAC mode.
	if len(c.RuleURLs) == 1 {
		switch normalizeLower(c.RuleURLs[0]) {
		case "global":
			c.ProxyMode = "global"
			c.RuleURLs = nil
		case "direct":
			c.ProxyMode = "direct"
			c.RuleURLs = nil
		}
	}
	if len(c.RuleURLs) > 0 {
		c.ProxyMode = "pac"
	} else {
		c.ProxyMode = normalizeProxyMode(c.ProxyMode)
	}

	if !c.EnablePureDownlink && c.AEAD == "none" {
		return fmt.Errorf("enable_pure_downlink=false requires AEAD to be enabled")
	}

	return nil
}

func (c *Config) HTTPMaskTunnelEnabled() bool {
	if c == nil || c.DisableHTTPMask {
		return false
	}
	switch c.HTTPMaskMode {
	case "stream", "poll", "auto":
		return true
	default:
		return false
	}
}

func (c *Config) HTTPMaskSessionMuxEnabled() bool {
	return c.HTTPMaskTunnelEnabled() && c.HTTPMaskMultiplex == "on"
}
