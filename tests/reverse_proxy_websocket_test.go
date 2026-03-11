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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/SUDOKU-ASCII/sudoku/internal/config"
)

func TestReverseProxy_WebSocket_Subpath(t *testing.T) {
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

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/app", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/app")

	httpClient := &http.Client{Timeout: 5 * time.Second}

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

	resp, err = httpClient.Get("http://" + reverseListen + "/app/app.js")
	if err != nil {
		t.Fatalf("reverse js: %v", err)
	}
	jsBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reverse js status: %d", resp.StatusCode)
	}
	js := string(jsBytes)
	if !strings.Contains(js, `new WebSocket("/ws")`) {
		t.Fatalf("expected ws url to remain unchanged in js, got: %q", js)
	}

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
	typ, msg, err := ws.Read(ctx)
	if err != nil {
		t.Fatalf("reverse websocket read: %v", err)
	}
	if typ != websocket.MessageText || string(msg) != "ping" {
		t.Fatalf("unexpected ws echo: typ=%v msg=%q", typ, string(msg))
	}
}
