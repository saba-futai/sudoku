package tests

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

func TestFallbackChain_AllowsHTTPMaskClientToReachInnerServer(t *testing.T) {
	ports, err := getFreePorts(4)
	if err != nil {
		t.Fatalf("get free ports: %v", err)
	}
	outerPort := ports[0]
	innerPort := ports[1]
	clientPort := ports[2]
	echoPort := ports[3]

	if err := startEchoServer(echoPort); err != nil {
		t.Fatalf("start echo server: %v", err)
	}
	waitForPort(t, echoPort)

	pathRoot := "chainmask"

	innerCfg := &config.Config{
		Mode:               "server",
		LocalPort:          innerPort,
		Key:                "inner-chain-key",
		AEAD:               testAEAD,
		ASCII:              testASCII,
		CustomTable:        testCustomTable,
		EnablePureDownlink: true,
		SuspiciousAction:   "fallback",
		HTTPMask: config.HTTPMaskConfig{
			Disable:  false,
			Mode:     "ws",
			PathRoot: pathRoot,
		},
	}
	startSudokuServer(t, innerCfg)

	outerCfg := &config.Config{
		Mode:               "server",
		LocalPort:          outerPort,
		FallbackAddr:       fmt.Sprintf("127.0.0.1:%d", innerPort),
		Key:                "outer-chain-key",
		AEAD:               testAEAD,
		ASCII:              "prefer_entropy",
		CustomTable:        "vpxxvpvv",
		EnablePureDownlink: true,
		SuspiciousAction:   "fallback",
		HTTPMask: config.HTTPMaskConfig{
			Disable:  false,
			Mode:     "ws",
			PathRoot: pathRoot,
		},
	}
	startSudokuServer(t, outerCfg)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", outerPort),
		Key:                innerCfg.Key,
		AEAD:               innerCfg.AEAD,
		ASCII:              innerCfg.ASCII,
		CustomTable:        innerCfg.CustomTable,
		EnablePureDownlink: innerCfg.EnablePureDownlink,
		ProxyMode:          "global",
		HTTPMask: config.HTTPMaskConfig{
			Disable:  false,
			Mode:     "ws",
			PathRoot: pathRoot,
		},
	}
	startSudokuClient(t, clientCfg)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("dial client proxy: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	sendHTTPConnect(t, conn, target)

	msg := []byte("httpmask fallback chain payload")
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})

	resp := make([]byte, len(msg))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	_ = conn.SetReadDeadline(time.Time{})

	if !bytes.Equal(resp, msg) {
		t.Fatalf("unexpected echo payload: got %q want %q", string(resp), string(msg))
	}
}
