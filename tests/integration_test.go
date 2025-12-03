package tests

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/app"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// === Helpers ===

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
			// Close already opened
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

// TrafficStats holds analysis results
type TrafficStats struct {
	TotalBytes   int64
	AsciiCount   int64
	HammingTotal int64
}

func (s *TrafficStats) AsciiRatio() float64 {
	if s.TotalBytes == 0 {
		return 0
	}
	return float64(s.AsciiCount) / float64(s.TotalBytes)
}

func (s *TrafficStats) AvgHammingWeight() float64 {
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

// Middleman forwards traffic and captures it for analysis
// protocol: "tcp" or "udp"
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

				// Send to analysis
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

					// Read from target and send back to client
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

	// TCP
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

				// Forward: Src -> Dst
				go func() {
					buf := make([]byte, 32*1024)
					for {
						n, err := src.Read(buf)
						if n > 0 {
							data := make([]byte, n)
							copy(data, buf[:n])
							select {
							case analysisChan <- data: // This is Upstream if Middleman is before Server
							default:
							}
							dst.Write(data)
						}
						if err != nil {
							break
						}
					}
					dst.Close() // Close write on dst? Or just close connection.
				}()

				// Backward: Dst -> Src

				io.Copy(src, dst)
			}(clientConn)
		}
	}()
	return nil
}

// Improved Middleman
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

				// Upstream: Src -> Dst
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

				// Downstream: Dst -> Src
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

func performUDPAssociate(t *testing.T, clientPort int) (net.Conn, *net.UDPAddr) {
	t.Helper()
	ctrl, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client control port: %v", err)
	}

	// Negotiate no-auth
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

	// UDP Associate request (0.0.0.0:0)
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

func startSudokuServer(cfg *config.Config) {
	// Generate a table if needed, but RunServer takes it.
	// We need to make sure the key matches.
	table := sudoku.NewTable(cfg.Key, cfg.ASCII)
	go app.RunServer(cfg, table)
	time.Sleep(100 * time.Millisecond) // Give it time to start
}

func startSudokuClient(cfg *config.Config) {
	table := sudoku.NewTable(cfg.Key, cfg.ASCII)
	go app.RunClient(cfg, table)

	time.Sleep(100 * time.Millisecond)
}

// TestMieruMemoryStress can be enabled manually via:
//
//	SUDOKU_MIERU_STRESS=1 go test ./tests -run TestMieruMemoryStress -v
//
// It simulates a long-running download over Mieru split mode and logs memory usage.
func TestMieruMemoryStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skip stress test in short mode")
	}
	if os.Getenv("SUDOKU_MIERU_STRESS") == "" {
		t.Skip("set SUDOKU_MIERU_STRESS=1 to run")
	}

	ports, err := getFreePorts(4)
	if err != nil {
		t.Fatalf("failed to get free ports: %v", err)
	}
	echoPort := ports[0]
	serverPort := ports[1]
	mieruPort := ports[2]
	clientPort := ports[3]

	if err := startEchoServer(echoPort); err != nil {
		t.Fatalf("failed to start echo server: %v", err)
	}

	serverCfg := &config.Config{
		Mode:        "server",
		LocalPort:   serverPort,
		Key:         "testkey",
		AEAD:        "aes-128-gcm",
		ASCII:       "prefer_entropy",
		EnableMieru: true,
		MieruConfig: &config.MieruConfig{
			Port:         mieruPort,
			Transport:    "TCP",
			MTU:          1400,
			Multiplexing: "MULTIPLEXING_HIGH",
			Username:     "default",
			Password:     "testkey",
		},
		FallbackAddr: "127.0.0.1:80",
	}
	startSudokuServer(serverCfg)

	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		EnableMieru:   true,
		MieruConfig: &config.MieruConfig{
			Port:         mieruPort,
			Transport:    "TCP",
			MTU:          1400,
			Multiplexing: "MULTIPLEXING_HIGH",
			Username:     "default",
			Password:     "testkey",
		},
		ProxyMode: "global",
	}
	startSudokuClient(clientCfg)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write CONNECT: %v", err)
	}

	headerBuf := make([]byte, 1024)
	n, err := conn.Read(headerBuf)
	if err != nil || !contains(headerBuf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("Proxy handshake failed: %v", string(headerBuf[:n]))
	}

	const totalBytes = 128 * 1024 * 1024 // 128 MiB
	chunk := make([]byte, 32*1024)
	for i := range chunk {
		chunk[i] = byte(i)
	}

	var written int64
	lastReport := time.Now()
	respBuf := make([]byte, len(chunk))

	for written < totalBytes {
		nw, err := conn.Write(chunk)
		if err != nil {
			t.Fatalf("write failed after %d bytes: %v", written, err)
		}
		written += int64(nw)

		// Read echo to keep flow going
		if _, err := io.ReadFull(conn, respBuf[:nw]); err != nil {
			t.Fatalf("read failed after %d bytes: %v", written, err)
		}

		if time.Since(lastReport) > 2*time.Second {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			t.Logf("written=%d MiB, alloc=%d MiB, sys=%d MiB, numGC=%d",
				written/1024/1024, ms.Alloc/1024/1024, ms.Sys/1024/1024, ms.NumGC)
			lastReport = time.Now()
		}
	}
}

func TestTCPPayload_ASCII(t *testing.T) {
	// 1. Setup Ports
	ports, _ := getFreePorts(4)
	echoPort := ports[0]
	serverPort := ports[1]
	middlemanPort := ports[2]
	clientPort := ports[3]

	// 2. Start Echo Server
	startEchoServer(echoPort)

	// 3. Start Sudoku Server
	serverCfg := &config.Config{
		Mode:         "server",
		LocalPort:    serverPort,
		Key:          "testkey",
		AEAD:         "aes-128-gcm",
		ASCII:        "prefer_ascii", // Changed
		EnableMieru:  false,
		FallbackAddr: "127.0.0.1:80",
		PaddingMin:   10,
		PaddingMax:   20,
	}
	startSudokuServer(serverCfg)

	// 4. Start Middleman
	upChan := make(chan []byte, 100)
	downChan := make(chan []byte, 100)
	startDualMiddleman(middlemanPort, serverPort, upChan, downChan)

	// 5. Start Sudoku Client
	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_ascii", // Changed
		EnableMieru:   false,
		ProxyMode:     "global",
	}
	startSudokuClient(clientCfg)

	// 6. Generate Traffic
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	conn.Write([]byte(req))

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || !contains(buf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("Proxy handshake failed: %v", string(buf[:n]))
	}

	conn.Write([]byte("Hello World, this is a test payload for ASCII check."))
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read echo: %v", err)
	}

	// 7. Analyze Traffic
	time.Sleep(500 * time.Millisecond)

	// ASCII Mode: Expect > 96% ASCII
	verifyTraffic(t, upChan, "Upstream", 0.96, 3.0, 1.1) // Hamming check relaxed or ignored? User said "prefer ascii下ascii占比高于96%".
	verifyTraffic(t, downChan, "Downstream", 0.96, 3.0, 1.1)
}

func TestTCPPayload_Mieru(t *testing.T) {
	// 1. Setup Ports
	ports, _ := getFreePorts(6)
	echoPort := ports[0]
	serverPort := ports[1]
	mieruPort := ports[2]
	middlemanSudokuPort := ports[3]
	middlemanMieruPort := ports[4]
	clientPort := ports[5]

	// 2. Start Echo Server
	startEchoServer(echoPort)

	// 3. Start Sudoku Server (Enable Mieru)
	serverCfg := &config.Config{
		Mode:        "server",
		LocalPort:   serverPort,
		Key:         "testkey",
		AEAD:        "aes-128-gcm",
		ASCII:       "prefer_entropy",
		EnableMieru: true,
		MieruConfig: &config.MieruConfig{
			Port:         mieruPort,
			Transport:    "TCP",
			MTU:          1400,
			Multiplexing: "LOW",
			Username:     "default",
			Password:     "testkey",
		},
		FallbackAddr: "127.0.0.1:80",
	}
	startSudokuServer(serverCfg)

	// 4. Start Middlemen
	// Middleman 1: Sudoku Uplink (Client -> Server)
	upChan := make(chan []byte, 100)
	startDualMiddleman(middlemanSudokuPort, serverPort, upChan, nil)

	// Middleman 2: Mieru Downlink (Server -> Client)

	downChan := make(chan []byte, 100)
	startDualMiddleman(middlemanMieruPort, mieruPort, nil, downChan)
	// 5. Start Sudoku Client
	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", middlemanSudokuPort), // Point Sudoku to Middleman 1
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		EnableMieru:   true,
		MieruConfig: &config.MieruConfig{
			Port:         middlemanMieruPort, // Point Mieru to Middleman 2
			Transport:    "TCP",
			MTU:          1400,
			Multiplexing: "LOW",
			Username:     "default",
			Password:     "testkey",
		},
		ProxyMode: "global",
	}
	startSudokuClient(clientCfg)

	// 6. Generate Traffic
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	conn.Write([]byte(req))

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || !contains(buf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("Proxy handshake failed: %v", string(buf[:n]))
	}

	conn.Write([]byte("Hello World, this is a test payload for Mieru check."))
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read echo: %v", err)
	}

	// 7. Analyze Traffic
	time.Sleep(1 * time.Second)

	// Upstream (Sudoku): ASCII > 50%, Hamming 3±0.3
	verifyTraffic(t, upChan, "Upstream (Sudoku)", 0.5, 3.0, 0.3)

	// Downstream (Mieru): Encrypted.
	// Encrypted traffic usually has Hamming weight ~4 (random bits) and low ASCII ratio (random bytes are ~37% ASCII [32-127] / 256).
	// 96 chars / 256 = 0.375.
	// So ASCII ratio should be around 0.3-0.4. Definitely not > 0.9.
	// Hamming should be around 4.0.
	verifyTraffic(t, downChan, "Downstream (Mieru)", 0.0, 4.0, 0.5) // Min ASCII 0.0 (ignore), Hamming 4.0±0.5
}

func TestTCPPayload_Entropy(t *testing.T) {
	// 1. Setup Ports
	ports, _ := getFreePorts(4)
	echoPort := ports[0]
	serverPort := ports[1]
	middlemanPort := ports[2]
	clientPort := ports[3]

	// 2. Start Echo Server (Target)
	startEchoServer(echoPort)

	// 3. Start Sudoku Server
	serverCfg := &config.Config{
		Mode:         "server",
		LocalPort:    serverPort,
		Key:          "testkey",
		AEAD:         "aes-128-gcm",
		ASCII:        "prefer_entropy",
		EnableMieru:  false,
		FallbackAddr: "127.0.0.1:80", // Dummy
		PaddingMin:   10,
		PaddingMax:   20,
	}
	startSudokuServer(serverCfg)

	// 4. Start Middleman (Client -> Middleman -> Server)
	upChan := make(chan []byte, 100)
	downChan := make(chan []byte, 100)
	startDualMiddleman(middlemanPort, serverPort, upChan, downChan)

	// 5. Start Sudoku Client
	// Point Client to Middleman instead of real Server to capture traffic
	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", middlemanPort),
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		EnableMieru:   false,
		ProxyMode:     "global",
	}
	startSudokuClient(clientCfg)

	// 6. Generate Traffic
	// Connect to Client SOCKS/HTTP port and send data
	// Sudoku Client listens on clientPort. It accepts HTTP/SOCKS.
	// We'll use a simple TCP connection and send an HTTP request to trigger it.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	// Send a request that will be proxied to Echo Server
	// Format: CONNECT target:port (if HTTP) or just SOCKS.
	// Let's use SOCKS5 for simplicity or just HTTP CONNECT.
	// Client.go handles HTTP/SOCKS.
	// Let's try HTTP Proxy request.
	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	conn.Write([]byte(req))

	// Read 200 OK
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || !contains(buf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("Proxy handshake failed: %v", string(buf[:n]))
	}

	// Send actual data
	payload := []byte("Hello World, this is a test payload for entropy check.")
	conn.Write(payload)

	// Read echo
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read echo: %v", err)
	}

	// 7. Analyze Traffic
	// Wait a bit for channels to fill
	time.Sleep(500 * time.Millisecond)

	verifyTraffic(t, upChan, "Upstream", 0.5, 3.0, 0.3)
	verifyTraffic(t, downChan, "Downstream", 0.5, 3.0, 0.3)
}

func TestUDPOverTCP(t *testing.T) {
	ports, _ := getFreePorts(2)
	serverPort := ports[0]
	clientPort := ports[1]

	udpEcho, udpPort, err := startUDPEchoServer()
	if err != nil {
		t.Fatalf("failed to start UDP echo: %v", err)
	}
	defer udpEcho.Close()

	serverCfg := &config.Config{
		Mode:         "server",
		LocalPort:    serverPort,
		Key:          "testkey",
		AEAD:         "aes-128-gcm",
		ASCII:        "prefer_entropy",
		FallbackAddr: "127.0.0.1:80",
	}
	startSudokuServer(serverCfg)

	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		ProxyMode:     "global",
	}
	startSudokuClient(clientCfg)

	ctrlConn, udpRelay := performUDPAssociate(t, clientPort)
	defer ctrlConn.Close()

	relayConn, err := net.DialUDP("udp", nil, udpRelay)
	if err != nil {
		t.Fatalf("failed to dial udp relay: %v", err)
	}
	defer relayConn.Close()

	targetAddr := fmt.Sprintf("127.0.0.1:%d", udpPort)

	sendAndVerify := func(payload []byte) {
		packet := buildSocksUDPRequest(t, targetAddr, payload)
		if _, err := relayConn.Write(packet); err != nil {
			t.Fatalf("failed to send udp payload: %v", err)
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
			t.Fatalf("udp payload mismatch: %v vs %v", data, payload)
		}
	}

	sendAndVerify([]byte("hello-udp"))
	sendAndVerify([]byte("second-round"))
}

func TestUDPOverTCP_LargePayload(t *testing.T) {
	ports, _ := getFreePorts(2)
	serverPort := ports[0]
	clientPort := ports[1]

	udpEcho, udpPort, err := startUDPEchoServer()
	if err != nil {
		t.Fatalf("failed to start UDP echo: %v", err)
	}
	defer udpEcho.Close()

	serverCfg := &config.Config{
		Mode:         "server",
		LocalPort:    serverPort,
		Key:          "testkey",
		AEAD:         "aes-128-gcm",
		ASCII:        "prefer_entropy",
		FallbackAddr: "127.0.0.1:80",
	}
	startSudokuServer(serverCfg)

	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		ProxyMode:     "global",
	}
	startSudokuClient(clientCfg)

	ctrlConn, udpRelay := performUDPAssociate(t, clientPort)
	defer ctrlConn.Close()

	relayConn, err := net.DialUDP("udp", nil, udpRelay)
	if err != nil {
		t.Fatalf("failed to dial udp relay: %v", err)
	}
	defer relayConn.Close()

	targetAddr := fmt.Sprintf("127.0.0.1:%d", udpPort)
	payload := make([]byte, 8000)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	packet := buildSocksUDPRequest(t, targetAddr, payload)
	if _, err := relayConn.Write(packet); err != nil {
		t.Fatalf("failed to send large udp payload: %v", err)
	}

	respBuf := make([]byte, len(payload)+64)
	n, err := relayConn.Read(respBuf)
	if err != nil {
		t.Fatalf("failed to read large udp response: %v", err)
	}
	addr, data := parseSocksUDPResponse(t, respBuf[:n])
	if addr != targetAddr {
		t.Fatalf("unexpected response addr: %s", addr)
	}
	if !bytes.Equal(data, payload) {
		t.Fatalf("large udp payload mismatch (%d vs %d)", len(data), len(payload))
	}
}

func contains(b []byte, sub string) bool {
	return len(b) >= len(sub) && string(b[:len(sub)]) == sub // Simple prefix check or use strings.Contains
}

func verifyTraffic(t *testing.T, ch chan []byte, name string, minAscii float64, targetHamming, tolerance float64) {
	var totalBytes int64
	var asciiCount int64
	var hammingTotal int64

	// Drain channel
	count := len(ch)
	if count == 0 {
		t.Errorf("%s: No traffic captured", name)
		return
	}

	for i := 0; i < count; i++ {
		data := <-ch
		stats := analyzeTraffic(data)
		totalBytes += stats.TotalBytes
		asciiCount += stats.AsciiCount
		hammingTotal += stats.HammingTotal
	}

	asciiRatio := float64(asciiCount) / float64(totalBytes)
	avgHamming := float64(hammingTotal) / float64(totalBytes)

	t.Logf("[%s] Bytes: %d, ASCII Ratio: %.2f, Avg Hamming: %.2f", name, totalBytes, asciiRatio, avgHamming)

	if asciiRatio <= minAscii {
		t.Errorf("[%s] ASCII Ratio too low: got %.2f, want > %.2f", name, asciiRatio, minAscii)
	}

	if avgHamming < targetHamming-tolerance || avgHamming > targetHamming+tolerance {
		t.Errorf("[%s] Hamming Weight out of range: got %.2f, want %.2f ± %.2f", name, avgHamming, targetHamming, tolerance)
	}
}

func TestFallback(t *testing.T) {
	// 1. Setup Ports
	ports, _ := getFreePorts(2)
	serverPort := ports[0]
	webPort := ports[1]

	// 2. Start Web Server (Fallback Target)
	startWebServer(webPort)

	// 3. Start Sudoku Server
	serverCfg := &config.Config{
		Mode:         "server",
		LocalPort:    serverPort,
		Key:          "testkey",
		AEAD:         "aes-128-gcm",
		ASCII:        "prefer_entropy",
		EnableMieru:  false,
		FallbackAddr: fmt.Sprintf("127.0.0.1:%d", webPort),
	}
	startSudokuServer(serverCfg)

	// 4. Connect directly to Server Port (Simulate Probe)
	// We use a standard HTTP client
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d", serverPort))
	if err != nil {
		t.Fatalf("Failed to connect to server: %v", err)
	}
	defer resp.Body.Close()

	// 5. Verify Response
	if resp.StatusCode != 200 {
		t.Errorf("Expected 200 OK, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello Fallback" {
		t.Errorf("Expected 'Hello Fallback', got '%s'", string(body))
	}
}

func TestMieruUDP(t *testing.T) {
	// 1. Setup Ports
	ports, _ := getFreePorts(5)
	echoPort := ports[0]
	serverPort := ports[1]
	mieruPort := ports[2]
	middlemanMieruPort := ports[3]
	clientPort := ports[4]

	// 2. Start Echo Server
	startEchoServer(echoPort)

	// 3. Start Sudoku Server (Enable Mieru UDP)
	serverCfg := &config.Config{
		Mode:        "server",
		LocalPort:   serverPort,
		Key:         "testkey",
		AEAD:        "aes-128-gcm",
		ASCII:       "prefer_entropy",
		EnableMieru: true,
		MieruConfig: &config.MieruConfig{
			Port:         mieruPort,
			Transport:    "UDP", // UDP Mode
			MTU:          1400,
			Multiplexing: "LOW",
			Username:     "default",
			Password:     "testkey",
		},
		FallbackAddr: "127.0.0.1:80",
	}
	startSudokuServer(serverCfg)

	// 4. Start Middleman for UDP
	// Listens on middlemanMieruPort (UDP), forwards to mieruPort (UDP)
	// Captures traffic to verify it is UDP
	udpChan := make(chan []byte, 100)
	err := startMiddleman(middlemanMieruPort, mieruPort, "udp", udpChan)
	if err != nil {
		t.Fatalf("Failed to start UDP middleman: %v", err)
	}

	// 5. Start Sudoku Client
	clientCfg := &config.Config{
		Mode:          "client",
		LocalPort:     clientPort,
		ServerAddress: fmt.Sprintf("127.0.0.1:%d", serverPort), // Sudoku Uplink (Direct)
		Key:           "testkey",
		AEAD:          "aes-128-gcm",
		ASCII:         "prefer_entropy",
		EnableMieru:   true,
		MieruConfig: &config.MieruConfig{
			Port:         middlemanMieruPort, // Point Mieru to Middleman
			Transport:    "UDP",
			MTU:          1400,
			Multiplexing: "LOW",
			Username:     "default",
			Password:     "testkey",
		},
		ProxyMode: "global",
	}
	startSudokuClient(clientCfg)

	// 6. Generate Traffic
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("Failed to connect to client: %v", err)
	}
	defer conn.Close()

	target := fmt.Sprintf("127.0.0.1:%d", echoPort)
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	conn.Write([]byte(req))

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil || !contains(buf[:n], "HTTP/1.1 200 Connection Established") {
		t.Fatalf("Proxy handshake failed: %v", string(buf[:n]))
	}

	conn.Write([]byte("Hello UDP Check"))
	n, err = conn.Read(buf)
	if err != nil {
		t.Fatalf("Failed to read echo: %v", err)
	}

	// 7. Verify UDP Traffic
	select {
	case <-udpChan:
		t.Log("UDP Traffic detected")
	case <-time.After(2 * time.Second):
		t.Error("No UDP traffic detected on Mieru port")
	}
}
