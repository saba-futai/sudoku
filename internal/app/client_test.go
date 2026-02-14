package app

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// MockConn implements net.Conn for testing
type MockConn struct {
	ReadBuf  *bytes.Buffer
	WriteBuf *bytes.Buffer
	Closed   bool
}

func NewMockConn(data []byte) *MockConn {
	return &MockConn{
		ReadBuf:  bytes.NewBuffer(data),
		WriteBuf: new(bytes.Buffer),
	}
}

func (m *MockConn) Read(b []byte) (n int, err error) {
	return m.ReadBuf.Read(b)
}

func (m *MockConn) Write(b []byte) (n int, err error) {
	return m.WriteBuf.Write(b)
}

func (m *MockConn) Close() error {
	m.Closed = true
	return nil
}

func (m *MockConn) LocalAddr() net.Addr                { return nil }
func (m *MockConn) RemoteAddr() net.Addr               { return nil }
func (m *MockConn) SetDeadline(t time.Time) error      { return nil }
func (m *MockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *MockConn) SetWriteDeadline(t time.Time) error { return nil }

// MockDialer implements tunnel.Dialer
type MockDialer struct {
	DialFunc func(destAddrStr string) (net.Conn, error)
}

func (m *MockDialer) Dial(destAddrStr string) (net.Conn, error) {
	if m.DialFunc != nil {
		return m.DialFunc(destAddrStr)
	}
	return NewMockConn(nil), nil
}

func TestDNSCache(t *testing.T) {
	cache := &DNSCache{
		cache: make(map[string]dnsCacheEntry),
		ttl:   100 * time.Millisecond,
	}

	host := "example.com"
	ip := net.ParseIP("1.2.3.4")

	// Test Set & Get
	cache.Set(host, ip)
	got := cache.Lookup(host)
	if !got.Equal(ip) {
		t.Errorf("Cache lookup failed: got %v, want %v", got, ip)
	}

	// Test Expiration
	time.Sleep(150 * time.Millisecond)
	got = cache.Lookup(host)
	if got != nil {
		t.Errorf("Cache expiration failed: got %v, want nil", got)
	}
}

func TestDNSCache_RefreshKeepsLatestValue(t *testing.T) {
	cache := &DNSCache{
		cache: make(map[string]dnsCacheEntry),
		ttl:   120 * time.Millisecond,
	}

	host := "example.com"
	ip1 := net.ParseIP("1.2.3.4")
	ip2 := net.ParseIP("5.6.7.8")

	cache.Set(host, ip1)
	time.Sleep(70 * time.Millisecond)
	cache.Set(host, ip2)

	time.Sleep(70 * time.Millisecond)
	got := cache.Lookup(host)
	if got == nil || !got.Equal(ip2) {
		t.Fatalf("expected refreshed ip %v, got %v", ip2, got)
	}
}

func TestHandleMixedConn_SOCKS4(t *testing.T) {
	// Construct SOCKS4 Connect Request
	// VN(4) | CD(1) | PORT(80) | IP(1.2.3.4) | USERID("user") | NULL
	buf := new(bytes.Buffer)
	buf.WriteByte(0x04)
	buf.WriteByte(0x01)
	binary.Write(buf, binary.BigEndian, uint16(80))
	buf.Write([]byte{1, 2, 3, 4})
	buf.WriteString("user")
	buf.WriteByte(0x00)

	conn := NewMockConn(buf.Bytes())
	cfg := &config.Config{ProxyMode: "global"}
	table := sudoku.NewTable("key", "prefer_entropy")

	// Mock Dialer to capture target
	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	handleMixedConn(conn, cfg, table, nil, dialer)

	// Verify Target
	expectedTarget := "1.2.3.4:80"
	if target != expectedTarget {
		t.Errorf("SOCKS4 target mismatch: got %q, want %q", target, expectedTarget)
	}

	// Verify Response (90 = Granted)
	// VN(0) | CD(90) | ...
	resp := conn.WriteBuf.Bytes()
	if len(resp) < 2 || resp[1] != 0x5A {
		t.Errorf("SOCKS4 response invalid: %v", resp)
	}
}

func TestHandleMixedConn_SOCKS5(t *testing.T) {
	// 1. Handshake: VER(5) | NMETHODS(1) | METHOD(0)
	// 2. Request: VER(5) | CMD(1) | RSV(0) | ATYP(1) | IP(1.2.3.4) | PORT(80)
	input := []byte{
		0x05, 0x01, 0x00,
		0x05, 0x01, 0x00, 0x01, 1, 2, 3, 4, 0, 80,
	}
	conn := NewMockConn(input)
	cfg := &config.Config{ProxyMode: "global"}
	table := sudoku.NewTable("key", "prefer_entropy")

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	handleMixedConn(conn, cfg, table, nil, dialer)

	expectedTarget := "1.2.3.4:80"
	if target != expectedTarget {
		t.Errorf("SOCKS5 target mismatch: got %q, want %q", target, expectedTarget)
	}
}

func TestHandleMixedConn_HTTP(t *testing.T) {
	reqStr := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}
	table := sudoku.NewTable("key", "prefer_entropy")

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	handleMixedConn(conn, cfg, table, nil, dialer)

	expectedTarget := "example.com:443"
	if target != expectedTarget {
		t.Errorf("HTTP target mismatch: got %q, want %q", target, expectedTarget)
	}
}

func TestHandleMixedConn_HTTPIPv6HostNoPort(t *testing.T) {
	reqStr := "CONNECT [2001:db8::1] HTTP/1.1\r\nHost: [2001:db8::1]\r\n\r\n"
	conn := NewMockConn([]byte(reqStr))
	cfg := &config.Config{ProxyMode: "global"}
	table := sudoku.NewTable("key", "prefer_entropy")

	var target string
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			target = destAddrStr
			return NewMockConn(nil), nil
		},
	}

	handleMixedConn(conn, cfg, table, nil, dialer)

	expectedTarget := "[2001:db8::1]:443"
	if target != expectedTarget {
		t.Errorf("HTTP IPv6 target mismatch: got %q, want %q", target, expectedTarget)
	}
}
