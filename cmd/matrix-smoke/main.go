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
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

var (
	flagFailFast = flag.Bool("failfast", true, "Stop at first failure")
	flagVerbose  = flag.Bool("v", false, "Verbose logs")
	flagTimeout  = flag.Duration("timeout", 10*time.Second, "Per-case timeout")
	flagQuick    = flag.Bool("quick", false, "Run a smaller representative subset")
	flagPayload  = flag.Int("payload-kib", 256, "Forward payload size in KiB (used to exercise key updates)")
)

type combo struct {
	enablePureDownlink bool
	httpmaskEnabled    bool
	mux                string // off|auto|on (httpmask.multiplex)
	httpmaskMode       string // auto|ws
	pathRoot           string // "" or a segment
	asciiMode          string // prefer_ascii|prefer_entropy
	tableSet           string // default|custom7
}

func (c combo) canonical() combo {
	out := c
	if !out.httpmaskEnabled {
		out.httpmaskMode = "legacy"
		out.pathRoot = ""
		out.mux = "off"
	}
	return out
}

func (c combo) String() string {
	cc := c.canonical()
	return fmt.Sprintf("downlink=%v httpmask=%v mode=%s mux=%s pathroot=%q ascii=%s tables=%s",
		cc.enablePureDownlink, cc.httpmaskEnabled, cc.httpmaskMode, cc.mux, cc.pathRoot, cc.asciiMode, cc.tableSet)
}

type tableCacheKey struct {
	mode     string
	patterns string
}

var globalTableCache sync.Map // map[tableCacheKey][]*sudoku.Table

func getTables(key, asciiMode, setName string) ([]*sudoku.Table, error) {
	patterns := []string(nil)
	switch setName {
	case "default":
		// empty => default layout
	case "custom7":
		patterns = []string{
			"xpxvvpvv",
			"xpvvxvpv",
			"vpxvvpvx",
			"vvpxvpvx",
			"vvpvpxvx",
			"pvxvvpvx",
			"vxpvpvvx",
		}
	default:
		return nil, fmt.Errorf("unknown table set: %q", setName)
	}

	k := tableCacheKey{mode: asciiMode, patterns: strings.Join(patterns, ",")}
	if v, ok := globalTableCache.Load(k); ok {
		if tables, _ := v.([]*sudoku.Table); len(tables) > 0 {
			return tables, nil
		}
	}

	ts, err := sudoku.NewTableSet(key, asciiMode, patterns)
	if err != nil {
		return nil, err
	}
	if ts == nil || len(ts.Tables) == 0 {
		return nil, fmt.Errorf("empty table set")
	}
	globalTableCache.Store(k, ts.Tables)
	return ts.Tables, nil
}

func startTCPEchoServer(ctx context.Context) (addr string, closeFn func() error, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	closeFn = func() error {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
	go func() {
		<-ctx.Done()
		_ = closeFn()
	}()
	return ln.Addr().String(), closeFn, nil
}

func startFallbackHTTPServer(ctx context.Context) (addr string, closeFn func() error, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "fallback ok")
	})
	srv := &http.Server{Handler: mux}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()

	closeFn = func() error {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
	go func() {
		<-ctx.Done()
		_ = closeFn()
	}()
	return ln.Addr().String(), closeFn, nil
}

func writeFull(conn net.Conn, b []byte) error {
	for len(b) > 0 {
		n, err := conn.Write(b)
		if n > 0 {
			b = b[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func proxyBidirectional(a, b net.Conn) {
	tryCloseWrite := func(c net.Conn) {
		if c == nil {
			return
		}
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
			return
		}
		_ = c.Close()
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		tryCloseWrite(b)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		tryCloseWrite(a)
	}()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

func handleFallback(rawConn net.Conn, fallbackAddr string, replayPrefix []byte) {
	handleFallbackWithVerbose(rawConn, fallbackAddr, replayPrefix, *flagVerbose)
}

func handleFallbackWithVerbose(rawConn net.Conn, fallbackAddr string, replayPrefix []byte, verbose bool) {
	if rawConn == nil {
		return
	}
	if verbose {
		snippet := replayPrefix
		if len(snippet) > 256 {
			snippet = snippet[:256]
		}
		fmt.Fprintf(os.Stderr, "fallback proxy: addr=%s replay=%d bytes snippet=%q\n", fallbackAddr, len(replayPrefix), string(snippet))
	}
	_ = rawConn.SetDeadline(time.Now().Add(3 * time.Second))
	dst, err := net.DialTimeout("tcp", fallbackAddr, 3*time.Second)
	if err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "fallback proxy: dial failed: %v\n", err)
		}
		_ = rawConn.Close()
		return
	}
	_ = dst.SetDeadline(time.Time{})
	_ = rawConn.SetDeadline(time.Time{})

	if len(replayPrefix) > 0 {
		_ = dst.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := writeFull(dst, replayPrefix); err != nil {
			if verbose {
				fmt.Fprintf(os.Stderr, "fallback proxy: write failed: %v\n", err)
			}
			_ = dst.Close()
			_ = rawConn.Close()
			return
		}
		_ = dst.SetWriteDeadline(time.Time{})
	}
	proxyBidirectional(rawConn, dst)
}

type serverHarness struct {
	ln           net.Listener
	serverAddr   string
	fallbackAddr string
	tunnelSrv    *httpmask.TunnelServer
	cfgBase      *apis.ProtocolConfig
	verbose      bool
	handshakes   atomic.Int64
}

func (s *serverHarness) close() error {
	if s == nil || s.ln == nil {
		return nil
	}
	return s.ln.Close()
}

func protocolTables(cfg *apis.ProtocolConfig) []*sudoku.Table {
	if cfg == nil {
		return nil
	}
	if len(cfg.Tables) > 0 {
		return cfg.Tables
	}
	if cfg.Table != nil {
		return []*sudoku.Table{cfg.Table}
	}
	return nil
}

func (s *serverHarness) serve(ctx context.Context) error {
	if s == nil || s.ln == nil || s.cfgBase == nil {
		return fmt.Errorf("invalid server harness")
	}
	errCh := make(chan error, 1)
	go func() {
		for {
			c, err := s.ln.Accept()
			if err != nil {
				errCh <- err
				return
			}
			go s.handleConn(c)
		}
	}()

	select {
	case <-ctx.Done():
		_ = s.close()
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
}

func (s *serverHarness) handleConn(rawConn net.Conn) {
	if rawConn == nil {
		return
	}

	handshakeConn := rawConn
	cfg := *s.cfgBase

	if s.tunnelSrv != nil {
		res, c, err := s.tunnelSrv.HandleConn(rawConn)
		if err != nil {
			_ = rawConn.Close()
			return
		}
		switch res {
		case httpmask.HandleDone:
			return
		case httpmask.HandleStartTunnel:
			handshakeConn = c
			cfg.DisableHTTPMask = true
		case httpmask.HandlePassThrough:
			handshakeConn = c
			if r, ok := c.(interface{ IsHTTPMaskRejected() bool }); ok && r.IsHTTPMaskRejected() {
				if s.verbose {
					_ = c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
					buf := make([]byte, 1024)
					n, _ := c.Read(buf)
					_ = c.SetReadDeadline(time.Time{})
					if n > 0 {
						fmt.Fprintf(os.Stderr, "httpmask rejected (prefix): %q\n", string(buf[:n]))
					} else {
						fmt.Fprintf(os.Stderr, "httpmask rejected (no prefix bytes)\n")
					}
				}
				_ = c.Close()
				return
			}
		default:
			_ = rawConn.Close()
			return
		}
	}

	s.handshakes.Add(1)
	conn, session, targetAddr, _, payload, err := apis.ServerHandshakeSessionAutoWithUserHash(handshakeConn, &cfg)
	if err != nil {
		if s.verbose {
			fmt.Fprintf(os.Stderr, "server handshake error: %T: %v\n", err, err)
		}
		var hsErr *apis.HandshakeError
		if errors.As(err, &hsErr) && hsErr != nil {
			replay := append(append([]byte(nil), hsErr.HTTPHeaderData...), hsErr.ReadData...)
			handleFallbackWithVerbose(hsErr.RawConn, s.fallbackAddr, replay, s.verbose)
			return
		}
		_ = rawConn.Close()
		return
	}

	switch session {
	case apis.SessionForward:
		dst, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
		if err != nil {
			_ = conn.Close()
			return
		}
		// Inline proxy here so we can surface tunnel-level errors under -v.
		tryCloseWrite := func(c net.Conn) {
			if c == nil {
				return
			}
			if cw, ok := c.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
				return
			}
			_ = c.Close()
		}

		var wg sync.WaitGroup
		var e1, e2 error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, e1 = io.Copy(dst, conn)
			tryCloseWrite(dst)
		}()
		go func() {
			defer wg.Done()
			_, e2 = io.Copy(conn, dst)
			tryCloseWrite(conn)
		}()
		wg.Wait()
		_ = conn.Close()
		_ = dst.Close()

		if s.verbose {
			if e1 != nil {
				fmt.Fprintf(os.Stderr, "server forward copy (tunnel->target) error: %v\n", e1)
			}
			if e2 != nil {
				fmt.Fprintf(os.Stderr, "server forward copy (target->tunnel) error: %v\n", e2)
			}
		}
	case apis.SessionMux:
		_ = tunnel.HandleMuxServer(conn, nil)
	case apis.SessionUoT:
		_ = tunnel.HandleUoTServer(conn)
	case apis.SessionReverse:
		// For smoke runs we only validate that the control plane can reach this state.
		_ = payload
		_ = conn.Close()
	default:
		_ = conn.Close()
	}
}

func startSudokuServer(ctx context.Context, baseCfg *apis.ProtocolConfig, fallbackAddr string, verbose bool) (*serverHarness, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	h := &serverHarness{
		ln:           ln,
		serverAddr:   ln.Addr().String(),
		fallbackAddr: fallbackAddr,
		cfgBase:      baseCfg,
		verbose:      verbose,
	}

	if !baseCfg.DisableHTTPMask {
		switch strings.ToLower(strings.TrimSpace(baseCfg.HTTPMaskMode)) {
		case "stream", "poll", "auto", "ws":
			h.tunnelSrv = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
				Mode:     baseCfg.HTTPMaskMode,
				PathRoot: baseCfg.HTTPMaskPathRoot,
				AuthKey:  baseCfg.Key,
				EarlyHandshake: tunnel.NewHTTPMaskServerEarlyHandshake(tunnel.EarlyCodecConfig{
					PSK:                baseCfg.Key,
					AEAD:               baseCfg.AEADMethod,
					EnablePureDownlink: baseCfg.EnablePureDownlink,
					PaddingMin:         baseCfg.PaddingMin,
					PaddingMax:         baseCfg.PaddingMax,
				}, protocolTables(baseCfg), nil),
				PassThroughOnReject: true,
			})
		}
	}

	go func() { _ = h.serve(ctx) }()
	return h, nil
}

func smokeFallback(ctx context.Context, serverAddr string) error {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		return err
	}
	defer c.Close()

	_ = c.SetDeadline(time.Now().Add(6 * time.Second))
	req := "GET / HTTP/1.1\r\nHost: example\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		return err
	}

	// Encourage fast handshake failure paths in cases without an HTTP tunnel server.
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}

	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if n == 0 && err != nil {
		return err
	}
	if !bytes.Contains(buf[:n], []byte("fallback ok")) {
		return fmt.Errorf("fallback response mismatch: %q", string(buf[:n]))
	}
	return nil
}

func smokeForward(ctx context.Context, cfg *apis.ProtocolConfig, msgSize int) error {
	c, err := apis.Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()

	payload := make([]byte, msgSize)
	for i := 0; i < len(payload); i++ {
		payload[i] = byte(i)
	}

	_ = c.SetWriteDeadline(time.Now().Add(6 * time.Second))
	if err := writeFull(c, payload); err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Time{})
	got := make([]byte, len(payload))
	_ = c.SetReadDeadline(time.Now().Add(6 * time.Second))
	readN := 0
	for readN < len(got) {
		n, err := c.Read(got[readN:])
		if n > 0 {
			readN += n
		}
		if err != nil {
			_ = c.SetReadDeadline(time.Time{})
			if err == io.EOF {
				return fmt.Errorf("read %d/%d: %w", readN, len(got), err)
			}
			return fmt.Errorf("read %d/%d: %w", readN, len(got), err)
		}
	}
	_ = c.SetReadDeadline(time.Time{})
	if !bytes.Equal(got, payload) {
		return fmt.Errorf("echo mismatch")
	}
	return nil
}

func smokeMux(ctx context.Context, base *apis.ProtocolConfig, target1, target2 string) error {
	mc, err := apis.NewMuxClient(base)
	if err != nil {
		return err
	}
	defer mc.Close()

	c1, err := mc.Dial(ctx, target1)
	if err != nil {
		return err
	}
	defer c1.Close()
	c2, err := mc.Dial(ctx, target2)
	if err != nil {
		return err
	}
	defer c2.Close()

	_ = c1.SetWriteDeadline(time.Now().Add(6 * time.Second))
	_ = c2.SetWriteDeadline(time.Now().Add(6 * time.Second))

	p1 := []byte("mux-one")
	p2 := []byte("mux-two")
	if err := writeFull(c1, p1); err != nil {
		return err
	}
	if err := writeFull(c2, p2); err != nil {
		return err
	}
	_ = c1.SetWriteDeadline(time.Time{})
	_ = c2.SetWriteDeadline(time.Time{})
	r1 := make([]byte, len(p1))
	r2 := make([]byte, len(p2))
	_ = c1.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, err := io.ReadFull(c1, r1); err != nil {
		return err
	}
	_ = c1.SetReadDeadline(time.Time{})
	_ = c2.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, err := io.ReadFull(c2, r2); err != nil {
		return err
	}
	_ = c2.SetReadDeadline(time.Time{})
	if !bytes.Equal(r1, p1) || !bytes.Equal(r2, p2) {
		return fmt.Errorf("mux echo mismatch")
	}
	return nil
}

func runOne(tc combo) error {
	tc = tc.canonical()
	lifeCtx := context.Background()
	timeout := *flagTimeout
	payloadKiB := *flagPayload
	verbose := *flagVerbose

	fallbackAddr, closeFallback, err := startFallbackHTTPServer(lifeCtx)
	if err != nil {
		return fmt.Errorf("fallback server: %w", err)
	}
	defer closeFallback()

	echo1, closeEcho1, err := startTCPEchoServer(lifeCtx)
	if err != nil {
		return fmt.Errorf("echo1: %w", err)
	}
	defer closeEcho1()
	echo2, closeEcho2, err := startTCPEchoServer(lifeCtx)
	if err != nil {
		return fmt.Errorf("echo2: %w", err)
	}
	defer closeEcho2()

	const seedKey = "matrix-smoke-key"
	tables, err := getTables(seedKey, tc.asciiMode, tc.tableSet)
	if err != nil {
		return fmt.Errorf("tables: %w", err)
	}

	serverCfg := apis.DefaultConfig()
	serverCfg.ServerAddress = ""
	serverCfg.TargetAddress = ""
	serverCfg.Key = seedKey
	serverCfg.AEADMethod = "chacha20-poly1305"
	serverCfg.Tables = tables
	serverCfg.PaddingMin = 5
	serverCfg.PaddingMax = 25
	serverCfg.EnablePureDownlink = tc.enablePureDownlink
	serverCfg.HandshakeTimeoutSeconds = 5
	serverCfg.DisableHTTPMask = !tc.httpmaskEnabled
	serverCfg.HTTPMaskMode = tc.httpmaskMode
	serverCfg.HTTPMaskTLSEnabled = false
	serverCfg.HTTPMaskHost = ""
	serverCfg.HTTPMaskPathRoot = tc.pathRoot
	serverCfg.HTTPMaskMultiplex = tc.mux

	srv, err := startSudokuServer(lifeCtx, serverCfg, fallbackAddr, verbose)
	if err != nil {
		return fmt.Errorf("start server: %w", err)
	}
	defer srv.close()

	fbCtx, fbCancel := context.WithTimeout(context.Background(), timeout)
	defer fbCancel()
	if err := smokeFallback(fbCtx, srv.serverAddr); err != nil {
		return fmt.Errorf("fallback smoke: %w", err)
	}

	clientCfg := *serverCfg
	clientCfg.ServerAddress = srv.serverAddr
	clientCfg.TargetAddress = echo1

	oldKU := atomic.LoadInt64(&crypto.KeyUpdateAfterBytes)
	atomic.StoreInt64(&crypto.KeyUpdateAfterBytes, 32<<10) // 32 KiB (smoke)
	defer atomic.StoreInt64(&crypto.KeyUpdateAfterBytes, oldKU)

	fwdCtx, fwdCancel := context.WithTimeout(context.Background(), timeout)
	defer fwdCancel()
	payloadSize := payloadKiB << 10
	if payloadSize <= 0 {
		payloadSize = 256 << 10
	}
	if err := smokeForward(fwdCtx, &clientCfg, payloadSize); err != nil { // large payload triggers key updates
		return fmt.Errorf("forward smoke: %w", err)
	}

	if tc.mux == "on" {
		clientCfg.TargetAddress = ""
		muxCtx, muxCancel := context.WithTimeout(context.Background(), timeout)
		defer muxCancel()
		if err := smokeMux(muxCtx, &clientCfg, echo1, echo2); err != nil {
			return fmt.Errorf("mux smoke: %w", err)
		}
		if got := srv.handshakes.Load(); got > 3 {
			// forward (1) + mux base (1) + fallback (1) => allow small overhead
			return fmt.Errorf("unexpected handshake count: %d", got)
		}
	}
	return nil
}

func combos(quick bool) []combo {
	enablePureDownlink := []bool{true, false}
	httpmaskEnabled := []bool{true, false}
	muxVals := []string{"off", "auto", "on"}
	httpmaskModes := []string{"auto", "ws"}
	pathRoots := []string{"", "aabbcc"}
	asciiModes := []string{"prefer_ascii", "prefer_entropy"}
	tableSets := []string{"default", "custom7"}

	if quick {
		return []combo{
			{true, true, "auto", "auto", "", "prefer_entropy", "default"},
			{false, true, "on", "ws", "aabbcc", "prefer_ascii", "custom7"},
			{true, false, "off", "ws", "", "prefer_entropy", "custom7"},
		}
	}

	var out []combo
	for _, dl := range enablePureDownlink {
		for _, hm := range httpmaskEnabled {
			for _, mux := range muxVals {
				for _, mode := range httpmaskModes {
					for _, pr := range pathRoots {
						for _, ascii := range asciiModes {
							for _, ts := range tableSets {
								out = append(out, combo{
									enablePureDownlink: dl,
									httpmaskEnabled:    hm,
									mux:                mux,
									httpmaskMode:       mode,
									pathRoot:           pr,
									asciiMode:          ascii,
									tableSet:           ts,
								})
							}
						}
					}
				}
			}
		}
	}
	return out
}

func main() {
	flag.Parse()

	all := combos(*flagQuick)
	seen := make(map[string]struct{}, len(all))
	var dedup []combo
	for _, c := range all {
		k := c.canonical().String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dedup = append(dedup, c.canonical())
	}
	all = dedup

	failures := 0
	for i, tc := range all {
		if *flagVerbose {
			fmt.Fprintf(os.Stdout, "[%d/%d] %s\n", i+1, len(all), tc.String())
		}
		if err := runOne(tc); err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", tc.String(), err)
			if *flagFailFast {
				os.Exit(1)
			}
		}
	}
	if failures > 0 {
		fmt.Fprintf(os.Stderr, "%d case(s) failed\n", failures)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stdout, "OK: %d case(s)\n", len(all))
}
