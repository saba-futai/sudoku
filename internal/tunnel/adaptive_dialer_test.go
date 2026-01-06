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

func TestSudokuTunnel_AdaptiveDialer(t *testing.T) {
	cfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		ServerAddress:      "127.0.0.1:0",
		Key:                "test-key-123",
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "legacy",
		HTTPMaskMultiplex:  "on",
	}
	table := sudoku.NewTable(cfg.Key, cfg.ASCII)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	cfg.ServerAddress = listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()

				sConn, _, err := HandshakeAndUpgradeWithTablesMeta(c, cfg, []*sudoku.Table{table})
				if err != nil {
					return
				}
				defer sConn.Close()

				target, _, _, err := protocol.ReadAddress(sConn)
				if err != nil || target == "" {
					return
				}

				io.Copy(sConn, sConn)
			}(conn)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	dialer := &AdaptiveDialer{
		BaseDialer: BaseDialer{
			Config: cfg,
			Tables: []*sudoku.Table{table},
		},
	}

	conn, err := dialer.Dial("example.com:80")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	message := "hello"
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, len(message))
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.ReadFull(conn, buf)
	}()
	_, _ = conn.Write([]byte(message))
	wg.Wait()
}

