package tunnel

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestSudokuTunnel_ChainDialer(t *testing.T) {
	key := "test-chain-key"

	serverCfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		Key:                key,
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
	}
	table := sudoku.NewTable(key, serverCfg.ASCII)

	// Exit hop (server2): handshake -> read target -> echo.
	exitLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen exit: %v", err)
	}
	defer exitLn.Close()

	go func() {
		for {
			c, err := exitLn.Accept()
			if err != nil {
				return
			}
			go func(raw net.Conn) {
				defer raw.Close()

				sConn, _, err := HandshakeAndUpgradeWithTablesMeta(raw, serverCfg, []*sudoku.Table{table})
				if err != nil {
					return
				}
				defer sConn.Close()

				_, _, _, err = protocol.ReadAddress(sConn)
				if err != nil {
					return
				}
				_, _ = io.Copy(sConn, sConn)
			}(c)
		}
	}()

	// Entry hop (server1): handshake -> read target (server2) -> dial -> pipe.
	entryLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen entry: %v", err)
	}
	defer entryLn.Close()

	go func() {
		for {
			c, err := entryLn.Accept()
			if err != nil {
				return
			}
			go func(raw net.Conn) {
				defer raw.Close()

				sConn, _, err := HandshakeAndUpgradeWithTablesMeta(raw, serverCfg, []*sudoku.Table{table})
				if err != nil {
					return
				}
				defer sConn.Close()

				nextHop, _, _, err := protocol.ReadAddress(sConn)
				if err != nil || nextHop == "" {
					return
				}

				up, err := net.DialTimeout("tcp", nextHop, 5*time.Second)
				if err != nil {
					return
				}
				pipeConn(sConn, up)
			}(c)
		}
	}()

	clientCfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		ServerAddress:      entryLn.Addr().String(),
		Chain:              &config.ChainConfig{Hops: []string{exitLn.Addr().String()}},
		Key:                key,
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
	}
	if err := clientCfg.Finalize(); err != nil {
		t.Fatalf("finalize client config: %v", err)
	}

	dialer := &AdaptiveDialer{
		BaseDialer: BaseDialer{
			Config: clientCfg,
			Tables: []*sudoku.Table{table},
		},
	}

	conn, err := dialer.Dial("example.com:80")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	msg := []byte("hello-chain")
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
