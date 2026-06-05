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
package tunnel

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestMuxSession_Echo(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)

		msg, err := ReadKIPMessage(serverConn)
		if err != nil {
			return
		}
		if msg.Type != KIPTypeStartMux {
			return
		}
		sess := newMuxSession(serverConn, func(stream *muxStream, _ []byte) {
			_, _ = io.Copy(stream, stream)
		})
		<-sess.closed
	}()

	if err := WriteKIPMessage(clientConn, KIPTypeStartMux, nil); err != nil {
		t.Fatalf("start mux: %v", err)
	}
	mux, err := NewMuxClient(clientConn)
	if err != nil {
		t.Fatalf("NewMuxClient: %v", err)
	}
	defer mux.Close()

	stream, err := mux.Dial("example.com:80")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer stream.Close()

	msg := []byte("hello mux")
	if _, err := stream.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: got %q want %q", buf, msg)
	}

	_ = mux.Close()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("server did not exit: %v", ctx.Err())
	}
}

func TestMuxStream_EnqueueBackpressure(t *testing.T) {
	st := newMuxStream(nil, 1)
	payload := make([]byte, muxMaxDataPayload)

	for queued := 0; queued < muxMaxQueuedBytesPerStream; queued += len(payload) {
		st.enqueue(payload)
	}

	done := make(chan struct{})
	go func() {
		st.enqueue(payload)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("enqueue completed while stream was at queued byte limit")
	case <-time.After(50 * time.Millisecond):
	}

	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(st, buf); err != nil {
		t.Fatalf("read: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("enqueue did not resume after reader consumed capacity")
	}
}

func TestMuxStream_EnqueueBackpressureUnblocksOnClose(t *testing.T) {
	st := newMuxStream(nil, 1)
	payload := make([]byte, muxMaxDataPayload)

	for queued := 0; queued < muxMaxQueuedBytesPerStream; queued += len(payload) {
		st.enqueue(payload)
	}

	done := make(chan struct{})
	go func() {
		st.enqueue(payload)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("enqueue completed while stream was at queued byte limit")
	case <-time.After(50 * time.Millisecond):
	}

	st.closeNoSend(io.ErrClosedPipe)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("enqueue did not unblock after stream close")
	}
}
