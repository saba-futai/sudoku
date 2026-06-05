package tests

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/apis"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/httpmask"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

const downlinkSourceChunkSize = 64 * 1024

var downlinkSourceChunk = makeDownlinkSourceChunk()

func makeDownlinkSourceChunk() []byte {
	chunk := make([]byte, downlinkSourceChunkSize)
	for i := range chunk {
		chunk[i] = byte(i)
	}
	return chunk
}

func BenchmarkDownlinkThroughputMatrix(b *testing.B) {
	bytesPerRun := int64(64 << 20)
	if v := strings.TrimSpace(os.Getenv("SUDOKU_DOWNLINK_BENCH_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			b.Fatalf("invalid SUDOKU_DOWNLINK_BENCH_BYTES=%q", v)
		}
		bytesPerRun = n
	}

	transports := []struct {
		name            string
		disableHTTPMask bool
		httpMaskMode    string
	}{
		{name: "httpmask_off", disableHTTPMask: true, httpMaskMode: "legacy"},
		{name: "httpmask_stream", httpMaskMode: "stream"},
		{name: "httpmask_ws", httpMaskMode: "ws"},
	}
	downlinks := []struct {
		name string
		pure bool
	}{
		{name: "pure", pure: true},
		{name: "packed", pure: false},
	}
	muxModes := []string{"off", "auto", "on"}

	for _, dl := range downlinks {
		for _, tr := range transports {
			for _, muxMode := range muxModes {
				name := fmt.Sprintf("%s/%s/mux_%s", dl.name, tr.name, muxMode)
				b.Run(name, func(b *testing.B) {
					benchmarkDownlinkThroughput(b, bytesPerRun, dl.pure, tr.disableHTTPMask, tr.httpMaskMode, muxMode)
				})
			}
		}
	}
}

func BenchmarkDownlinkThroughputConcurrentMatrix(b *testing.B) {
	connCount := 200
	if v := strings.TrimSpace(os.Getenv("SUDOKU_DOWNLINK_CONCURRENT_CONNS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			b.Fatalf("invalid SUDOKU_DOWNLINK_CONCURRENT_CONNS=%q", v)
		}
		connCount = n
	}

	bytesPerConn := int64(1 << 20)
	if v := strings.TrimSpace(os.Getenv("SUDOKU_DOWNLINK_CONCURRENT_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			b.Fatalf("invalid SUDOKU_DOWNLINK_CONCURRENT_BYTES=%q", v)
		}
		bytesPerConn = n
	}

	transports := []struct {
		name            string
		disableHTTPMask bool
		httpMaskMode    string
	}{
		{name: "httpmask_off", disableHTTPMask: true, httpMaskMode: "legacy"},
		{name: "httpmask_stream", httpMaskMode: "stream"},
		{name: "httpmask_ws", httpMaskMode: "ws"},
	}
	downlinks := []struct {
		name string
		pure bool
	}{
		{name: "pure", pure: true},
		{name: "packed", pure: false},
	}
	muxModes := []string{"off", "auto", "on"}

	for _, dl := range downlinks {
		for _, tr := range transports {
			for _, muxMode := range muxModes {
				name := fmt.Sprintf("%s/%s/mux_%s", dl.name, tr.name, muxMode)
				b.Run(name, func(b *testing.B) {
					benchmarkDownlinkThroughputConcurrent(b, connCount, bytesPerConn, dl.pure, tr.disableHTTPMask, tr.httpMaskMode, muxMode)
				})
			}
		}
	}
}

func benchmarkDownlinkThroughput(b *testing.B, bytesPerRun int64, pureDownlink bool, disableHTTPMask bool, httpMaskMode, muxMode string) {
	b.Helper()

	table := sudoku.NewTable("downlink-throughput-key", "prefer_ascii")
	key := "downlink-throughput-key"
	targetAddr, stopTarget := startDownlinkSourceServer(b, bytesPerRun)
	defer stopTarget()

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      pureDownlink,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         disableHTTPMask,
		HTTPMaskMode:            httpMaskMode,
		HTTPMaskMultiplex:       muxMode,
	}
	serverAddr, stopTunnel := startDownlinkTunnelServer(b, serverCfg)
	defer stopTunnel()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      serverAddr,
		TargetAddress:      targetAddr,
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: pureDownlink,
		DisableHTTPMask:    disableHTTPMask,
		HTTPMaskMode:       httpMaskMode,
		HTTPMaskMultiplex:  muxMode,
	}

	var muxClient *apis.MuxClient
	if muxMode == "on" {
		var err error
		muxClient, err = apis.NewMuxClient(clientCfg)
		if err != nil {
			b.Fatalf("NewMuxClient: %v", err)
		}
		defer muxClient.Close()
	}

	buf := make([]byte, 128*1024)
	b.SetBytes(bytesPerRun)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var (
			conn net.Conn
			err  error
		)
		if muxClient != nil {
			conn, err = muxClient.Dial(ctx, targetAddr)
		} else {
			conn, err = apis.Dial(ctx, clientCfg)
		}
		cancel()
		if err != nil {
			b.Fatalf("dial: %v", err)
		}

		n, copyErr := copyExactly(io.Discard, conn, buf, bytesPerRun)
		_ = conn.Close()
		if copyErr != nil {
			b.Fatalf("copy: %v", copyErr)
		}
		if n != bytesPerRun {
			b.Fatalf("downloaded %d bytes, want %d", n, bytesPerRun)
		}
	}
}

func benchmarkDownlinkThroughputConcurrent(b *testing.B, connCount int, bytesPerConn int64, pureDownlink bool, disableHTTPMask bool, httpMaskMode, muxMode string) {
	b.Helper()

	table := sudoku.NewTable("downlink-throughput-concurrent-key", "prefer_ascii")
	key := "downlink-throughput-concurrent-key"
	targetAddr, newSourceBatch, sourceDialer := newControlledDownlinkPipeSource(bytesPerConn)

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      pureDownlink,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         disableHTTPMask,
		HTTPMaskMode:            httpMaskMode,
		HTTPMaskMultiplex:       muxMode,
	}
	serverAddr, stopTunnel := startDownlinkTunnelServerWithDialer(b, serverCfg, sourceDialer)
	defer stopTunnel()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      serverAddr,
		TargetAddress:      targetAddr,
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: pureDownlink,
		DisableHTTPMask:    disableHTTPMask,
		HTTPMaskMode:       httpMaskMode,
		HTTPMaskMultiplex:  muxMode,
	}

	var muxClient *apis.MuxClient
	if muxMode == "on" {
		var err error
		muxClient, err = apis.NewMuxClient(clientCfg)
		if err != nil {
			b.Fatalf("NewMuxClient: %v", err)
		}
		defer muxClient.Close()
	}

	totalBytes := int64(connCount) * bytesPerConn
	b.SetBytes(totalBytes)
	b.ReportMetric(float64(connCount), "conns")
	b.ReportMetric(float64(bytesPerConn), "bytes/conn")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		sourceBatch := newSourceBatch(connCount)
		conns, err := openConcurrentDownlinkConns(clientCfg, muxClient, targetAddr, connCount)
		if err != nil {
			closeDownlinkConns(conns)
			b.Fatalf("open concurrent downlink conns: %v", err)
		}
		if err := sourceBatch.waitReady(60 * time.Second); err != nil {
			closeDownlinkConns(conns)
			b.Fatalf("wait source conns: %v", err)
		}

		b.StartTimer()
		err = runConcurrentDownlinkBatch(conns, bytesPerConn, sourceBatch.release)
		b.StopTimer()
		closeDownlinkConns(conns)
		if err != nil {
			b.Fatalf("concurrent downlink: %v; %s", err, sourceBatch.stats())
		}
	}
}

func openConcurrentDownlinkConns(clientCfg *apis.ProtocolConfig, muxClient *apis.MuxClient, targetAddr string, connCount int) ([]net.Conn, error) {
	conns := make([]net.Conn, 0, connCount)
	for i := 0; i < connCount; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		var (
			conn net.Conn
			err  error
		)
		if muxClient != nil {
			conn, err = muxClient.Dial(ctx, targetAddr)
		} else {
			conn, err = apis.Dial(ctx, clientCfg)
		}
		cancel()
		if err != nil {
			return conns, err
		}
		conns = append(conns, conn)
	}
	return conns, nil
}

func closeDownlinkConns(conns []net.Conn) {
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func runConcurrentDownlinkBatch(conns []net.Conn, bytesPerConn int64, releaseSource func()) error {
	errCh := make(chan error, len(conns))
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(len(conns))
	for idx, conn := range conns {
		go func(idx int, conn net.Conn) {
			ready.Done()
			<-start
			errCh <- runOnePreparedDownlink(idx, conn, bytesPerConn)
		}(idx, conn)
	}
	ready.Wait()
	close(start)
	if releaseSource != nil {
		releaseSource()
	}

	var firstErr error
	for i := 0; i < len(conns); i++ {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func runOnePreparedDownlink(idx int, conn net.Conn, bytesPerConn int64) error {
	buf := make([]byte, 32*1024)
	n, err := copyExactly(io.Discard, conn, buf, bytesPerConn)
	if err != nil {
		return fmt.Errorf("conn %d read after %d/%d bytes: %w", idx, n, bytesPerConn, err)
	}
	if n != bytesPerConn {
		return fmt.Errorf("conn %d downloaded %d bytes, want %d", idx, n, bytesPerConn)
	}
	return nil
}

func copyExactly(dst io.Writer, src io.Reader, buf []byte, want int64) (int64, error) {
	var copied int64
	for copied < want {
		remaining := want - copied
		readBuf := buf
		if remaining < int64(len(readBuf)) {
			readBuf = readBuf[:remaining]
		}
		nr, er := src.Read(readBuf)
		if nr > 0 {
			nw, ew := dst.Write(readBuf[:nr])
			copied += int64(nw)
			if ew != nil {
				return copied, ew
			}
			if nw != nr {
				return copied, io.ErrShortWrite
			}
			if copied >= want {
				return copied, nil
			}
		}
		if er != nil {
			return copied, er
		}
	}
	return copied, nil
}

func startDownlinkSourceServer(tb testing.TB, bytesPerConn int64) (addr string, stop func()) {
	tb.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen source: %v", err)
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
				_, _ = writeDownlinkSourcePayload(conn, downlinkSourceChunk, bytesPerConn)
				finishDownlinkSourceConn(conn)
			}(c)
		}
	}()

	return ln.Addr().String(), func() {
		_ = ln.Close()
		<-done
	}
}

type downlinkSourceBatch struct {
	want int

	mu        sync.Mutex
	ready     int
	done      int
	written   int64
	firstErr  error
	readyCh   chan struct{}
	releaseCh chan struct{}
}

func newDownlinkSourceBatch(want int) *downlinkSourceBatch {
	return &downlinkSourceBatch{
		want:      want,
		readyCh:   make(chan struct{}),
		releaseCh: make(chan struct{}),
	}
}

func (b *downlinkSourceBatch) markReady() {
	b.mu.Lock()
	b.ready++
	if b.ready == b.want {
		close(b.readyCh)
	}
	b.mu.Unlock()
}

func (b *downlinkSourceBatch) waitReady(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-b.readyCh:
		return nil
	case <-timer.C:
		b.mu.Lock()
		ready := b.ready
		b.mu.Unlock()
		return fmt.Errorf("only %d/%d source conns ready", ready, b.want)
	}
}

func (b *downlinkSourceBatch) release() {
	close(b.releaseCh)
}

func (b *downlinkSourceBatch) markDone(written int64, err error) {
	b.mu.Lock()
	b.done++
	b.written += written
	if err != nil && b.firstErr == nil {
		b.firstErr = err
	}
	b.mu.Unlock()
}

func (b *downlinkSourceBatch) stats() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.firstErr != nil {
		return fmt.Sprintf("source ready=%d/%d done=%d written=%d firstErr=%v", b.ready, b.want, b.done, b.written, b.firstErr)
	}
	return fmt.Sprintf("source ready=%d/%d done=%d written=%d", b.ready, b.want, b.done, b.written)
}

func newControlledDownlinkPipeSource(bytesPerConn int64) (targetAddr string, newBatch func(int) *downlinkSourceBatch, dialer func(string) (net.Conn, error)) {
	var batchMu sync.Mutex
	var batch *downlinkSourceBatch
	newBatch = func(want int) *downlinkSourceBatch {
		batchMu.Lock()
		defer batchMu.Unlock()
		batch = newDownlinkSourceBatch(want)
		return batch
	}

	dialer = func(string) (net.Conn, error) {
		target, source := net.Pipe()
		batchMu.Lock()
		connBatch := batch
		batchMu.Unlock()
		go serveControlledDownlinkPipeSource(source, connBatch, bytesPerConn)
		return target, nil
	}

	return "127.0.0.1:1", newBatch, dialer
}

func serveControlledDownlinkPipeSource(conn net.Conn, batch *downlinkSourceBatch, bytesPerConn int64) {
	defer conn.Close()
	if batch != nil {
		batch.markReady()
		<-batch.releaseCh
	}
	written, err := writeDownlinkSourcePayload(conn, downlinkSourceChunk, bytesPerConn)
	if batch != nil {
		batch.markDone(written, err)
	}
}

func writeDownlinkSourcePayload(conn net.Conn, chunk []byte, bytesPerConn int64) (int64, error) {
	remaining := bytesPerConn
	var written int64
	for remaining > 0 {
		n := int64(len(chunk))
		if remaining < n {
			n = remaining
		}
		nw, err := writeFullSourceChunk(conn, chunk[:n])
		written += int64(nw)
		if err != nil {
			return written, err
		}
		remaining -= n
	}
	return written, nil
}

func writeFullSourceChunk(conn net.Conn, chunk []byte) (int, error) {
	var written int
	for len(chunk) > 0 {
		n, err := conn.Write(chunk)
		if n > 0 {
			written += n
			chunk = chunk[n:]
		}
		if err == nil {
			continue
		}
		return written, err
	}
	return written, nil
}

func finishDownlinkSourceConn(conn net.Conn) {
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.SetReadDeadline(time.Time{})
}

func startDownlinkTunnelServer(tb testing.TB, cfg *apis.ProtocolConfig) (addr string, stop func()) {
	return startDownlinkTunnelServerWithDialer(tb, cfg, func(targetAddr string) (net.Conn, error) {
		return net.DialTimeout("tcp", targetAddr, 10*time.Second)
	})
}

func startDownlinkTunnelServerWithDialer(tb testing.TB, cfg *apis.ProtocolConfig, dialTarget func(string) (net.Conn, error)) (addr string, stop func()) {
	tb.Helper()
	if dialTarget == nil {
		tb.Fatalf("nil tunnel target dialer")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen tunnel: %v", err)
	}

	var hm *httpmask.TunnelServer
	if cfg != nil && !cfg.DisableHTTPMask {
		hm = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
			Mode:     cfg.HTTPMaskMode,
			PathRoot: cfg.HTTPMaskPathRoot,
			AuthKey:  cfg.Key,
			EarlyHandshake: tunnel.NewHTTPMaskServerEarlyHandshake(tunnel.EarlyCodecConfig{
				PSK:                cfg.Key,
				AEAD:               cfg.AEADMethod,
				EnablePureDownlink: cfg.EnablePureDownlink,
				PaddingMin:         cfg.PaddingMin,
				PaddingMax:         cfg.PaddingMax,
			}, []*sudoku.Table{cfg.Table}, nil),
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := ln.Accept()
			if err != nil {
				return
			}
			go handleDownlinkTunnelConn(raw, cfg, hm, dialTarget)
		}
	}()

	return ln.Addr().String(), func() {
		_ = ln.Close()
		<-done
	}
}

func handleDownlinkTunnelConn(raw net.Conn, cfg *apis.ProtocolConfig, hm *httpmask.TunnelServer, dialTarget func(string) (net.Conn, error)) {
	defer raw.Close()

	conn, session, targetAddr, err := acceptDownlinkTunnelSession(raw, cfg, hm)
	if err != nil || conn == nil {
		return
	}

	switch session {
	case apis.SessionForward:
		target, err := dialTarget(targetAddr)
		if err != nil {
			_ = conn.Close()
			return
		}
		connutil.PipeConn(conn, target)
	case apis.SessionMux:
		_ = tunnel.HandleMuxWithDialer(conn, nil, dialTarget)
	default:
		_ = conn.Close()
	}
}

func acceptDownlinkTunnelSession(raw net.Conn, cfg *apis.ProtocolConfig, hm *httpmask.TunnelServer) (net.Conn, apis.SessionKind, string, error) {
	if hm == nil {
		conn, session, targetAddr, _, _, err := apis.ServerHandshakeSessionAutoWithUserHash(raw, cfg)
		return conn, session, targetAddr, err
	}

	res, c, err := hm.HandleConn(raw)
	if err != nil {
		return nil, apis.SessionForward, "", err
	}
	switch res {
	case httpmask.HandleDone:
		return nil, apis.SessionForward, "", nil
	case httpmask.HandlePassThrough:
		conn, session, targetAddr, _, _, err := apis.ServerHandshakeSessionAutoWithUserHash(c, cfg)
		return conn, session, targetAddr, err
	case httpmask.HandleStartTunnel:
		inner := *cfg
		inner.DisableHTTPMask = true
		conn, session, targetAddr, _, _, err := apis.ServerHandshakeSessionAutoWithUserHash(c, &inner)
		return conn, session, targetAddr, err
	default:
		return nil, apis.SessionForward, "", nil
	}
}
