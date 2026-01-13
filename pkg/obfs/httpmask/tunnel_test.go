package httpmask

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
			rxc:        make(chan []byte, 1),
			closed:     make(chan struct{}),
			writeCh:    make(chan []byte, n),
			localAddr:  &net.TCPAddr{},
			remoteAddr: &net.TCPAddr{},
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

	_ = c.Close()
	wg.Wait()
}
