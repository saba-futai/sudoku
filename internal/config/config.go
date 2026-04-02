/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package config

type Config struct {
	Mode               string         `json:"mode"`      // "client" or "server"
	Transport          string         `json:"transport"` // "tcp" or "udp"
	LocalPort          int            `json:"local_port"`
	ServerAddress      string         `json:"server_address"`
	FallbackAddr       string         `json:"fallback_address"`
	Key                string         `json:"key"`
	AEAD               string         `json:"aead"`              // "aes-128-gcm", "chacha20-poly1305", "none"
	SuspiciousAction   string         `json:"suspicious_action"` // "fallback" or "silent"
	PaddingMin         int            `json:"padding_min"`
	PaddingMax         int            `json:"padding_max"`
	RuleURLs           []string       `json:"rule_urls"`            // Routing rule sources; supports "global"/"direct" keywords and !url reject sources
	ProxyMode          string         `json:"proxy_mode"`           // Runtime state, populated by Load logic
	ASCII              string         `json:"ascii"`                // "prefer_entropy", "prefer_ascii", or directional "up_ascii_down_entropy"/"up_entropy_down_ascii"
	CustomTable        string         `json:"custom_table"`         // Optional: defines X/P/V layout, e.g. "xpxvvpvv"
	CustomTables       []string       `json:"custom_tables"`        // Optional: rotate among multiple X/P/V layouts
	EnablePureDownlink bool           `json:"enable_pure_downlink"` // Enable pure Sudoku downlink; false uses bandwidth-optimized packed encoding
	HTTPMask           HTTPMaskConfig `json:"httpmask"`

	Reverse *ReverseConfig `json:"reverse,omitempty"`
}
