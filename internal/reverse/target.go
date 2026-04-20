package reverse

import (
	"net"
	"strings"
)

func defaultUpstreamHostHeader(target string) string {
	target = strings.TrimSpace(target)
	host, _, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return ""
	}

	normalizedHost := host
	if i := strings.IndexByte(normalizedHost, '%'); i >= 0 {
		normalizedHost = normalizedHost[:i]
	}
	if ip := net.ParseIP(normalizedHost); ip != nil {
		return ""
	}

	return target
}
