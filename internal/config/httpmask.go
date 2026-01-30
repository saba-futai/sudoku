package config

// HTTPMaskConfig groups all HTTP masking / tunnel related settings.
//
// This is a "presentation layer" config that can be serialized to config.json as:
//
//	"httpmask": {
//	  "disable": false,
//	  "mode": "legacy|stream|poll|auto",
//	  "tls": false,
//	  "host": "",
//	  "path_root": "",
//	  "multiplex": "off|auto|on"
//	}
//
// Backward compatibility with legacy top-level fields is implemented in Config.UnmarshalJSON.
type HTTPMaskConfig struct {
	Disable bool   `json:"disable"`
	Mode    string `json:"mode"`
	TLS     bool   `json:"tls"`
	Host    string `json:"host"`
	// PathRoot optionally prefixes all HTTP mask paths with a first-level segment.
	// Example: "aabbcc" => "/aabbcc/session", "/aabbcc/api/v1/upload", ...
	PathRoot string `json:"path_root"`
	// Multiplex controls how connections are multiplexed when HTTP mask tunnel modes are enabled:
	//   - "off": disable reuse; each target dials its own HTTPMask tunnel
	//   - "auto": reuse underlying HTTP connections across multiple tunnel dials (HTTP/1.1 keep-alive / HTTP/2)
	//   - "on": keep a single long-lived tunnel and multiplex multiple target streams inside it (single tunnel, multi-target)
	Multiplex string `json:"multiplex"`
}
