package tunnel

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestPipeConn_HalfCloseDoesNotTruncate(t *testing.T) {
	lnA, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen A: %v", err)
	}
	t.Cleanup(func() { _ = lnA.Close() })

	lnB, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen B: %v", err)
	}
	t.Cleanup(func() { _ = lnB.Close() })

	aCh := make(chan net.Conn, 1)
	bCh := make(chan net.Conn, 1)
	go func() {
		c, err := lnA.Accept()
		if err == nil {
			aCh <- c
		}
	}()
	go func() {
		c, err := lnB.Accept()
		if err == nil {
			bCh <- c
		}
	}()

	clientConn, err := net.Dial("tcp", lnA.Addr().String())
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })
	clientTCP, ok := clientConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("client conn is not TCP")
	}

	serverConn, err := net.Dial("tcp", lnB.Addr().String())
	if err != nil {
		t.Fatalf("dial server: %v", err)
	}
	t.Cleanup(func() { _ = serverConn.Close() })
	serverTCP, ok := serverConn.(*net.TCPConn)
	if !ok {
		t.Fatalf("server conn is not TCP")
	}

	pipeA := <-aCh
	pipeB := <-bCh
	t.Cleanup(func() { _ = pipeA.Close() })
	t.Cleanup(func() { _ = pipeB.Close() })

	go pipeConn(pipeA, pipeB)

	deadline := time.Now().Add(2 * time.Second)
	_ = clientConn.SetDeadline(deadline)
	_ = serverConn.SetDeadline(deadline)

	req := []byte("hello")
	resp := bytes.Repeat([]byte("world"), 1024)

	serverDone := make(chan error, 1)
	go func() {
		defer close(serverDone)
		buf := make([]byte, len(req))
		if _, err := io.ReadFull(serverConn, buf); err != nil {
			serverDone <- err
			return
		}
		if !bytes.Equal(buf, req) {
			serverDone <- io.ErrUnexpectedEOF
			return
		}
		if _, err := serverConn.Write(resp); err != nil {
			serverDone <- err
			return
		}
		_ = serverTCP.CloseWrite()
		serverDone <- nil
	}()

	if _, err := clientConn.Write(req); err != nil {
		t.Fatalf("client write: %v", err)
	}
	_ = clientTCP.CloseWrite()

	got := make([]byte, len(resp))
	if _, err := io.ReadFull(clientConn, got); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if !bytes.Equal(got, resp) {
		t.Fatalf("response mismatch")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server side: %v", err)
	}
}
