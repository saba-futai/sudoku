package apis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestUserHash_StableAcrossTableRotation(t *testing.T) {
	tables := []*sudoku.Table{
		sudoku.NewTable("seed-a", "prefer_ascii"),
		sudoku.NewTable("seed-b", "prefer_ascii"),
	}
	key := "userhash-stability-key"

	serverCfg := &ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Tables:                  tables,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         true,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	const attempts = 32
	hashCh := make(chan string, attempts)
	errCh := make(chan error, attempts)

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				tunnelConn, userHash, fail, err := ServerHandshakeFlexibleWithUserHash(conn, serverCfg)
				if err != nil {
					_ = fail(err)
					errCh <- err
					return
				}
				_ = tunnelConn.Close()
				hashCh <- userHash
			}(c)
		}
	}()

	clientCfg := &ProtocolConfig{
		ServerAddress:      ln.Addr().String(),
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Tables:             tables,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	validate := func(c *ProtocolConfig) error {
		if c == nil {
			return fmt.Errorf("config is required")
		}
		if err := c.Validate(); err != nil {
			return err
		}
		if strings.TrimSpace(c.ServerAddress) == "" {
			return fmt.Errorf("ServerAddress cannot be empty")
		}
		return nil
	}

	for i := 0; i < attempts; i++ {
		c, err := establishBaseConn(ctx, clientCfg, validate)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.Close()
	}

	unique := map[string]struct{}{}
	deadline := time.After(10 * time.Second)
	for i := 0; i < attempts; i++ {
		select {
		case err := <-errCh:
			t.Fatalf("server handshake error: %v", err)
		case h := <-hashCh:
			unique[h] = struct{}{}
		case <-deadline:
			t.Fatalf("timeout waiting for server handshakes")
		}
	}
	if len(unique) != 1 {
		t.Fatalf("user hash should be stable across table rotation; got %d distinct values", len(unique))
	}
}
