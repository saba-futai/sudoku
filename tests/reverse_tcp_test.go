package tests

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
)

func TestReverseProxy_TCP_DefaultRoute(t *testing.T) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen origin: %v", err)
	}
	defer originLn.Close()

	originAddr := originLn.Addr().String()
	go func() {
		for {
			c, err := originLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("keygen failed: %v", err)
	}
	serverKey := crypto.EncodePoint(pair.Public)
	clientKey := crypto.EncodeScalar(pair.Private)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := fmt.Sprintf("127.0.0.1:%d", reversePort)

	serverCfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          serverPort,
		FallbackAddr:       "",
		Key:                serverKey,
		AEAD:               "chacha20-poly1305",
		SuspiciousAction:   "fallback",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_ascii",
		CustomTable:        "xpxvvpvv",
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
		Reverse: &config.ReverseConfig{
			Listen: reverseListen,
		},
	}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:                clientKey,
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_ascii",
		CustomTable:        "xpxvvpvv",
		EnablePureDownlink: true,
		ProxyMode:          "direct",
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
		Reverse: &config.ReverseConfig{
			ClientID: "r4s",
			Routes: []config.ReverseRoute{
				// Path empty => raw TCP reverse on reverse.listen.
				{Target: originAddr},
			},
		},
	}
	startSudokuClient(t, clientCfg)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", reverseListen, 200*time.Millisecond)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}

		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			_ = c.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		buf := make([]byte, 4)
		_, err = io.ReadFull(c, buf)
		_ = c.Close()
		if err == nil && string(buf) == "ping" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatalf("reverse tcp proxy did not become ready")
}
