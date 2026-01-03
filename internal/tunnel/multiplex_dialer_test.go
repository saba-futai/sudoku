package tunnel

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/multiplex"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestAdaptiveDialer_MultiplexReusesOneConn(t *testing.T) {
	cfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		Key:                "test-key-123",
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "legacy",
		HTTPMaskMultiplex:  "on",
	}
	table := sudoku.NewTable(cfg.Key, cfg.ASCII)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	cfg.ServerAddress = ln.Addr().String()

	var accepts int64
	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt64(&accepts, 1)
			go func(c net.Conn) {
				defer c.Close()

				upgraded, _, err := HandshakeAndUpgradeWithTablesMeta(c, cfg, []*sudoku.Table{table})
				if err != nil {
					return
				}
				defer upgraded.Close()

				first := make([]byte, 1)
				if _, err := io.ReadFull(upgraded, first); err != nil {
					return
				}
				if first[0] != multiplex.MagicByte {
					return
				}
				v, err := multiplex.ReadVersion(upgraded)
				if err != nil {
					return
				}
				if err := multiplex.ValidateVersion(v); err != nil {
					return
				}

				sess, err := multiplex.NewServerSession(upgraded)
				if err != nil {
					return
				}
				defer sess.Close()

				for {
					stream, err := sess.AcceptStream()
					if err != nil {
						return
					}
					go func(s net.Conn) {
						defer s.Close()
						if _, _, _, err := protocol.ReadAddress(s); err != nil {
							return
						}
						io.Copy(s, s)
					}(stream)
				}
			}(raw)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	dialer := &AdaptiveDialer{
		BaseDialer: BaseDialer{
			Config: cfg,
			Tables: []*sudoku.Table{table},
		},
	}

	c1, err := dialer.Dial("example.com:80")
	if err != nil {
		t.Fatalf("dial 1: %v", err)
	}
	_, _ = c1.Write([]byte("hi"))
	buf := make([]byte, 2)
	_, _ = io.ReadFull(c1, buf)
	_ = c1.Close()

	c2, err := dialer.Dial("example.com:80")
	if err != nil {
		t.Fatalf("dial 2: %v", err)
	}
	_, _ = c2.Write([]byte("ok"))
	_, _ = io.ReadFull(c2, buf)
	_ = c2.Close()

	if got := atomic.LoadInt64(&accepts); got != 1 {
		t.Fatalf("expected 1 underlying TCP accept, got %d", got)
	}
}
