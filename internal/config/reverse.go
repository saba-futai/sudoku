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

// ReverseConfig enables "reverse proxy over Sudoku tunnel": a client behind NAT keeps a tunnel to the server,
// and the server exposes client services via HTTP path prefixes (e.g. /gitea -> gitea.intra.example.com:3000 on client).
//
// - Server mode: set Listen (e.g. ":8081") to start the reverse HTTP entry.
// - Client mode: set Routes to expose local services to the server.
type ReverseConfig struct {
	// Listen enables the server-side reverse HTTP entrypoint.
	// Example: ":8081"
	// Empty disables the reverse HTTP server.
	Listen string `json:"listen,omitempty"`

	// ClientID is an optional stable identifier for this reverse client (used for logging).
	// When empty, the server may fall back to the handshake user hash (if available).
	ClientID string `json:"client_id,omitempty"`

	// Routes is the list of client services to expose.
	// Each route maps a public path prefix to a client-side TCP target (HTTP service).
	// Targets may use either an IP literal or a domain name.
	Routes []ReverseRoute `json:"routes,omitempty"`
}

type ReverseRoute struct {
	// Path is the public path prefix for HTTP reverse. Examples: "/gitea", "/nas".
	//
	// When Path is empty, the route becomes a raw TCP reverse mapping on reverse.listen (no HTTP).
	// In that case, the server will forward non-HTTP connections to Target.
	// Only one TCP route is supported per reverse.listen entry.
	Path string `json:"path"`
	// Target is the client-side TCP target in "host:port" form.
	// The host may be an IP literal or a domain name.
	Target string `json:"target"`
	// StripPrefix controls whether the prefix should be stripped before proxying.
	// Default: true.
	StripPrefix *bool `json:"strip_prefix,omitempty"`
	// HostHeader optionally overrides the HTTP Host header when proxying.
	// When empty and Target uses a domain host, the proxy forwards that target host by default.
	HostHeader string `json:"host_header,omitempty"`
}
