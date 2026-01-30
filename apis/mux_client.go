package apis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/saba-futai/sudoku/internal/tunnel"
)

// MuxClient opens multiple target connections over a single Sudoku tunnel (single tunnel, multi-target).
//
// This avoids paying the HTTP tunnel + Sudoku handshake RTT for every target connection when HTTPMask
// tunnel modes are enabled.
type MuxClient struct {
	cfg *ProtocolConfig

	mu       sync.Mutex
	cond     *sync.Cond
	creating bool
	mux      *tunnel.MuxClient
}

func NewMuxClient(cfg *ProtocolConfig) (*MuxClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	if strings.TrimSpace(cfg.ServerAddress) == "" {
		return nil, fmt.Errorf("ServerAddress cannot be empty")
	}
	return &MuxClient{cfg: cfg}, nil
}

func (c *MuxClient) Dial(ctx context.Context, targetAddr string) (net.Conn, error) {
	if c == nil || c.cfg == nil {
		return nil, fmt.Errorf("nil mux client")
	}
	if strings.TrimSpace(targetAddr) == "" {
		return nil, fmt.Errorf("target address cannot be empty")
	}

	mux, err := c.getOrCreateMux(ctx)
	if err != nil {
		return nil, err
	}

	conn, err := mux.Dial(targetAddr)
	if err == nil {
		return conn, nil
	}

	// One retry on a potentially stale session.
	c.resetMux()
	mux, err2 := c.getOrCreateMux(ctx)
	if err2 != nil {
		return nil, err
	}
	return mux.Dial(targetAddr)
}

func (c *MuxClient) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	mux := c.mux
	c.mux = nil
	c.creating = false
	if c.cond != nil {
		c.cond.Broadcast()
	}
	c.mu.Unlock()
	if mux != nil {
		return mux.Close()
	}
	return nil
}

func (c *MuxClient) resetMux() {
	c.mu.Lock()
	mux := c.mux
	c.mux = nil
	c.mu.Unlock()
	if mux != nil {
		_ = mux.Close()
	}
}

func (c *MuxClient) getOrCreateMux(ctx context.Context) (*tunnel.MuxClient, error) {
	c.mu.Lock()
	if c.cond == nil {
		c.cond = sync.NewCond(&c.mu)
	}
	for {
		if existing := c.mux; existing != nil {
			c.mu.Unlock()
			return existing, nil
		}
		if !c.creating {
			c.creating = true
			break
		}
		c.cond.Wait()
	}
	c.mu.Unlock()

	baseConn, err := establishBaseConn(ctx, c.cfg, func(cfg *ProtocolConfig) error { return cfg.Validate() }, nil)
	if err != nil {
		c.mu.Lock()
		c.creating = false
		c.cond.Broadcast()
		c.mu.Unlock()
		return nil, err
	}

	createdMux, err := tunnel.NewMuxClient(baseConn)
	if err != nil {
		_ = baseConn.Close()
		c.mu.Lock()
		c.creating = false
		c.cond.Broadcast()
		c.mu.Unlock()
		return nil, fmt.Errorf("start mux session failed: %w", err)
	}

	c.mu.Lock()
	c.mux = createdMux
	c.creating = false
	c.cond.Broadcast()
	c.mu.Unlock()
	return createdMux, nil
}
