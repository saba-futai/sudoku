package tests

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net"
	"net/http"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/app"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// Helpers to bootstrap test infra.
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func getFreePorts(count int) ([]int, error) {
	var listeners []net.Listener
	var ports []int
	for i := 0; i < count; i++ {
		l, err := net.Listen("tcp", "localhost:0")
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, err
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}
	for _, l := range listeners {
		l.Close()
	}
	return ports, nil
}

func pickNonLoopbackIPv4() net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if !ip.IsGlobalUnicast() {
			continue
		}
		return ip
	}
	return nil
}

func isUDPLocalRoutingError(err error) bool {
	if err == nil {
		return false
	}
	// Some OSes (notably Windows) reject connecting a UDP socket from a loopback
	// source to a non-loopback destination.
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		// Windows Winsock errors.
		case syscall.Errno(10051), syscall.Errno(10065), syscall.Errno(10049):
			return true
		// Conventional POSIX-ish errors.
		case syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.EADDRNOTAVAIL:
			return true
		}
	}
	return false
}

func startEchoServer(port int) error {
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return err
	}
	go func() {
		defer l.Close()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()
	return nil
}

func startWebServer(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello Fallback"))
	})
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	go func() {
		server.ListenAndServe()
	}()
	return nil
}

func startUDPEchoServer() (*net.UDPConn, int, error) {
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, 0, err
	}

	go func() {
		buf := make([]byte, 65535)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			conn.WriteToUDP(buf[:n], src)
		}
	}()

	port := conn.LocalAddr().(*net.UDPAddr).Port
	return conn, port, nil
}

// Traffic stats + analysis helpers.
type TrafficStats struct {
	TotalBytes   int64
	AsciiCount   int64
	HammingTotal int64
}

func (s TrafficStats) AsciiRatio() float64 {
	if s.TotalBytes == 0 {
		return 0
	}
	return float64(s.AsciiCount) / float64(s.TotalBytes)
}

func (s TrafficStats) AvgHammingWeight() float64 {
	if s.TotalBytes == 0 {
		return 0
	}
	return float64(s.HammingTotal) / float64(s.TotalBytes)
}

func analyzeTraffic(data []byte) TrafficStats {
	var stats TrafficStats
	stats.TotalBytes = int64(len(data))
	for _, b := range data {
		// "prefer_ascii" intentionally maps the single non-printable marker (0x7F) to '\n'.
		if b == '\n' || (b >= 32 && b <= 126) {
			stats.AsciiCount++
		}
		stats.HammingTotal += int64(bits.OnesCount8(b))
	}
	return stats
}

type closeWriter interface {
	CloseWrite() error
}

type closeReader interface {
	CloseRead() error
}

func writeFullConn(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if n > 0 {
			data = data[n:]
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

func copyWithCapture(dst, src net.Conn, ch chan []byte) error {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if ch != nil {
				data := make([]byte, n)
				copy(data, buf[:n])
				select {
				case ch <- data:
				default:
				}
			}
			if writeErr := writeFullConn(dst, buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func pipeConnWithCapture(a, b net.Conn, upChan, downChan chan []byte) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_ = copyWithCapture(b, a, upChan)
		if cw, ok := b.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		if cr, ok := a.(closeReader); ok {
			_ = cr.CloseRead()
		}
	}()

	go func() {
		defer wg.Done()
		_ = copyWithCapture(a, b, downChan)
		if cw, ok := a.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		if cr, ok := b.(closeReader); ok {
			_ = cr.CloseRead()
		}
	}()

	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

// Middleman utilities to observe traffic.
func startMiddleman(listenPort, targetPort int, protocol string, analysisChan chan []byte) error {
	targetAddr := fmt.Sprintf("127.0.0.1:%d", targetPort)

	if protocol == "udp" {
		lAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf(":%d", listenPort))
		if err != nil {
			return err
		}
		conn, err := net.ListenUDP("udp", lAddr)
		if err != nil {
			return err
		}

		sessions := make(map[string]*net.UDPConn)
		var mu sync.Mutex

		go func() {
			buf := make([]byte, 65535)
			for {
				n, clientAddr, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				data := make([]byte, n)
				copy(data, buf[:n])

				select {
				case analysisChan <- data:
				default:
				}

				mu.Lock()
				proxyConn, ok := sessions[clientAddr.String()]
				if !ok {
					rAddr, _ := net.ResolveUDPAddr("udp", targetAddr)
					proxyConn, err = net.DialUDP("udp", nil, rAddr)
					if err != nil {
						mu.Unlock()
						continue
					}
					sessions[clientAddr.String()] = proxyConn

					go func(pc *net.UDPConn, ca *net.UDPAddr) {
						defer pc.Close()
						b := make([]byte, 65535)
						for {
							nn, _, err := pc.ReadFromUDP(b)
							if err != nil {
								return
							}
							conn.WriteToUDP(b[:nn], ca)
						}
					}(proxyConn, clientAddr)
				}
				mu.Unlock()

				proxyConn.Write(data)
			}
		}()
		return nil
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		return err
	}

	go func() {
		for {
			clientConn, err := l.Accept()
			if err != nil {
				return
			}
			go func(src net.Conn) {
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					_ = src.Close()
					return
				}
				pipeConnWithCapture(src, dst, analysisChan, nil)
			}(clientConn)
		}
	}()
	return nil
}

func startDualMiddleman(listenPort, targetPort int, upChan, downChan chan []byte) error {
	targetAddr := fmt.Sprintf("127.0.0.1:%d", targetPort)
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		return err
	}

	go func() {
		for {
			clientConn, err := l.Accept()
			if err != nil {
				return
			}
			go func(src net.Conn) {
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					_ = src.Close()
					return
				}
				pipeConnWithCapture(src, dst, upChan, downChan)
			}(clientConn)
		}
	}()
	return nil
}

// SOCKS helpers for UoT tests.
func performUDPAssociate(t *testing.T, serverAddr string, dialer *net.Dialer) (net.Conn, *net.UDPAddr) {
	t.Helper()
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	ctrl, err := dialer.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("Failed to connect to client control port: %v", err)
	}

	if _, err := ctrl.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("Failed to write socks greeting: %v", err)
	}
	methodResp := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, methodResp); err != nil {
		t.Fatalf("Failed to read socks method response: %v", err)
	}
	if methodResp[1] != 0x00 {
		t.Fatalf("Unexpected method selection: %v", methodResp[1])
	}

	req := []byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := ctrl.Write(req); err != nil {
		t.Fatalf("Failed to write UDP associate: %v", err)
	}

	header := make([]byte, 3)
	if _, err := io.ReadFull(ctrl, header); err != nil {
		t.Fatalf("Failed to read UDP associate reply header: %v", err)
	}
	if header[0] != 0x05 || header[2] != 0x00 {
		t.Fatalf("invalid UDP associate reply header: %v", header)
	}
	if header[1] != 0x00 {
		t.Fatalf("UDP associate rejected: %v", header[1])
	}

	addrStr, _, _, err := protocol.ReadAddress(ctrl)
	if err != nil {
		t.Fatalf("Failed to read UDP associate addr: %v", err)
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addrStr)
	if err != nil {
		t.Fatalf("Failed to resolve UDP relay addr %q: %v", addrStr, err)
	}

	// Some SOCKS5 servers return 0.0.0.0/:: to indicate "use the TCP endpoint host".
	if udpAddr.IP == nil || udpAddr.IP.IsUnspecified() {
		host, _, err := net.SplitHostPort(serverAddr)
		if err != nil {
			t.Fatalf("invalid server addr %q: %v", serverAddr, err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			ips, lookupErr := net.DefaultResolver.LookupIP(context.Background(), "ip", host)
			if lookupErr != nil || len(ips) == 0 {
				t.Fatalf("resolve server host %q failed: %v", host, lookupErr)
			}
			ip = ips[0]
		}
		udpAddr.IP = ip
	}

	return ctrl, udpAddr
}

func buildSocksUDPRequest(t *testing.T, addr string, payload []byte) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00, 0x00, 0x00})
	if err := protocol.WriteAddress(buf, addr); err != nil {
		t.Fatalf("failed to encode addr %s: %v", addr, err)
	}
	buf.Write(payload)
	return buf.Bytes()
}

func parseSocksUDPResponse(t *testing.T, packet []byte) (string, []byte) {
	t.Helper()
	if len(packet) < 4 {
		t.Fatalf("response too short: %d", len(packet))
	}
	reader := bytes.NewReader(packet[3:])
	addr, _, _, err := protocol.ReadAddress(reader)
	if err != nil {
		t.Fatalf("failed to parse response address: %v", err)
	}
	data := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, data); err != nil {
		t.Fatalf("failed to read response payload: %v", err)
	}
	return addr, data
}

// Start Sudoku endpoints.
func startSudokuServer(t testing.TB, cfg *config.Config) {
	t.Helper()
	table, err := sudoku.NewTableWithCustom(cfg.Key, cfg.ASCII, cfg.CustomTable)
	if err != nil {
		t.Fatalf("build table: %v", err)
	}
	go app.RunServer(cfg, []*sudoku.Table{table})
	waitForPort(t, cfg.LocalPort)
}

func startSudokuClient(t testing.TB, cfg *config.Config) {
	t.Helper()
	table, err := sudoku.NewTableWithCustom(cfg.Key, cfg.ASCII, cfg.CustomTable)
	if err != nil {
		t.Fatalf("build table: %v", err)
	}
	go app.RunClient(cfg, []*sudoku.Table{table})
	waitForPort(t, cfg.LocalPort)
}

func waitForPort(t testing.TB, port int) {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("port not ready: %s", addr)
}

// Shared helpers for tests.
func sendHTTPConnect(t *testing.T, conn net.Conn, target string) {
	t.Helper()
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("proxy handshake write failed: %v", err)
	}
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || !contains(buf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("proxy handshake failed: %v", string(buf[:n]))
	}
}

func collectTraffic(ch chan []byte) TrafficStats {
	var stats TrafficStats
	count := len(ch)
	for i := 0; i < count; i++ {
		s := analyzeTraffic(<-ch)
		stats.TotalBytes += s.TotalBytes
		stats.AsciiCount += s.AsciiCount
		stats.HammingTotal += s.HammingTotal
	}
	return stats
}

func runTCPTransfer(t *testing.T, asciiMode string, pureDownlink bool, key string, payload []byte, custom string) (TrafficStats, TrafficStats) {
	t.Helper()

	ports, _ := getFreePorts(4)
	echoPort := ports[0]
	serverPort := ports[1]
	middlemanPort := ports[2]
	clientPort := ports[3]

	startEchoServer(echoPort)

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                key,
		AEAD:               "aes-128-gcm",
		ASCII:              asciiMode,
		CustomTable:        custom,
		EnablePureDownlink: pureDownlink,
		FallbackAddr:       "127.0.0.1:80",
		PaddingMin:         8,
		PaddingMax:         18,
	}
	startSudokuServer(t, serverCfg)

	upChan := make(chan []byte, 256)
	downChan := make(chan []byte, 256)
	startDualMiddleman(middlemanPort, serverPort, upChan, downChan)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:                key,
		AEAD:               "aes-128-gcm",
		ASCII:              asciiMode,
		CustomTable:        custom,
		EnablePureDownlink: pureDownlink,
		ProxyMode:          "global",
	}
	startSudokuClient(t, clientCfg)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	sendHTTPConnect(t, conn, target)

	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload failed: %v", err)
	}
	echoBuf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echoBuf); err != nil {
		t.Fatalf("read echo failed: %v", err)
	}
	if !bytes.Equal(echoBuf, payload) {
		t.Fatalf("echo mismatch")
	}

	time.Sleep(300 * time.Millisecond)
	return collectTraffic(upChan), collectTraffic(downChan)
}

// === Tests ===

func TestDownlinkASCIIAndPacked(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 8192) // ~128KB

	upPure, downPure := runTCPTransfer(t, "prefer_ascii", true, "testkey-ascii", payload, "")
	upPacked, downPacked := runTCPTransfer(t, "prefer_ascii", false, "testkey-ascii", payload, "")

	if downPure.TotalBytes == 0 || downPacked.TotalBytes == 0 {
		t.Fatalf("no traffic captured")
	}
	if downPacked.TotalBytes >= downPure.TotalBytes {
		t.Errorf("packed downlink did not reduce bytes: pure=%d packed=%d", downPure.TotalBytes, downPacked.TotalBytes)
	}
	if float64(downPacked.TotalBytes) > float64(downPure.TotalBytes)*0.9 {
		t.Errorf("bandwidth gain too small: pure=%d packed=%d", downPure.TotalBytes, downPacked.TotalBytes)
	}
	if downPure.AsciiRatio() < 0.9 || downPacked.AsciiRatio() < 0.7 {
		t.Errorf("ascii ratios too low: pure=%.2f packed=%.2f", downPure.AsciiRatio(), downPacked.AsciiRatio())
	}
	if upPure.AsciiRatio() < 0.9 {
		t.Errorf("uplink ascii ratio too low: %.2f", upPure.AsciiRatio())
	}
	if upPacked.AsciiRatio() < 0.9 {
		t.Errorf("uplink ascii ratio too low: %.2f", upPacked.AsciiRatio())
	}
}

func TestDownlinkEntropyModes(t *testing.T) {
	payload := bytes.Repeat([]byte("entropy-test-payload"), 6000)
	upPure, downPure := runTCPTransfer(t, "prefer_entropy", true, "entropy-key", payload, "")
	upPacked, downPacked := runTCPTransfer(t, "prefer_entropy", false, "entropy-key", payload, "")

	if downPacked.TotalBytes >= downPure.TotalBytes {
		t.Errorf("packed entropy downlink did not shrink traffic: pure=%d packed=%d", downPure.TotalBytes, downPacked.TotalBytes)
	}
	if downPacked.AsciiRatio() < 0.5 || downPure.AsciiRatio() < 0.5 {
		t.Errorf("entropy ascii ratios too low: pure=%.2f packed=%.2f", downPure.AsciiRatio(), downPacked.AsciiRatio())
	}
	if downPacked.AvgHammingWeight() < 2.4 || downPacked.AvgHammingWeight() > 3.6 {
		t.Errorf("entropy packed hamming unexpected: %.2f", downPacked.AvgHammingWeight())
	}
	if downPure.AvgHammingWeight() < 2.4 || downPure.AvgHammingWeight() > 3.6 {
		t.Errorf("entropy pure hamming unexpected: %.2f", downPure.AvgHammingWeight())
	}
	if upPure.AvgHammingWeight() < 2.4 || upPacked.AvgHammingWeight() < 2.4 {
		t.Errorf("uplink entropy hamming too low: pure=%.2f packed=%.2f", upPure.AvgHammingWeight(), upPacked.AvgHammingWeight())
	}
}

func TestCustomTableTraffic(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAA, 0x55, 0xF0, 0x0F}, 4096)
	customPattern := "xpxvvpvv"

	upPure, downPure := runTCPTransfer(t, "prefer_entropy", true, "custom-key", payload, customPattern)
	upPacked, downPacked := runTCPTransfer(t, "prefer_entropy", false, "custom-key", payload, customPattern)

	if downPure.TotalBytes == 0 || downPacked.TotalBytes == 0 {
		t.Fatalf("no traffic captured for custom table")
	}
	if downPure.AvgHammingWeight() < 4.6 || downPacked.AvgHammingWeight() < 4.6 {
		t.Fatalf("custom table downlink hamming too low: pure=%.2f packed=%.2f", downPure.AvgHammingWeight(), downPacked.AvgHammingWeight())
	}
	if upPure.AvgHammingWeight() < 4.6 || upPacked.AvgHammingWeight() < 4.6 {
		t.Fatalf("custom table uplink hamming too low: pure=%.2f packed=%.2f", upPure.AvgHammingWeight(), upPacked.AvgHammingWeight())
	}
}

func TestUDPOverTCPWithPackedDownlink(t *testing.T) {
	ports, _ := getFreePorts(3)
	serverPort := ports[0]
	middlemanPort := ports[1]
	clientPort := ports[2]

	udpConn, udpPortReal, err := startUDPEchoServer()
	if err != nil {
		t.Fatalf("failed to start udp echo: %v", err)
	}
	defer udpConn.Close()

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                "testkey",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		EnablePureDownlink: false,
		FallbackAddr:       "127.0.0.1:80",
	}
	startSudokuServer(t, serverCfg)
	startDualMiddleman(middlemanPort, serverPort, nil, nil)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:                "testkey",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		EnablePureDownlink: false,
		ProxyMode:          "global",
	}
	startSudokuClient(t, clientCfg)

	ctrlConn, udpRelay := performUDPAssociate(t, fmt.Sprintf("127.0.0.1:%d", clientPort), nil)
	defer ctrlConn.Close()

	relayConn, err := net.DialUDP("udp", nil, udpRelay)
	if err != nil {
		t.Fatalf("failed to dial udp relay: %v", err)
	}
	defer relayConn.Close()

	targetAddr := fmt.Sprintf("127.0.0.1:%d", udpPortReal)
	payload := bytes.Repeat([]byte{0xAB}, 2048)

	packet := buildSocksUDPRequest(t, targetAddr, payload)
	if _, err := relayConn.Write(packet); err != nil {
		t.Fatalf("failed to send udp packet: %v", err)
	}

	respBuf := make([]byte, len(payload)+64)
	n, err := relayConn.Read(respBuf)
	if err != nil {
		t.Fatalf("failed to read udp response: %v", err)
	}

	addr, data := parseSocksUDPResponse(t, respBuf[:n])
	if addr != targetAddr {
		t.Fatalf("unexpected response addr: %s", addr)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("unexpected udp payload size=%d", len(data))
	}
}

func TestUDPOverTCP_UDPAssociate_RemoteAddressAndIPFilter(t *testing.T) {
	ports, _ := getFreePorts(3)
	serverPort := ports[0]
	middlemanPort := ports[1]
	clientPort := ports[2]

	udpConn, udpPortReal, err := startUDPEchoServer()
	if err != nil {
		t.Fatalf("failed to start udp echo: %v", err)
	}
	defer udpConn.Close()

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                "testkey",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		FallbackAddr:       "127.0.0.1:80",
	}
	startSudokuServer(t, serverCfg)
	startDualMiddleman(middlemanPort, serverPort, nil, nil)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:                "testkey",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		ProxyMode:          "global",
	}
	startSudokuClient(t, clientCfg)

	// Connect via a non-loopback local interface so the UDP relay must not be stuck on 127.0.0.1.
	hostIP := pickNonLoopbackIPv4()
	if hostIP == nil {
		t.Skip("no non-loopback IPv4 address found")
	}
	serverAddr := net.JoinHostPort(hostIP.String(), fmt.Sprintf("%d", clientPort))
	ctrlConn, udpRelay := performUDPAssociate(t, serverAddr, nil)
	defer ctrlConn.Close()

	if got := udpRelay.IP.String(); got != hostIP.String() {
		t.Fatalf("unexpected udp relay ip: got %s want %s", got, hostIP.String())
	}

	targetAddr := fmt.Sprintf("127.0.0.1:%d", udpPortReal)

	// Allowed client IP (hostIP) should work.
	relayConn, err := net.DialUDP("udp", &net.UDPAddr{IP: hostIP, Port: 0}, udpRelay)
	if err != nil {
		t.Fatalf("failed to dial udp relay: %v", err)
	}
	defer relayConn.Close()

	payload := bytes.Repeat([]byte{0xCD}, 1024)
	packet := buildSocksUDPRequest(t, targetAddr, payload)
	if _, err := relayConn.Write(packet); err != nil {
		t.Fatalf("failed to send udp packet: %v", err)
	}

	respBuf := make([]byte, len(payload)+64)
	_ = relayConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := relayConn.Read(respBuf)
	if err != nil {
		t.Fatalf("failed to read udp response: %v", err)
	}
	addr, data := parseSocksUDPResponse(t, respBuf[:n])
	if addr != targetAddr {
		t.Fatalf("unexpected response addr: %s", addr)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("unexpected udp payload size=%d", len(data))
	}

	// Boundary: FRAG!=0 must be ignored, but must not break the session.
	{
		bad := &bytes.Buffer{}
		bad.Write([]byte{0x00, 0x00, 0x01}) // FRAG=1 (unsupported)
		if err := protocol.WriteAddress(bad, targetAddr); err != nil {
			t.Fatalf("failed to encode addr: %v", err)
		}
		bad.Write([]byte("bad-frag"))
		_, _ = relayConn.Write(bad.Bytes())
		_ = relayConn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		if _, err := relayConn.Read(make([]byte, 1024)); err == nil {
			t.Fatalf("expected no response for fragmented packet")
		}

		packet2 := buildSocksUDPRequest(t, targetAddr, []byte("ok"))
		if _, err := relayConn.Write(packet2); err != nil {
			t.Fatalf("failed to send follow-up packet: %v", err)
		}
		_ = relayConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := relayConn.Read(respBuf); err != nil {
			t.Fatalf("failed to read follow-up response: %v", err)
		}
	}

	// A different source IP must be ignored (no open relay / no hijack).
	badConn, err := net.DialUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0}, udpRelay)
	if err != nil {
		if isUDPLocalRoutingError(err) {
			t.Logf("skipping bad-source-ip UDP relay check due to platform routing restrictions: %v", err)
			return
		}
		t.Fatalf("failed to dial udp relay from bad ip: %v", err)
	}
	defer badConn.Close()
	_, _ = badConn.Write(packet)
	_ = badConn.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	if _, err := badConn.Read(make([]byte, 1024)); err == nil {
		t.Fatalf("expected bad udp client to be ignored, but got a response")
	}
}

func TestUDPOverTCP_Stress_ManyDatagrams(t *testing.T) {
	ports, _ := getFreePorts(3)
	serverPort := ports[0]
	middlemanPort := ports[1]
	clientPort := ports[2]

	udpConn, udpPortReal, err := startUDPEchoServer()
	if err != nil {
		t.Fatalf("failed to start udp echo: %v", err)
	}
	defer udpConn.Close()

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                "stress-key",
		AEAD:               "chacha20-poly1305",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: true,
		FallbackAddr:       "127.0.0.1:80",
		PaddingMin:         5,
		PaddingMax:         20,
	}
	startSudokuServer(t, serverCfg)
	startDualMiddleman(middlemanPort, serverPort, nil, nil)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:                "stress-key",
		AEAD:               "chacha20-poly1305",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: true,
		ProxyMode:          "global",
		PaddingMin:         5,
		PaddingMax:         20,
	}
	startSudokuClient(t, clientCfg)

	ctrlConn, udpRelay := performUDPAssociate(t, fmt.Sprintf("127.0.0.1:%d", clientPort), nil)
	defer ctrlConn.Close()

	relayConn, err := net.DialUDP("udp", nil, udpRelay)
	if err != nil {
		t.Fatalf("failed to dial udp relay: %v", err)
	}
	defer relayConn.Close()

	targetAddr := fmt.Sprintf("127.0.0.1:%d", udpPortReal)
	respBuf := make([]byte, 70*1024)

	// Exercise a lot of datagrams with varying sizes.
	for i := 0; i < 200; i++ {
		size := 32 + (i % 8192)
		payload := bytes.Repeat([]byte{byte(i)}, size)
		packet := buildSocksUDPRequest(t, targetAddr, payload)
		if _, err := relayConn.Write(packet); err != nil {
			t.Fatalf("write #%d failed: %v", i, err)
		}

		_ = relayConn.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, err := relayConn.Read(respBuf)
		if err != nil {
			t.Fatalf("read #%d failed: %v", i, err)
		}
		addr, data := parseSocksUDPResponse(t, respBuf[:n])
		if addr != targetAddr {
			t.Fatalf("resp #%d addr mismatch: %s", i, addr)
		}
		if !bytes.Equal(data, payload) {
			t.Fatalf("resp #%d payload mismatch: got=%d want=%d", i, len(data), len(payload))
		}
	}
}

func TestFallback(t *testing.T) {
	ports, _ := getFreePorts(2)
	serverPort := ports[0]
	webPort := ports[1]

	startWebServer(webPort)

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                "testkey",
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_entropy",
		EnablePureDownlink: true,
		FallbackAddr:       fmt.Sprintf("127.0.0.1:%d", webPort),
	}
	startSudokuServer(t, serverCfg)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", serverPort))
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello Fallback" {
		t.Errorf("Expected 'Hello Fallback', got '%s'", string(body))
	}
}

func TestConcurrentPackedSessions(t *testing.T) {
	ports, _ := getFreePorts(3)
	serverPort := ports[0]
	middlemanPort := ports[1]
	clientPort := ports[2]

	echoPort, _ := getFreePort()
	startEchoServer(echoPort)

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                "concurrent-key",
		AEAD:               "chacha20-poly1305",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: false,
		FallbackAddr:       fmt.Sprintf("127.0.0.1:%d", echoPort),
		PaddingMin:         5,
		PaddingMax:         20,
	}
	startSudokuServer(t, serverCfg)
	startDualMiddleman(middlemanPort, serverPort, nil, nil)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:                "concurrent-key",
		AEAD:               "chacha20-poly1305",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: false,
		ProxyMode:          "global",
	}
	startSudokuClient(t, clientCfg)

	var wg sync.WaitGroup
	conns := 16
	for i := 0; i < conns; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
			if err != nil {
				t.Errorf("dial client %d failed: %v", id, err)
				return
			}
			defer conn.Close()
			target := fmt.Sprintf("127.0.0.1:%d", echoPort)
			sendHTTPConnect(t, conn, target)
			msg := []byte(fmt.Sprintf("hello-%d-%d", id, time.Now().UnixNano()))
			if _, err := conn.Write(msg); err != nil {
				t.Errorf("write %d failed: %v", id, err)
				return
			}
			resp := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, resp); err != nil {
				t.Errorf("read %d failed: %v", id, err)
				return
			}
			if !bytes.Equal(resp, msg) {
				t.Errorf("echo mismatch %d", id)
			}
		}(i)
	}
	wg.Wait()
}

func TestEd25519KeyInterop(t *testing.T) {
	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("keygen failed: %v", err)
	}

	publicKey := crypto.EncodePoint(pair.Public)
	privateKey := crypto.EncodeScalar(pair.Private)

	ports, _ := getFreePorts(3)
	echoPort := ports[0]
	serverPort := ports[1]
	clientPort := ports[2]

	startEchoServer(echoPort)

	serverCfg := &config.Config{
		Mode:               "server",
		LocalPort:          serverPort,
		Key:                publicKey,
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: false,
		FallbackAddr:       "127.0.0.1:80",
	}
	startSudokuServer(t, serverCfg)

	clientCfg := &config.Config{
		Mode:               "client",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:                privateKey,
		AEAD:               "aes-128-gcm",
		ASCII:              "prefer_ascii",
		EnablePureDownlink: false,
		ProxyMode:          "global",
	}
	startSudokuClient(t, clientCfg)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("dial client failed: %v", err)
	}
	defer conn.Close()
	sendHTTPConnect(t, conn, fmt.Sprintf("127.0.0.1:%d", echoPort))
	payload := []byte("ed25519-key-test")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	resp := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !bytes.Equal(resp, payload) {
		t.Fatalf("echo mismatch")
	}
}

// contains is a lightweight prefix check for CONNECT responses.
func contains(b []byte, sub string) bool {
	return len(b) >= len(sub) && string(b[:len(sub)]) == sub
}
