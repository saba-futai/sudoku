package config

// ReverseConfig enables "reverse proxy over Sudoku tunnel": a client behind NAT keeps a tunnel to the server,
// and the server exposes client services via HTTP path prefixes (e.g. /gitea -> 127.0.0.1:3000 on client).
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
	Routes []ReverseRoute `json:"routes,omitempty"`
}

type ReverseRoute struct {
	// Path is the public path prefix. Examples: "/gitea", "/nas".
	Path string `json:"path"`
	// Target is the client-side TCP target in "host:port" form. Example: "127.0.0.1:3000".
	Target string `json:"target"`
	// StripPrefix controls whether the prefix should be stripped before proxying.
	// Default: true.
	StripPrefix *bool `json:"strip_prefix,omitempty"`
	// HostHeader optionally overrides the HTTP Host header when proxying.
	HostHeader string `json:"host_header,omitempty"`
}
