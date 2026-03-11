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
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/geodata"
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

func TestDialTarget_PACAcceptsAnyMatchingIP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = w.Write([]byte("payload:\n  - '1.2.3.0/24'\n"))
	}))
	t.Cleanup(srv.Close)

	geoMgr := geodata.NewManager([]string{srv.URL})
	geoMgr.Update()

	oldLookup := lookupIPsWithCache
	oldDirectDial := directDial
	t.Cleanup(func() {
		lookupIPsWithCache = oldLookup
		directDial = oldDirectDial
	})

	lookupIPsWithCache = func(ctx context.Context, resolver *dnsutil.Resolver, host string) ([]net.IP, error) {
		return []net.IP{
			net.ParseIP("2001:db8::1"),
			net.ParseIP("1.2.3.4"),
		}, nil
	}

	var dialed string
	directDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		dialed = addr
		return NewMockConn(nil), nil
	}

	cfg := &config.Config{ProxyMode: "pac"}
	dialer := &MockDialer{
		DialFunc: func(destAddrStr string) (net.Conn, error) {
			t.Fatalf("unexpected proxy dial: %s", destAddrStr)
			return nil, nil
		},
	}

	_, ok := dialTarget("TCP", nil, "foo.example:443", nil, cfg, geoMgr, dialer, nil)
	if !ok {
		t.Fatalf("expected direct dial success")
	}
	if dialed != "1.2.3.4:443" {
		t.Fatalf("unexpected direct addr: %q", dialed)
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

	handleMixedConn(conn, cfg, table, nil, dialer, nil)

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

	handleMixedConn(conn, cfg, table, nil, dialer, nil)

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

	handleMixedConn(conn, cfg, table, nil, dialer, nil)

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

	handleMixedConn(conn, cfg, table, nil, dialer, nil)

	expectedTarget := "[2001:db8::1]:443"
	if target != expectedTarget {
		t.Errorf("HTTP IPv6 target mismatch: got %q, want %q", target, expectedTarget)
	}
}

func TestSelectUDPAssociateReplyIP_LoopbackPolicy(t *testing.T) {
	tests := []struct {
		name     string
		localIP  net.IP
		remoteIP net.IP
		want     string
	}{
		{
			name:     "drops loopback for loopback peers",
			localIP:  net.ParseIP("127.0.0.1"),
			remoteIP: net.ParseIP("127.0.0.1"),
			want:     "",
		},
		{
			name:     "drops loopback for non-loopback peers",
			localIP:  net.ParseIP("127.0.0.1"),
			remoteIP: net.ParseIP("172.19.0.2"),
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := selectUDPAssociateReplyIP(tc.localIP, tc.remoteIP)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected nil reply ip, got %v", got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("unexpected reply ip: got %v want %s", got, tc.want)
			}
		})
	}
}

func TestInitialUDPAssociateClientIP(t *testing.T) {
	tests := []struct {
		name string
		ip   net.IP
		want string
	}{
		{name: "nil stays nil", ip: nil, want: ""},
		{name: "loopback becomes nil", ip: net.ParseIP("127.0.0.1"), want: ""},
		{name: "private ipv4 is kept", ip: net.ParseIP("172.19.0.2"), want: "172.19.0.2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := initialUDPAssociateClientIP(tc.ip)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("unexpected client ip: got %v want %s", got, tc.want)
			}
		})
	}
}

func TestSelectUDPAssociateAdvertiseIP_UsesNonLoopbackFallback(t *testing.T) {
	oldNetworkInterfaces := networkInterfaces
	oldInterfaceAddrs := interfaceAddrs
	networkInterfaces = func() ([]net.Interface, error) {
		return []net.Interface{
			{Index: 1, Flags: net.FlagUp | net.FlagLoopback},
			{Index: 2, Flags: net.FlagUp | net.FlagPointToPoint},
			{Index: 3, Flags: net.FlagUp | net.FlagBroadcast},
		}, nil
	}
	interfaceAddrs = func(iface net.Interface) ([]net.Addr, error) {
		switch iface.Index {
		case 1:
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			}, nil
		case 2:
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("172.19.0.2"), Mask: net.CIDRMask(16, 32)},
			}, nil
		case 3:
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("10.0.0.148"), Mask: net.CIDRMask(24, 32)},
			}, nil
		default:
			return nil, nil
		}
	}
	t.Cleanup(func() {
		networkInterfaces = oldNetworkInterfaces
		interfaceAddrs = oldInterfaceAddrs
	})

	got := selectUDPAssociateAdvertiseIP(net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1"))
	if got == nil || got.String() != "10.0.0.148" {
		t.Fatalf("unexpected advertise ip: got %v want 10.0.0.148", got)
	}
}
