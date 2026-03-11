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
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/reverse"
	"golang.org/x/crypto/ssh"
)

func TestBDD_ReverseProxy_WebUI_BehindTLSEdge(t *testing.T) {
	// Given: a local HTTP app that uses root-absolute assets and root-absolute WebSocket paths.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><script src="/app.js"></script></head><body>ok</body></html>`))
		case "/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`const ws = new WebSocket("/ws"); void ws;`))
		case "/ws":
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")

			ctx := context.Background()
			for {
				typ, msg, err := c.Read(ctx)
				if err != nil {
					return
				}
				if err := c.Write(ctx, typ, msg); err != nil {
					return
				}
			}
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

	// And: a Sudoku server + client with reverse proxy route "/app".
	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "bdd",
		Routes:   []config.ReverseRoute{{Path: "/app", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/app")

	// And: an "edge" reverse proxy (CDN-like) that terminates TLS and forwards to reverse.listen.
	targetURL, _ := url.Parse("http://" + reverseListen)
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	edge := httptest.NewTLSServer(rp)
	defer edge.Close()

	jar, _ := cookiejar.New(nil)
	httpClient := edge.Client()
	httpClient.Timeout = 5 * time.Second
	httpClient.Jar = jar

	// When: a user loads the app through the edge under /app/
	resp, err := httpClient.Get(edge.URL + "/app/")
	if err != nil {
		t.Fatalf("edge reverse html: %v", err)
	}
	htmlBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge reverse html status: %d", resp.StatusCode)
	}
	html := string(htmlBytes)

	// Then: the HTML is rewritten so browsers request /app/app.js instead of /app.js.
	if !strings.Contains(html, `src="/app/app.js"`) {
		t.Fatalf("expected rewritten script src, got: %q", html)
	}

	// When: the browser fetches the script.
	resp, err = httpClient.Get(edge.URL + "/app/app.js")
	if err != nil {
		t.Fatalf("edge reverse js: %v", err)
	}
	jsBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge reverse js status: %d", resp.StatusCode)
	}
	js := string(jsBytes)

	// Then: the JS keeps the root-absolute WebSocket path ("/ws").
	if !strings.Contains(js, `new WebSocket("/ws")`) {
		t.Fatalf("expected ws url to remain unchanged in js, got: %q", js)
	}

	// When: the browser upgrades to WebSocket through the edge (no prefix in the path).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := strings.Replace(edge.URL, "https://", "wss://", 1) + "/ws"
	wsHeader := http.Header{}
	if u, err := url.Parse(edge.URL); err == nil {
		if cookies := jar.Cookies(u); len(cookies) > 0 {
			wsHeader.Set("Cookie", cookieHeaderValue(cookies))
		}
	}
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:      edge.Client(),
		HTTPHeader:      wsHeader,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("edge reverse websocket dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	if err := ws.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("edge reverse websocket write: %v", err)
	}
	typ, msg, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("edge reverse websocket read: %v", err)
	}
	if typ != websocket.MessageText || string(msg) != "ping" {
		t.Fatalf("unexpected ws echo: typ=%v msg=%q", typ, string(msg))
	}
}

func TestBDD_ReverseProxy_TCPOverWS_BehindTLSEdge_WithBuiltInForwarder(t *testing.T) {
	// Given: a client-local raw TCP service.
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

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(4)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]
	forwardPort := ports[3]

	reverseListen := localServerAddr(reversePort)
	forwardListen := localServerAddr(forwardPort)

	// And: a Sudoku server + client with reverse route "/ssh" -> originAddr.
	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "bdd",
		Routes:   []config.ReverseRoute{{Path: "/ssh", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/ssh")

	// And: an "edge" reverse proxy (CDN-like) that terminates TLS and forwards to reverse.listen.
	targetURL, _ := url.Parse("http://" + reverseListen)
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	edge := httptest.NewTLSServer(rp)
	defer edge.Close()

	// When: the built-in forwarder listens locally and dials the tunnel over wss.
	dialURL := strings.Replace(edge.URL, "https://", "wss://", 1) + "/ssh"
	go func() {
		_ = reverse.ServeLocalWSForward(forwardListen, dialURL, true) // self-signed edge cert
	}()
	waitForAddr(t, forwardListen)

	// Then: connecting to the local port reaches the origin TCP service.
	c, err := net.DialTimeout("tcp", forwardListen, 1*time.Second)
	if err != nil {
		t.Fatalf("dial forwarder: %v", err)
	}
	defer c.Close()

	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("unexpected echo: %q", string(buf))
	}
}

func TestBDD_ReverseProxy_SSHOverWS_BehindTLSEdge_WithBuiltInForwarder(t *testing.T) {
	sshSrv := startTestSSHServer(t, "127.0.0.1:0", "u", "p")
	defer sshSrv.Close()

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(4)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]
	forwardPort := ports[3]

	reverseListen := localServerAddr(reversePort)
	forwardListen := localServerAddr(forwardPort)

	// And: a Sudoku server + client with reverse route "/ssh" -> ssh service.
	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "bdd",
		Routes:   []config.ReverseRoute{{Path: "/ssh", Target: sshSrv.Addr()}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/ssh")

	// And: an "edge" reverse proxy (CDN-like) that terminates TLS and forwards to reverse.listen.
	targetURL, _ := url.Parse("http://" + reverseListen)
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	edge := httptest.NewTLSServer(rp)
	defer edge.Close()

	// When: the built-in forwarder listens locally and dials the tunnel over wss.
	dialURL := strings.Replace(edge.URL, "https://", "wss://", 1) + "/ssh"
	go func() {
		_ = reverse.ServeLocalWSForward(forwardListen, dialURL, true) // self-signed edge cert
	}()
	waitForAddr(t, forwardListen)

	// Then: a real SSH client can authenticate and run an exec request.
	sshCfg := &ssh.ClientConfig{
		User:            "u",
		Auth:            []ssh.AuthMethod{ssh.Password("p")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	rawConn, err := net.DialTimeout("tcp", forwardListen, 5*time.Second)
	if err != nil {
		t.Fatalf("ssh tcp dial: %v", err)
	}
	_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))

	cconn, chans, reqs, err := ssh.NewClientConn(rawConn, forwardListen, sshCfg)
	if err != nil {
		_ = rawConn.Close()
		t.Fatalf("ssh handshake: %v", err)
	}
	_ = rawConn.SetDeadline(time.Time{})

	client := ssh.NewClient(cconn, chans, reqs)
	defer client.Close()

	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("ssh session: %v", err)
	}
	defer sess.Close()

	_ = rawConn.SetDeadline(time.Now().Add(10 * time.Second))
	defer rawConn.SetDeadline(time.Time{})

	out, err := sess.CombinedOutput("echo hello")
	if err != nil {
		t.Fatalf("ssh exec: %v (out=%q)", err, string(out))
	}
	if string(out) != "echo hello" {
		t.Fatalf("unexpected ssh output: %q", string(out))
	}
}

func TestBDD_ReverseProxy_HTTPMaskTunnel_StillWorks(t *testing.T) {
	// Given: a client-local HTTP app with root-absolute WebSocket paths.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><script src="/app.js"></script></head><body>ok</body></html>`))
		case "/app.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write([]byte(`const ws = new WebSocket("/ws"); void ws;`))
		case "/ws":
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
			if err != nil {
				return
			}
			defer c.Close(websocket.StatusNormalClosure, "")

			ctx := context.Background()
			for {
				typ, msg, err := c.Read(ctx)
				if err != nil {
					return
				}
				if err := c.Write(ctx, typ, msg); err != nil {
					return
				}
			}
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

	// And: client-server tunnel uses HTTPMask poll mode (CDN-friendly HTTP tunnel).
	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.HTTPMask = config.HTTPMaskConfig{Disable: false, Mode: "poll", TLS: false}
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.HTTPMask = config.HTTPMaskConfig{Disable: false, Mode: "poll", TLS: false}
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "bdd",
		Routes:   []config.ReverseRoute{{Path: "/app", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/app")

	httpClient := &http.Client{Timeout: 5 * time.Second}

	// When: a user loads the app under /app/
	resp, err := httpClient.Get("http://" + reverseListen + "/app/")
	if err != nil {
		t.Fatalf("reverse html: %v", err)
	}
	htmlBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse html status: %d", resp.StatusCode)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, `src="/app/app.js"`) {
		t.Fatalf("expected rewritten script src, got: %q", html)
	}

	// And: WebSocket works under subpath.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, "ws://"+reverseListen+"/app/ws", &websocket.DialOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("reverse websocket dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	if err := ws.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("reverse websocket write: %v", err)
	}
	_, msg, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("reverse websocket read: %v", err)
	}
	if string(msg) != "ping" {
		t.Fatalf("unexpected ws echo: %q", string(msg))
	}
}

func TestBDD_ReverseProxy_SubpathAssets_RedirectAndCookies_BehindTLSEdge(t *testing.T) {
	// Given: a local HTTP app that uses root-absolute asset paths and does root-absolute redirects/cookies.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><link rel="stylesheet" href="/style.css"></head><body><img src="/logo.svg"><img srcset="/a.png 1x, /b.png 2x"><a href="/login">login</a></body></html>`))
		case "/style.css":
			// Force an empty Content-Type header to exercise reverse's path-based inference.
			w.Header().Set("Content-Type", "")
			_, _ = w.Write([]byte(`body{background:url(/bg.svg)}`))
		case "/logo.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
		case "/bg.svg":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
		case "/a.png", "/b.png":
			w.WriteHeader(http.StatusNoContent)
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/", HttpOnly: true})
			w.Header().Set("Location", "/home")
			w.WriteHeader(http.StatusFound)
		case "/home":
			if _, err := r.Cookie("sid"); err != nil {
				http.Error(w, "missing cookie", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte("home"))
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
		ClientID: "bdd",
		Routes:   []config.ReverseRoute{{Path: "/app", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/app")

	// And: an "edge" reverse proxy (CDN-like) that terminates TLS and forwards to reverse.listen.
	targetURL, _ := url.Parse("http://" + reverseListen)
	rp := httputil.NewSingleHostReverseProxy(targetURL)
	edge := httptest.NewTLSServer(rp)
	defer edge.Close()

	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{
		Timeout:   5 * time.Second,
		Transport: edge.Client().Transport,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// When: user visits /app (no trailing slash)
	resp, err := httpClient.Get(edge.URL + "/app")
	if err != nil {
		t.Fatalf("edge reverse /app: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Fatalf("expected 308 for /app, got: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/app/" {
		t.Fatalf("expected Location /app/, got: %q", loc)
	}

	// And: user loads the app under /app/
	resp, err = httpClient.Get(edge.URL + "/app/")
	if err != nil {
		t.Fatalf("edge reverse html: %v", err)
	}
	htmlBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge reverse html status: %d", resp.StatusCode)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, `href="/app/style.css"`) || !strings.Contains(html, `src="/app/logo.svg"`) || !strings.Contains(html, `href="/app/login"`) {
		t.Fatalf("expected rewritten asset urls in html, got: %q", html)
	}
	if !strings.Contains(html, `srcset="/app/a.png 1x, /app/b.png 2x"`) {
		t.Fatalf("expected rewritten srcset in html, got: %q", html)
	}

	// Then: CSS root-absolute url() is rewritten.
	resp, err = httpClient.Get(edge.URL + "/app/style.css")
	if err != nil {
		t.Fatalf("edge reverse css: %v", err)
	}
	cssBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge reverse css status: %d", resp.StatusCode)
	}
	if !strings.Contains(string(cssBytes), "url(/app/bg.svg)") {
		t.Fatalf("expected rewritten css url(), got: %q", string(cssBytes))
	}

	// And: assets are loadable under the prefixed path.
	resp, err = httpClient.Get(edge.URL + "/app/logo.svg")
	if err != nil {
		t.Fatalf("edge reverse svg: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edge reverse svg status: %d", resp.StatusCode)
	}

	// When: the app redirects and sets cookies on root-absolute paths.
	resp, err = httpClient.Get(edge.URL + "/app/login")
	if err != nil {
		t.Fatalf("edge reverse login: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 for login, got: %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/app/home" {
		t.Fatalf("expected rewritten redirect, got Location: %q", loc)
	}
	setCookie := strings.Join(resp.Header.Values("Set-Cookie"), "\n")
	if !strings.Contains(setCookie, "Path=/app/") {
		t.Fatalf("expected rewritten cookie path, got Set-Cookie: %q", setCookie)
	}

	// Then: the cookie is sent back for /app/home (after following the rewritten Location).
	resp, err = httpClient.Get(edge.URL + "/app/home")
	if err != nil {
		t.Fatalf("edge reverse home: %v", err)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(bodyBytes) != "home" {
		t.Fatalf("expected authed home, got status=%d body=%q", resp.StatusCode, string(bodyBytes))
	}
}
