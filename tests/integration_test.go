package tests

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"net/http"
	"sync"
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
		if b >= 32 && b <= 127 {
			stats.AsciiCount++
		}
		stats.HammingTotal += int64(bits.OnesCount8(b))
	}
	return stats
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
				defer src.Close()
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					return
				}
				defer dst.Close()

				go func() {
					buf := make([]byte, 32*1024)
					for {
						n, err := src.Read(buf)
						if n > 0 {
							data := make([]byte, n)
							copy(data, buf[:n])
							select {
							case analysisChan <- data:
							default:
							}
							dst.Write(data)
						}
						if err != nil {
							break
						}
					}
				}()

				io.Copy(src, dst)
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
				defer src.Close()
				dst, err := net.Dial("tcp", targetAddr)
				if err != nil {
					return
				}
				defer dst.Close()

				var wg sync.WaitGroup
				wg.Add(2)

				go func() {
					defer wg.Done()
					buf := make([]byte, 32*1024)
					for {
						n, err := src.Read(buf)
						if n > 0 {
							data := make([]byte, n)
							copy(data, buf[:n])
							if upChan != nil {
								select {
								case upChan <- data:
								default:
								}
							}
							dst.Write(data)
						}
						if err != nil {
							return
						}
					}
				}()

				go func() {
					defer wg.Done()
					buf := make([]byte, 32*1024)
					for {
						n, err := dst.Read(buf)
						if n > 0 {
							data := make([]byte, n)
							copy(data, buf[:n])
							if downChan != nil {
								select {
								case downChan <- data:
								default:
								}
							}
							src.Write(data)
						}
						if err != nil {
							return
						}
					}
				}()

				wg.Wait()
			}(clientConn)
		}
	}()
	return nil
}

// SOCKS helpers for UoT tests.
func performUDPAssociate(t *testing.T, clientPort int) (net.Conn, *net.UDPAddr) {
	t.Helper()
	ctrl, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
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

	reply := make([]byte, 10)
	if _, err := io.ReadFull(ctrl, reply); err != nil {
		t.Fatalf("Failed to read UDP associate reply: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("UDP associate rejected: %v", reply[1])
	}

	port := int(binary.BigEndian.Uint16(reply[8:10]))
	udpAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
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
func startSudokuServer(cfg *config.Config) {
	table, err := sudoku.NewTableWithCustom(cfg.Key, cfg.ASCII, cfg.CustomTable)
	if err != nil {
		panic(err)
	}
	go app.RunServer(cfg, []*sudoku.Table{table})
	time.Sleep(200 * time.Millisecond)
	waitForPort(cfg.LocalPort)
}

func startSudokuClient(cfg *config.Config) {
	table, err := sudoku.NewTableWithCustom(cfg.Key, cfg.ASCII, cfg.CustomTable)
	if err != nil {
		panic(err)
	}
	go app.RunClient(cfg, []*sudoku.Table{table})
	time.Sleep(200 * time.Millisecond)
	waitForPort(cfg.LocalPort)
}

func waitForPort(port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for i := 0; i < 10; i++ {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
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
	startSudokuServer(serverCfg)

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
	startSudokuClient(clientCfg)

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
	startSudokuServer(serverCfg)
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
	startSudokuClient(clientCfg)

	ctrlConn, udpRelay := performUDPAssociate(t, clientPort)
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
	startSudokuServer(serverCfg)

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
	startSudokuServer(serverCfg)
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
	startSudokuClient(clientCfg)

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
	startSudokuServer(serverCfg)

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
	startSudokuClient(clientCfg)

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
