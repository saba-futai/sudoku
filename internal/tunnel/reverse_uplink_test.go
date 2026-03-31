package tunnel

import (
	"net"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func TestBaseDialerDialBase_UsesPureUplink(t *testing.T) {
	testBaseDialerUplinkMode(t, false)
}

func TestBaseDialerDialReverseBase_UsesPackedUplink(t *testing.T) {
	testBaseDialerUplinkMode(t, true)
}

func testBaseDialerUplinkMode(t *testing.T, reverse bool) {
	t.Helper()

	table := sudoku.NewTable("reverse-uplink-seed", "prefer_entropy")
	serverCfg := &config.Config{
		Key:                "k",
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		HTTPMask:           config.HTTPMaskConfig{Disable: true},
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	metaCh := make(chan *HandshakeMeta, 1)
	errCh := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		conn, meta, err := HandshakeAndUpgradeWithTablesMeta(raw, serverCfg, []*sudoku.Table{table})
		if err == nil && conn != nil {
			_ = conn.Close()
		}
		if err != nil {
			errCh <- err
			return
		}
		metaCh <- meta
	}()

	dialer := &BaseDialer{
		Config: &config.Config{
			ServerAddress:      ln.Addr().String(),
			Key:                "k",
			AEAD:               "chacha20-poly1305",
			PaddingMin:         0,
			PaddingMax:         0,
			EnablePureDownlink: true,
			HTTPMask:           config.HTTPMaskConfig{Disable: true},
		},
		Tables: []*sudoku.Table{table},
	}

	var conn net.Conn
	if reverse {
		conn, err = dialer.DialReverseBase()
	} else {
		conn, err = dialer.DialBase()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()

	select {
	case err := <-errCh:
		t.Fatalf("server handshake: %v", err)
	case meta := <-metaCh:
		if meta == nil {
			t.Fatalf("missing handshake meta")
		}
		if meta.UplinkPacked != reverse {
			t.Fatalf("uplink packed=%v, want %v", meta.UplinkPacked, reverse)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("handshake timeout")
	}
}
