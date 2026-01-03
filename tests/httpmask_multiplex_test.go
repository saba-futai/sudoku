package tests

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

type muxEchoServerStats struct {
	handshakes int64
	streams    int64
}

func (s *muxEchoServerStats) Handshakes() int64 { return atomic.LoadInt64(&s.handshakes) }
func (s *muxEchoServerStats) Streams() int64    { return atomic.LoadInt64(&s.streams) }

func startHTTPMaskMultiplexEchoServer(t testing.TB, serverCfg *apis.ProtocolConfig) (addr string, stats *muxEchoServerStats, stop func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	stats = &muxEchoServerStats{}

	ts := httpmask.NewTunnelServer(httpmask.TunnelServerOptions{Mode: serverCfg.HTTPMaskMode})

	go func() {
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}

			go func(c net.Conn) {
				defer c.Close()

				res, tunneled, err := ts.HandleConn(c)
				if err != nil {
					return
				}
				connForHandshake := c
				handshakeCfg := serverCfg

				switch res {
				case httpmask.HandleDone:
					return
				case httpmask.HandlePassThrough:
					connForHandshake = tunneled
				case httpmask.HandleStartTunnel:
					inner := *serverCfg
					inner.DisableHTTPMask = true
					handshakeCfg = &inner
					connForHandshake = tunneled
				default:
					return
				}

				tunnelConn, _, fail, err := apis.ServerHandshakeFlexibleWithUserHash(connForHandshake, handshakeCfg)
				if err != nil {
					_ = fail(err)
					return
				}
				defer tunnelConn.Close()
				atomic.AddInt64(&stats.handshakes, 1)

				peek := make([]byte, 1)
				if _, err := io.ReadFull(tunnelConn, peek); err != nil {
					return
				}

				if peek[0] != apis.MultiplexMagicByte {
					// Non-mux fallback (shouldn't happen in mux tests).
					tuned := apis.NewPreBufferedConn(tunnelConn, peek)
					if _, err := apis.ReadTargetAddress(tuned); err != nil {
						return
					}
					io.Copy(tuned, tuned)
					return
				}

				mux, err := apis.AcceptMultiplexServer(tunnelConn)
				if err != nil {
					return
				}
				defer mux.Close()

				for {
					stream, _, err := mux.AcceptTCP()
					if err != nil {
						return
					}
					atomic.AddInt64(&stats.streams, 1)
					go func(s net.Conn) {
						defer s.Close()
						io.Copy(s, s)
					}(stream)
				}
			}(raw)
		}
	}()

	return ln.Addr().String(), stats, func() { _ = ln.Close() }
}

func TestHTTPMaskTunnel_Multiplex_Stream(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-mux-stream"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}

	addr, stats, stop := startHTTPMaskMultiplexEchoServer(t, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "stream",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	sess, err := apis.DialMultiplex(ctx, clientCfg)
	if err != nil {
		t.Fatalf("DialMultiplex: %v", err)
	}
	defer sess.Close()

	for i := 0; i < 6; i++ {
		c, err := sess.Dial(ctx, "example.com:80")
		if err != nil {
			t.Fatalf("Dial stream %d: %v", i, err)
		}

		msg := []byte("hello-mux")
		if _, err := c.Write(msg); err != nil {
			_ = c.Close()
			t.Fatalf("write: %v", err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(c, buf); err != nil {
			_ = c.Close()
			t.Fatalf("read: %v", err)
		}
		_ = c.Close()
		if string(buf) != string(msg) {
			t.Fatalf("echo mismatch: got %q", buf)
		}
	}

	// One Sudoku handshake should serve multiple streams.
	if got := stats.Handshakes(); got != 1 {
		t.Fatalf("unexpected handshake count: %d", got)
	}
	if got := stats.Streams(); got < 6 {
		t.Fatalf("unexpected stream count: %d", got)
	}
}

func TestHTTPMaskTunnel_Multiplex_Stress(t *testing.T) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "test-key-mux-stress"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}

	addr, _, stop := startHTTPMaskMultiplexEchoServer(t, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "stream",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()

	sess, err := apis.DialMultiplex(ctx, clientCfg)
	if err != nil {
		t.Fatalf("DialMultiplex: %v", err)
	}
	defer sess.Close()

	const streams = 200
	var wg sync.WaitGroup
	wg.Add(streams)

	errCh := make(chan error, streams)
	for i := 0; i < streams; i++ {
		go func() {
			defer wg.Done()
			c, err := sess.Dial(ctx, "example.com:80")
			if err != nil {
				errCh <- err
				return
			}
			defer c.Close()

			msg := []byte("ping")
			if _, err := c.Write(msg); err != nil {
				errCh <- err
				return
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(c, buf); err != nil {
				errCh <- err
				return
			}
			if string(buf) != string(msg) {
				errCh <- io.ErrUnexpectedEOF
				return
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("stress failure: %v", err)
		}
	}
}

func TestMultiplex_Boundary_InvalidVersion(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	t.Cleanup(func() { _ = server.Close() })

	errCh := make(chan error, 1)
	go func() {
		_, err := apis.AcceptMultiplexServer(server)
		errCh <- err
	}()

	// AcceptMultiplexServer expects the magic byte to have been consumed already; write a bad version byte.
	_, _ = client.Write([]byte{0xFF})
	err := <-errCh
	if err == nil {
		t.Fatalf("expected error")
	}
}
