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
	TunnelModeWS     TunnelMode = "ws"
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
	case string(TunnelModeWS):
		return TunnelModeWS
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
	// EarlyHandshake folds the protocol handshake into the HTTP/WS setup round trip.
	// When the server accepts the early payload, DialTunnel returns a conn that is already post-handshake.
	// When the server does not echo early data, DialTunnel falls back to Upgrade.
	EarlyHandshake *ClientEarlyHandshake
	// Multiplex controls whether DialTunnel reuses underlying HTTP connections (keep-alive / h2).
	// Values: "off" disables global reuse; "auto"/"on" enables it. Empty defaults to "auto".
	Multiplex string
}

type ClientEarlyHandshake struct {
	RequestPayload []byte
	HandleResponse func(payload []byte) error
	Ready          func() bool
	WrapConn       func(raw net.Conn) (net.Conn, error)
}

type TunnelServerEarlyHandshake struct {
	Prepare func(payload []byte) (*PreparedServerEarlyHandshake, error)
}

type PreparedServerEarlyHandshake struct {
	ResponsePayload []byte
	WrapConn        func(raw net.Conn) (net.Conn, error)
	UserHash        string
}

type earlyHandshakeMeta interface {
	HTTPMaskEarlyHandshakeUserHash() string
	HTTPMaskEarlyHandshakeUplinkPacked() bool
}

type obfsMeta interface {
	SudokuUplinkPacked() bool
}

type earlyHandshakeConn struct {
	net.Conn
	userHash     string
	uplinkPacked bool
}

func (c *earlyHandshakeConn) HTTPMaskEarlyHandshakeUserHash() string {
	if c == nil {
		return ""
	}
	return c.userHash
}

func (c *earlyHandshakeConn) HTTPMaskEarlyHandshakeUplinkPacked() bool {
	if c == nil {
		return false
	}
	return c.uplinkPacked
}

func wrapEarlyHandshakeConn(conn net.Conn, userHash string, uplinkPacked bool) net.Conn {
	if conn == nil {
		return nil
	}
	return &earlyHandshakeConn{Conn: conn, userHash: userHash, uplinkPacked: uplinkPacked}
}

func wrappedConnUplinkPacked(conn net.Conn) bool {
	if conn == nil {
		return false
	}
	v, ok := conn.(obfsMeta)
	if !ok {
		return false
	}
	return v.SudokuUplinkPacked()
}

func EarlyHandshakeUserHash(conn net.Conn) (string, bool) {
	if conn == nil {
		return "", false
	}
	v, ok := conn.(earlyHandshakeMeta)
	if !ok {
		return "", false
	}
	return v.HTTPMaskEarlyHandshakeUserHash(), true
}

func EarlyHandshakeUplinkPacked(conn net.Conn) (bool, bool) {
	if conn == nil {
		return false, false
	}
	v, ok := conn.(earlyHandshakeMeta)
	if !ok {
		return false, false
	}
	return v.HTTPMaskEarlyHandshakeUplinkPacked(), true
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
	case TunnelModeWS:
		c, err := dialWS(ctx, serverAddress, opts)
		if err != nil {
			return nil, err
		}
		outConn := net.Conn(c)
		if opts.EarlyHandshake != nil && opts.EarlyHandshake.WrapConn != nil && (opts.EarlyHandshake.Ready == nil || opts.EarlyHandshake.Ready()) {
			upgraded, err := opts.EarlyHandshake.WrapConn(c)
			if err != nil {
				_ = c.Close()
				return nil, err
			}
			if upgraded != nil {
				outConn = upgraded
			}
			return outConn, nil
		}
		if opts.Upgrade != nil {
			upgraded, err := opts.Upgrade(c)
			if err != nil {
				_ = c.Close()
				return nil, err
			}
			if upgraded != nil {
				outConn = upgraded
			}
		}
		return outConn, nil
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
}
