package config

import (
	"fmt"
	"net"
	"path"
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

	if c.Chain != nil {
		if len(c.Chain.Hops) > 0 {
			out := c.Chain.Hops[:0]
			for _, v := range c.Chain.Hops {
				v = strings.TrimSpace(v)
				if v != "" {
					out = append(out, v)
				}
			}
			c.Chain.Hops = out
		}
		if len(c.Chain.Hops) == 0 {
			c.Chain = nil
		}
	}

	// Allow chain-only client configs where the first hop is provided in chain.hops and server_address is empty.
	if c.Mode == "client" && c.ServerAddress == "" && c.Chain != nil && len(c.Chain.Hops) > 0 {
		c.ServerAddress = c.Chain.Hops[0]
		c.Chain.Hops = c.Chain.Hops[1:]
		if len(c.Chain.Hops) == 0 {
			c.Chain = nil
		}
	}

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

	c.HTTPMask.Mode = normalizeHTTPMaskMode(c.HTTPMask.Mode)
	c.HTTPMask.Multiplex = normalizeHTTPMaskMultiplex(c.HTTPMask.Disable, c.HTTPMask.Multiplex)
	c.SuspiciousAction = normalizeSuspiciousAction(c.SuspiciousAction)
	c.HTTPMask.Host = strings.TrimSpace(c.HTTPMask.Host)
	c.HTTPMask.PathRoot = strings.TrimSpace(c.HTTPMask.PathRoot)

	if c.Reverse != nil {
		c.Reverse.Listen = strings.TrimSpace(c.Reverse.Listen)
		c.Reverse.ClientID = strings.TrimSpace(c.Reverse.ClientID)

		if len(c.Reverse.Routes) > 0 {
			seen := make(map[string]struct{}, len(c.Reverse.Routes))
			seenTCP := false
			out := c.Reverse.Routes[:0]
			for _, r := range c.Reverse.Routes {
				r.Path = strings.TrimSpace(r.Path)
				r.Target = strings.TrimSpace(r.Target)
				r.HostHeader = strings.TrimSpace(r.HostHeader)

				if r.Path == "" && r.Target == "" {
					continue
				}
				// Path empty => raw TCP reverse on reverse.listen (no HTTP path prefix).
				// Only one TCP route is supported per server entry.
				if r.Path != "" {
					if !strings.HasPrefix(r.Path, "/") {
						r.Path = "/" + r.Path
					}
					r.Path = path.Clean(r.Path)
					if r.Path != "/" {
						r.Path = strings.TrimRight(r.Path, "/")
					}
				}

				if r.Target == "" {
					if r.Path == "" {
						return fmt.Errorf("reverse tcp route: missing target")
					}
					return fmt.Errorf("reverse route %q: missing target", r.Path)
				}
				if _, _, err := net.SplitHostPort(r.Target); err != nil {
					if r.Path == "" {
						return fmt.Errorf("reverse tcp route: invalid target %q: %w", r.Target, err)
					}
					return fmt.Errorf("reverse route %q: invalid target %q: %w", r.Path, r.Target, err)
				}

				if r.Path == "" {
					if seenTCP {
						return fmt.Errorf("reverse route duplicate tcp mapping")
					}
					seenTCP = true
					out = append(out, r)
					continue
				}

				if _, ok := seen[r.Path]; ok {
					return fmt.Errorf("reverse route duplicate path: %q", r.Path)
				}
				seen[r.Path] = struct{}{}
				out = append(out, r)
			}
			c.Reverse.Routes = out
		}

		if c.Reverse.Listen == "" && c.Reverse.ClientID == "" && len(c.Reverse.Routes) == 0 {
			c.Reverse = nil
		}
	}

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
	if c == nil || c.HTTPMask.Disable {
		return false
	}
	switch c.HTTPMask.Mode {
	case "stream", "poll", "auto":
		return true
	default:
		return false
	}
}

func (c *Config) HTTPMaskSessionMuxEnabled() bool {
	return c.HTTPMaskTunnelEnabled() && c.HTTPMask.Multiplex == "on"
}
