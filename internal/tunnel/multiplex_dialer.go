package tunnel

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/multiplex"
)

type AdaptiveDialer struct {
	BaseDialer

	mu               sync.Mutex
	mux              *multiplex.Session
	muxBackoffUntil  time.Time
	muxLastFailError error
}

func (d *AdaptiveDialer) Dial(destAddrStr string) (net.Conn, error) {
	mode := normalizeHTTPMaskMultiplex(d.Config)
	if mode == "off" || d.Config == nil || d.Config.DisableHTTPMask {
		return d.dialDirect(destAddrStr)
	}
	if mode == "auto" && !httpTunnelModeEnabled(d.Config) {
		return d.dialDirect(destAddrStr)
	}

	conn, err := d.dialMultiplex(destAddrStr, mode)
	if err == nil {
		return conn, nil
	}

	if mode == "auto" {
		return d.dialDirect(destAddrStr)
	}
	return nil, err
}

func (d *AdaptiveDialer) DialUDPOverTCP() (net.Conn, error) {
	return d.dialUoT()
}

func (d *AdaptiveDialer) dialDirect(destAddrStr string) (net.Conn, error) {
	cConn, err := d.dialBase()
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteAddress(cConn, destAddrStr); err != nil {
		_ = cConn.Close()
		return nil, fmt.Errorf("write address failed: %w", err)
	}
	return cConn, nil
}

func (d *AdaptiveDialer) dialMultiplex(destAddrStr, mode string) (net.Conn, error) {
	for attempt := 0; attempt < 2; attempt++ {
		sess, err := d.getOrCreateMuxSession(mode)
		if err != nil {
			return nil, err
		}

		stream, err := sess.OpenStream()
		if err != nil {
			d.resetMuxSession()
			continue
		}
		if err := protocol.WriteAddress(stream, destAddrStr); err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("write address failed: %w", err)
		}
		return stream, nil
	}
	return nil, fmt.Errorf("multiplex open stream failed")
}

func httpTunnelModeEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMode))
	switch mode {
	case "stream", "poll", "auto":
		return true
	default:
		return false
	}
}

func normalizeHTTPMaskMultiplex(cfg *config.Config) string {
	if cfg == nil {
		return "off"
	}
	switch strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMultiplex)) {
	case "", "off":
		return "off"
	case "auto":
		return "auto"
	case "on":
		return "on"
	default:
		return "off"
	}
}

func (d *AdaptiveDialer) getOrCreateMuxSession(mode string) (*multiplex.Session, error) {
	if d == nil || d.Config == nil {
		return nil, fmt.Errorf("nil dialer/config")
	}

	if mode == "auto" {
		d.mu.Lock()
		backoffUntil := d.muxBackoffUntil
		d.mu.Unlock()
		if time.Now().Before(backoffUntil) {
			return nil, fmt.Errorf("multiplex temporarily disabled: %v", d.muxLastFailError)
		}
	}

	d.mu.Lock()
	if d.mux != nil && !d.mux.IsClosed() {
		s := d.mux
		d.mu.Unlock()
		return s, nil
	}
	d.mu.Unlock()

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.mux != nil && !d.mux.IsClosed() {
		return d.mux, nil
	}

	baseConn, err := d.dialBase()
	if err != nil {
		d.noteMuxFailure(mode, err)
		return nil, err
	}

	if err := multiplex.WritePreface(baseConn); err != nil {
		_ = baseConn.Close()
		d.noteMuxFailure(mode, err)
		return nil, fmt.Errorf("write multiplex preface failed: %w", err)
	}

	sess, err := multiplex.NewClientSession(baseConn)
	if err != nil {
		d.noteMuxFailure(mode, err)
		return nil, fmt.Errorf("start multiplex session failed: %w", err)
	}

	d.mux = sess
	return sess, nil
}

func (d *AdaptiveDialer) noteMuxFailure(mode string, err error) {
	if mode != "auto" {
		return
	}
	d.muxLastFailError = err
	d.muxBackoffUntil = time.Now().Add(45 * time.Second)
}

func (d *AdaptiveDialer) resetMuxSession() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mux != nil {
		_ = d.mux.Close()
		d.mux = nil
	}
}
