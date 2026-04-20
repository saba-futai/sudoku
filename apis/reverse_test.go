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

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	itunnel "github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
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

	baseConn, err := dialReverseBase(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dialReverseBase: %v", err)
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

func TestDialReverseClientSession_UsesPackedUplink(t *testing.T) {
	table := sudoku.NewTable("seed-packed-reverse", "prefer_entropy")

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

	serverCfg := &config.Config{
		Key:                "k",
		AEAD:               "chacha20-poly1305",
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		HTTPMask:           config.HTTPMaskConfig{Disable: true},
	}

	serverErr := make(chan error, 1)
	connCh := make(chan net.Conn, 1)
	go func() {
		raw, err := serverLn.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		conn, meta, err := itunnel.HandshakeAndUpgradeWithTablesMeta(raw, serverCfg, []*sudoku.Table{table})
		if err != nil {
			serverErr <- err
			return
		}
		if meta == nil || !meta.UplinkPacked {
			_ = conn.Close()
			serverErr <- fmt.Errorf("reverse session did not use packed uplink")
			return
		}
		connCh <- conn

		msg, err := itunnel.ReadKIPMessage(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if msg.Type != itunnel.KIPTypeStartRev {
			_ = conn.Close()
			serverErr <- fmt.Errorf("unexpected session kind: %d", msg.Type)
			return
		}
		serverErr <- revMgr.HandleServerSession(conn, meta.UserHash, msg.Payload)
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

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- DialReverseClientSession(ctx, clientCfg, "client", []ReverseRoute{
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
			t.Fatalf("reverse proxy not ready: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	select {
	case conn := <-connCh:
		_ = conn.Close()
	case <-time.After(5 * time.Second):
		t.Fatalf("server conn not captured")
	}

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

func TestServeReverseClientSession_RejectsPureUplink(t *testing.T) {
	table := sudoku.NewTable("seed-pure-reverse-reject", "prefer_entropy")

	revMgr := NewReverseManager()
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
		err = revMgr.HandleServerSession(conn, userHash, helloPayload)
		_ = conn.Close()
		serverErr <- err
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
	defer func() { _ = baseConn.Close() }()

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- ServeReverseClientSession(baseConn, "client", []ReverseRoute{
			{Path: "/gitea", Target: "127.0.0.1:3000"},
		})
	}()

	select {
	case err := <-serverErr:
		if err == nil || !strings.Contains(err.Error(), "packed uplink") {
			t.Fatalf("expected packed uplink rejection, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("server session timeout")
	}

	select {
	case err := <-clientErr:
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatalf("client session timeout")
	}
}

func TestReverseProxySession_TargetDomainUsesUpstreamHost(t *testing.T) {
	table := sudoku.NewTable("seed-reverse-domain-target", "prefer_entropy")

	backendLn, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	defer backendLn.Close()

	_, port, err := net.SplitHostPort(backendLn.Addr().String())
	if err != nil {
		t.Fatalf("split backend addr: %v", err)
	}
	targetAddr := net.JoinHostPort("localhost", port)

	backend := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != targetAddr {
			http.Error(w, "unexpected host: "+r.Host, http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok host=" + r.Host + " path=" + r.URL.Path))
	}))
	backend.Listener = backendLn
	backend.Start()
	defer backend.Close()

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

	baseConn, err := dialReverseBase(ctx, clientCfg)
	if err != nil {
		t.Fatalf("dialReverseBase: %v", err)
	}

	clientErr := make(chan error, 1)
	go func() {
		clientErr <- ServeReverseClientSession(baseConn, "client", []ReverseRoute{
			{Path: "/gitea", Target: targetAddr},
		})
	}()

	client := &http.Client{Timeout: 3 * time.Second}
	url := "http://" + revLn.Addr().String() + "/gitea/check"
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(url)
		if err == nil && resp != nil {
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(string(body), "host="+targetAddr) {
				break
			}
		}
		if time.Now().After(deadline) {
			_ = baseConn.Close()
			t.Fatalf("reverse proxy with domain target not ready: %v", err)
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
