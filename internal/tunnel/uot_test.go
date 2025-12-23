package tunnel

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestUoTDatagram_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	addr := "example.com:53"
	payload := []byte("hello uot")

	if err := WriteUoTDatagram(&buf, addr, payload); err != nil {
		t.Fatalf("WriteUoTDatagram error: %v", err)
	}
	gotAddr, gotPayload, err := ReadUoTDatagram(&buf)
	if err != nil {
		t.Fatalf("ReadUoTDatagram error: %v", err)
	}
	if gotAddr != addr {
		t.Fatalf("addr mismatch: got %q want %q", gotAddr, addr)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Fatalf("payload mismatch: got %q want %q", gotPayload, payload)
	}
}

func TestReadUoTDatagram_InvalidAddrLen(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	// addrLen=0 => invalid
	binary.BigEndian.PutUint16(header[:2], 0)
	binary.BigEndian.PutUint16(header[2:], 1)
	_, _ = buf.Write(header)
	_, _ = buf.Write([]byte{0x00})

	if _, _, err := ReadUoTDatagram(&buf); err == nil {
		t.Fatalf("expected error")
	}
}

func TestReadUoTDatagram_Truncated(t *testing.T) {
	var buf bytes.Buffer
	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[:2], 3) // addrLen
	binary.BigEndian.PutUint16(header[2:], 2) // payloadLen
	_, _ = buf.Write(header)
	_, _ = buf.Write([]byte{0x01, 0x02}) // truncated addr

	if _, _, err := ReadUoTDatagram(&buf); err == nil {
		t.Fatalf("expected error")
	}
}

func TestHandleUoTServer_BadVersion(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	errCh := make(chan error, 1)
	go func() { errCh <- HandleUoTServer(server) }()

	_, _ = client.Write([]byte{0xFF})
	if err := <-errCh; err == nil {
		t.Fatalf("expected error")
	}
}
