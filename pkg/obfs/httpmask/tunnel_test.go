package httpmask

import "testing"

func TestCanonicalHeaderHost(t *testing.T) {
	tests := []struct {
		name     string
		urlHost  string
		scheme   string
		wantHost string
	}{
		{name: "https default port strips", urlHost: "example.com:443", scheme: "https", wantHost: "example.com"},
		{name: "http default port strips", urlHost: "example.com:80", scheme: "http", wantHost: "example.com"},
		{name: "non-default port keeps", urlHost: "example.com:8443", scheme: "https", wantHost: "example.com:8443"},
		{name: "unknown scheme keeps", urlHost: "example.com:443", scheme: "ftp", wantHost: "example.com:443"},
		{name: "ipv6 https strips brackets kept", urlHost: "[::1]:443", scheme: "https", wantHost: "[::1]"},
		{name: "ipv6 non-default keeps", urlHost: "[::1]:8080", scheme: "http", wantHost: "[::1]:8080"},
		{name: "no port returns input", urlHost: "example.com", scheme: "https", wantHost: "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canonicalHeaderHost(tt.urlHost, tt.scheme); got != tt.wantHost {
				t.Fatalf("canonicalHeaderHost(%q, %q) = %q, want %q", tt.urlHost, tt.scheme, got, tt.wantHost)
			}
		})
	}
}
