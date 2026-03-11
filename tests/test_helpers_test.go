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
package tests

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
)

const (
	testAEAD         = "chacha20-poly1305"
	testASCII        = "prefer_ascii"
	testCustomTable  = "xpxvvpvv"
	reverseReadyWait = 25 * time.Second
	reverseProbeWait = 5 * time.Second
)

func newTestKeys(t testing.TB) (serverKey, clientKey string) {
	t.Helper()
	pair, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("keygen failed: %v", err)
	}
	return crypto.EncodePoint(pair.Public), crypto.EncodeScalar(pair.Private)
}

func newTestServerConfig(port int, serverKey string) *config.Config {
	return &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          port,
		FallbackAddr:       "",
		Key:                serverKey,
		AEAD:               testAEAD,
		SuspiciousAction:   "fallback",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              testASCII,
		CustomTable:        testCustomTable,
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
	}
}

func newTestClientConfig(port int, serverAddr, clientKey string) *config.Config {
	return &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          port,
		ServerAddress:      serverAddr,
		Key:                clientKey,
		AEAD:               testAEAD,
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              testASCII,
		CustomTable:        testCustomTable,
		EnablePureDownlink: true,
		ProxyMode:          "direct",
		HTTPMask: config.HTTPMaskConfig{
			Disable: true,
		},
	}
}

func localServerAddr(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func waitForReverseRouteReady(t testing.TB, reverseListen, prefix string) {
	t.Helper()
	if prefix == "" || prefix == "/" {
		return
	}

	noFollowClient := &http.Client{
		Timeout: reverseProbeWait,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	deadline := time.Now().Add(reverseReadyWait)
	var lastErr error
	lastStatus := 0
	for time.Now().Before(deadline) {
		resp, err := noFollowClient.Get("http://" + reverseListen + prefix)
		if err == nil {
			lastStatus = resp.StatusCode
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusPermanentRedirect && strings.HasSuffix(resp.Header.Get("Location"), prefix+"/") {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("reverse route not ready: %s (last status=%d, last err=%v)", prefix, lastStatus, lastErr)
	}
	t.Fatalf("reverse route not ready: %s (last status=%d)", prefix, lastStatus)
}

func waitForReverseTCPRouteReady(t testing.TB, reverseListen string, probe func(net.Conn) error) {
	t.Helper()

	deadline := time.Now().Add(reverseReadyWait)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", reverseListen, 250*time.Millisecond)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}

		_ = conn.SetDeadline(time.Now().Add(reverseProbeWait))
		err = probe(conn)
		_ = conn.Close()
		if err == nil {
			return
		}

		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}

	if lastErr != nil {
		t.Fatalf("reverse tcp route not ready: %s: %v", reverseListen, lastErr)
	}
	t.Fatalf("reverse tcp route not ready: %s", reverseListen)
}

func cookieHeaderValue(cookies []*http.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c == nil || c.Name == "" {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}
