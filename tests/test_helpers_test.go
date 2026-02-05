package tests

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
)

const (
	testAEAD        = "chacha20-poly1305"
	testASCII       = "prefer_ascii"
	testCustomTable = "xpxvvpvv"
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
		Timeout: 500 * time.Millisecond,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := noFollowClient.Get("http://" + reverseListen + prefix)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusPermanentRedirect && strings.HasSuffix(resp.Header.Get("Location"), prefix+"/") {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("reverse route not ready: %s", prefix)
}
