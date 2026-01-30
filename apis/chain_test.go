package apis

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestDial_ChainHops(t *testing.T) {
	key := "test-chain"
	table := sudoku.NewTable(key, "prefer_entropy")

	serverCfg := &ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         true,
	}
	if err := serverCfg.Validate(); err != nil {
		t.Fatalf("server cfg validate: %v", err)
	}

	exitLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen exit: %v", err)
	}
	defer exitLn.Close()

	go func() {
		for {
			raw, err := exitLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tunnelConn, _, err := ServerHandshake(c, serverCfg)
				if err != nil {
					return
				}
				defer tunnelConn.Close()
				_, _ = io.Copy(tunnelConn, tunnelConn)
			}(raw)
		}
	}()

	entryLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen entry: %v", err)
	}
	defer entryLn.Close()

	go func() {
		for {
			raw, err := entryLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()

				tunnelConn, target, err := ServerHandshake(c, serverCfg)
				if err != nil || target == "" {
					return
				}
				defer tunnelConn.Close()

				up, err := net.DialTimeout("tcp", target, 5*time.Second)
				if err != nil {
					return
				}
				defer up.Close()

				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _, _ = io.Copy(up, tunnelConn) }()
				go func() { defer wg.Done(); _, _ = io.Copy(tunnelConn, up) }()
				wg.Wait()
			}(raw)
		}
	}()

	clientCfg := &ProtocolConfig{
		ServerAddress:      entryLn.Addr().String(),
		ChainHops:          []string{exitLn.Addr().String()},
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := Dial(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello")
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", string(buf), string(msg))
	}
}
