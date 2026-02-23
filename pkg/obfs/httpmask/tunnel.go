package httpmask

import (
	"context"
	"fmt"
	mrand "math/rand"
	"net"
	"net/http"
	"strings"
	"time"
)

type TunnelMode string

const (
	TunnelModeLegacy TunnelMode = "legacy"
	TunnelModeStream TunnelMode = "stream"
	TunnelModePoll   TunnelMode = "poll"
	TunnelModeAuto   TunnelMode = "auto"
)

func normalizeTunnelMode(mode string) TunnelMode {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", string(TunnelModeLegacy):
		return TunnelModeLegacy
	case string(TunnelModeStream):
		return TunnelModeStream
	case string(TunnelModePoll):
		return TunnelModePoll
	case string(TunnelModeAuto):
		return TunnelModeAuto
	default:
		// Be conservative: unknown => legacy
		return TunnelModeLegacy
	}
}

func multiplexEnabled(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "auto", "on":
		return true
	default:
		return false
	}
}

type HandleResult int

const (
	HandlePassThrough HandleResult = iota
	HandleStartTunnel
	HandleDone
)

type TunnelDialOptions struct {
	Mode         string
	TLSEnabled   bool   // when true, use HTTPS; when false, use HTTP (no port-based inference)
	HostOverride string // optional Host header / SNI host (without scheme); port inferred from ServerAddress
	// PathRoot is an optional first-level path prefix for all HTTP tunnel endpoints.
	// Example: "aabbcc" => "/aabbcc/session", "/aabbcc/api/v1/upload", ...
	PathRoot string
	// AuthKey enables short-term HMAC auth for HTTP tunnel requests (anti-probing).
	// When set (non-empty), each HTTP request carries an Authorization bearer token derived from AuthKey.
	AuthKey string
	// Upgrade optionally wraps the raw tunnel conn and/or writes a small prelude before DialTunnel returns.
	// It is called with the raw tunnel conn; if it returns a non-nil conn, that conn is returned by DialTunnel.
	//
	// Upgrade is primarily used to reduce RTT by sending initial bytes (e.g. protocol handshake) while the
	// HTTP tunnel is still establishing.
	Upgrade func(raw net.Conn) (net.Conn, error)
	// Multiplex controls whether DialTunnel reuses underlying HTTP connections (keep-alive / h2).
	// Values: "off" disables global reuse; "auto"/"on" enables it. Empty defaults to "auto".
	Multiplex string
}

// DialTunnel establishes a bidirectional stream over HTTP:
//   - stream: split-stream (authorize + upload/pull endpoints; CDN-friendly)
//   - poll: authorize + push/pull polling tunnel (base64 framed)
//   - auto: try stream then fall back to poll
//
// The returned net.Conn carries the raw Sudoku stream (no HTTP headers).
func DialTunnel(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
	mode := normalizeTunnelMode(opts.Mode)
	if mode == TunnelModeLegacy {
		return nil, fmt.Errorf("legacy mode does not use http tunnel")
	}

	switch mode {
	case TunnelModeStream:
		return dialStreamFn(ctx, serverAddress, opts)
	case TunnelModePoll:
		return dialPollFn(ctx, serverAddress, opts)
	case TunnelModeAuto:
		// "stream" can hang on some CDNs that buffer uploads until request body completes.
		// Keep it on a short leash so we can fall back to poll within the caller's deadline.
		streamCtx, cancelStream := context.WithTimeout(ctx, 3*time.Second)
		c, errStream := dialStreamFn(streamCtx, serverAddress, opts)
		cancelStream()
		if errStream == nil {
			return c, nil
		}
		c, errPoll := dialPollFn(ctx, serverAddress, opts)
		if errPoll == nil {
			return c, nil
		}
		return nil, fmt.Errorf("auto tunnel failed: stream: %v; poll: %w", errStream, errPoll)
	default:
		return dialStreamFn(ctx, serverAddress, opts)
	}
}

var (
	dialStreamFn = dialStream
	dialPollFn   = dialPoll
)

func applyTunnelHeaders(h http.Header, host string, mode TunnelMode) {
	r := rngPool.Get().(*mrand.Rand)
	ua := userAgents[r.Intn(len(userAgents))]
	accept := accepts[r.Intn(len(accepts))]
	lang := acceptLanguages[r.Intn(len(acceptLanguages))]
	rngPool.Put(r)

	h.Set("User-Agent", ua)
	h.Set("Accept", accept)
	h.Set("Accept-Language", lang)
	h.Set("Cache-Control", "no-cache")
	h.Set("Pragma", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("Host", host)
	h.Set("X-Sudoku-Tunnel", string(mode))
	h.Set("X-Sudoku-Version", "1")
}
