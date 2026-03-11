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
	"io"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func BenchmarkHTTPMaskTunnel_Stream(b *testing.B) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "bench-key-stream"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}
	addr, stop := startHTTPMaskTunnelEchoServer(b, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "stream",
	}

	msg := []byte("ping")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, err := apis.Dial(ctx, clientCfg)
		if err != nil {
			cancel()
			b.Fatalf("dial: %v", err)
		}
		_, _ = conn.Write(msg)
		buf := make([]byte, len(msg))
		_, _ = io.ReadFull(conn, buf)
		_ = conn.Close()
		cancel()
	}
}

func BenchmarkHTTPMaskTunnel_Poll(b *testing.B) {
	table := sudoku.NewTable("seed", "prefer_ascii")
	key := "bench-key-poll"

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
		HTTPMaskMode:            "auto",
	}
	addr, stop := startHTTPMaskTunnelEchoServer(b, serverCfg)
	defer stop()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      addr,
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Table:              table,
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
		HTTPMaskMode:       "poll",
	}

	msg := []byte("ping")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conn, err := apis.Dial(ctx, clientCfg)
		if err != nil {
			cancel()
			b.Fatalf("dial: %v", err)
		}
		_, _ = conn.Write(msg)
		buf := make([]byte, len(msg))
		_, _ = io.ReadFull(conn, buf)
		_ = conn.Close()
		cancel()
	}
}
