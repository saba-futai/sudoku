package tests

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func BenchmarkHTTPMaskTunnel_XHTTP(b *testing.B) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "bench-key-xhttp"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}
	addr, stop := startHTTPMaskTunnelEchoServer(b, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "xhttp",
	}

	msg := []byte("ping")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := apis.Dial(ctx, clientCfg)
		if err != nil {
			cancel()
			b.Fatalf("dial: %v", err)
		}
		_, _ = conn.Write(msg)
		buf := make([]byte, len(msg))
		_, _ = io.ReadFull(conn, buf)
		_ = conn.Close()
		cancel()
	}
}

func BenchmarkHTTPMaskTunnel_PHT(b *testing.B) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "bench-key-pht"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}
	addr, stop := startHTTPMaskTunnelEchoServer(b, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "pht",
	}

	msg := []byte("ping")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := apis.Dial(ctx, clientCfg)
		if err != nil {
			cancel()
			b.Fatalf("dial: %v", err)
		}
		_, _ = conn.Write(msg)
		buf := make([]byte, len(msg))
		_, _ = io.ReadFull(conn, buf)
		_ = conn.Close()
		cancel()
	}
}
