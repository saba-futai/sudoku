package config

import (
	"bytes"
	"encoding/json"
	"strings"
)

func (c *Config) UnmarshalJSON(data []byte) error {
	type Alias Config
	// First, unmarshal the new schema (including the "httpmask" object).
	if err := json.Unmarshal(data, (*Alias)(c)); err != nil {
		return err
	}

	// Detect whether the new "httpmask" key exists. When present, it wins over legacy top-level fields.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}

	if raw, ok := obj["httpmask"]; ok {
		if v := bytes.TrimSpace(raw); len(v) > 0 && string(v) != "null" {
			return nil
		}
	}

	// Legacy flat fields -> httpmask object.
	var legacy struct {
		LegacyDisableHTTPMask    *bool  `json:"disable_http_mask"`
		LegacyHTTPMaskMode       string `json:"http_mask_mode"`
		LegacyHTTPMaskTLS        *bool  `json:"http_mask_tls"`
		LegacyHTTPMaskHost       string `json:"http_mask_host"`
		LegacyHTTPMaskPathRoot   string `json:"path_root"`
		LegacyHTTPMaskPathRootV0 string `json:"http_mask_path_root"`
		LegacyHTTPMaskMultiplex  string `json:"http_mask_multiplex"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		return err
	}

	if legacy.LegacyDisableHTTPMask != nil {
		c.HTTPMask.Disable = *legacy.LegacyDisableHTTPMask
	}
	if strings.TrimSpace(legacy.LegacyHTTPMaskMode) != "" {
		c.HTTPMask.Mode = strings.TrimSpace(legacy.LegacyHTTPMaskMode)
	}
	if legacy.LegacyHTTPMaskTLS != nil {
		c.HTTPMask.TLS = *legacy.LegacyHTTPMaskTLS
	}
	if strings.TrimSpace(legacy.LegacyHTTPMaskHost) != "" {
		c.HTTPMask.Host = strings.TrimSpace(legacy.LegacyHTTPMaskHost)
	}
	if strings.TrimSpace(legacy.LegacyHTTPMaskMultiplex) != "" {
		c.HTTPMask.Multiplex = strings.TrimSpace(legacy.LegacyHTTPMaskMultiplex)
	}

	pathRoot := strings.TrimSpace(legacy.LegacyHTTPMaskPathRoot)
	if pathRoot == "" {
		pathRoot = strings.TrimSpace(legacy.LegacyHTTPMaskPathRootV0)
	}
	if strings.TrimSpace(c.HTTPMask.PathRoot) == "" && pathRoot != "" {
		c.HTTPMask.PathRoot = pathRoot
	}

	return nil
}
