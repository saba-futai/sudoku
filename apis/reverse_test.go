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
package apis

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestReverseProxySession(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_entropy")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	backendAddr := strings.TrimPrefix(backend.URL, "http://")

	revMgr := NewReverseManager()
	revLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen reverse http: %v", err)
	}
	defer revLn.Close()

	revSrv := &http.Server{
		Handler:           revMgr,
		ReadHeaderTimeout: 3 * time.Second,
	}
	go func() {
		_ = revSrv.Serve(revLn)
	}()
	defer func() { _ = revSrv.Close() }()

	serverLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen server: %v", err)
	}
	defer serverLn.Close()

	serverCfg := &ProtocolConfig{
		Key:                     "k",
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         true,
	}

	serverErr := make(chan error, 1)
	go func() {
		raw, err := serverLn.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		conn, session, _, userHash, helloPayload, err := ServerHandshakeSessionAutoWithUserHash(raw, serverCfg)
		if err != nil {
			serverErr <- err
			return
		}
		if session != SessionReverse {
			_ = conn.Close()
			serverErr <- fmt.Errorf("unexpected session kind: %v", session)
			return
		}
		serverErr <- revMgr.HandleServerSession(conn, userHash, helloPayload)
	}()

	clientCfg := &ProtocolConfig{
		ServerAddress:      serverLn.Addr().String(),
		Key:                "k",
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	baseConn, err := DialBase(ctx, clientCfg)
	if err != nil {
		t.Fatalf("DialBase: %v", err)
	}

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- ServeReverseClientSession(baseConn, "client", []ReverseRoute{
			{Path: "/gitea", Target: backendAddr},
		})
	}()

	client := &http.Client{Timeout: 3 * time.Second}
	url := "http://" + revLn.Addr().String() + "/gitea/hello"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil && resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(body) == "ok" {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = baseConn.Close()
			t.Fatalf("reverse proxy not ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	_ = baseConn.Close()

	select {
	case err := <-clientErr:
		if err != nil {
			t.Fatalf("client session: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("client session timeout")
	}

	select {
	case err := <-serverErr:
		if err != nil {
			t.Fatalf("server session: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server session timeout")
	}
}
