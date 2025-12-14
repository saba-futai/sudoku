package httpmask

import (
	"bufio"
	"strings"
	"testing"
)

func TestConsumeHeader_ReturnsConsumedData(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name: "Valid POST request",
			input: "POST /api/v1/upload HTTP/1.1\r\n" +
				"Host: example.com\r\n" +
				"User-Agent: Go-Test\r\n" +
				"\r\n" +
				"Body data",
			wantErr: false,
		},
		{
			name: "Valid GET request",
			input: "GET /ws HTTP/1.1\r\n" +
				"Host: example.com\r\n" +
				"\r\n",
			wantErr: false,
		},
		{
			name: "Invalid method",
			input: "BREW / HTTP/1.1\r\n" +
				"Host: example.com\r\n" +
				"\r\n",
			wantErr: true,
		},
		{
			name:    "Garbage data",
			input:   "NotHTTPData\r\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.input))
			consumed, err := ConsumeHeader(r)

			if (err != nil) != tt.wantErr {
				t.Errorf("ConsumeHeader() error = %v, wantErr %v", err, tt.wantErr)
			}

			// Verify consumed data matches the beginning of input
			if !strings.HasPrefix(tt.input, string(consumed)) {
				t.Errorf("ConsumeHeader() consumed data mismatch.\nGot: %q\nInput starts with: %q", consumed, tt.input[:len(consumed)])
			}

			// If success, verify we consumed up to the empty line
			if !tt.wantErr {
				expectedHeaderEnd := "\r\n\r\n"
				if !strings.Contains(string(consumed), expectedHeaderEnd) {
					t.Errorf("ConsumeHeader() did not consume full header. Got: %q", consumed)
				}
			}
		})
	}
}
