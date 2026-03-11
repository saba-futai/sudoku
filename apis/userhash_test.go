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
package apis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
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
		c, err := establishBaseConn(ctx, clientCfg, validate, nil)
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
