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
	"io"
	"net"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func TestMuxSession_ServerHandshakeSessionAuto(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_entropy")

	targetLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer targetLn.Close()
	targetAddr := targetLn.Addr().String()

	go func() {
		c, err := targetLn.Accept()
		if err != nil {
			return
		}
		defer c.Close()

		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return
		}
		if string(buf) != "ping" {
			return
		}
		_, _ = c.Write([]byte("pong"))
	}()

	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen server: %v", err)
	}
	defer serverLn.Close()

	serverCfg := &ProtocolConfig{
		Key:                     "k",
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         true,
	}

	serverErr := make(chan error, 1)
	go func() {
		raw, err := serverLn.Accept()
		if err != nil {
			serverErr <- err
			return
		}

		conn, session, _, _, _, err := ServerHandshakeSessionAutoWithUserHash(raw, serverCfg)
		if err != nil {
			serverErr <- err
			return
		}
		if session != SessionMux {
			_ = conn.Close()
			serverErr <- fmt.Errorf("unexpected session kind: %v", session)
			return
		}

		serverErr <- HandleMuxWithDialer(conn, nil, func(target string) (net.Conn, error) {
			return net.DialTimeout("tcp", target, 5*time.Second)
		})
	}()

	clientCfg := &ProtocolConfig{
		ServerAddress:      serverLn.Addr().String(),
		Key:                "k",
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    true,
	}

	mux, err := NewMuxClient(clientCfg)
	if err != nil {
		t.Fatalf("NewMuxClient: %v", err)
	}
	defer mux.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c1, err := mux.Dial(ctx, targetAddr)
	if err != nil {
		t.Fatalf("mux.Dial: %v", err)
	}
	_, _ = c1.Write([]byte("ping"))
	out := make([]byte, 4)
	if _, err := io.ReadFull(c1, out); err != nil {
		_ = c1.Close()
		t.Fatalf("read: %v", err)
	}
	_ = c1.Close()
	if string(out) != "pong" {
		t.Fatalf("unexpected response: %q", string(out))
	}

	_ = mux.Close()
	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server timeout")
	}
}
