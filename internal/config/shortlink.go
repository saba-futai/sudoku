package config

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// shortLinkPayload holds the minimal fields we expose in sudoku:// links.
type shortLinkPayload struct {
	Host           string   `json:"h"`            // server host / IP
	Port           int      `json:"p"`            // server port
	Key            string   `json:"k"`            // shared key
	ASCII          string   `json:"a,omitempty"`  // "ascii" or "entropy"
	AEAD           string   `json:"e,omitempty"`  // AEAD method
	MixPort        int      `json:"m,omitempty"`  // local mixed proxy port
	PackedDownlink bool     `json:"x,omitempty"`  // bandwidth-optimized downlink (non-pure Sudoku)
	CustomTable    string   `json:"t,omitempty"`  // optional custom byte layout
	CustomTables   []string `json:"ts,omitempty"` // optional custom byte layouts (rotation)
	// HTTP mask / tunnel controls (optional).
	DisableHTTPMask bool   `json:"hd,omitempty"` // when true, disable HTTP mask completely
	HTTPMaskMode    string `json:"hm,omitempty"` // "legacy" / "xhttp" / "pht" / "auto"
	HTTPMaskTLS     bool   `json:"ht,omitempty"` // enable HTTPS explicitly (otherwise inferred by port)
	HTTPMaskHost    string `json:"hh,omitempty"` // override HTTP Host/SNI in tunnel modes
}

// BuildShortLinkFromConfig builds a sudoku:// short link from the provided config.
//
// If cfg.ServerAddress is empty, advertiseHost can be used to provide the public host[:port].
// For server configs, when advertiseHost is empty, we try to derive the host from fallback_address (host part)
// and use local_port as the advertised port.
func BuildShortLinkFromConfig(cfg *Config, advertiseHost string) (string, error) {
	if cfg == nil {
		return "", errors.New("nil config")
	}

	host, port, err := deriveAdvertiseAddress(cfg, advertiseHost)
	if err != nil {
		return "", err
	}

	payload := shortLinkPayload{
		Host: host,
		Port: port,
		Key:  cfg.Key,
		AEAD: cfg.AEAD,
	}

	if cfg.Mode == "client" && cfg.LocalPort > 0 {
		payload.MixPort = cfg.LocalPort
	}
	if payload.MixPort == 0 {
		payload.MixPort = 1080 // reasonable default for mixed proxy
	}

	payload.PackedDownlink = !cfg.EnablePureDownlink
	payload.CustomTable = cfg.CustomTable
	if len(cfg.CustomTables) > 0 {
		payload.CustomTables = append([]string(nil), cfg.CustomTables...)
		// Keep field "t" as a backward-compatible single-table fallback for older clients.
		if strings.TrimSpace(payload.CustomTable) == "" {
			payload.CustomTable = cfg.CustomTables[0]
		}
	}

	payload.DisableHTTPMask = cfg.DisableHTTPMask
	mode := strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMode))
	if mode != "" && mode != "legacy" {
		payload.HTTPMaskMode = mode
	}
	if cfg.HTTPMaskTLS {
		payload.HTTPMaskTLS = true
	}
	if strings.TrimSpace(cfg.HTTPMaskHost) != "" {
		payload.HTTPMaskHost = strings.TrimSpace(cfg.HTTPMaskHost)
	}

	payload.ASCII = encodeASCII(cfg.ASCII)
	if payload.AEAD == "" {
		payload.AEAD = "chacha20-poly1305"
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	return "sudoku://" + base64.RawURLEncoding.EncodeToString(data), nil
}

// BuildConfigFromShortLink parses a sudoku:// short link and returns a client config.
// The generated config is ready to run a PAC proxy.
func BuildConfigFromShortLink(link string) (*Config, error) {
	if !strings.HasPrefix(link, "sudoku://") {
		return nil, errors.New("invalid scheme")
	}

	encoded := strings.TrimPrefix(link, "sudoku://")
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode short link failed: %w", err)
	}

	var payload shortLinkPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("invalid short link payload: %w", err)
	}

	if payload.Host == "" || payload.Port == 0 || payload.Key == "" {
		return nil, errors.New("short link missing required fields")
	}

	cfg := &Config{
		Mode:            "client",
		Transport:       "tcp",
		LocalPort:       payload.MixPort,
		ServerAddress:   net.JoinHostPort(payload.Host, strconv.Itoa(payload.Port)),
		Key:             payload.Key,
		CustomTable:     payload.CustomTable,
		CustomTables:    append([]string(nil), payload.CustomTables...),
		DisableHTTPMask: payload.DisableHTTPMask,
		HTTPMaskMode:    payload.HTTPMaskMode,
		HTTPMaskTLS:     payload.HTTPMaskTLS,
		HTTPMaskHost:    payload.HTTPMaskHost,
		AEAD:            payload.AEAD,
		PaddingMin:      5,
		PaddingMax:      15,
		ProxyMode:       "pac",
		RuleURLs: []string{
			"https://gh-proxy.org/https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/rule/Clash/China/China.list",
			"https://gh-proxy.org/https://raw.githubusercontent.com/fernvenue/chn-cidr-list/master/ipv4.yaml",
		},
	}

	if cfg.LocalPort == 0 {
		cfg.LocalPort = 1080
	}

	cfg.EnablePureDownlink = !payload.PackedDownlink
	if strings.TrimSpace(cfg.HTTPMaskMode) == "" {
		cfg.HTTPMaskMode = "legacy"
	}

	cfg.ASCII = decodeASCII(payload.ASCII)
	if cfg.AEAD == "" {
		cfg.AEAD = "none"
	}

	return cfg, nil
}

func encodeASCII(mode string) string {
	if strings.ToLower(mode) == "prefer_ascii" || mode == "ascii" {
		return "ascii"
	}
	return "entropy"
}

func decodeASCII(val string) string {
	switch strings.ToLower(val) {
	case "ascii", "prefer_ascii":
		return "prefer_ascii"
	default:
		return "prefer_entropy"
	}
}

func deriveAdvertiseAddress(cfg *Config, advertiseHost string) (string, int, error) {
	if cfg.ServerAddress != "" {
		host, portStr, err := net.SplitHostPort(cfg.ServerAddress)
		if err != nil {
			return "", 0, fmt.Errorf("invalid server_address: %w", err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			return "", 0, fmt.Errorf("invalid port in server_address: %w", err)
		}
		return host, port, nil
	}

	if advertiseHost != "" {
		// Allow advertiseHost in either "host" form (use cfg.LocalPort) or "host:port" form (explicit port).
		if h, p, err := net.SplitHostPort(advertiseHost); err == nil && h != "" && p != "" {
			port, err := strconv.Atoi(p)
			if err != nil {
				return "", 0, fmt.Errorf("invalid port in advertise host: %w", err)
			}
			return h, port, nil
		}
		if cfg.LocalPort > 0 {
			return advertiseHost, cfg.LocalPort, nil
		}
	}

	// Best-effort fallback for server-side configs:
	// if the user didn't provide a public host (CLI) nor server_address (config),
	// try to reuse fallback_address's host as the advertised host.
	//
	// This makes `-export-link` usable with typical server configs where the fallback
	// runs on the same machine (e.g. 127.0.0.1:80) or same public IP but different port.
	if cfg.Mode == "server" && advertiseHost == "" && cfg.LocalPort > 0 && cfg.FallbackAddr != "" {
		if h, _, err := net.SplitHostPort(cfg.FallbackAddr); err == nil && h != "" {
			return h, cfg.LocalPort, nil
		}
	}

	return "", 0, errors.New("cannot derive server address; set server_address or provide advertise host")
}
