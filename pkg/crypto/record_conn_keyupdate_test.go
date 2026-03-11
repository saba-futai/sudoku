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
package crypto

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

func TestRecordConn_KeyUpdate_FullDuplex(t *testing.T) {
	old := atomic.LoadInt64(&KeyUpdateAfterBytes)
	atomic.StoreInt64(&KeyUpdateAfterBytes, 64*1024) // force many epochs quickly
	t.Cleanup(func() { atomic.StoreInt64(&KeyUpdateAfterBytes, old) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	acceptCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			acceptCh <- c
		}
	}()

	clientRaw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = clientRaw.Close() })

	serverRaw := <-acceptCh
	t.Cleanup(func() { _ = serverRaw.Close() })

	deadline := time.Now().Add(10 * time.Second)
	_ = clientRaw.SetDeadline(deadline)
	_ = serverRaw.SetDeadline(deadline)

	pskC2S := sha256.Sum256([]byte("psk-c2s"))
	pskS2C := sha256.Sum256([]byte("psk-s2c"))

	rcClient, err := NewRecordConn(clientRaw, "chacha20-poly1305", pskC2S[:], pskS2C[:])
	if err != nil {
		t.Fatalf("client new: %v", err)
	}
	rcServer, err := NewRecordConn(serverRaw, "chacha20-poly1305", pskS2C[:], pskC2S[:])
	if err != nil {
		t.Fatalf("server new: %v", err)
	}

	// Rekey once to mimic the post-handshake session rekey path.
	sessC2S := sha256.Sum256([]byte("sess-c2s"))
	sessS2C := sha256.Sum256([]byte("sess-s2c"))
	if err := rcClient.Rekey(sessC2S[:], sessS2C[:]); err != nil {
		t.Fatalf("client rekey: %v", err)
	}
	if err := rcServer.Rekey(sessS2C[:], sessC2S[:]); err != nil {
		t.Fatalf("server rekey: %v", err)
	}

	serverErr := make(chan error, 1)
	go func() {
		defer close(serverErr)
		buf := make([]byte, 32*1024)
		for {
			n, err := rcServer.Read(buf)
			if n > 0 {
				if _, werr := rcServer.Write(buf[:n]); werr != nil {
					serverErr <- werr
					return
				}
			}
			if err != nil {
				if err == io.EOF {
					_ = rcServer.CloseWrite()
					serverErr <- nil
					return
				}
				serverErr <- err
				return
			}
		}
	}()

	const totalBytes = 2 * 1024 * 1024 // 2 MiB
	writeH := sha256.New()
	readH := sha256.New()

	readDone := make(chan error, 1)
	go func() {
		defer close(readDone)
		buf := make([]byte, 32*1024)
		var remain int64 = totalBytes
		for remain > 0 {
			n := int64(len(buf))
			if n > remain {
				n = remain
			}
			_, err := io.ReadFull(rcClient, buf[:n])
			if err != nil {
				readDone <- err
				return
			}
			_, _ = readH.Write(buf[:n])
			remain -= n
		}
		readDone <- nil
	}()

	rng := rand.New(rand.NewSource(1))
	buf := make([]byte, 32*1024)
	var remain int64 = totalBytes
	for remain > 0 {
		n := int64(len(buf))
		if n > remain {
			n = remain
		}
		_, _ = rng.Read(buf[:n])
		_, _ = writeH.Write(buf[:n])
		if err := connutil.WriteFull(rcClient, buf[:n]); err != nil {
			t.Fatalf("client write: %v", err)
		}
		remain -= n
	}
	_ = rcClient.CloseWrite()

	if err := <-readDone; err != nil {
		t.Fatalf("client read: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server echo: %v", err)
	}
	if !bytes.Equal(writeH.Sum(nil), readH.Sum(nil)) {
		t.Fatalf("hash mismatch")
	}
}
