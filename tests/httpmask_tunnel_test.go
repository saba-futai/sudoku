package tests

import (
	"bufio"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func startHTTPMaskTunnelEchoServer(t testing.TB, serverCfg *apis.ProtocolConfig) (addr string, stop func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := apis.NewHTTPMaskTunnelServer(serverCfg)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(raw net.Conn) {
				defer raw.Close()

				tunnelConn, _, handled, err := srv.HandleConn(raw)
				if err != nil || !handled || tunnelConn == nil {
					return
				}
				defer tunnelConn.Close()
				io.Copy(tunnelConn, tunnelConn)
			}(c)
		}
	}()

	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestHTTPMaskTunnel_XHTTP(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-xhttp"

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

	addr, stop := startHTTPMaskTunnelEchoServer(t, serverCfg)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := apis.Dial(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := []byte(strings.Repeat("a", 256*1024)) // large enough to exercise streaming
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch")
	}
}

func TestHTTPMaskTunnel_PHT(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-pht"

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

	addr, stop := startHTTPMaskTunnelEchoServer(t, serverCfg)
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

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	conn, err := apis.Dial(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello pht")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q", buf)
	}
}

func TestHTTPMaskTunnel_AutoFallbackToPHT(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-auto-fallback"

	// Server only enables PHT; XHTTP must fail fast so client can fall back.
	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "pht",
	}

	addr, stop := startHTTPMaskTunnelEchoServer(t, serverCfg)
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
		HTTPMaskMode:       "auto",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, err := apis.Dial(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello auto")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q", buf)
	}
}

func TestHTTPMaskTunnel_Boundary_InvalidToken(t *testing.T) {
	// This test validates the HTTP surface behavior (no panics, proper rejection).
	addr, stop := startHTTPMaskTunnelEchoServer(t, &apis.ProtocolConfig{
		Key:                     "k",
		AEADMethod:              "chacha20-poly1305",
		Table:                   sudoku.NewTable("seed", "prefer_ascii"),
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	})
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// PHT pull with invalid token must be rejected.
	io.WriteString(c, "GET /stream?token=bad HTTP/1.1\r\nHost: x\r\nX-Sudoku-Tunnel: pht\r\nX-Sudoku-Version: 1\r\n\r\n")
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(line, "403") {
		t.Fatalf("expected 403, got %q", strings.TrimSpace(line))
	}
}

func TestHTTPMaskTunnel_Boundary_XHTTPBadPath(t *testing.T) {
	addr, stop := startHTTPMaskTunnelEchoServer(t, &apis.ProtocolConfig{
		Key:                     "k",
		AEADMethod:              "chacha20-poly1305",
		Table:                   sudoku.NewTable("seed", "prefer_ascii"),
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	})
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	io.WriteString(c, "POST /not-allowed HTTP/1.1\r\nHost: x\r\nX-Sudoku-Tunnel: xhttp\r\nX-Sudoku-Version: 1\r\n\r\n")
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(line, "404") {
		t.Fatalf("expected 404, got %q", strings.TrimSpace(line))
	}
}

func TestHTTPMaskTunnel_Stress(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-stress-httpmask"

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

	addr, stop := startHTTPMaskTunnelEchoServer(t, serverCfg)
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const clients = 80
	var wg sync.WaitGroup
	wg.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			defer wg.Done()
			conn, err := apis.Dial(ctx, clientCfg)
			if err != nil {
				return
			}
			defer conn.Close()
			msg := []byte("ping")
			_, _ = conn.Write(msg)
			buf := make([]byte, len(msg))
			_, _ = io.ReadFull(conn, buf)
		}()
	}
	wg.Wait()
}

func TestHTTPMaskTunnel_StressPHT(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-stress-pht"

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

	addr, stop := startHTTPMaskTunnelEchoServer(t, serverCfg)
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

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	const clients = 20
	var wg sync.WaitGroup
	wg.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			defer wg.Done()
			conn, err := apis.Dial(ctx, clientCfg)
			if err != nil {
				return
			}
			defer conn.Close()
			msg := []byte("ping")
			_, _ = conn.Write(msg)
			buf := make([]byte, len(msg))
			_, _ = io.ReadFull(conn, buf)
		}()
	}
	wg.Wait()
}
