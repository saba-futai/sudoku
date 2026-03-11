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
package tunnel

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestSudokuTunnel_Standard(t *testing.T) {
	// 1. Setup Config & Table
	cfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		ServerAddress:      "127.0.0.1:0", // Random port
		Key:                "test-key-123",
		AEAD:               "chacha20-poly1305",
		PaddingMin:         10,
		PaddingMax:         20,
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
	}
	table := sudoku.NewTable(cfg.Key, cfg.ASCII)
	privateKey := []byte("test-private-key-for-user-hash")
	wantUserHash := func() string {
		h := sha256.Sum256(privateKey)
		return hex.EncodeToString(h[:8])
	}()

	// 2. Start Mock Server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()
	serverAddr := listener.Addr().String()
	cfg.ServerAddress = serverAddr

	// Server logic
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Handshake
				sConn, meta, err := HandshakeAndUpgradeWithTablesMeta(c, cfg, []*sudoku.Table{table})
				if err != nil {
					t.Errorf("Server handshake failed: %v", err)
					return
				}
				defer sConn.Close()
				if meta == nil {
					t.Errorf("missing handshake meta")
					return
				}
				if meta.UserHash != wantUserHash {
					t.Errorf("unexpected user hash: got %q want %q", meta.UserHash, wantUserHash)
					return
				}

				msg, err := ReadKIPMessage(sConn)
				if err != nil {
					t.Errorf("Server read open failed: %v", err)
					return
				}
				if msg.Type != KIPTypeOpenTCP {
					t.Errorf("Unexpected first message: %d", msg.Type)
					return
				}

				// Read Target Address (Standard Mode)
				target, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
				if err != nil {
					t.Errorf("Server decode address failed: %v", err)
					return
				}
				if target != "example.com:80" {
					t.Errorf("Unexpected target: %s", target)
					return
				}

				// Echo Loop
				io.Copy(sConn, sConn)
			}(conn)
		}
	}()

	// 3. Client Logic (Dialer)
	dialer := &StandardDialer{
		BaseDialer: BaseDialer{
			Config: cfg,
			Tables: []*sudoku.Table{table},
			// The official client sends sha256(privateKey)[:8] in the handshake nonce for multi-user ID.
			PrivateKey: privateKey,
		},
	}

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// 4. Connect
	conn, err := dialer.Dial("example.com:80")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer conn.Close()

	// 5. Verify Data Transfer
	message := "Hello, Sudoku!"
	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		buf := make([]byte, 1024)
		n, err := conn.Read(buf)
		if err != nil {
			t.Errorf("Client read failed: %v", err)
			return
		}
		received := string(buf[:n])
		if received != message {
			t.Errorf("Client received wrong message: got %q, want %q", received, message)
		}
	}()

	_, err = conn.Write([]byte(message))
	if err != nil {
		t.Fatalf("Client write failed: %v", err)
	}

	wg.Wait()
}
