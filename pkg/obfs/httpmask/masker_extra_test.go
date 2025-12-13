package httpmask

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
)

func TestWriteRandomRequestHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteRandomRequestHeader(&buf, "example.com"); err != nil {
		t.Fatalf("WriteRandomRequestHeader error: %v", err)
	}
	raw := buf.String()
	if !(strings.HasPrefix(raw, "POST ") || strings.HasPrefix(raw, "GET ")) {
		t.Fatalf("invalid request line: %q", raw)
	}
	if !strings.Contains(raw, "Host: example.com") {
		t.Fatalf("missing host header")
	}
	if !strings.Contains(raw, "\r\n\r\n") {
		t.Fatalf("missing header terminator")
	}
}

func TestConsumeHeader(t *testing.T) {
	req := "POST /test HTTP/1.1\r\nHost: a\r\n\r\nBODY"
	r := bufio.NewReader(strings.NewReader(req))
	consumed, err := ConsumeHeader(r)
	if err != nil {
		t.Fatalf("ConsumeHeader error: %v", err)
	}
	if string(consumed) != "POST /test HTTP/1.1\r\nHost: a\r\n\r\n" {
		t.Fatalf("unexpected consumed data: %q", string(consumed))
	}
	rest, _ := r.ReadString('\n')
	if rest != "BODY" {
		t.Fatalf("body not left in reader, got %q", rest)
	}
}
