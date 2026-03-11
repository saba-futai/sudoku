package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/app"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
)

func TestAppProxyHTTPMaskAutoPathRoot(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "matrix-smoke-app-httpmask-ok")
	}))
	defer origin.Close()

	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	serverPort := freeTCPPort(t)
	clientPort := freeTCPPort(t)

	serverCfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          serverPort,
		Key:                crypto.EncodePoint(pair.Public),
		AEAD:               "chacha20-poly1305",
		SuspiciousAction:   "silent",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_ascii",
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable:   false,
			Mode:      "auto",
			PathRoot:  "aabbcc",
			Multiplex: "off",
		},
	}
	if err := serverCfg.Finalize(); err != nil {
		t.Fatalf("finalize server config: %v", err)
	}
	serverTables, err := app.BuildTables(serverCfg)
	if err != nil {
		t.Fatalf("build server tables: %v", err)
	}
	go app.RunServer(serverCfg, serverTables)
	waitForTCPPort(t, serverPort)

	clientCfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:                crypto.EncodeScalar(pair.Private),
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              "prefer_ascii",
		EnablePureDownlink: true,
		ProxyMode:          "global",
		HTTPMask: config.HTTPMaskConfig{
			Disable:   false,
			Mode:      "auto",
			PathRoot:  "aabbcc",
			Multiplex: "off",
		},
	}
	if err := clientCfg.Finalize(); err != nil {
		t.Fatalf("finalize client config: %v", err)
	}
	go app.RunClient(clientCfg, nil)
	waitForTCPPort(t, clientPort)

	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("proxy url: %v", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(proxyURL),
			DisableKeepAlives: true,
		},
		Timeout: 10 * time.Second,
	}

	resp, err := httpClient.Get(origin.URL)
	if err != nil {
		t.Fatalf("proxy get: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bad status: %s body=%q", resp.Status, string(body))
	}
	if string(body) != "matrix-smoke-app-httpmask-ok" {
		t.Fatalf("unexpected body: %q", string(body))
	}
}

func freeTCPPort(t testing.TB) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate port: %v", err)
	}
	defer ln.Close()

	return ln.Addr().(*net.TCPAddr).Port
}

func waitForTCPPort(t testing.TB, port int) {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port not ready: %s", addr)
}
