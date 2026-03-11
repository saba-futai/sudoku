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
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	mrand "math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

var (
	flagHost     = flag.String("host", "127.0.0.1", "Server host (IPv4/IPv6 literal or domain)")
	flagKey      = flag.String("key", "", "Client key (split private or shared key)")
	flagTimeout  = flag.Duration("timeout", 90*time.Second, "Per-case timeout")
	flagFailFast = flag.Bool("failfast", true, "Stop at first failure")
	flagVerbose  = flag.Bool("v", false, "Verbose logs")

	flagPortEntropyNoRoot = flag.Int("port-entropy-noroot", 443, "Server port for ascii=entropy path_root=''")
	flagPortEntropyRoot   = flag.Int("port-entropy-root", 444, "Server port for ascii=entropy path_root='aabbcc'")
	flagPortAsciiNoRoot   = flag.Int("port-ascii-noroot", 445, "Server port for ascii=ascii path_root=''")
	flagPortAsciiRoot     = flag.Int("port-ascii-root", 446, "Server port for ascii=ascii path_root='aabbcc'")

	flagPortEntropyNoRootPacked = flag.Int("port-entropy-noroot-packed", 447, "Server port for ascii=entropy packed downlink path_root=''")
	flagPortEntropyRootPacked   = flag.Int("port-entropy-root-packed", 448, "Server port for ascii=entropy packed downlink path_root='aabbcc'")
	flagPortAsciiNoRootPacked   = flag.Int("port-ascii-noroot-packed", 449, "Server port for ascii=ascii packed downlink path_root=''")
	flagPortAsciiRootPacked     = flag.Int("port-ascii-root-packed", 450, "Server port for ascii=ascii packed downlink path_root='aabbcc'")

	flagEcho1 = flag.Int("echo1", 46000, "Remote echo port 1 (server dials 127.0.0.1:<echo1>)")
	flagEcho2 = flag.Int("echo2", 46001, "Remote echo port 2 (server dials 127.0.0.1:<echo2>)")

	flagPayloadKiB   = flag.Int("payload-kib", 32, "Forward payload size in KiB")
	flagStart        = flag.Int("start", 0, "Start case index (for debugging)")
	flagCount        = flag.Int("count", -1, "Number of cases to run from start (-1 = all)")
	flagVerifyRTT    = flag.Bool("verify-rtt", true, "Verify httpmask first-byte RTT matches the no-httpmask baseline")
	flagKeyUpdate    = flag.Bool("keyupdate", true, "Run an extra large transfer to exercise key updates")
	flagKeyUpdateMiB = flag.Int("keyupdate-mib", 40, "Key update transfer size in MiB (must exceed 32MiB)")
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
	seed     string
	mode     string
	patterns string
}

var globalTableCache sync.Map // map[tableCacheKey][]*sudoku.Table

func canonicalSeedKey(key string) string {
	if recoveredFromKey, err := crypto.RecoverPublicKey(key); err == nil {
		return crypto.EncodePoint(recoveredFromKey)
	}
	return key
}

func getTables(seed, asciiMode, setName string) ([]*sudoku.Table, error) {
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

	k := tableCacheKey{seed: seed, mode: asciiMode, patterns: strings.Join(patterns, ",")}
	if v, ok := globalTableCache.Load(k); ok {
		if tables, _ := v.([]*sudoku.Table); len(tables) > 0 {
			return tables, nil
		}
	}

	ts, err := sudoku.NewTableSet(seed, asciiMode, patterns)
	if err != nil {
		return nil, err
	}
	if ts == nil || len(ts.Tables) == 0 {
		return nil, fmt.Errorf("empty table set")
	}
	globalTableCache.Store(k, ts.Tables)
	return ts.Tables, nil
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	var nerr interface{ Timeout() bool }
	if errors.As(err, &nerr) && nerr.Timeout() {
		return true
	}
	// Fallback for wrapped errors that may lose types across boundaries.
	s := err.Error()
	return strings.Contains(s, "operation timed out") || strings.Contains(s, "i/o timeout") || strings.Contains(s, "context deadline exceeded")
}

func serverAddr(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}

func portFor(c combo) int {
	cc := c.canonical()
	hasRoot := strings.TrimSpace(cc.pathRoot) != ""
	if !cc.enablePureDownlink {
		switch cc.asciiMode {
		case "prefer_ascii":
			if hasRoot {
				return *flagPortAsciiRootPacked
			}
			return *flagPortAsciiNoRootPacked
		default:
			if hasRoot {
				return *flagPortEntropyRootPacked
			}
			return *flagPortEntropyNoRootPacked
		}
	}
	switch cc.asciiMode {
	case "prefer_ascii":
		if hasRoot {
			return *flagPortAsciiRoot
		}
		return *flagPortAsciiNoRoot
	default:
		if hasRoot {
			return *flagPortEntropyRoot
		}
		return *flagPortEntropyNoRoot
	}
}

func smokeFallback(ctx context.Context, host string, port int) error {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/", serverAddr(host, port)), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if !bytes.Contains(body, []byte("fallback ok")) {
		return fmt.Errorf("fallback response mismatch: %q", string(body))
	}
	return nil
}

func measureForward(ctx context.Context, cfg *apis.ProtocolConfig, msgSize int, ioTimeout time.Duration) (time.Duration, error) {
	start := time.Now()
	c, err := apis.Dial(ctx, cfg)
	if err != nil {
		return 0, err
	}
	defer c.Close()

	payload := make([]byte, msgSize)
	for i := 0; i < len(payload); i++ {
		payload[i] = byte(i)
	}

	if ioTimeout <= 0 {
		ioTimeout = 10 * time.Second
	}
	_ = c.SetWriteDeadline(time.Now().Add(ioTimeout))
	if err := connutil.WriteFull(c, payload); err != nil {
		return 0, err
	}
	_ = c.SetWriteDeadline(time.Time{})

	got := make([]byte, len(payload))
	_ = c.SetReadDeadline(time.Now().Add(ioTimeout))
	readN := 0
	for readN < len(got) {
		n, err := c.Read(got[readN:])
		if n > 0 {
			readN += n
		}
		if err != nil {
			_ = c.SetReadDeadline(time.Time{})
			return 0, fmt.Errorf("read %d/%d: %w", readN, len(got), err)
		}
	}
	_ = c.SetReadDeadline(time.Time{})
	if !bytes.Equal(got, payload) {
		return 0, fmt.Errorf("echo mismatch")
	}
	return time.Since(start), nil
}

func smokeMux(ctx context.Context, base *apis.ProtocolConfig, target1, target2 string) error {
	mc, err := apis.NewMuxClient(base)
	if err != nil {
		return err
	}
	defer mc.Close()

	c1, err := mc.Dial(ctx, target1)
	if err != nil {
		return fmt.Errorf("mux dial1: %w", err)
	}
	defer c1.Close()
	c2, err := mc.Dial(ctx, target2)
	if err != nil {
		return fmt.Errorf("mux dial2: %w", err)
	}
	defer c2.Close()

	p1 := []byte("hello-1")
	p2 := []byte("hello-2")
	if _, err := c1.Write(p1); err != nil {
		return err
	}
	if _, err := c2.Write(p2); err != nil {
		return err
	}

	b1 := make([]byte, len(p1))
	b2 := make([]byte, len(p2))
	_ = c1.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, err := io.ReadFull(c1, b1); err != nil {
		return err
	}
	_ = c2.SetReadDeadline(time.Now().Add(6 * time.Second))
	if _, err := io.ReadFull(c2, b2); err != nil {
		return err
	}
	if !bytes.Equal(b1, p1) || !bytes.Equal(b2, p2) {
		return fmt.Errorf("mux echo mismatch")
	}
	return nil
}

func smokeForwardHash(ctx context.Context, cfg *apis.ProtocolConfig, totalBytes int64) error {
	conn, err := apis.Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Shared read/write deadline to avoid indefinite stalls while allowing full-duplex progress.
	_ = conn.SetDeadline(time.Now().Add(12 * time.Minute))

	writeH := sha256.New()
	readH := sha256.New()

	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		buf := make([]byte, 64*1024)
		remain := totalBytes
		for remain > 0 {
			n := int64(len(buf))
			if n > remain {
				n = remain
			}
			_, err := io.ReadFull(conn, buf[:n])
			if err != nil {
				errCh <- err
				return
			}
			_, _ = readH.Write(buf[:n])
			remain -= n
		}
		errCh <- nil
	}()

	rng := mrand.New(mrand.NewSource(1))
	buf := make([]byte, 64*1024)
	remain := totalBytes
	for remain > 0 {
		n := int64(len(buf))
		if n > remain {
			n = remain
		}
		_, _ = rng.Read(buf[:n])
		_, _ = writeH.Write(buf[:n])
		if err := connutil.WriteFull(conn, buf[:n]); err != nil {
			return err
		}
		remain -= n
	}

	if err := <-errCh; err != nil {
		return err
	}
	if !bytes.Equal(writeH.Sum(nil), readH.Sum(nil)) {
		return fmt.Errorf("hash mismatch")
	}
	return nil
}

func allCombos() []combo {
	enablePureDownlink := []bool{true, false}
	httpmaskEnabled := []bool{true, false}
	muxVals := []string{"off", "auto", "on"}
	httpmaskModes := []string{"auto", "ws"}
	pathRoots := []string{"", "aabbcc"}
	asciiModes := []string{"prefer_ascii", "prefer_entropy"}
	tableSets := []string{"default", "custom7"}

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

func rttTolerance(base time.Duration) time.Duration {
	tol := base / 4
	if tol < 75*time.Millisecond {
		tol = 75 * time.Millisecond
	}
	return tol
}

func verifyRTTParity(ctx context.Context, host string, enabled combo, cfg *apis.ProtocolConfig, ioTimeout time.Duration) error {
	if !*flagVerifyRTT || !enabled.httpmaskEnabled {
		return nil
	}

	baseline := enabled.canonical()
	baseline.httpmaskEnabled = false
	baseline.httpmaskMode = "legacy"
	baseline.mux = "off"
	baseline.pathRoot = ""

	baseCfg := *cfg
	baseCfg.ServerAddress = serverAddr(host, portFor(baseline))
	baseCfg.DisableHTTPMask = true
	baseCfg.HTTPMaskMode = "legacy"
	baseCfg.HTTPMaskMultiplex = "off"
	baseCfg.HTTPMaskPathRoot = ""

	enabledProbeCfg := *cfg
	enabledDur, err := measureForward(ctx, &enabledProbeCfg, 1, ioTimeout)
	if err != nil {
		return fmt.Errorf("httpmask probe failed: %w", err)
	}
	baseDur, err := measureForward(ctx, &baseCfg, 1, ioTimeout)
	if err != nil {
		return fmt.Errorf("baseline no-httpmask probe failed: %w", err)
	}
	if enabledDur > baseDur+rttTolerance(baseDur) {
		return fmt.Errorf("rtt mismatch: httpmask=%v baseline=%v tolerance=%v", enabledDur, baseDur, rttTolerance(baseDur))
	}
	return nil
}

func main() {
	flag.Parse()
	if strings.TrimSpace(*flagKey) == "" {
		fmt.Fprintln(os.Stderr, "missing -key")
		os.Exit(2)
	}

	all := allCombos()
	seen := make(map[string]struct{}, len(all))
	var combos []combo
	for _, c := range all {
		cc := c.canonical()
		k := cc.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		combos = append(combos, cc)
	}

	var failures int32
	seenFallback := sync.Map{} // map[int]bool

	start := *flagStart
	if start < 0 {
		start = 0
	}
	end := len(combos)
	if *flagCount >= 0 && start+*flagCount < end {
		end = start + *flagCount
	}
	if start > len(combos) {
		start = len(combos)
	}

	for i := start; i < end; i++ {
		c := combos[i]
		cc := c.canonical()
		port := portFor(cc)
		seed := canonicalSeedKey(*flagKey)

		caseCtx, cancel := context.WithTimeout(context.Background(), *flagTimeout)
		err := func() error {
			// Fallback once per server instance.
			if _, ok := seenFallback.Load(port); !ok {
				if err := smokeFallback(caseCtx, *flagHost, port); err != nil {
					return fmt.Errorf("fallback: %w", err)
				}
				seenFallback.Store(port, true)
			}

			tables, err := getTables(seed, cc.asciiMode, cc.tableSet)
			if err != nil {
				return fmt.Errorf("tables: %w", err)
			}

			cfg := apis.DefaultConfig()
			cfg.ServerAddress = serverAddr(*flagHost, port)
			cfg.Key = *flagKey
			cfg.AEADMethod = "chacha20-poly1305"
			cfg.EnablePureDownlink = cc.enablePureDownlink
			cfg.PaddingMin = 5
			cfg.PaddingMax = 15

			cfg.DisableHTTPMask = !cc.httpmaskEnabled
			cfg.HTTPMaskMode = cc.httpmaskMode
			cfg.HTTPMaskTLSEnabled = false
			cfg.HTTPMaskHost = ""
			cfg.HTTPMaskPathRoot = cc.pathRoot
			cfg.HTTPMaskMultiplex = cc.mux

			if cc.tableSet == "default" {
				cfg.Table = tables[0]
			} else {
				cfg.Tables = tables
			}

			cfg.TargetAddress = fmt.Sprintf("127.0.0.1:%d", *flagEcho1)
			ioTimeout := 20 * time.Second
			if cc.httpmaskEnabled {
				ioTimeout = 45 * time.Second
			}
			if dl, ok := caseCtx.Deadline(); ok {
				remain := time.Until(dl) - 5*time.Second
				if remain > 0 && remain < ioTimeout {
					ioTimeout = remain
				}
			}
			_, err = measureForward(caseCtx, cfg, (*flagPayloadKiB)*1024, ioTimeout)
			if err != nil {
				// One retry for transient network / transport timeouts.
				if isTimeoutErr(err) {
					time.Sleep(250 * time.Millisecond)
					if _, err2 := measureForward(caseCtx, cfg, (*flagPayloadKiB)*1024, ioTimeout); err2 == nil {
						err = nil
					}
				}
				if err != nil {
					return fmt.Errorf("forward: %w", err)
				}
			}
			if err := verifyRTTParity(caseCtx, *flagHost, cc, cfg, ioTimeout); err != nil {
				return fmt.Errorf("rtt: %w", err)
			}

			if cc.httpmaskEnabled && strings.EqualFold(cc.mux, "on") {
				if err := smokeMux(caseCtx, cfg, fmt.Sprintf("127.0.0.1:%d", *flagEcho1), fmt.Sprintf("127.0.0.1:%d", *flagEcho2)); err != nil {
					return fmt.Errorf("mux: %w", err)
				}
			}

			return nil
		}()
		cancel()

		if err != nil {
			atomic.AddInt32(&failures, 1)
			fmt.Fprintf(os.Stderr, "FAIL #%d %s (port=%d): %v\n", i, c.String(), port, err)
			if *flagFailFast {
				os.Exit(1)
			}
			continue
		}
		if *flagVerbose {
			fmt.Fprintf(os.Stderr, "OK   #%d %s (port=%d)\n", i, c.String(), port)
		}

		// Avoid bursty dialing patterns against real servers.
		time.Sleep(25 * time.Millisecond)
	}

	ran := end - start
	if failures != 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d case(s) (%d run)\n", failures, ran)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "OK: %d case(s)\n", ran)

	if *flagKeyUpdate {
		if *flagKeyUpdateMiB <= 33 {
			fmt.Fprintf(os.Stderr, "SKIP keyupdate: keyupdate-mib=%d too small\n", *flagKeyUpdateMiB)
			return
		}
		fmt.Fprintf(os.Stderr, "KeyUpdate: running %d MiB transfer...\n", *flagKeyUpdateMiB)

		bigCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()

		tables, err := getTables(canonicalSeedKey(*flagKey), "prefer_entropy", "default")
		if err != nil {
			fmt.Fprintf(os.Stderr, "KeyUpdate: tables error: %v\n", err)
			os.Exit(1)
		}

		cfg := apis.DefaultConfig()
		// Run the large transfer over a CDN-friendly transport and the packed downlink codec to avoid
		// unreliable middleboxes on long raw-TCP sessions.
		cfg.ServerAddress = serverAddr(*flagHost, *flagPortEntropyNoRootPacked)
		cfg.Key = *flagKey
		cfg.AEADMethod = "chacha20-poly1305"
		cfg.EnablePureDownlink = false
		cfg.PaddingMin = 0
		cfg.PaddingMax = 0
		cfg.DisableHTTPMask = false
		cfg.HTTPMaskMode = "ws"
		cfg.HTTPMaskTLSEnabled = false
		cfg.HTTPMaskMultiplex = "off"
		cfg.HTTPMaskPathRoot = ""
		cfg.Table = tables[0]
		cfg.TargetAddress = fmt.Sprintf("127.0.0.1:%d", *flagEcho1)

		if err := smokeForwardHash(bigCtx, cfg, int64(*flagKeyUpdateMiB)*1024*1024); err != nil {
			fmt.Fprintf(os.Stderr, "KeyUpdate: FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "KeyUpdate: OK\n")
	}
}
