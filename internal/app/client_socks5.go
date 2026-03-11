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
package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/dnsutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/geodata"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

var networkInterfaces = net.Interfaces
var interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() }

func writeSocks5Reply(w io.Writer, rep byte) {
	// VER, REP, RSV, ATYP, BND.ADDR(IPv4=0.0.0.0), BND.PORT(0)
	_, _ = w.Write([]byte{0x05, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

func handleClientSocks5(conn net.Conn, cfg *config.Config, _ *sudoku.Table, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
	defer conn.Close()

	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(conn, buf[:nMethods]); err != nil {
		return
	}
	_, _ = conn.Write([]byte{0x05, 0x00}) // no auth

	header := make([]byte, 3)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}

	switch header[1] {
	case 0x01: // CONNECT
	case 0x03: // UDP ASSOCIATE
		handleSocks5UDPAssociate(conn, cfg, geoMgr, dialer, resolver)
		return
	default:
		writeSocks5Reply(conn, 0x07) // Command not supported
		return
	}

	destAddrStr, _, destIP, err := protocol.ReadAddress(conn)
	if err != nil {
		return
	}

	targetConn, success := dialTarget("TCP", conn.RemoteAddr(), destAddrStr, destIP, cfg, geoMgr, dialer, resolver)
	if !success {
		writeSocks5Reply(conn, 0x04) // Host unreachable
		return
	}

	writeSocks5Reply(conn, 0x00)
	connutil.PipeConn(conn, targetConn)
}

func handleSocks5UDPAssociate(ctrl net.Conn, cfg *config.Config, geoMgr *geodata.Manager, dialer tunnel.Dialer, resolver *dnsutil.Resolver) {
	uotDialer, ok := dialer.(tunnel.UoTDialer)
	if !ok {
		writeSocks5Reply(ctrl, 0x07)
		return
	}

	localIP := ipFromNetAddr(ctrl.LocalAddr())
	remoteIP := ipFromNetAddr(ctrl.RemoteAddr())
	replyIP := selectUDPAssociateAdvertiseIP(localIP, remoteIP)

	udpConn, udpNet, err := listenUDPAssociate(localIP, remoteIP, replyIP)
	if err != nil {
		writeSocks5Reply(ctrl, 0x01)
		return
	}

	uotConn, err := uotDialer.DialUDPOverTCP()
	if err != nil {
		_ = udpConn.Close()
		writeSocks5Reply(ctrl, 0x01)
		return
	}

	reply := buildUDPAssociateReply(replyIP, udpConn.LocalAddr().(*net.UDPAddr).Port, localIP, remoteIP)
	if err := connutil.WriteFull(ctrl, reply); err != nil {
		_ = udpConn.Close()
		_ = uotConn.Close()
		return
	}

	logx.Infof("SOCKS5/UDP", "Associate ready on %s -> %s", udpConn.LocalAddr().String(), cfg.ServerAddress)
	newUoTClientSession(ctrl, udpConn, uotConn, udpNet, cfg, geoMgr, resolver).run()
}

func buildUDPAssociateReply(host net.IP, port int, localIP net.IP, remoteIP net.IP) []byte {
	if host == nil {
		// Reply 0.0.0.0/:: to indicate "use the TCP endpoint host" (common SOCKS5 client behavior).
		host = net.IPv4zero
		if isIPv6Only(localIP, remoteIP) {
			host = net.IPv6unspecified
		}
	}

	out := make([]byte, 0, 32)
	out = append(out, 0x05, 0x00, 0x00)

	if ip4 := host.To4(); ip4 != nil {
		out = append(out, 0x01)
		out = append(out, ip4...)
	} else {
		out = append(out, 0x04)
		out = append(out, host.To16()...)
	}

	var portBytes [2]byte
	binary.BigEndian.PutUint16(portBytes[:], uint16(port))
	out = append(out, portBytes[:]...)
	return out
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
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)

	// Best-effort family match with the TCP control peer.
	if remoteIP != nil {
		if remoteIP.To4() != nil {
			return "udp4", net.IPv4zero
		}
		return "udp6", net.IPv6unspecified
	}

	// Unknown peer family: try dual-stack first when possible.
	if isIPv6Only(localIP, remoteIP) {
		return "udp6", net.IPv6unspecified
	}
	return "udp", nil
}

func listenUDPAssociate(localIP net.IP, remoteIP net.IP, bindIP net.IP) (*net.UDPConn, string, error) {
	bindIP = normalizeIP(bindIP)
	if bindIP != nil && !bindIP.IsUnspecified() {
		udpNet := "udp6"
		if bindIP.To4() != nil {
			udpNet = "udp4"
		}
		c, err := net.ListenUDP(udpNet, &net.UDPAddr{IP: bindIP, Port: 0})
		return c, udpNet, err
	}

	// Prefer a dual-stack socket when possible (and safe), then fall back to a family-specific one.
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: nil, Port: 0})
	if err == nil && udpConn != nil {
		la, _ := udpConn.LocalAddr().(*net.UDPAddr)
		laIP := net.IP(nil)
		if la != nil {
			laIP = normalizeIP(la.IP)
		}
		peerIP := normalizeIP(remoteIP)

		// Avoid surprising family mismatch on platforms where "udp" may bind IPv6-only sockets by default.
		if peerIP != nil && laIP != nil {
			if peerIP.To4() != nil && laIP.To4() == nil {
				_ = udpConn.Close()
				udpConn = nil
			}
			if peerIP.To4() == nil && laIP.To4() != nil {
				_ = udpConn.Close()
				udpConn = nil
			}
		}

		if udpConn != nil {
			return udpConn, "udp", nil
		}
	}

	udpNet, udpBindIP := udpNetworkAndBindIP(localIP, remoteIP)
	c, err := net.ListenUDP(udpNet, &net.UDPAddr{IP: udpBindIP, Port: 0})
	return c, udpNet, err
}

func selectUDPAssociateReplyIP(localIP net.IP, remoteIP net.IP) net.IP {
	localIP = normalizeIP(localIP)
	remoteIP = normalizeIP(remoteIP)

	if localIP == nil || localIP.IsUnspecified() {
		return nil
	}
	if localIP.IsLoopback() {
		// Prefer 0.0.0.0/:: for loopback-bound control sessions so clients reuse
		// the TCP endpoint host instead of being pinned to 127.0.0.1.
		return nil
	}
	if !localIP.IsGlobalUnicast() {
		return nil
	}
	if localIP.IsPrivate() && (remoteIP == nil || !remoteIP.IsPrivate()) {
		return nil
	}
	return localIP
}

func selectUDPAssociateAdvertiseIP(localIP net.IP, remoteIP net.IP) net.IP {
	if ip := selectUDPAssociateReplyIP(localIP, remoteIP); ip != nil {
		return ip
	}
	localIP = normalizeIP(localIP)
	if localIP == nil || !localIP.IsLoopback() {
		return nil
	}
	return firstGlobalUnicastIPv4()
}

func firstGlobalUnicastIPv4() net.IP {
	ifaces, err := networkInterfaces()
	if err != nil {
		return nil
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := interfaceAddrs(iface)
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet == nil || ipNet.IP == nil {
				continue
			}
			ip := normalizeIP(ipNet.IP)
			if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			if !ip.IsGlobalUnicast() {
				continue
			}
			return ip
		}
	}
	return nil
}

func initialUDPAssociateClientIP(ip net.IP) net.IP {
	ip = normalizeIP(ip)
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
		return nil
	}
	return ip
}

type uotClientSession struct {
	ctrlConn net.Conn
	udpConn  *net.UDPConn
	uotConn  net.Conn
	udpNet   string
	cfg      *config.Config
	geoMgr   *geodata.Manager
	resolver *dnsutil.Resolver

	closeOnce sync.Once
	closed    chan struct{}

	clientAddrMu sync.RWMutex
	clientAddr   *net.UDPAddr

	allowedClientIP net.IP
	peerTTL         time.Duration
	peers           ttlSet
	logTTL          time.Duration
	logOnce         ttlSet
}

func newUoTClientSession(ctrl net.Conn, udpConn *net.UDPConn, uotConn net.Conn, udpNet string, cfg *config.Config, geoMgr *geodata.Manager, resolver *dnsutil.Resolver) *uotClientSession {
	return &uotClientSession{
		ctrlConn:        ctrl,
		udpConn:         udpConn,
		uotConn:         uotConn,
		udpNet:          udpNet,
		cfg:             cfg,
		geoMgr:          geoMgr,
		resolver:        resolver,
		closed:          make(chan struct{}),
		allowedClientIP: initialUDPAssociateClientIP(ipFromNetAddr(ctrl.RemoteAddr())),
		peerTTL:         2 * time.Minute,
		peers:           newTTLSet(256),
		logTTL:          30 * time.Second,
		logOnce:         newTTLSet(1024),
	}
}

func (s *uotClientSession) run() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); s.consumeControl() }()
	go func() { defer wg.Done(); s.pipeClientToServer() }()
	go func() { defer wg.Done(); s.pipeServerToClient() }()
	wg.Wait()
	s.close()
}

func (s *uotClientSession) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.udpConn.Close()
		_ = s.uotConn.Close()
		_ = s.ctrlConn.Close()
	})
}

func (s *uotClientSession) consumeControl() {
	_, _ = io.Copy(io.Discard, s.ctrlConn)
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
		srcIP := normalizeIP(addr.IP)

		if addr != nil && s.allowedClientIP != nil && !s.allowedClientIP.Equal(srcIP) {
			if !s.peers.Has(peerKey(addr)) {
				continue
			}
			clientAddr := s.getClientAddr()
			if clientAddr == nil {
				continue
			}
			if resp := buildUDPResponsePacket(udpAddrString(addr), buf[:n]); resp != nil {
				if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
					s.close()
					return
				}
			}
			continue
		}

		destAddr, destIP, payload, err := decodeSocks5UDPRequest(buf[:n])
		if err != nil {
			continue
		}
		if s.allowedClientIP == nil && srcIP != nil {
			s.allowedClientIP = srcIP
		}
		s.setClientAddr(addr)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		decision := decideRoute(ctx, s.cfg, s.geoMgr, destAddr, destIP, s.resolver)
		shouldProxy, match, actionKey := decision.shouldProxy, decision.match, "PROXY"

		if !shouldProxy {
			directAddr, err := resolveUDPAddr(ctx, decision.directAddr, s.resolver)
			if err != nil {
				shouldProxy, match = true, match+"/RESOLVE_FAIL"
			} else if directAddr != nil && directAddr.IP != nil && directAddr.IP.To4() == nil && s.udpNet == "udp4" {
				shouldProxy, match = true, match+"/IPV6->PROXY"
			} else if directAddr != nil {
				actionKey = "DIRECT"
				s.peers.Add(peerKey(directAddr), s.peerTTL)
				if _, err := s.udpConn.WriteToUDP(payload, directAddr); err != nil {
					s.close()
					cancel()
					return
				}
			}
		}

		if shouldProxy {
			if err := tunnel.WriteUoTDatagram(s.uotConn, destAddr, payload); err != nil {
				s.close()
				cancel()
				return
			}
		}

		if s.logOnce.Allow(actionKey+"|"+destAddr, s.logTTL) {
			logRoute("UDP", addr, destAddr, match, shouldProxy)
		}
		cancel()
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
		if resp := buildUDPResponsePacket(addrStr, payload); resp != nil {
			if _, err := s.udpConn.WriteToUDP(resp, clientAddr); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *uotClientSession) setClientAddr(addr *net.UDPAddr) {
	if addr == nil {
		return
	}
	s.clientAddrMu.Lock()
	defer s.clientAddrMu.Unlock()
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

type ttlSet struct {
	mu         sync.Mutex
	m          map[string]time.Time
	maxEntries int
}

func newTTLSet(maxEntries int) ttlSet {
	if maxEntries <= 0 {
		maxEntries = 1024
	}
	return ttlSet{m: make(map[string]time.Time), maxEntries: maxEntries}
}

func (s *ttlSet) pruneLocked(now time.Time) {
	if len(s.m) < s.maxEntries {
		return
	}
	for k, exp := range s.m {
		if now.After(exp) {
			delete(s.m, k)
		}
	}
}

func (s *ttlSet) Add(key string, ttl time.Duration) {
	key = strings.TrimSpace(key)
	if key == "" || ttl <= 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	s.m[key] = now.Add(ttl)
}

func (s *ttlSet) Has(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.m[key]
	if !ok || now.After(exp) {
		delete(s.m, key)
		return false
	}
	return true
}

func (s *ttlSet) Allow(key string, ttl time.Duration) bool {
	key = strings.TrimSpace(key)
	if key == "" || ttl <= 0 {
		return false
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if exp, ok := s.m[key]; ok && now.Before(exp) {
		return false
	}
	s.m[key] = now.Add(ttl)
	return true
}

func peerKey(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	cpy := *addr
	cpy.IP = normalizeIP(cpy.IP)
	return cpy.String()
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

func udpAddrString(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	ip := normalizeIP(addr.IP)
	if ip == nil {
		return addr.String()
	}
	return net.JoinHostPort(ip.String(), fmt.Sprintf("%d", addr.Port))
}
