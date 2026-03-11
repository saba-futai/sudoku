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
package tests

import (
	"compress/gzip"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

func waitForAddr(t testing.TB, addr string) {
	t.Helper()
	deadline := time.Now().Add(reverseReadyWait)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
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

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

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

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Timeout: 5 * time.Second, Jar: jar}

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

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

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
			// This payload intentionally includes regex literals that contain quotes, to ensure the
			// reverse-proxy rewrite does not corrupt JavaScript by rewriting regex delimiters.
			_, _ = w.Write([]byte(
				"fetch(`/api/ping`); " +
					`const re1=/"/g; ` +
					"const re2=/'/g; " +
					`const s="a".replace(/"/g,"x");`,
			))
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Timeout: 5 * time.Second, Jar: jar}

	// JS should be rewritten safely so root-absolute paths work under a subpath (e.g. WebSocket "/ws").
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
		t.Fatalf("expected js root path to remain unchanged, got: %q", js)
	}
	if !strings.Contains(js, `const s="a".replace(/"/g,"x");`) {
		t.Fatalf("expected regex literal with quote to remain unchanged, got: %q", js)
	}
	if !strings.Contains(js, `const re1=/"/g;`) {
		t.Fatalf("expected regex literal /\"/g to remain unchanged, got: %q", js)
	}
	if !strings.Contains(js, "const re2=/'/g;") {
		t.Fatalf("expected regex literal /'/g to remain unchanged, got: %q", js)
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

	// Cookie-based fallback should route even without Referer.
	req, err = http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s/api/ping", reverseListen), nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		t.Fatalf("reverse cookie api ping: %v", err)
	}
	pong, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(pong) != "pong" {
		t.Fatalf("expected pong via cookie, got status=%d body=%q", resp.StatusCode, string(pong))
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

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

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
		t.Fatalf("expected js root path to remain unchanged, got: %q", string(jsBytes))
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

func TestReverseProxy_HTML_BareSlashNotRewritten(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// Mimic Navidrome: JSON is embedded as a string with escaped quotes.
			// The separator "/" is a plain value (not a URL) and must not be rewritten.
			_, _ = w.Write([]byte(`<html><head><script>window.__APP_CONFIG__="{\"separator\":\"/\",\"api\":\"/api/ping\",\"bg\":\"/backgrounds\"}"</script></head></html>`))
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
		case "/backgrounds":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("bg"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
	if err != nil {
		t.Fatalf("reverse html: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse html status: %d", resp.StatusCode)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, `\"separator\":\"/\"`) {
		t.Fatalf("expected bare slash to remain unchanged, got: %q", body)
	}
	if !strings.Contains(body, `\"api\":\"/api/ping\"`) {
		t.Fatalf("expected api url to remain unchanged, got: %q", body)
	}
	if !strings.Contains(body, `\"bg\":\"/backgrounds\"`) {
		t.Fatalf("expected background url to remain unchanged, got: %q", body)
	}
}

func TestReverseProxy_HTML_InlineJS_RegexNotCorrupted(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><script>const s="a".replace(/"/g,"x");fetch("/api/ping");</script></head></html>`))
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/", reverseListen))
	if err != nil {
		t.Fatalf("reverse html: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse html status: %d", resp.StatusCode)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, `fetch("/api/ping")`) {
		t.Fatalf("expected api url to remain unchanged, got: %q", body)
	}
	if !strings.Contains(body, `const s="a".replace(/"/g,"x");`) {
		t.Fatalf("expected regex literal with quote to remain unchanged, got: %q", body)
	}
}

func TestReverseProxy_JS_MisleadingContentType_NoRewrite(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app.js":
			// Simulate a server that serves JS with a generic Content-Type.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(`fetch("/api/ping"); const re=/"/g;`))
		case "/api/ping":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("pong"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/gitea", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/gitea")

	httpClient := &http.Client{Timeout: 5 * time.Second}
	resp, err := httpClient.Get(fmt.Sprintf("http://%s/gitea/app.js", reverseListen))
	if err != nil {
		t.Fatalf("reverse js: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse js status: %d", resp.StatusCode)
	}
	body := string(bodyBytes)
	if !strings.Contains(body, `fetch("/api/ping")`) {
		t.Fatalf("expected js root path to remain unchanged, got: %q", body)
	}
	if !strings.Contains(body, `const re=/"/g;`) {
		t.Fatalf("expected regex literal to remain unchanged, got: %q", body)
	}
}
