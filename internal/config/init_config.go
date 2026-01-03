// internal/config/init_config.go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	cfg := Config{
		EnablePureDownlink: true,
		HTTPMaskMode:       "legacy",
	}
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}

	if cfg.Transport == "" {
		cfg.Transport = "tcp"
	}

	if cfg.ASCII == "" {
		cfg.ASCII = "prefer_entropy"
	}
	if cfg.HTTPMaskMode == "" {
		cfg.HTTPMaskMode = "legacy"
	}
	if cfg.DisableHTTPMask {
		cfg.HTTPMaskMultiplex = "off"
	} else {
		switch strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMultiplex)) {
		case "", "off":
			cfg.HTTPMaskMultiplex = "off"
		case "auto", "on":
			cfg.HTTPMaskMultiplex = strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMultiplex))
		default:
			cfg.HTTPMaskMultiplex = "off"
		}
	}

	if !cfg.EnablePureDownlink && cfg.AEAD == "none" {
		return nil, fmt.Errorf("enable_pure_downlink=false requires AEAD to be enabled")
	}

	// 处理 ProxyMode 和 默认规则
	// 如果用户显式设置了 rule_urls 为 ["global"] 或 ["direct"]，则覆盖模式
	if len(cfg.RuleURLs) > 0 && (cfg.RuleURLs[0] == "global" || cfg.RuleURLs[0] == "direct") {
		cfg.ProxyMode = cfg.RuleURLs[0]
		cfg.RuleURLs = nil
	} else if len(cfg.RuleURLs) > 0 {
		cfg.ProxyMode = "pac"
	} else {
		if cfg.ProxyMode == "" {
			cfg.ProxyMode = "global" // 默认为全局代理模式
		}
	}

	return &cfg, nil
}
