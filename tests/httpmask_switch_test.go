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
package tests

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/apis"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func TestHTTPMaskSwitch(t *testing.T) {
	// Setup server
	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer serverListener.Close()

	serverAddr := serverListener.Addr().String()
	table := sudoku.NewTable("test-seed", "prefer_ascii")
	key := "test-key-123456"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              10,
		PaddingMax:              20,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false, // Server enables mask (but should auto-detect)
	}

	go func() {
		for {
			conn, err := serverListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tunnelConn, _, err := apis.ServerHandshake(c, serverCfg)
				if err != nil {
					// Handshake failed
					return
				}
				defer tunnelConn.Close()
				// Echo server
				io.Copy(tunnelConn, tunnelConn)
			}(conn)
		}
	}()

	// Test Case 1: Client with Mask (Default)
	t.Run("ClientWithMask", func(t *testing.T) {
		clientCfg := &apis.ProtocolConfig{
			ServerAddress:      serverAddr,
			TargetAddress:      "example.com:80",
			Key:                key,
			AEADMethod:         "chacha20-poly1305",
			Table:              table,
			PaddingMin:         10,
			PaddingMax:         20,
			EnablePureDownlink: true,
			DisableHTTPMask:    false,
		}

		conn, err := apis.Dial(context.Background(), clientCfg)
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer conn.Close()

		msg := []byte("hello masked")
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if string(buf) != string(msg) {
			t.Fatalf("expected %s, got %s", msg, buf)
		}
	})

	// Test Case 2: Client without Mask
	t.Run("ClientWithoutMask", func(t *testing.T) {
		clientCfg := &apis.ProtocolConfig{
			ServerAddress:      serverAddr,
			TargetAddress:      "example.com:80",
			Key:                key,
			AEADMethod:         "chacha20-poly1305",
			Table:              table,
			PaddingMin:         10,
			PaddingMax:         20,
			EnablePureDownlink: true,
			DisableHTTPMask:    true,
		}

		conn, err := apis.Dial(context.Background(), clientCfg)
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer conn.Close()

		msg := []byte("hello unmasked")
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if string(buf) != string(msg) {
			t.Fatalf("expected %s, got %s", msg, buf)
		}
	})
}
