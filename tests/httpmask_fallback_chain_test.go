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

type fallbackChainCase struct {
	name       string
	httpMode   string
	rejectKind string
}

func TestFallbackChain_AllowsHTTPMaskClientsAcrossModes(t *testing.T) {
	cases := []fallbackChainCase{
		{name: "ws_auth_reject", httpMode: "ws", rejectKind: "auth"},
		{name: "ws_early_reject", httpMode: "ws", rejectKind: "early"},
		{name: "auto_auth_reject", httpMode: "auto", rejectKind: "auth"},
		{name: "auto_early_reject", httpMode: "auto", rejectKind: "early"},
		{name: "poll_auth_reject", httpMode: "poll", rejectKind: "auth"},
		{name: "poll_early_reject", httpMode: "poll", rejectKind: "early"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runHTTPMaskFallbackChainCase(t, tc)
		})
	}
}

func runHTTPMaskFallbackChainCase(t *testing.T, tc fallbackChainCase) {
	t.Helper()

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

	pathRoot := fmt.Sprintf("chain-%s", tc.name)
	innerKey := "inner-chain-key"
	outerKey := innerKey
	outerAEAD := testAEAD
	outerASCII := testASCII
	outerTable := testCustomTable

	switch tc.rejectKind {
	case "auth":
		outerKey = "outer-chain-key"
	case "early":
		outerAEAD = "aes-128-gcm"
		outerASCII = "prefer_entropy"
		outerTable = "vpxxvpvv"
	default:
		t.Fatalf("unknown rejectKind: %s", tc.rejectKind)
	}

	innerCfg := &config.Config{
		Mode:               "server",
		LocalPort:          innerPort,
		Key:                innerKey,
		AEAD:               testAEAD,
		ASCII:              testASCII,
		CustomTable:        testCustomTable,
		EnablePureDownlink: true,
		SuspiciousAction:   "fallback",
		HTTPMask: config.HTTPMaskConfig{
			Disable:  false,
			Mode:     tc.httpMode,
			PathRoot: pathRoot,
		},
	}
	startSudokuServer(t, innerCfg)

	outerCfg := &config.Config{
		Mode:               "server",
		LocalPort:          outerPort,
		FallbackAddr:       fmt.Sprintf("127.0.0.1:%d", innerPort),
		Key:                outerKey,
		AEAD:               outerAEAD,
		ASCII:              outerASCII,
		CustomTable:        outerTable,
		EnablePureDownlink: true,
		SuspiciousAction:   "fallback",
		HTTPMask: config.HTTPMaskConfig{
			Disable:  false,
			Mode:     tc.httpMode,
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
			Mode:     tc.httpMode,
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

	msg := []byte("httpmask fallback chain payload:" + tc.name)
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
