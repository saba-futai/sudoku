package config

import (
	"encoding/json"
	"strings"
)

func (c *Config) UnmarshalJSON(data []byte) error {
	type Alias Config
	aux := struct {
		*Alias
		LegacyHTTPMaskPathRoot string `json:"http_mask_path_root"`
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	if strings.TrimSpace(c.HTTPMaskPathRoot) == "" && strings.TrimSpace(aux.LegacyHTTPMaskPathRoot) != "" {
		c.HTTPMaskPathRoot = strings.TrimSpace(aux.LegacyHTTPMaskPathRoot)
	}

	return nil
}
