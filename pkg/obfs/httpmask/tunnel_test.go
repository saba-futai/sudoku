package httpmask

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCanonicalHeaderHost(t *testing.T) {
	tests := []struct {
		name     string
		urlHost  string
		scheme   string
		wantHost string
	}{
		{name: "https default port strips", urlHost: "example.com:443", scheme: "https", wantHost: "example.com"},
		{name: "http default port strips", urlHost: "example.com:80", scheme: "http", wantHost: "example.com"},
		{name: "non-default port keeps", urlHost: "example.com:8443", scheme: "https", wantHost: "example.com:8443"},
		{name: "unknown scheme keeps", urlHost: "example.com:443", scheme: "ftp", wantHost: "example.com:443"},
		{name: "ipv6 https strips brackets kept", urlHost: "[::1]:443", scheme: "https", wantHost: "[::1]"},
		{name: "ipv6 non-default keeps", urlHost: "[::1]:8080", scheme: "http", wantHost: "[::1]:8080"},
		{name: "no port returns input", urlHost: "example.com", scheme: "https", wantHost: "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalHeaderHost(tt.urlHost, tt.scheme); got != tt.wantHost {
				t.Fatalf("canonicalHeaderHost(%q, %q) = %q, want %q", tt.urlHost, tt.scheme, got, tt.wantHost)
			}
		})
	}
}

func TestDialTunnel_Auto_FallsBackToPollWithFreshContext(t *testing.T) {
	prevStream := dialStreamFn
	prevPoll := dialPollFn
	t.Cleanup(func() {
		dialStreamFn = prevStream
		dialPollFn = prevPoll
	})

	var streamCalled, pollCalled int
	dialStreamFn = func(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
		streamCalled++
		dl, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("stream ctx missing deadline")
		}
		remain := time.Until(dl)
		if remain < 2*time.Second || remain > 4*time.Second {
			t.Fatalf("stream ctx deadline not in expected range, remaining=%s", remain)
		}
		return nil, errors.New("stream forced fail")
	}

	var peer net.Conn
	dialPollFn = func(ctx context.Context, serverAddress string, opts TunnelDialOptions) (net.Conn, error) {
		pollCalled++
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		c1, c2 := net.Pipe()
		peer = c2
		return c1, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := DialTunnel(ctx, "example.com:443", TunnelDialOptions{Mode: "auto", TLSEnabled: true})
	if err != nil {
		t.Fatalf("DialTunnel(auto) error: %v", err)
	}
	if streamCalled != 1 || pollCalled != 1 {
		_ = c.Close()
		if peer != nil {
			_ = peer.Close()
		}
		t.Fatalf("unexpected calls: stream=%d poll=%d", streamCalled, pollCalled)
	}
	_ = c.Close()
	if peer != nil {
		_ = peer.Close()
	}
}

func TestDialSession_GzipErrorBody_DoesNotLeakBinary(t *testing.T) {
	acceptEncCh := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case acceptEncCh <- r.Header.Get("Accept-Encoding"):
		default:
		}

		body := []byte("404 page not found")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusNotFound)
			gz := gzip.NewWriter(w)
			_, _ = gz.Write(body)
			_ = gz.Close()
			return
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write(body)
	}))
	t.Cleanup(ts.Close)

	serverAddr := strings.TrimPrefix(ts.URL, "http://")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	_, err := dialSession(ctx, serverAddr, TunnelDialOptions{PathRoot: "asdfghjkl"}, TunnelModePoll)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if got := <-acceptEncCh; got != "" {
		t.Fatalf("unexpected Accept-Encoding request header: %q", got)
	}
	if strings.Contains(err.Error(), "\x1f\x8b") {
		t.Fatalf("error includes gzipped bytes: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "404 page not found") {
		t.Fatalf("error missing body snippet: %q", err.Error())
	}
}

func TestTunnelServer_InferModeWithoutTunnelHeader_Stream(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PathRoot:        "httpmaskpath",
		AuthKey:         "secret",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      50 * time.Millisecond,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	auth := newTunnelAuth("secret", 0)
	authToken := auth.token(TunnelModeStream, http.MethodGet, "/session", time.Now())

	_, _ = io.WriteString(client, fmt.Sprintf(
		"GET /httpmaskpath/session HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"Authorization: Bearer %s\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"\r\n", authToken))

	raw, _ := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}

	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		_ = c.Close()
		t.Fatalf("invalid http response: %q", string(raw))
	}
	body := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(body, "token=") {
		_ = c.Close()
		t.Fatalf("missing token, body=%q", body)
	}
	sessToken := strings.TrimPrefix(body, "token=")
	if sessToken == "" {
		_ = c.Close()
		t.Fatalf("empty session token")
	}
	srv.sessionClose(sessToken)
	_ = c.Close()
}

func TestTunnelServer_InferModeWithoutTunnelHeader_Poll(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PathRoot:        "httpmaskpath",
		AuthKey:         "secret",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      50 * time.Millisecond,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	auth := newTunnelAuth("secret", 0)
	authToken := auth.token(TunnelModePoll, http.MethodGet, "/session", time.Now())

	_, _ = io.WriteString(client, fmt.Sprintf(
		"GET /httpmaskpath/session HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"Authorization: Bearer %s\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"\r\n", authToken))

	raw, _ := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}

	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		_ = c.Close()
		t.Fatalf("invalid http response: %q", string(raw))
	}
	body := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(body, "token=") {
		_ = c.Close()
		t.Fatalf("missing token, body=%q", body)
	}
	sessToken := strings.TrimPrefix(body, "token=")
	if sessToken == "" {
		_ = c.Close()
		t.Fatalf("empty session token")
	}
	srv.sessionClose(sessToken)
	_ = c.Close()
}

func TestTunnelServer_InferModeWithoutTunnelHeader_Stream_AuthQuery(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PathRoot:        "httpmaskpath",
		AuthKey:         "secret",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      50 * time.Millisecond,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	auth := newTunnelAuth("secret", 0)
	authToken := auth.token(TunnelModeStream, http.MethodGet, "/session", time.Now())

	_, _ = io.WriteString(client, fmt.Sprintf(
		"GET /httpmaskpath/session?auth=%s HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"\r\n", authToken))

	raw, _ := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}

	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		_ = c.Close()
		t.Fatalf("invalid http response: %q", string(raw))
	}
	body := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(body, "token=") {
		_ = c.Close()
		t.Fatalf("missing token, body=%q", body)
	}
	sessToken := strings.TrimPrefix(body, "token=")
	if sessToken == "" {
		_ = c.Close()
		t.Fatalf("empty session token")
	}
	srv.sessionClose(sessToken)
	_ = c.Close()
}

func TestTunnelServer_InferModeWithoutTunnelHeader_Poll_AuthQuery(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PathRoot:        "httpmaskpath",
		AuthKey:         "secret",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      50 * time.Millisecond,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	auth := newTunnelAuth("secret", 0)
	authToken := auth.token(TunnelModePoll, http.MethodGet, "/session", time.Now())

	_, _ = io.WriteString(client, fmt.Sprintf(
		"GET /httpmaskpath/session?auth=%s HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"\r\n", authToken))

	raw, _ := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}

	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		_ = c.Close()
		t.Fatalf("invalid http response: %q", string(raw))
	}
	body := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(body, "token=") {
		_ = c.Close()
		t.Fatalf("missing token, body=%q", body)
	}
	sessToken := strings.TrimPrefix(body, "token=")
	if sessToken == "" {
		_ = c.Close()
		t.Fatalf("empty session token")
	}
	srv.sessionClose(sessToken)
	_ = c.Close()
}

func TestTunnelServer_Stream_SplitSession_PushPull(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "stream",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      5 * time.Second,
	})

	authorize := func() (token string, stream net.Conn) {
		client, server := net.Pipe()
		t.Cleanup(func() { _ = client.Close() })

		var (
			res HandleResult
			c   net.Conn
			err error
		)
		done := make(chan struct{})
		go func() {
			res, c, err = srv.HandleConn(server)
			close(done)
		}()

		_, _ = io.WriteString(client,
			"GET /session HTTP/1.1\r\n"+
				"Host: example.com\r\n"+
				"X-Sudoku-Tunnel: stream\r\n"+
				"X-Sudoku-Version: 1\r\n"+
				"\r\n")
		raw, _ := io.ReadAll(client)
		<-done

		if err != nil {
			t.Fatalf("authorize HandleConn error: %v", err)
		}
		if res != HandleStartTunnel || c == nil {
			t.Fatalf("authorize unexpected result: res=%v conn=%v", res, c)
		}

		parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
		if len(parts) != 2 {
			_ = c.Close()
			t.Fatalf("authorize invalid http response: %q", string(raw))
		}
		body := strings.TrimSpace(parts[1])
		if !strings.HasPrefix(body, "token=") {
			_ = c.Close()
			t.Fatalf("authorize missing token, body=%q", body)
		}
		token = strings.TrimPrefix(body, "token=")
		if token == "" {
			_ = c.Close()
			t.Fatalf("authorize empty token")
		}
		return token, c
	}

	token, stream := authorize()
	t.Cleanup(func() {
		srv.sessionClose(token)
		_ = stream.Close()
	})

	// Push bytes into the session.
	{
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			_, _, _ = srv.HandleConn(server)
			close(done)
		}()

		payload := "abc"
		type readResult struct {
			b   []byte
			err error
		}
		readCh := make(chan readResult, 1)
		go func() {
			buf := make([]byte, len(payload))
			_ = stream.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, err := io.ReadFull(stream, buf)
			readCh <- readResult{b: buf, err: err}
		}()

		_, _ = io.WriteString(client, fmt.Sprintf(
			"POST /api/v1/upload?token=%s HTTP/1.1\r\n"+
				"Host: example.com\r\n"+
				"X-Sudoku-Tunnel: stream\r\n"+
				"X-Sudoku-Version: 1\r\n"+
				"Content-Length: %d\r\n"+
				"\r\n"+
				"%s", token, len(payload), payload))
		_, _ = io.ReadAll(client)
		<-done
		_ = client.Close()

		rr := <-readCh
		if rr.err != nil {
			t.Fatalf("read pushed payload error: %v", rr.err)
		}
		if got := string(rr.b); got != payload {
			t.Fatalf("pushed payload mismatch: got %q want %q", got, payload)
		}
	}

	// Pull bytes from the session.
	{
		client, server := net.Pipe()
		done := make(chan struct{})
		go func() {
			_, _, _ = srv.HandleConn(server)
			close(done)
		}()

		_, _ = io.WriteString(client, fmt.Sprintf(
			"GET /stream?token=%s HTTP/1.1\r\n"+
				"Host: example.com\r\n"+
				"X-Sudoku-Tunnel: stream\r\n"+
				"X-Sudoku-Version: 1\r\n"+
				"\r\n", token))

		br := bufio.NewReader(client)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
		if err != nil {
			t.Fatalf("read pull response error: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pull status=%s", resp.Status)
		}

		writeDone := make(chan struct{})
		go func() {
			_, _ = stream.Write([]byte("xyz"))
			close(writeDone)
		}()

		body, _ := io.ReadAll(resp.Body)
		<-writeDone
		<-done
		_ = client.Close()

		if string(body) != "xyz" {
			t.Fatalf("pulled payload mismatch: got %q want %q", string(body), "xyz")
		}
	}
}

func TestTunnelServer_SessionTTL_ReapsAfterIdle(t *testing.T) {
	const ttl = 150 * time.Millisecond
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:       "poll",
		SessionTTL: ttl,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.sessionAuthorize(server)
		close(done)
	}()

	raw, readErr := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("sessionAuthorize error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected sessionAuthorize result: res=%v conn=%v", res, c)
	}
	t.Cleanup(func() { _ = c.Close() })

	if readErr != nil {
		t.Fatalf("read response: %v", readErr)
	}
	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid http response: %q", string(raw))
	}
	token, err := parseTunnelToken([]byte(parts[1]))
	if err != nil || token == "" {
		t.Fatalf("parse token: %v (%q)", err, strings.TrimSpace(parts[1]))
	}

	// Ensure the session is considered active right before the first TTL check, then becomes idle.
	time.Sleep(ttl / 2)
	if _, ok := srv.sessionGet(token); !ok {
		t.Fatalf("session missing before reaping: %q", token)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !srv.sessionHas(token) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	srv.sessionClose(token)
	t.Fatalf("session not reaped: %q", token)
}

func TestTunnelServer_Stream_Auth_RejectsMissingToken(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:                "stream",
		AuthKey:             "secret-key",
		PassThroughOnReject: true,
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	_, _ = io.WriteString(client,
		"GET /session HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"X-Sudoku-Tunnel: stream\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"\r\n")
	_ = client.Close()
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandlePassThrough || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}
	r, ok := c.(interface{ IsHTTPMaskRejected() bool })
	if !ok || !r.IsHTTPMaskRejected() {
		_ = c.Close()
		t.Fatalf("expected rejected passthrough conn")
	}
	_ = c.Close()
}

func TestTunnelServer_Stream_Auth_AllowsValidToken(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:    "stream",
		AuthKey: "secret-key",
	})

	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })

	var (
		res HandleResult
		c   net.Conn
		err error
	)
	done := make(chan struct{})
	go func() {
		res, c, err = srv.HandleConn(server)
		close(done)
	}()

	auth := newTunnelAuth("secret-key", 0)
	token := auth.token(TunnelModeStream, http.MethodGet, "/session", time.Now())

	_, _ = io.WriteString(client, fmt.Sprintf(
		"GET /session HTTP/1.1\r\n"+
			"Host: example.com\r\n"+
			"X-Sudoku-Tunnel: stream\r\n"+
			"X-Sudoku-Version: 1\r\n"+
			"Authorization: Bearer %s\r\n"+
			"\r\n", token))

	raw, _ := io.ReadAll(client)
	<-done

	if err != nil {
		t.Fatalf("HandleConn error: %v", err)
	}
	if res != HandleStartTunnel || c == nil {
		t.Fatalf("unexpected result: res=%v conn=%v", res, c)
	}
	defer c.Close()

	parts := strings.SplitN(string(raw), "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid http response: %q", string(raw))
	}
	body := strings.TrimSpace(parts[1])
	if !strings.HasPrefix(body, "token=") {
		t.Fatalf("missing token, body=%q", body)
	}
	sessionToken := strings.TrimPrefix(body, "token=")
	if sessionToken == "" {
		t.Fatalf("empty token")
	}

	srv.sessionClose(sessionToken)
}

func TestPollConn_CloseWrite_NoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const n = 1000
	c := &pollConn{
		ctx:    ctx,
		cancel: cancel,
		queuedConn: queuedConn{
			rxc:         make(chan []byte, 1),
			closed:      make(chan struct{}),
			writeCh:     make(chan []byte, n),
			writeClosed: make(chan struct{}),
			localAddr:   &net.TCPAddr{},
			remoteAddr:  &net.TCPAddr{},
		},
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Write([]byte("x"))
		}()
	}

	_ = c.CloseWrite()
	_ = c.Close()
	wg.Wait()
}

func startRawTunnelServer(t testing.TB, srv *TunnelServer) (addr string, stop func(), tunnelCh <-chan net.Conn) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	tch := make(chan net.Conn, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				res, c, err := srv.HandleConn(conn)
				if err != nil {
					_ = conn.Close()
					return
				}
				switch res {
				case HandleStartTunnel:
					select {
					case tch <- c:
					default:
						_ = c.Close()
					}
				case HandlePassThrough:
					_ = c.Close()
				case HandleDone:
				default:
				}
			}(raw)
		}
	}()

	stop = func() {
		_ = ln.Close()
		<-done
	}

	return ln.Addr().String(), stop, tch
}

func TestDialStreamSplit_CloseWrite_SendsFIN(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      2 * time.Second,
	})

	addr, stop, tunnelCh := startRawTunnelServer(t, srv)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialStreamSplit(ctx, addr, TunnelDialOptions{})
	if err != nil {
		t.Fatalf("dialStreamSplit: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var tunnel net.Conn
	select {
	case tunnel = <-tunnelCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for server tunnel conn")
	}
	t.Cleanup(func() { _ = tunnel.Close() })

	payload := []byte("hello")

	readDone := make(chan struct{})
	var (
		got  []byte
		rerr error
	)
	go func() {
		_ = tunnel.SetReadDeadline(time.Now().Add(3 * time.Second))
		got, rerr = io.ReadAll(tunnel)
		close(readDone)
	}()

	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("client conn missing CloseWrite")
	}
	_ = cw.CloseWrite()

	select {
	case <-readDone:
	case <-time.After(4 * time.Second):
		t.Fatalf("server read timeout (FIN not delivered)")
	}
	if rerr != nil {
		t.Fatalf("server read error: %v", rerr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("server payload mismatch: got=%q want=%q", string(got), string(payload))
	}
}

func TestDialPoll_CloseWrite_SendsFIN(t *testing.T) {
	srv := NewTunnelServer(TunnelServerOptions{
		Mode:            "auto",
		PullReadTimeout: 50 * time.Millisecond,
		SessionTTL:      2 * time.Second,
	})

	addr, stop, tunnelCh := startRawTunnelServer(t, srv)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialPoll(ctx, addr, TunnelDialOptions{})
	if err != nil {
		t.Fatalf("dialPoll: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	var tunnel net.Conn
	select {
	case tunnel = <-tunnelCh:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for server tunnel conn")
	}
	t.Cleanup(func() { _ = tunnel.Close() })

	payload := []byte("hello")

	readDone := make(chan struct{})
	var (
		got  []byte
		rerr error
	)
	go func() {
		_ = tunnel.SetReadDeadline(time.Now().Add(3 * time.Second))
		got, rerr = io.ReadAll(tunnel)
		close(readDone)
	}()

	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("client write: %v", err)
	}
	cw, ok := conn.(interface{ CloseWrite() error })
	if !ok {
		t.Fatalf("client conn missing CloseWrite")
	}
	_ = cw.CloseWrite()

	select {
	case <-readDone:
	case <-time.After(4 * time.Second):
		t.Fatalf("server read timeout (FIN not delivered)")
	}
	if rerr != nil {
		t.Fatalf("server read error: %v", rerr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("server payload mismatch: got=%q want=%q", string(got), string(payload))
	}
}

func TestDialStreamOne_UpgradeHonorsContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	holdConn := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		<-holdConn
		_ = conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = dialStreamOne(ctx, ln.Addr().String(), TunnelDialOptions{
		TLSEnabled: true,
		Upgrade: func(raw net.Conn) (net.Conn, error) {
			_, werr := raw.Write([]byte("x"))
			return raw, werr
		},
	})
	close(holdConn)

	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("dialStreamOne took too long: %s", took)
	}
}
