package tests

import (
	"compress/gzip"
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

	noFollowClient := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := noFollowClient.Get(fmt.Sprintf("http://%s/gitea", reverseListen))
	if err != nil {
		t.Fatalf("reverse redirect: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("expected 308 for /gitea redirect, got: %d", resp.StatusCode)
	}
	if !strings.HasSuffix(resp.Header.Get("Location"), "/gitea/") {
		t.Fatalf("expected Location to end with /gitea/, got: %q", resp.Header.Get("Location"))
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}

	// HTML must be rewritten so the browser requests /gitea/assets/... instead of /assets/...
	resp, err = httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
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

func TestReverseProxy_PathPrefix_Srcset(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><body><img alt="徽标" src="/assets/logo.png" srcset="/assets/logo.png 1x, /assets/logo@2x.png 2x"></body></html>`))
		case "/assets/logo.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png1x"))
		case "/assets/logo@2x.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png2x"))
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
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
	if err != nil {
		t.Fatalf("reverse html: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse html status: %d", resp.StatusCode)
	}
	html := string(body)
	if !strings.Contains(html, `srcset="/gitea/assets/logo.png 1x, /gitea/assets/logo@2x.png 2x"`) {
		t.Fatalf("expected rewritten srcset urls, got: %q", html)
	}
	if !strings.Contains(html, `src="/gitea/assets/logo.png"`) {
		t.Fatalf("expected rewritten img src, got: %q", html)
	}
}

func TestReverseProxy_RefererFallback_RootPaths(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			// Contains a regex literal with "'\\/api\\/...'" which previously broke when JS rewriting
			// attempted to insert prefixes into escaped slashes.
			_, _ = w.Write([]byte("fetch(`/api/ping`); const re=/'\\\\/api\\\\/ping'/g;"))
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
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

	// JS should not be rewritten (to avoid breaking regex/minified code).
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/app.js", reverseListen))
	if err != nil {
		t.Fatalf("reverse js: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse js status: %d", resp.StatusCode)
	}
	js := string(body)
	if !strings.Contains(js, "fetch(`/api/ping`)") {
		t.Fatalf("expected js to remain unchanged, got: %q", js)
	}
	if !strings.Contains(js, "const re=/'\\\\/api\\\\/ping'/g") {
		t.Fatalf("expected regex literal to remain unchanged, got: %q", js)
	}

	// Root-absolute asset/API calls should still work via Referer-based routing.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/app.js", reverseListen), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Referer", fmt.Sprintf("http://%s/gitea/", reverseListen))
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatalf("reverse referer js: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse referer js status: %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/ping", reverseListen), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Referer", fmt.Sprintf("http://%s/gitea/", reverseListen))
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatalf("reverse referer api ping: %v", err)
	}
	pong, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(pong) != "pong" {
		t.Fatalf("expected pong, got status=%d body=%q", resp.StatusCode, string(pong))
	}
}

func TestReverseProxy_PathPrefix_GzipOrigin(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeGzip := func(ct, body string) {
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			_, _ = gz.Write([]byte(body))
			_ = gz.Close()
		}

		switch r.URL.Path {
		case "/":
			// Simulate an app that always gzips and uses root-absolute assets (/static/...).
			writeGzip("text/html; charset=utf-8", `<html><head><link rel="stylesheet" href="/static/css/main.css"></head><body><script src="/static/js/app.js"></script></body></html>`)
		case "/static/css/main.css":
			writeGzip("text/css; charset=utf-8", `body{background:url(/static/img/bg.png)}`)
		case "/static/js/app.js":
			writeGzip("application/javascript", `fetch("/api/ping")`)
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
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

	var html string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		html = string(body)
		break
	}
	if html == "" {
		t.Fatalf("reverse html did not become ready")
	}
	if !strings.Contains(html, `href="/gitea/static/css/main.css"`) {
		t.Fatalf("expected rewritten css url, got: %q", html)
	}
	if !strings.Contains(html, `src="/gitea/static/js/app.js"`) {
		t.Fatalf("expected rewritten js url, got: %q", html)
	}

	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/static/css/main.css", reverseListen))
	if err != nil {
		t.Fatalf("reverse css: %v", err)
	}
	cssBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse css status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(cssBytes), `url(/gitea/static/img/bg.png)`) {
		t.Fatalf("expected rewritten css url(), got: %q", string(cssBytes))
	}

	resp, err = httpClient.Get(fmt.Sprintf("http://%s/gitea/static/js/app.js", reverseListen))
	if err != nil {
		t.Fatalf("reverse js: %v", err)
	}
	jsBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse js status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(jsBytes), `fetch("/api/ping")`) {
		t.Fatalf("expected js to remain unchanged, got: %q", string(jsBytes))
	}

	// Root-absolute requests should route correctly using Referer.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/static/js/app.js", reverseListen), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Referer", fmt.Sprintf("http://%s/gitea/", reverseListen))
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatalf("reverse referer js: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse referer js status: %d", resp.StatusCode)
	}

	req, err = http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/ping", reverseListen), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Referer", fmt.Sprintf("http://%s/gitea/", reverseListen))
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatalf("reverse referer api ping: %v", err)
	}
	pong, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(pong) != "pong" {
		t.Fatalf("expected pong, got status=%d body=%q", resp.StatusCode, string(pong))
	}
}
