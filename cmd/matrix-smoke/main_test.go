package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
)

func TestMatrixSmoke(t *testing.T) {
	t.Helper()

	*flagFailFast = true
	*flagVerbose = false
	*flagTimeout = 10 * time.Second
	*flagPayload = 64 // KiB
	*flagQuick = testing.Short()

	all := combos(*flagQuick)
	seen := make(map[string]struct{}, len(all))
	dedup := make([]combo, 0, len(all))
	for _, c := range all {
		cc := c.canonical()
		k := cc.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dedup = append(dedup, cc)
	}
	all = dedup

	for _, tc := range all {
		tc := tc
		name := fmt.Sprintf(
			"dl=%t_hm=%t_mode=%s_mux=%s_root=%s_ascii=%s_tables=%s",
			tc.enablePureDownlink,
			tc.httpmaskEnabled,
			tc.httpmaskMode,
			tc.mux,
			func() string {
				if tc.pathRoot == "" {
					return "none"
				}
				return tc.pathRoot
			}(),
			tc.asciiMode,
			tc.tableSet,
		)
		t.Run(name, func(t *testing.T) {
			if err := runOne(tc); err != nil {
				t.Fatalf("matrix smoke failed: %v", err)
			}
		})
	}
}

func TestHTTPMaskRTTParity(t *testing.T) {
	t.Helper()

	cases := []combo{
		{enablePureDownlink: true, httpmaskEnabled: true, mux: "off", httpmaskMode: "auto", pathRoot: "aabbcc", asciiMode: "prefer_entropy", tableSet: "default"},
		{enablePureDownlink: false, httpmaskEnabled: true, mux: "off", httpmaskMode: "ws", pathRoot: "", asciiMode: "prefer_ascii", tableSet: "custom7"},
	}

	for _, tc := range cases {
		tc := tc.canonical()
		t.Run(tc.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			fallbackAddr, closeFallback, err := startFallbackHTTPServer(ctx)
			if err != nil {
				t.Fatalf("fallback server: %v", err)
			}
			defer closeFallback()

			echoAddr, closeEcho, err := startTCPEchoServer(ctx)
			if err != nil {
				t.Fatalf("echo server: %v", err)
			}
			defer closeEcho()

			const seedKey = "matrix-smoke-key"
			tables, err := getTables(seedKey, tc.asciiMode, tc.tableSet)
			if err != nil {
				t.Fatalf("tables: %v", err)
			}

			onCfg := apis.DefaultConfig()
			onCfg.Key = seedKey
			onCfg.AEADMethod = "chacha20-poly1305"
			onCfg.Tables = tables
			onCfg.PaddingMin = 5
			onCfg.PaddingMax = 25
			onCfg.EnablePureDownlink = tc.enablePureDownlink
			onCfg.HandshakeTimeoutSeconds = 5
			onCfg.DisableHTTPMask = false
			onCfg.HTTPMaskMode = tc.httpmaskMode
			onCfg.HTTPMaskPathRoot = tc.pathRoot
			onCfg.HTTPMaskMultiplex = tc.mux

			offCfg := *onCfg
			offCfg.DisableHTTPMask = true
			offCfg.HTTPMaskMode = "legacy"
			offCfg.HTTPMaskPathRoot = ""
			offCfg.HTTPMaskMultiplex = "off"

			onSrv, err := startSudokuServer(ctx, onCfg, fallbackAddr, false)
			if err != nil {
				t.Fatalf("start httpmask server: %v", err)
			}
			defer onSrv.close()

			offSrv, err := startSudokuServer(ctx, &offCfg, fallbackAddr, false)
			if err != nil {
				t.Fatalf("start baseline server: %v", err)
			}
			defer offSrv.close()

			const oneWayDelay = 40 * time.Millisecond
			onProxyAddr, closeOnProxy, err := startDelayProxy(ctx, onSrv.serverAddr, oneWayDelay)
			if err != nil {
				t.Fatalf("start httpmask proxy: %v", err)
			}
			defer closeOnProxy()

			offProxyAddr, closeOffProxy, err := startDelayProxy(ctx, offSrv.serverAddr, oneWayDelay)
			if err != nil {
				t.Fatalf("start baseline proxy: %v", err)
			}
			defer closeOffProxy()

			onClient := *onCfg
			onClient.ServerAddress = onProxyAddr
			onClient.TargetAddress = echoAddr

			offClient := offCfg
			offClient.ServerAddress = offProxyAddr
			offClient.TargetAddress = echoAddr

			const warmupRuns = 2
			for i := 0; i < warmupRuns; i++ {
				if _, err := measureFirstEchoRTT(context.Background(), &onClient); err != nil {
					t.Fatalf("warm up httpmask rtt: %v", err)
				}
				if _, err := measureFirstEchoRTT(context.Background(), &offClient); err != nil {
					t.Fatalf("warm up baseline rtt: %v", err)
				}
			}

			const sampleRuns = 5
			enabledSamples := make([]time.Duration, 0, sampleRuns)
			baselineSamples := make([]time.Duration, 0, sampleRuns)
			for i := 0; i < sampleRuns; i++ {
				if i%2 == 0 {
					enabledDur, err := measureFirstEchoRTT(context.Background(), &onClient)
					if err != nil {
						t.Fatalf("measure httpmask rtt: %v", err)
					}
					enabledSamples = append(enabledSamples, enabledDur)

					baselineDur, err := measureFirstEchoRTT(context.Background(), &offClient)
					if err != nil {
						t.Fatalf("measure baseline rtt: %v", err)
					}
					baselineSamples = append(baselineSamples, baselineDur)
					continue
				}

				baselineDur, err := measureFirstEchoRTT(context.Background(), &offClient)
				if err != nil {
					t.Fatalf("measure baseline rtt: %v", err)
				}
				baselineSamples = append(baselineSamples, baselineDur)

				enabledDur, err := measureFirstEchoRTT(context.Background(), &onClient)
				if err != nil {
					t.Fatalf("measure httpmask rtt: %v", err)
				}
				enabledSamples = append(enabledSamples, enabledDur)
			}

			enabledDur := trimmedMeanDuration(enabledSamples)
			baselineDur := trimmedMeanDuration(baselineSamples)
			const tolerance = 45 * time.Millisecond
			if enabledDur > baselineDur+tolerance {
				t.Fatalf(
					"httpmask RTT mismatch: enabled=%v baseline=%v tolerance=%v enabled_samples=%v baseline_samples=%v",
					enabledDur,
					baselineDur,
					tolerance,
					enabledSamples,
					baselineSamples,
				)
			}
		})
	}
}

func trimmedMeanDuration(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	ordered := slices.Clone(samples)
	slices.Sort(ordered)
	if len(ordered) > 2 {
		ordered = ordered[1 : len(ordered)-1]
	}
	var total time.Duration
	for _, sample := range ordered {
		total += sample
	}
	return total / time.Duration(len(ordered))
}

func measureFirstEchoRTT(ctx context.Context, cfg *apis.ProtocolConfig) (time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()
	conn, err := apis.Dial(runCtx, cfg)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte{0x42}); err != nil {
		return 0, err
	}
	var b [1]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}

func startDelayProxy(ctx context.Context, backend string, oneWayDelay time.Duration) (addr string, closeFn func() error, err error) {
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
			go handleDelayProxyConn(c, backend, oneWayDelay)
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

func handleDelayProxyConn(client net.Conn, backend string, oneWayDelay time.Duration) {
	defer client.Close()

	server, err := net.DialTimeout("tcp", backend, 3*time.Second)
	if err != nil {
		return
	}
	defer server.Close()

	copyWithDelay := func(dst net.Conn, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				time.Sleep(oneWayDelay)
				if werr := writeFull(dst, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if cw, ok := dst.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
				return
			}
		}
	}

	done := make(chan struct{}, 2)
	go func() {
		copyWithDelay(server, client)
		done <- struct{}{}
	}()
	go func() {
		copyWithDelay(client, server)
		done <- struct{}{}
	}()
	<-done
	<-done
}
