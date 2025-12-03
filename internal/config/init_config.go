// internal/config/init_config.go
package config

import (
	"encoding/json"
	"os"

	"github.com/saba-futai/sudoku/pkg/crypto"
)

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}

	if cfg.Transport == "" {
		cfg.Transport = "tcp"
	}

	if cfg.ASCII == "" {
		cfg.ASCII = "prefer_entropy"
	}

	InitMieruconfig(&cfg)

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

func InitMieruconfig(cfg *Config) {
	if !cfg.EnableMieru {
		return
	}

	if cfg.MieruConfig == nil {
		cfg.MieruConfig = &MieruConfig{
			Port:      cfg.LocalPort,
			Transport: "UDP",
		}
	}
	if cfg.MieruConfig.Port == 0 {
		cfg.MieruConfig.Port = cfg.LocalPort + 1000 // 默认偏移
	}
	if cfg.MieruConfig.Transport == "" {
		cfg.MieruConfig.Transport = "TCP"
	}

	derivedIdentity := deriveIdentityFromKey(cfg.Key)
	if cfg.MieruConfig.Username == "" {
		cfg.MieruConfig.Username = derivedIdentity
	}
	if cfg.MieruConfig.Password == "" {
		cfg.MieruConfig.Password = derivedIdentity
	}
	if cfg.MieruConfig.MTU == 0 {
		cfg.MieruConfig.MTU = 1400
	}
	if cfg.MieruConfig.Multiplexing == "" {
		// Align with mieru docs: default to low multiplexing to reduce
		// per-underlay memory usage on small devices.
		cfg.MieruConfig.Multiplexing = "MULTIPLEXING_LOW"
	}
}

func deriveIdentityFromKey(key string) string {
	recoveredFromKey, err := crypto.RecoverPublicKey(key)
	if err != nil {
		return key
	}
	return crypto.EncodePoint(recoveredFromKey)
}
