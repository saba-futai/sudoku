// internal/app/client.go
package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// PeekConn 允许查看第一个字节不消耗它
type PeekConn struct {
	net.Conn
	peeked []byte
}

func (c *PeekConn) Read(p []byte) (n int, err error) {
	if len(c.peeked) > 0 {
		n = copy(p, c.peeked)
		c.peeked = c.peeked[n:]
		return n, nil
	}
	if c.Conn == nil {
		return 0, io.EOF
	}
	return c.Conn.Read(p)
}

// DNSCache 简单的 DNS 缓存
type DNSCache struct {
	cache map[string]net.IP
	mu    sync.RWMutex
	ttl   time.Duration
}

var globalDNSCache = &DNSCache{
	cache: make(map[string]net.IP),
	ttl:   10 * time.Minute,
}

func normalizeClientKey(cfg *config.Config) ([]byte, bool, error) {
	pubKeyPoint, err := crypto.RecoverPublicKey(cfg.Key)
	if err != nil {
		return nil, false, nil
	}

	privateKeyBytes, err := hex.DecodeString(cfg.Key)
	if err != nil {
		return nil, false, fmt.Errorf("decode key: %w", err)
	}

	cfg.Key = crypto.EncodePoint(pubKeyPoint)
	return privateKeyBytes, true, nil
}

func (d *DNSCache) Lookup(host string) net.IP {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if ip, ok := d.cache[host]; ok {
		return ip
	}
	return nil
}

func (d *DNSCache) Set(host string, ip net.IP) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[host] = ip
	// 简单的清理逻辑，实际可以使用更复杂的过期策略
	time.AfterFunc(d.ttl, func() {
		d.mu.Lock()
		delete(d.cache, host)
		d.mu.Unlock()
	})
}

func buildTablesFromConfig(cfg *config.Config) ([]*sudoku.Table, error) {
	patterns := cfg.CustomTables
	if len(patterns) == 0 && strings.TrimSpace(cfg.CustomTable) != "" {
		patterns = []string{cfg.CustomTable}
	}
	if len(patterns) == 0 {
		patterns = []string{""}
	}
	tableSet, err := sudoku.NewTableSet(cfg.Key, cfg.ASCII, patterns)
	if err != nil {
		return nil, err
	}
	return tableSet.Candidates(), nil
}

func RunClient(cfg *config.Config, tables []*sudoku.Table) {
	// 1. Initialize Dialer
	var dialer tunnel.Dialer

	privateKeyBytes, changed, err := normalizeClientKey(cfg)
	if err != nil {
		log.Fatalf("Failed to process key: %v", err)
	}
	if changed {
		log.Printf("Derived Public Key: %s", cfg.Key)
	}

	if tables == nil || len(tables) == 0 || changed {
		var tErr error
		tables, tErr = buildTablesFromConfig(cfg)
		if tErr != nil {
			log.Fatalf("Failed to build table(s): %v", tErr)
		}
	}

	baseDialer := tunnel.BaseDialer{
		Config:     cfg,
		Tables:     tables,
		PrivateKey: privateKeyBytes,
	}

	dialer = &tunnel.StandardDialer{
		BaseDialer: baseDialer,
	}

	// 2. 初始化 GeoIP/PAC 管理器
	var geoMgr *geodata.Manager
	if cfg.ProxyMode == "pac" {
		geoMgr = geodata.GetInstance(cfg.RuleURLs)
	}

	// 3. 监听本地端口
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Client (Mixed) on :%d -> %s | Mode: %s | Rules: %d",
		cfg.LocalPort, cfg.ServerAddress, cfg.ProxyMode, len(cfg.RuleURLs))

	var primaryTable *sudoku.Table
	if len(tables) > 0 {
		primaryTable = tables[0]
	}
	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go handleMixedConn(c, cfg, primaryTable, geoMgr, dialer)
	}
}

func handleMixedConn(c net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	// peek第一个字节以确定协议
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		c.Close()
		return
	}

	// 把读取的字节放回去
	pConn := &PeekConn{Conn: c, peeked: buf}

	switch buf[0] {
	case 0x05:
		// SOCKS5
		handleClientSocks5(pConn, cfg, table, geoMgr, dialer)
	case 0x04:
		// SOCKS4
		handleClientSocks4(pConn, cfg, table, geoMgr, dialer)
	default:
		// 假设是 HTTP/HTTPS
		handleHTTP(pConn, cfg, table, geoMgr, dialer)
	}
}

// ==== SOCKS5 Handler ====

func handleClientSocks5(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	// 1. SOCKS5 握手
	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
		return
	}
	conn.Write([]byte{0x05, 0x00})

	// 2. 读取请求
	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}

	// CMD: header[1] (0x01 Connect)
	switch header[1] {
	case 0x01:
		// CONNECT
	case 0x03:
		// UDP Associate
		handleSocks5UDPAssociate(conn, cfg, dialer)
		return
	default:
		// 不支持 Bind 或其他命令
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	destAddrStr, _, destIP, err := protocol.ReadAddress(conn)
	if err != nil {
		return
	}

	// 3. 路由与连接
	targetConn, success := dialTarget(destAddrStr, destIP, cfg, geoMgr, dialer)
	if !success {
		// SOCKS5 Error
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// SOCKS5 Success
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// 4. 转发
	pipeConn(conn, targetConn)
}

func handleSocks5UDPAssociate(ctrl net.Conn, cfg *config.Config, dialer tunnel.Dialer) {
	uotDialer, ok := dialer.(tunnel.UoTDialer)
	if !ok {
		ctrl.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		ctrl.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	uotConn, err := uotDialer.DialUDPOverTCP()
	if err != nil {
		udpConn.Close()
		ctrl.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	reply := buildUDPAssociateReply(udpConn)
	if _, err := ctrl.Write(reply); err != nil {
		udpConn.Close()
		uotConn.Close()
		return
	}

	log.Printf("[SOCKS5][UDP] Associate ready on %s -> %s", udpConn.LocalAddr().String(), cfg.ServerAddress)
	session := newUoTClientSession(ctrl, udpConn, uotConn)
	session.run()
}

func buildUDPAssociateReply(udpConn *net.UDPConn) []byte {
	addr := udpConn.LocalAddr().(*net.UDPAddr)
	host := addr.IP
	if host == nil || host.IsUnspecified() {
		host = net.ParseIP("127.0.0.1")
	}

	buf := &bytes.Buffer{}
	buf.Write([]byte{0x05, 0x00, 0x00})

	if ip4 := host.To4(); ip4 != nil {
		buf.WriteByte(0x01)
		buf.Write(ip4)
	} else {
		buf.WriteByte(0x04)
		buf.Write(host.To16())
	}

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(addr.Port))
	buf.Write(portBytes)
	return buf.Bytes()
}

type uotClientSession struct {
	ctrlConn  net.Conn
	udpConn   *net.UDPConn
	uotConn   net.Conn
	closeOnce sync.Once
	closed    chan struct{}

	clientAddrMu sync.RWMutex
	clientAddr   *net.UDPAddr
}

func newUoTClientSession(ctrl net.Conn, udpConn *net.UDPConn, uotConn net.Conn) *uotClientSession {
	return &uotClientSession{
		ctrlConn: ctrl,
		udpConn:  udpConn,
		uotConn:  uotConn,
		closed:   make(chan struct{}),
	}
}

func (s *uotClientSession) run() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		s.consumeControl()
	}()
	go func() {
		defer wg.Done()
		s.pipeClientToServer()
	}()
	go func() {
		defer wg.Done()
		s.pipeServerToClient()
	}()
	wg.Wait()
	s.close()
}

func (s *uotClientSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.udpConn.Close()
		s.uotConn.Close()
		s.ctrlConn.Close()
	})
}

func (s *uotClientSession) consumeControl() {
	io.Copy(io.Discard, s.ctrlConn)
	s.close()
}

func (s *uotClientSession) pipeClientToServer() {
	buf := make([]byte, 65535)
	for {
		n, addr, err := s.udpConn.ReadFromUDP(buf)
		if err != nil {
			s.close()
			return
		}
		destAddr, payload, err := decodeSocks5UDPRequest(buf[:n])
		if err != nil {
			continue
		}
		s.setClientAddr(addr)

		if err := tunnel.WriteUoTDatagram(s.uotConn, destAddr, payload); err != nil {
			s.close()
			return
		}
	}
}

func (s *uotClientSession) pipeServerToClient() {
	for {
		addrStr, payload, err := tunnel.ReadUoTDatagram(s.uotConn)
		if err != nil {
			s.close()
			return
		}

		clientAddr := s.getClientAddr()
		if clientAddr == nil {
			continue
		}

		resp := buildUDPResponsePacket(addrStr, payload)
		if resp == nil {
			continue
		}
		if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
			s.close()
			return
		}
	}
}

func (s *uotClientSession) setClientAddr(addr *net.UDPAddr) {
	s.clientAddrMu.Lock()
	defer s.clientAddrMu.Unlock()
	if s.clientAddr == nil {
		s.clientAddr = addr
	}
}

func (s *uotClientSession) getClientAddr() *net.UDPAddr {
	s.clientAddrMu.RLock()
	defer s.clientAddrMu.RUnlock()
	return s.clientAddr
}

// ==== SOCKS4 Handler ====

func handleClientSocks4(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	// SOCKS4 Request Format:
	// VN (1) | CD (1) | DSTPORT (2) | DSTIP (4) | USERID (variable) | NULL (1)
	// SOCKS4a extension: if DSTIP is 0.0.0.x (x!=0), then DOMAIN (variable) | NULL (1) follows USERID

	buf := make([]byte, 8)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}

	vn := buf[0]
	cd := buf[1]
	if vn != 0x04 || cd != 0x01 { // Only support Connect (0x01)
		return
	}

	port := binary.BigEndian.Uint16(buf[2:4])
	ipBytes := buf[4:8]

	// Read UserID
	if _, err := readString(conn); err != nil {
		return
	}

	var destAddrStr string
	var destIP net.IP

	// Check for SOCKS4a (0.0.0.x where x != 0)
	if ipBytes[0] == 0 && ipBytes[1] == 0 && ipBytes[2] == 0 && ipBytes[3] != 0 {
		// SOCKS4a: Read Domain
		domain, err := readString(conn)
		if err != nil {
			return
		}
		destAddrStr = fmt.Sprintf("%s:%d", domain, port)
	} else {
		destIP = net.IP(ipBytes)
		destAddrStr = fmt.Sprintf("%s:%d", destIP.String(), port)
	}

	// Route & Connect
	targetConn, success := dialTarget(destAddrStr, destIP, cfg, geoMgr, dialer)
	if !success {
		// SOCKS4 Error (91 = request rejected)
		conn.Write([]byte{0x00, 0x5B, 0, 0, 0, 0, 0, 0})
		return
	}

	// SOCKS4 Success (90 = request granted)
	conn.Write([]byte{0x00, 0x5A, 0, 0, 0, 0, 0, 0})

	pipeConn(conn, targetConn)
}

func readString(r io.Reader) (string, error) {
	var buf []byte
	b := make([]byte, 1)
	for {
		if _, err := r.Read(b); err != nil {
			return "", err
		}
		if b[0] == 0 {
			break
		}
		buf = append(buf, b[0])
	}
	return string(buf), nil
}

func decodeSocks5UDPRequest(pkt []byte) (string, []byte, error) {
	if len(pkt) < 4 {
		return "", nil, fmt.Errorf("packet too short")
	}
	if pkt[2] != 0x00 {
		return "", nil, fmt.Errorf("frag not supported")
	}

	reader := bytes.NewReader(pkt[3:])
	addrStr, _, _, err := protocol.ReadAddress(reader)
	if err != nil {
		return "", nil, err
	}
	payload := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", nil, err
	}
	return addrStr, payload, nil
}

func buildUDPResponsePacket(addr string, payload []byte) []byte {
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00, 0x00, 0x00}) // RSV RSV FRAG(=0)
	if err := protocol.WriteAddress(buf, addr); err != nil {
		return nil
	}
	buf.Write(payload)
	return buf.Bytes()
}

// ==== HTTP Handler ====

func handleHTTP(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	host := req.Host
	// 如果不带端口，默认补全
	if !strings.Contains(host, ":") {
		if req.Method == http.MethodConnect {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	// 解析 IP (为了路由决策)
	hostName, _, _ := net.SplitHostPort(host)
	destIP := net.ParseIP(hostName)

	// 路由决策与连接
	targetConn, success := dialTarget(host, destIP, cfg, geoMgr, dialer)
	if !success {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	if req.Method == http.MethodConnect {
		// HTTPS Tunnel: 建立连接后回复 200 OK，然后纯透传
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		pipeConn(conn, targetConn)
	} else {
		req.RequestURI = ""
		// 如果是绝对路径转换为相对路径
		if req.URL.Scheme != "" {
			req.URL.Scheme = ""
			req.URL.Host = ""
		}

		if err := req.Write(targetConn); err != nil {
			targetConn.Close()
			return
		}
		pipeConn(conn, targetConn)
	}
}

// ==== Common Logic  ====

func dialTarget(destAddrStr string, destIP net.IP, cfg *config.Config, geoMgr *geodata.Manager, dialer tunnel.Dialer) (net.Conn, bool) {
	shouldProxy := true

	if cfg.ProxyMode == "global" {
		shouldProxy = true
	} else if cfg.ProxyMode == "direct" {
		shouldProxy = false
	} else if cfg.ProxyMode == "pac" {
		// 1. 检查域名或已知 IP 是否在 CN 列表
		if geoMgr.IsCN(destAddrStr, destIP) {
			shouldProxy = false
			log.Printf("[PAC] %s -> DIRECT (Rule Match)", destAddrStr)
		} else {
			// 2. 如果没有匹配且 destIP 未知 (是域名)，尝试解析 IP 再检查
			if destIP == nil {
				host, _, _ := net.SplitHostPort(destAddrStr)

				// Try Cache First
				if cachedIP := globalDNSCache.Lookup(host); cachedIP != nil {
					if geoMgr.IsCN(destAddrStr, cachedIP) {
						shouldProxy = false
						log.Printf("[PAC] %s (%s) -> DIRECT (Cache Rule Match)", destAddrStr, cachedIP)
					} else {
						log.Printf("[PAC] %s (%s) -> PROXY (Cache)", destAddrStr, cachedIP)
					}
				} else {
					// Real Lookup
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
					cancel()

					if err == nil && len(ips) > 0 {
						globalDNSCache.Set(host, ips[0]) // Cache it
						if geoMgr.IsCN(destAddrStr, ips[0]) {
							shouldProxy = false
							log.Printf("[PAC] %s (%s) -> DIRECT (IP Rule Match)", destAddrStr, ips[0])
						} else {
							log.Printf("[PAC] %s (%s) -> PROXY", destAddrStr, ips[0])
						}
					} else {
						log.Printf("[PAC] %s -> PROXY (Default)", destAddrStr)
					}
				}
			} else {
				// 解析失败或无 IP，默认代理
				log.Printf("[PAC] %s -> PROXY", destAddrStr)
			}
		}
	}

	if shouldProxy {
		conn, err := dialer.Dial(destAddrStr)
		if err != nil {
			log.Printf("[Proxy] Dial Failed: %v", err)
			return nil, false
		}
		return conn, true
	} else {
		// 直连模式
		dConn, err := net.DialTimeout("tcp", destAddrStr, 5*time.Second)
		if err != nil {
			log.Printf("[Direct] Dial Failed: %v", err)
			return nil, false
		}
		return dConn, true
	}
}
