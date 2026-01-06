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

		// Simulate server-side first-byte dispatch (magic already consumed by caller).
		var magic [1]byte
		if _, err := io.ReadFull(serverConn, magic[:]); err != nil {
			return
		}
		if magic[0] != MuxMagicByte {
			return
		}
		var ver [1]byte
		if _, err := io.ReadFull(serverConn, ver[:]); err != nil {
			return
		}
		if ver[0] != muxVersion {
			return
		}

		sess := newMuxSession(serverConn, func(stream *muxStream, _ []byte) {
			_, _ = io.Copy(stream, stream)
		})
		<-sess.closed
	}()

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
