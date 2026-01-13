package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cfg.json")

	data := `{
		"mode": "client",
		"local_port": 8080,
		"server_address": "1.1.1.1:443",
		"key": "k",
		"aead": "none",
		"rule_urls": ["global"]
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}

	if cfg.Transport != "tcp" {
		t.Fatalf("Transport default not applied")
	}
	if cfg.ASCII != "prefer_entropy" {
		t.Fatalf("ASCII default not applied, got %s", cfg.ASCII)
	}
	if cfg.ProxyMode != "global" || cfg.RuleURLs != nil {
		t.Fatalf("ProxyMode parsing failed, mode=%s urls=%v", cfg.ProxyMode, cfg.RuleURLs)
	}
	if !cfg.EnablePureDownlink {
		t.Fatalf("EnablePureDownlink should default to true")
	}
}

func TestLoadRejectsPackedWithoutAEAD(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "cfg.json")

	data := `{
		"mode": "server",
		"local_port": 8080,
		"server_address": "0.0.0.0:8080",
		"key": "k",
		"aead": "none",
		"enable_pure_downlink": false
	}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatalf("expected error when packed downlink used without AEAD")
	}
}

func TestLoadPathRootCompat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		configKey   string
		expectValue string
	}{
		{name: "new", configKey: "path_root", expectValue: "aabbcc"},
		{name: "legacy", configKey: "http_mask_path_root", expectValue: "aabbcc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "cfg.json")

			data := fmt.Sprintf(`{
				"mode": "client",
				"local_port": 8080,
				"server_address": "1.1.1.1:443",
				"key": "k",
				"aead": "none",
				"%s": "%s",
				"rule_urls": ["global"]
			}`, tc.configKey, tc.expectValue)

			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatalf("write file: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load error: %v", err)
			}
			if cfg.HTTPMaskPathRoot != tc.expectValue {
				t.Fatalf("HTTPMaskPathRoot mismatch: got %q want %q", cfg.HTTPMaskPathRoot, tc.expectValue)
			}
		})
	}
}
