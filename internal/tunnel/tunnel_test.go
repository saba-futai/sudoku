package tunnel

import (
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
				sConn, err := HandshakeAndUpgrade(c, cfg, table)
				if err != nil {
					t.Errorf("Server handshake failed: %v", err)
					return
				}
				defer sConn.Close()

				// Read Target Address (Standard Mode)
				target, _, _, err := protocol.ReadAddress(sConn)
				if err != nil {
					t.Errorf("Server read address failed: %v", err)
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
