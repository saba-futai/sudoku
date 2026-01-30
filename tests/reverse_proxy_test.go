package tests

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
)

func waitForAddr(t testing.TB, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("address not ready: %s", addr)
}

func TestReverseProxy_PathPrefix(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// Simulate an app that emits root-absolute paths (common for many web UIs).
			_, _ = w.Write([]byte(`<html><head><script src="/assets/app.js"></script></head><body><a href="/hello">hi</a></body></html>`))
		case "/hello":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		case "/assets/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`console.log("ok")`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

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
				{
					Path:   "/gitea",
					Target: originAddr,
				},
			},
		},
	}
	startSudokuClient(t, clientCfg)

	httpClient := &http.Client{Timeout: 5 * time.Second}

	// HTML must be rewritten so the browser requests /gitea/assets/... instead of /assets/...
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
	if err != nil {
		t.Fatalf("reverse html: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse html status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `src="/gitea/assets/app.js"`) {
		t.Fatalf("expected rewritten asset url, got: %q", string(body))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/hello", reverseListen))
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && string(body) == "ok" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("reverse proxy did not become ready")
}
