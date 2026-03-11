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
package apis

import (
	"fmt"
	"strings"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// ProtocolConfig defines all parameters required by the Sudoku protocol stack.
//
// The Sudoku protocol is a multi-layer encrypted tunnel:
//  1. HTTP mask layer: disguises traffic as HTTP POST requests
//  2. Sudoku obfuscation layer: encodes data using Sudoku puzzle patterns
//  3. AEAD encryption layer: provides confidentiality and integrity
//  4. Protocol layer: handles handshake, address transfer, etc.
type ProtocolConfig struct {
	// ============ Connection Info ============

	// ServerAddress is the server address (client-side only).
	// Format: "host:port" or "ip:port"
	// Example: "example.com:443" or "1.2.3.4:8080"
	ServerAddress string

	// ============ Encryption & Obfuscation ============

	// Key is the pre-shared key used for AEAD encryption.
	// Both sides must agree on this value; use "./sudoku -keygen" to generate one.
	Key string

	// AEADMethod specifies the AEAD cipher.
	// Valid values:
	//   - "aes-128-gcm": AES-128-GCM (fast, good hardware acceleration)
	//   - "chacha20-poly1305": ChaCha20-Poly1305 (good pure-software performance)
	//   - "none": no encryption (testing only, never use in production)
	AEADMethod string

	// Table is the Sudoku encoding map (must be identical on client and server).
	// Create with sudoku.NewTable(seed, "prefer_ascii"|"prefer_entropy") or
	// sudoku.NewTableWithCustom(seed, "prefer_entropy", "<xpxvvpvv>").
	// Must not be nil (unless Tables is set).
	Table *sudoku.Table

	// Tables is an optional candidate set for table rotation.
	// If provided (len>0), the client will pick one table per connection and the server will
	// probe the handshake to detect which one was used, keeping the handshake format unchanged.
	// When Tables is set, Table may be nil.
	Tables []*sudoku.Table

	// ============ Sudoku Padding ============

	// PaddingMin is the minimum padding rate (0-100).
	// Minimum probability (%) of inserting a padding byte during encoding.
	PaddingMin int

	// PaddingMax is the maximum padding rate (0-100).
	// Maximum probability (%) of inserting a padding byte during encoding.
	// Must be >= PaddingMin.
	PaddingMax int

	// EnablePureDownlink controls downlink encoding.
	// When false, bandwidth-optimized 6-bit packed downlink is used (requires AEAD).
	EnablePureDownlink bool

	// ============ Client-Only Fields ============

	// TargetAddress is the final destination the client wants to reach (client-side only).
	// Format: "host:port"
	// Example: "google.com:443" or "1.1.1.1:53"
	TargetAddress string

	// ============ Server-Only Fields ============

	// HandshakeTimeoutSeconds is the handshake timeout in seconds (server-side only).
	// Recommended: 5-10.
	// Too small may cause failures on slow networks.
	// Too large may make the server vulnerable to slowloris attacks.
	HandshakeTimeoutSeconds int

	// ============ General Switches ============

	// DisableHTTPMask disables the HTTP mask layer.
	// Default false (mask enabled).
	// When true, the client does not send a mask header and the server does not check for one.
	// Note: the server supports auto-detection, so even with this set to false it can handle
	// clients without a mask header (as long as the first bytes don't look like an HTTP method).
	DisableHTTPMask bool

	// HTTPMaskMode controls how the "HTTP mask" behaves:
	//   - "legacy": write a fake HTTP/1.1 header then switch to raw stream (default, not CDN-compatible)
	//   - "stream": real HTTP tunnel (split-stream), CDN-compatible
	//   - "poll": plain HTTP tunnel (authorize/push/pull), strong restricted-network pass-through
	//   - "auto": try stream then fall back to poll
	//   - "ws": WebSocket tunnel (ws:// or wss://)
	HTTPMaskMode string

	// HTTPMaskTLSEnabled enables HTTPS for HTTP tunnel modes (client-side).
	// When false, HTTP tunnel modes use plain HTTP (no port-based TLS inference).
	HTTPMaskTLSEnabled bool

	// HTTPMaskHost optionally overrides the HTTP Host header / SNI host for HTTP tunnel modes (client-side).
	// When empty, it is derived from ServerAddress.
	HTTPMaskHost string

	// HTTPMaskPathRoot optionally prefixes all HTTP mask paths with a first-level segment.
	// Example: "aabbcc" => "/aabbcc/session", "/aabbcc/api/v1/upload", ...
	HTTPMaskPathRoot string

	// HTTPMaskMultiplex controls multiplex behavior when HTTPMask tunnel modes are enabled:
	//   - "off": disable reuse; each Dial establishes its own HTTPMask tunnel
	//   - "auto": reuse underlying HTTP connections across multiple tunnel dials (HTTP/1.1 keep-alive / HTTP/2)
	//   - "on": enable "single tunnel, multi-target" mux when using apis.NewMuxClient (Dial behaves like "auto")
	HTTPMaskMultiplex string
}

// Validate checks the configuration for correctness.
// Returns the first error found, or nil if the configuration is valid.
func (c *ProtocolConfig) Validate() error {
	if c.Table == nil && len(c.Tables) == 0 {
		return fmt.Errorf("Table cannot be nil (or provide Tables)")
	}
	for i, t := range c.Tables {
		if t == nil {
			return fmt.Errorf("Tables[%d] cannot be nil", i)
		}
	}

	if c.Key == "" {
		return fmt.Errorf("Key cannot be empty")
	}

	switch c.AEADMethod {
	case "aes-128-gcm", "chacha20-poly1305", "none":
		// Valid values
	default:
		return fmt.Errorf("invalid AEADMethod: %s, must be one of: aes-128-gcm, chacha20-poly1305, none", c.AEADMethod)
	}

	if c.PaddingMin < 0 || c.PaddingMin > 100 {
		return fmt.Errorf("PaddingMin must be between 0 and 100, got %d", c.PaddingMin)
	}

	if c.PaddingMax < 0 || c.PaddingMax > 100 {
		return fmt.Errorf("PaddingMax must be between 0 and 100, got %d", c.PaddingMax)
	}

	if c.PaddingMax < c.PaddingMin {
		return fmt.Errorf("PaddingMax (%d) must be >= PaddingMin (%d)", c.PaddingMax, c.PaddingMin)
	}

	if !c.EnablePureDownlink && c.AEADMethod == "none" {
		return fmt.Errorf("bandwidth optimized downlink requires AEAD")
	}

	if c.HandshakeTimeoutSeconds < 0 {
		return fmt.Errorf("HandshakeTimeoutSeconds must be >= 0, got %d", c.HandshakeTimeoutSeconds)
	}

	switch strings.ToLower(strings.TrimSpace(c.HTTPMaskMode)) {
	case "", "legacy", "stream", "poll", "auto", "ws":
	default:
		return fmt.Errorf("invalid HTTPMaskMode: %s, must be one of: legacy, stream, poll, auto, ws", c.HTTPMaskMode)
	}

	switch strings.ToLower(strings.TrimSpace(c.HTTPMaskMultiplex)) {
	case "", "off", "auto", "on":
	default:
		return fmt.Errorf("invalid HTTPMaskMultiplex: %s, must be one of: off, auto, on", c.HTTPMaskMultiplex)
	}

	if v := strings.TrimSpace(c.HTTPMaskPathRoot); v != "" {
		v = strings.Trim(v, "/")
		if v == "" || strings.Contains(v, "/") {
			return fmt.Errorf("invalid HTTPMaskPathRoot: must be a single path segment")
		}
		for i := 0; i < len(v); i++ {
			c := v[i]
			switch {
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c >= '0' && c <= '9':
			case c == '_' || c == '-':
			default:
				return fmt.Errorf("invalid HTTPMaskPathRoot: contains invalid character %q", c)
			}
		}
	}

	return nil
}

// ValidateClient ensures the config carries the required client-side fields.
func (c *ProtocolConfig) ValidateClient() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.ServerAddress == "" {
		return fmt.Errorf("ServerAddress cannot be empty")
	}
	if c.TargetAddress == "" {
		return fmt.Errorf("TargetAddress cannot be empty")
	}
	return nil
}

// DefaultConfig returns a safe default configuration.
// Note: the returned config still requires Key, Table, ServerAddress (client) or TargetAddress (server).
func DefaultConfig() *ProtocolConfig {
	return &ProtocolConfig{
		AEADMethod:              "chacha20-poly1305",
		PaddingMin:              10,
		PaddingMax:              30,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		HTTPMaskMode:            "legacy",
		HTTPMaskMultiplex:       "off",
	}
}

func (c *ProtocolConfig) tableCandidates() []*sudoku.Table {
	if c == nil {
		return nil
	}
	if len(c.Tables) > 0 {
		return c.Tables
	}
	if c.Table != nil {
		return []*sudoku.Table{c.Table}
	}
	return nil
}
