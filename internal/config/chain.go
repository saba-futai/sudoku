package config

// ChainConfig configures multi-hop (chained) Sudoku proxying.
//
// When enabled on the client side, the client first connects to server_address, then asks that server to
// connect to each hop in order, performing a full Sudoku handshake on every hop (nested tunnels).
//
// Example:
//
//	"server_address": "entry.example.com:443",
//	"chain": { "hops": ["mid.example.com:443", "exit.example.com:443"] }
type ChainConfig struct {
	// Hops is the ordered list of additional Sudoku servers (host:port) after the entry server.
	// When empty, chaining is disabled.
	Hops []string `json:"hops"`
}
