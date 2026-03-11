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
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
)

func TestHTTPProxy_Multiplex_LargeDownloads(t *testing.T) {
	downloadBytes := int64(8 << 20) // 8 MiB per download (tunable via env var)
	if v := strings.TrimSpace(os.Getenv("SUDOKU_MUX_DOWNLOAD_BYTES")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			t.Fatalf("invalid SUDOKU_MUX_DOWNLOAD_BYTES=%q", v)
		}
		downloadBytes = n
	}

	downloads := 6
	if v := strings.TrimSpace(os.Getenv("SUDOKU_MUX_DOWNLOAD_CONCURRENCY")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("invalid SUDOKU_MUX_DOWNLOAD_CONCURRENCY=%q", v)
		}
		downloads = n
	}

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(r.URL.Query().Get("id"))
		want := byte('a' + byte(id%26))

		size := downloadBytes
		if v := strings.TrimSpace(r.URL.Query().Get("bytes")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
				size = n
			}
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)

		chunk := make([]byte, 32*1024)
		for i := range chunk {
			chunk[i] = want
		}

		remaining := size
		for remaining > 0 {
			n := len(chunk)
			if remaining < int64(n) {
				n = int(remaining)
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			remaining -= int64(n)
		}
	}))
	defer origin.Close()

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(2)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]

	serverCfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          serverPort,
		FallbackAddr:       "",
		Key:                serverKey,
		AEAD:               testAEAD,
		SuspiciousAction:   "fallback",
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              testASCII,
		EnablePureDownlink: true,
		HTTPMask: config.HTTPMaskConfig{
			Disable: false,
			Mode:    "auto",
			TLS:     false,
		},
	}
	startSudokuServer(t, serverCfg)

	clientCfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          clientPort,
		ServerAddress:      fmt.Sprintf("127.0.0.1:%d", serverPort),
		Key:                clientKey,
		AEAD:               testAEAD,
		PaddingMin:         0,
		PaddingMax:         0,
		ASCII:              testASCII,
		EnablePureDownlink: true,
		ProxyMode:          "global",
		HTTPMask: config.HTTPMaskConfig{
			Disable:   false,
			Mode:      "stream",
			TLS:       false,
			Multiplex: "on",
		},
	}
	startSudokuClient(t, clientCfg)

	proxyURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", clientPort))
	if err != nil {
		t.Fatalf("proxy url: %v", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:              http.ProxyURL(proxyURL),
			DisableCompression: true,
			DisableKeepAlives:  true, // force a new upstream dial per request -> stresses HTTPMask tunnel dials + underlying HTTP reuse
		},
		Timeout: 90 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	startCh := make(chan struct{})
	errCh := make(chan error, downloads)

	var wg sync.WaitGroup
	wg.Add(downloads)
	for i := 0; i < downloads; i++ {
		id := i
		go func() {
			defer wg.Done()
			<-startCh

			req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL+fmt.Sprintf("/file?id=%d&bytes=%d", id, downloadBytes), nil)
			if err != nil {
				errCh <- err
				return
			}
			req.Header.Set("Connection", "close")

			resp, err := httpClient.Do(req)
			if err != nil {
				errCh <- err
				return
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
				errCh <- fmt.Errorf("bad status: %s (%s)", resp.Status, strings.TrimSpace(string(body)))
				return
			}
			if resp.ContentLength != downloadBytes {
				errCh <- fmt.Errorf("content-length mismatch: got %d want %d", resp.ContentLength, downloadBytes)
				return
			}

			want := byte('a' + byte(id%26))
			buf := make([]byte, 32*1024)
			var got int64
			for {
				n, err := resp.Body.Read(buf)
				if n > 0 {
					got += int64(n)
					for j := 0; j < n; j++ {
						if buf[j] != want {
							errCh <- fmt.Errorf("payload mismatch: id=%d at=%d got=%d want=%d", id, got-int64(n)+int64(j), buf[j], want)
							return
						}
					}
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					errCh <- err
					return
				}
			}
			if got != downloadBytes {
				errCh <- fmt.Errorf("download size mismatch: got %d want %d", got, downloadBytes)
				return
			}

			errCh <- nil
		}()
	}

	close(startCh)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("download failed: %v", err)
		}
	}
}
