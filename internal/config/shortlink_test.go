package config

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"testing"
)

func TestShortLinkRoundTrip_Client(t *testing.T) {
	cfg := &Config{
		Mode:               "client",
		LocalPort:          1081,
		ServerAddress:      "8.8.8.8:443",
		Key:                "deadbeef",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_ascii",
		CustomTable:        "xpxvvpvv",
		EnablePureDownlink: false,
	}

	link, err := BuildShortLinkFromConfig(cfg, "")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}
	if link == "" {
		t.Fatalf("empty link")
	}

	decoded, err := BuildConfigFromShortLink(link)
	if err != nil {
		t.Fatalf("BuildConfigFromShortLink error: %v", err)
	}

	if decoded.ServerAddress != cfg.ServerAddress {
		t.Fatalf("server address mismatch, got %s", decoded.ServerAddress)
	}
	if decoded.LocalPort != cfg.LocalPort {
		t.Fatalf("local port mismatch, got %d", decoded.LocalPort)
	}
	if decoded.Key != cfg.Key {
		t.Fatalf("key mismatch, got %s", decoded.Key)
	}
	if decoded.AEAD != cfg.AEAD {
		t.Fatalf("aead mismatch, got %s", decoded.AEAD)
	}
	if decoded.CustomTable != cfg.CustomTable {
		t.Fatalf("custom table mismatch, got %s", decoded.CustomTable)
	}
	if decoded.EnablePureDownlink != cfg.EnablePureDownlink {
		t.Fatalf("downlink mode mismatch")
	}
	if decoded.ASCII != "prefer_ascii" {
		t.Fatalf("ascii mismatch, got %s", decoded.ASCII)
	}
}

func TestShortLinkRoundTrip_CustomTablesAndCDN(t *testing.T) {
	cfg := &Config{
		Mode:               "client",
		LocalPort:          1081,
		ServerAddress:      "cc.futai.io:443",
		Key:                "deadbeef",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		CustomTables:       []string{"xpxvvpvv", "vxpvxvvp"},
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "auto",
		HTTPMaskTLS:        true,
		HTTPMaskMultiplex:  "auto",
	}

	link, err := BuildShortLinkFromConfig(cfg, "")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}

	decoded, err := BuildConfigFromShortLink(link)
	if err != nil {
		t.Fatalf("BuildConfigFromShortLink error: %v", err)
	}

	if decoded.ServerAddress != cfg.ServerAddress {
		t.Fatalf("server address mismatch, got %s", decoded.ServerAddress)
	}
	if len(decoded.CustomTables) != len(cfg.CustomTables) {
		t.Fatalf("custom tables length mismatch, got %d", len(decoded.CustomTables))
	}
	for i := range cfg.CustomTables {
		if decoded.CustomTables[i] != cfg.CustomTables[i] {
			t.Fatalf("custom tables[%d] mismatch, got %s", i, decoded.CustomTables[i])
		}
	}
	if decoded.CustomTable != cfg.CustomTables[0] {
		t.Fatalf("custom table fallback mismatch, got %s", decoded.CustomTable)
	}
	if decoded.HTTPMaskMode != "auto" {
		t.Fatalf("http mask mode mismatch, got %s", decoded.HTTPMaskMode)
	}
	if !decoded.HTTPMaskTLS {
		t.Fatalf("http mask tls mismatch, got %v", decoded.HTTPMaskTLS)
	}
	if decoded.HTTPMaskMultiplex != "auto" {
		t.Fatalf("http mask multiplex mismatch, got %s", decoded.HTTPMaskMultiplex)
	}
	if decoded.DisableHTTPMask {
		t.Fatalf("disable http mask mismatch, got %v", decoded.DisableHTTPMask)
	}
}

func TestShortLinkIPv6ServerAddress(t *testing.T) {
	serverAddr := net.JoinHostPort("2001:db8::1", "443")
	cfg := &Config{
		Mode:          "client",
		LocalPort:     1081,
		ServerAddress: serverAddr,
		Key:           "deadbeef",
	}

	link, err := BuildShortLinkFromConfig(cfg, "")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}

	decoded, err := BuildConfigFromShortLink(link)
	if err != nil {
		t.Fatalf("BuildConfigFromShortLink error: %v", err)
	}
	if decoded.ServerAddress != serverAddr {
		t.Fatalf("server address mismatch, got %s", decoded.ServerAddress)
	}
}

func TestShortLinkAdvertiseServer(t *testing.T) {
	cfg := &Config{
		Mode:               "server",
		LocalPort:          9443,
		Key:                "deadbeef",
		ASCII:              "",
		AEAD:               "",
		EnablePureDownlink: true,
		FallbackAddr:       "127.0.0.1:80",
	}

	link, err := BuildShortLinkFromConfig(cfg, "example.com")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}
	if link == "" {
		t.Fatalf("empty link")
	}
}

func TestShortLinkAdvertiseHostWithPort(t *testing.T) {
	cfg := &Config{
		Mode:               "server",
		LocalPort:          8080,
		Key:                "deadbeef",
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "auto",
	}

	link, err := BuildShortLinkFromConfig(cfg, "cc.futai.io:443")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}

	decoded, err := BuildConfigFromShortLink(link)
	if err != nil {
		t.Fatalf("BuildConfigFromShortLink error: %v", err)
	}
	if decoded.ServerAddress != "cc.futai.io:443" {
		t.Fatalf("server address mismatch, got %s", decoded.ServerAddress)
	}
	if decoded.HTTPMaskMode != "auto" {
		t.Fatalf("http mask mode mismatch, got %s", decoded.HTTPMaskMode)
	}
}

func TestShortLinkServerDeriveHostFromFallback(t *testing.T) {
	cfg := &Config{
		Mode:               "server",
		LocalPort:          10059,
		Key:                "deadbeef",
		EnablePureDownlink: true,
		FallbackAddr:       "8.219.204.112:11415",
		DisableHTTPMask:    false,
		HTTPMaskMode:       "poll",
	}

	link, err := BuildShortLinkFromConfig(cfg, "")
	if err != nil {
		t.Fatalf("BuildShortLinkFromConfig error: %v", err)
	}

	decoded, err := BuildConfigFromShortLink(link)
	if err != nil {
		t.Fatalf("BuildConfigFromShortLink error: %v", err)
	}

	if decoded.ServerAddress != "8.219.204.112:10059" {
		t.Fatalf("server address mismatch, got %s", decoded.ServerAddress)
	}
	if decoded.HTTPMaskMode != "poll" {
		t.Fatalf("http mask mode mismatch, got %s", decoded.HTTPMaskMode)
	}
}

func TestShortLinkInvalidScheme(t *testing.T) {
	if _, err := BuildConfigFromShortLink("http://bad"); err == nil {
		t.Fatalf("expected error for bad scheme")
	}
}

func TestShortLinkMissingFields(t *testing.T) {
	payload := map[string]string{}
	raw, _ := json.Marshal(payload)
	link := "sudoku://" + base64.RawURLEncoding.EncodeToString(raw)
	if _, err := BuildConfigFromShortLink(link); err == nil {
		t.Fatalf("expected error for missing fields")
	}
}
