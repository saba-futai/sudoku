package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
	"github.com/saba-futai/sudoku/pkg/logx"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

type PeekConn struct {
	net.Conn
	peeked []byte
}

func (c *PeekConn) CloseWrite() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseWrite(c.Conn)
}

func (c *PeekConn) CloseRead() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseRead(c.Conn)
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

var lookupIPsWithCache = dnsutil.LookupIPsWithCache
var directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout(network, addr, timeout)
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

func RunClient(cfg *config.Config, tables []*sudoku.Table) {
	logx.InstallStd()
	var dialer tunnel.Dialer

	privateKeyBytes, changed, err := normalizeClientKey(cfg)
	if err != nil {
		logx.Fatalf("Client", "Failed to process key: %v", err)
	}
	if changed {
		logx.Infof("Init", "Derived Public Key: %s", cfg.Key)
	}

	if tables == nil || len(tables) == 0 || changed {
		var err error
		tables, err = BuildTables(cfg)
		if err != nil {
			logx.Fatalf("Init", "Failed to build table(s): %v", err)
		}
	}

	baseDialer := tunnel.BaseDialer{
		Config:     cfg,
		Tables:     tables,
		PrivateKey: privateKeyBytes,
	}

	if cfg.HTTPMaskSessionMuxEnabled() {
		dialer = &tunnel.MuxDialer{BaseDialer: baseDialer}
		logx.Infof("Init", "Enabled HTTPMask session mux (single tunnel, multi-target)")
	} else {
		dialer = &tunnel.AdaptiveDialer{
			BaseDialer: baseDialer,
		}
	}

	startReverseClient(cfg, &baseDialer)

	var geoMgr *geodata.Manager
	if cfg.ProxyMode == "pac" {
		geoMgr = geodata.GetInstance(cfg.RuleURLs)
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		logx.Fatalf("Client", "%v", err)
	}
	logx.Infof("Client", "Client (Mixed) on :%d -> %s | Mode: %s | Rules: %d",
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
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c, buf); err != nil {
		c.Close()
		return
	}

	pConn := &PeekConn{Conn: c, peeked: buf}

	switch buf[0] {
	case 0x05:
		handleClientSocks5(pConn, cfg, table, geoMgr, dialer)
	case 0x04:
		handleClientSocks4(pConn, cfg, table, geoMgr, dialer)
	default:
		handleHTTP(pConn, cfg, table, geoMgr, dialer)
	}
}

func handleClientSocks5(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
		return
	}
	conn.Write([]byte{0x05, 0x00})

	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}

	switch header[1] {
	case 0x01:
	case 0x03:
		handleSocks5UDPAssociate(conn, cfg, geoMgr, dialer)
		return
	default:
		conn.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	destAddrStr, _, destIP, err := protocol.ReadAddress(conn)
	if err != nil {
		return
	}

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), destAddrStr, destIP, cfg, geoMgr, dialer)
	if !success {
		conn.Write([]byte{0x05, 0x04, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	pipeConn(conn, targetConn)
}

func handleSocks5UDPAssociate(ctrl net.Conn, cfg *config.Config, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	uotDialer, ok := dialer.(tunnel.UoTDialer)
	if !ok {
		ctrl.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	localIP := ipFromNetAddr(ctrl.LocalAddr())
	remoteIP := ipFromNetAddr(ctrl.RemoteAddr())
	udpNet, udpBindIP := udpNetworkAndBindIP(localIP, remoteIP)

	udpConn, err := net.ListenUDP(udpNet, &net.UDPAddr{IP: udpBindIP, Port: 0})
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

	replyIP := selectUDPAssociateReplyIP(localIP, remoteIP)
	reply := buildUDPAssociateReply(replyIP, udpConn.LocalAddr().(*net.UDPAddr).Port, localIP, remoteIP)
	if _, err := ctrl.Write(reply); err != nil {
		udpConn.Close()
		uotConn.Close()
		return
	}

	logx.Infof("SOCKS5/UDP", "Associate ready on %s -> %s", udpConn.LocalAddr().String(), cfg.ServerAddress)
	session := newUoTClientSession(ctrl, udpConn, uotConn, udpNet, cfg, geoMgr)
	session.run()
}

func buildUDPAssociateReply(host net.IP, port int, localIP net.IP, remoteIP net.IP) []byte {
	if host == nil {
		// Reply 0.0.0.0/:: to indicate "use the TCP endpoint host" (common SOCKS5 client behavior).
		host = net.IPv4zero
		if isIPv6Only(localIP, remoteIP) {
			host = net.IPv6unspecified
		}
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
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	buf.Write(portBytes)
	return buf.Bytes()
}

func normalizeIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip.To16()
}

func ipFromNetAddr(addr net.Addr) net.IP {
	if addr == nil {
		return nil
	}
	switch a := addr.(type) {
	case *net.TCPAddr:
		return normalizeIP(a.IP)
	case *net.UDPAddr:
		return normalizeIP(a.IP)
	default:
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return nil
		}
		return normalizeIP(net.ParseIP(host))
	}
}

func isIPv6Only(localIP net.IP, remoteIP net.IP) bool {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)
	if localIP != nil {
		return localIP.To4() == nil
	}
	if remoteIP != nil {
		return remoteIP.To4() == nil
	}
	return false
}

func udpNetworkAndBindIP(localIP net.IP, remoteIP net.IP) (string, net.IP) {
	if isIPv6Only(localIP, remoteIP) {
		return "udp6", net.IPv6unspecified
	}
	return "udp4", net.IPv4zero
}

func selectUDPAssociateReplyIP(localIP net.IP, remoteIP net.IP) net.IP {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)

	if localIP == nil || localIP.IsUnspecified() {
		return nil
	}
	if localIP.IsLoopback() {
		// Local client: return loopback for maximum compatibility.
		return localIP
	}
	if !localIP.IsGlobalUnicast() {
		return nil
	}
	// Avoid advertising a private IP to a public client (typical NAT port-forward situation).
	if localIP.IsPrivate() && (remoteIP == nil || !remoteIP.IsPrivate()) {
		return nil
	}
	return localIP
}

type uotClientSession struct {
	ctrlConn  net.Conn
	udpConn   *net.UDPConn
	uotConn   net.Conn
	udpNet    string
	cfg       *config.Config
	geoMgr    *geodata.Manager
	closeOnce sync.Once
	closed    chan struct{}

	clientAddrMu sync.RWMutex
	clientAddr   *net.UDPAddr

	allowedClientIP net.IP

	peerMu   sync.Mutex
	peers    map[string]time.Time
	peerTTL  time.Duration
	routeMu  sync.Mutex
	routeLog map[string]time.Time
	routeTTL time.Duration
}

func newUoTClientSession(ctrl net.Conn, udpConn *net.UDPConn, uotConn net.Conn, udpNet string, cfg *config.Config, geoMgr *geodata.Manager) *uotClientSession {
	return &uotClientSession{
		ctrlConn: ctrl,
		udpConn:  udpConn,
		uotConn:  uotConn,
		udpNet:   udpNet,
		cfg:      cfg,
		geoMgr:   geoMgr,
		closed:   make(chan struct{}),
		// Restrict UDP relay to the control connection's source IP to prevent hijacking/open relay.
		allowedClientIP: ipFromNetAddr(ctrl.RemoteAddr()),
		peers:           make(map[string]time.Time),
		peerTTL:         2 * time.Minute,
		routeLog:        make(map[string]time.Time),
		routeTTL:        30 * time.Second,
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
		if s.allowedClientIP != nil && !s.allowedClientIP.Equal(normalizeIP(addr.IP)) {
			if !s.isKnownPeer(addr) {
				continue
			}
			clientAddr := s.getClientAddr()
			if clientAddr == nil {
				continue
			}
			resp := buildUDPResponsePacket(addr.String(), buf[:n])
			if resp == nil {
				continue
			}
			if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
				s.close()
				return
			}
			continue
		}

		destAddr, destIP, payload, err := decodeSocks5UDPRequest(buf[:n])
		if err != nil {
			continue
		}
		s.setClientAddr(addr)

		shouldProxy, match, directAddr := s.routeUDPTarget(destAddr, destIP)
		s.logUDPRoute(addr, destAddr, match, shouldProxy)

		if shouldProxy {
			if err := tunnel.WriteUoTDatagram(s.uotConn, destAddr, payload); err != nil {
				s.close()
				return
			}
			continue
		}

		if directAddr == nil {
			continue
		}
		s.notePeer(directAddr)
		if _, err := s.udpConn.WriteToUDP(payload, directAddr); err != nil {
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
	if addr == nil {
		return
	}
	cpy := *addr
	if s.clientAddr == nil || s.clientAddr.Port != addr.Port || !normalizeIP(s.clientAddr.IP).Equal(normalizeIP(addr.IP)) {
		s.clientAddr = &cpy
	}
}

func (s *uotClientSession) getClientAddr() *net.UDPAddr {
	s.clientAddrMu.RLock()
	defer s.clientAddrMu.RUnlock()
	return s.clientAddr
}

func (s *uotClientSession) notePeer(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	key := addr.String()
	now := time.Now()

	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for k, exp := range s.peers {
		if now.After(exp) {
			delete(s.peers, k)
		}
	}
	s.peers[key] = now.Add(s.peerTTL)
}

func (s *uotClientSession) isKnownPeer(addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	key := addr.String()
	now := time.Now()

	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	for k, exp := range s.peers {
		if now.After(exp) {
			delete(s.peers, k)
		}
	}
	exp, ok := s.peers[key]
	return ok && now.Before(exp)
}

func (s *uotClientSession) logUDPRoute(src *net.UDPAddr, destAddr string, match string, shouldProxy bool) {
	if strings.TrimSpace(destAddr) == "" {
		return
	}
	key := destAddr
	if shouldProxy {
		key = "PROXY|" + key
	} else {
		key = "DIRECT|" + key
	}

	now := time.Now()
	s.routeMu.Lock()
	for k, exp := range s.routeLog {
		if now.After(exp) {
			delete(s.routeLog, k)
		}
	}
	if exp, ok := s.routeLog[key]; ok && now.Before(exp) {
		s.routeMu.Unlock()
		return
	}
	s.routeLog[key] = now.Add(s.routeTTL)
	s.routeMu.Unlock()

	srcStr := "<unknown>"
	if src != nil {
		srcStr = src.String()
	}
	action := "PROXY"
	if !shouldProxy {
		action = "DIRECT"
	}
	actionText := action
	if action == "DIRECT" {
		actionText = logx.Bold(logx.Green(action))
	} else {
		actionText = logx.Bold(logx.Magenta(action))
	}
	matchText := logx.Yellow(match)
	logx.Infof("UDP", "%s --> %s match %s using %s", srcStr, destAddr, matchText, actionText)
}

func (s *uotClientSession) routeUDPTarget(destAddr string, destIP net.IP) (bool, string, *net.UDPAddr) {
	cfg := s.cfg
	if cfg == nil {
		return true, "CFG(nil)", nil
	}

	switch cfg.ProxyMode {
	case "global":
		return true, "MODE(global)", nil
	case "direct":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		addr, err := s.resolveDirectUDPAddr(ctx, destAddr, destIP, nil)
		if err != nil {
			return true, "MODE(direct)/RESOLVE_FAIL", nil
		}
		return false, "MODE(direct)", addr
	case "pac":
		if s.geoMgr == nil {
			return true, "PAC(no-rules)", nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		ok, m := s.geoMgr.MatchCN(destAddr, destIP)
		if ok {
			addr, err := s.resolveDirectUDPAddr(ctx, destAddr, destIP, nil)
			if err != nil {
				return true, m.String() + "/RESOLVE_FAIL", nil
			}
			if addr != nil && addr.IP != nil && addr.IP.To4() == nil && s.udpNet == "udp4" {
				return true, m.String() + "/IPV6->PROXY", nil
			}
			return false, m.String(), addr
		}

		host, port, err := net.SplitHostPort(destAddr)
		if err != nil {
			return true, "PAC/ADDR_INVALID", nil
		}

		if destIP != nil {
			return true, "PAC/NONE", nil
		}

		ips, err := lookupIPsWithCache(ctx, host)
		if err != nil || len(ips) == 0 {
			return true, "PAC/DNS_FAIL", nil
		}

		var directIP net.IP
		var directMatch geodata.Match
		for _, ip := range ips {
			if ok, m := s.geoMgr.MatchCN(destAddr, ip); ok {
				directIP = ip
				directMatch = m
				break
			}
		}
		if directIP == nil {
			return true, "PAC/NONE", nil
		}

		addr, err := s.resolveDirectUDPAddr(ctx, net.JoinHostPort(directIP.String(), port), directIP, directIP)
		if err != nil {
			return true, "DNS->" + directMatch.String() + "/RESOLVE_FAIL", nil
		}
		if addr != nil && addr.IP != nil && addr.IP.To4() == nil && s.udpNet == "udp4" {
			return true, "DNS->" + directMatch.String() + "/IPV6->PROXY", nil
		}
		return false, "DNS->" + directMatch.String(), addr
	default:
		return true, "MODE(unknown)", nil
	}
}

func (s *uotClientSession) resolveDirectUDPAddr(ctx context.Context, destAddr string, destIP net.IP, preferIP net.IP) (*net.UDPAddr, error) {
	if strings.TrimSpace(destAddr) == "" {
		return nil, fmt.Errorf("empty addr")
	}

	host, port, err := net.SplitHostPort(destAddr)
	if err != nil {
		return nil, err
	}
	portNum, err := net.LookupPort("udp", port)
	if err != nil {
		return nil, err
	}

	if preferIP != nil {
		return &net.UDPAddr{IP: normalizeIP(preferIP), Port: portNum}, nil
	}
	if destIP != nil {
		return &net.UDPAddr{IP: normalizeIP(destIP), Port: portNum}, nil
	}

	ips, err := lookupIPsWithCache(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("dns lookup failed: %w", err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no ip records")
	}

	var chosen net.IP
	for _, ip := range ips {
		if ip != nil && ip.To4() != nil {
			chosen = ip
			break
		}
	}
	if chosen == nil {
		chosen = ips[0]
	}
	if chosen == nil {
		return nil, fmt.Errorf("no ip")
	}
	return &net.UDPAddr{IP: normalizeIP(chosen), Port: portNum}, nil
}

func handleClientSocks4(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

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

	if _, err := readString(conn); err != nil {
		return
	}

	var destAddrStr string
	var destIP net.IP

	if ipBytes[0] == 0 && ipBytes[1] == 0 && ipBytes[2] == 0 && ipBytes[3] != 0 {
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
	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), destAddrStr, destIP, cfg, geoMgr, dialer)
	if !success {
		conn.Write([]byte{0x00, 0x5B, 0, 0, 0, 0, 0, 0})
		return
	}

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

func decodeSocks5UDPRequest(pkt []byte) (string, net.IP, []byte, error) {
	if len(pkt) < 4 {
		return "", nil, nil, fmt.Errorf("packet too short")
	}
	if pkt[2] != 0x00 {
		return "", nil, nil, fmt.Errorf("frag not supported")
	}

	reader := bytes.NewReader(pkt[3:])
	addrStr, _, _, err := protocol.ReadAddress(reader)
	if err != nil {
		return "", nil, nil, err
	}
	destIP := net.ParseIP(hostOnly(addrStr))
	payload := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", nil, nil, err
	}
	return addrStr, destIP, payload, nil
}

func buildUDPResponsePacket(addr string, payload []byte) []byte {
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00, 0x00, 0x00})
	if err := protocol.WriteAddress(buf, addr); err != nil {
		return nil
	}
	buf.Write(payload)
	return buf.Bytes()
}

func handleHTTP(conn net.Conn, cfg *config.Config, table *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer) {
	defer conn.Close()

	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}

	host := req.Host
	host = ensureHostPort(host, req.Method)

	destIP := net.ParseIP(hostOnly(host))

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), host, destIP, cfg, geoMgr, dialer)
	if !success {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}

	if req.Method == http.MethodConnect {
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		pipeConn(conn, targetConn)
	} else {
		req.RequestURI = ""
		if req.URL.Scheme != "" {
			req.URL.Scheme = ""
			req.URL.Host = ""
		}

		if err := req.Write(targetConn); err != nil {
			_ = targetConn.Close()

			retryable := req.Body == nil || req.Body == http.NoBody
			if retryable && (req.ContentLength <= 0) {
				if targetConn2, ok := dialTarget("TCP", conn.RemoteAddr(), host, destIP, cfg, geoMgr, dialer); ok {
					if err2 := req.Write(targetConn2); err2 == nil {
						pipeConn(conn, targetConn2)
						return
					}
					_ = targetConn2.Close()
				}
			}

			_, _ = conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
			return
		}
		pipeConn(conn, targetConn)
	}
}

func dialTarget(network string, src net.Addr, destAddrStr string, destIP net.IP, cfg *config.Config, geoMgr *geodata.Manager, dialer tunnel.Dialer) (net.Conn, bool) {
	shouldProxy := true
	directAddr := destAddrStr
	match := "NONE"

	switch cfg.ProxyMode {
	case "direct":
		shouldProxy = false
		match = "MODE(direct)"
	case "pac":
		if geoMgr == nil {
			break
		}

		if ok, m := geoMgr.MatchCN(destAddrStr, destIP); ok {
			shouldProxy = false
			match = m.String()
			break
		}

		if destIP == nil {
			host, port, err := net.SplitHostPort(destAddrStr)
			if err != nil {
				break
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			ips, lookupErr := lookupIPsWithCache(ctx, host)
			cancel()
			if lookupErr != nil || len(ips) == 0 {
				break
			}

			var directIP net.IP
			var directMatch geodata.Match
			for _, ip := range ips {
				if ok, m := geoMgr.MatchCN(destAddrStr, ip); ok {
					directIP = ip
					directMatch = m
					break
				}
			}
			if directIP != nil {
				shouldProxy = false
				directAddr = net.JoinHostPort(directIP.String(), port)
				match = "DNS->" + directMatch.String()
			}
		}
	default: // global
		shouldProxy = true
		match = "MODE(global)"
	}

	srcStr := "<unknown>"
	if src != nil {
		srcStr = src.String()
	}
	action := "PROXY"
	if !shouldProxy {
		action = "DIRECT"
	}
	matchText := match
	actionText := action
	if action == "DIRECT" {
		actionText = logx.Bold(logx.Green(action))
	} else {
		actionText = logx.Bold(logx.Magenta(action))
	}
	matchText = logx.Yellow(matchText)
	logx.Infof(strings.ToUpper(strings.TrimSpace(network)), "%s --> %s match %s using %s", srcStr, destAddrStr, matchText, actionText)

	if shouldProxy {
		conn, err := dialer.Dial(destAddrStr)
		if err != nil {
			logx.Warnf("Proxy", "Dial Failed: %v", err)
			return nil, false
		}
		return conn, true
	}

	dConn, err := directDial("tcp", directAddr, 5*time.Second)
	if err != nil {
		if directAddr != destAddrStr {
			dConn, err = directDial("tcp", destAddrStr, 5*time.Second)
		}
		if err != nil {
			logx.Warnf("Direct", "Dial Failed: %v", err)
			return nil, false
		}
	}
	return dConn, true
}

func defaultPortForMethod(method string) string {
	if method == http.MethodConnect {
		return "443"
	}
	return "80"
}

func ensureHostPort(host string, method string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}

	port := defaultPortForMethod(method)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
		if ip := net.ParseIP(inner); ip != nil {
			return net.JoinHostPort(ip.String(), port)
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return net.JoinHostPort(ip.String(), port)
	}
	if strings.Contains(host, ":") {
		// Likely a malformed IPv6 literal without brackets; keep as-is and let downstream fail fast.
		return host
	}
	return net.JoinHostPort(host, port)
}

func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	addr = strings.TrimPrefix(addr, "[")
	addr = strings.TrimSuffix(addr, "]")
	return addr
}
