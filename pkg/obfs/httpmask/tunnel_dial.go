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
package httpmask

import (
	"container/list"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/pkg/dnsutil"
)

const (
	tunnelEarlyDataQueryKey = "ed"
	tunnelEarlyDataHeader   = "X-Sudoku-Early"
)

type authorizeResponse struct {
	token        string
	earlyPayload []byte
}

func canonicalHeaderHost(urlHost, scheme string) string {
	host, port, err := net.SplitHostPort(urlHost)
	if err != nil {
		return urlHost
	}

	defaultPort := ""
	switch scheme {
	case "https":
		defaultPort = "443"
	case "http":
		defaultPort = "80"
	}
	if defaultPort == "" || port != defaultPort {
		return urlHost
	}

	// If we strip the port from an IPv6 literal, re-add brackets to keep the Host header valid.
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func parseTunnelToken(body []byte) (string, error) {
	resp, err := parseAuthorizeResponse(body)
	if err != nil {
		return "", err
	}
	return resp.token, nil
}

func parseAuthorizeResponse(body []byte) (*authorizeResponse, error) {
	s := strings.TrimSpace(string(body))
	idx := strings.Index(s, "token=")
	if idx < 0 {
		return nil, errors.New("missing token")
	}
	s = s[idx+len("token="):]
	if s == "" {
		return nil, errors.New("empty token")
	}
	// Token is base64.RawURLEncoding (A-Z a-z 0-9 - _). Strip any trailing bytes (e.g. from CDN compression).
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteByte(c)
			continue
		}
		break
	}
	token := b.String()
	if token == "" {
		return nil, errors.New("empty token")
	}
	out := &authorizeResponse{token: token}
	if earlyLine := findAuthorizeField(body, "ed="); earlyLine != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(earlyLine)
		if err != nil {
			return nil, fmt.Errorf("decode early authorize payload failed: %w", err)
		}
		out.earlyPayload = decoded
	}
	return out, nil
}

func findAuthorizeField(body []byte, prefix string) string {
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func setEarlyDataQuery(rawURL string, payload []byte) (string, error) {
	if len(payload) == 0 {
		return rawURL, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set(tunnelEarlyDataQueryKey, base64.RawURLEncoding.EncodeToString(payload))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseEarlyDataQuery(u *url.URL) ([]byte, error) {
	if u == nil {
		return nil, nil
	}
	val := strings.TrimSpace(u.Query().Get(tunnelEarlyDataQueryKey))
	if val == "" {
		return nil, nil
	}
	return base64.RawURLEncoding.DecodeString(val)
}

type sessionDialInfo struct {
	client     *http.Client
	pushURL    string
	pullURL    string
	finURL     string
	closeURL   string
	headerHost string
	auth       *tunnelAuth
}

type transportKey struct {
	scheme     string
	urlHost    string
	dialAddr   string
	serverName string
}

type transportCacheEntry struct {
	key transportKey
	tr  *http.Transport
}

// transportCache bounds the memory footprint of globally reused *http.Transport instances.
//
// Each dial creates its own http.Client, but clients can share Transports to reuse
// underlying TCP/TLS connections (keep-alive / HTTP/2) when Multiplex is enabled.
type transportCache struct {
	mu  sync.Mutex
	max int
	ll  *list.List
	m   map[transportKey]*list.Element
}

func newTransportCache(maxEntries int) *transportCache {
	if maxEntries <= 0 {
		maxEntries = 64
	}
	return &transportCache{
		max: maxEntries,
		ll:  list.New(),
		m:   make(map[transportKey]*list.Element),
	}
}

func (c *transportCache) getOrCreate(key transportKey, build func() *http.Transport) *http.Transport {
	if c == nil {
		return build()
	}

	c.mu.Lock()
	if el := c.m[key]; el != nil {
		c.ll.MoveToFront(el)
		ent := el.Value.(*transportCacheEntry)
		tr := ent.tr
		c.mu.Unlock()
		if tr == nil {
			return build()
		}
		return tr
	}
	c.mu.Unlock()

	tr := build()

	c.mu.Lock()
	// Another goroutine might have inserted while we were building.
	if el := c.m[key]; el != nil {
		c.ll.MoveToFront(el)
		ent := el.Value.(*transportCacheEntry)
		existing := ent.tr
		c.mu.Unlock()
		if existing != nil {
			// We created an extra transport; best-effort close any idle conns then drop it.
			tr.CloseIdleConnections()
			return existing
		}
		return tr
	}
	el := c.ll.PushFront(&transportCacheEntry{key: key, tr: tr})
	c.m[key] = el
	for c.max > 0 && c.ll.Len() > c.max {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*transportCacheEntry)
		delete(c.m, ent.key)
		c.ll.Remove(back)
		if ent.tr != nil {
			ent.tr.CloseIdleConnections()
		}
	}
	c.mu.Unlock()
	return tr
}

var globalTransportCache = newTransportCache(128)

func newHTTPClient(urlHost, dialAddr, serverName, scheme string, maxIdleConns int, reuseTransport bool) *http.Client {
	build := func() *http.Transport {
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     scheme == "https",
			DisableCompression:    true,
			MaxIdleConns:          maxIdleConns,
			MaxIdleConnsPerHost:   maxIdleConns,
			IdleConnTimeout:       30 * time.Second,
			ResponseHeaderTimeout: 20 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			DialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
				d := dnsutil.OutboundDialer(0)
				if addr == urlHost {
					addr = dialAddr
				}
				return d.DialContext(dialCtx, network, addr)
			},
		}
		if scheme == "https" {
			transport.TLSClientConfig = &tls.Config{
				ServerName: serverName,
				MinVersion: tls.VersionTLS12,
			}
		}
		return transport
	}

	if !reuseTransport {
		return &http.Client{Transport: build()}
	}

	key := transportKey{
		scheme:     scheme,
		urlHost:    urlHost,
		dialAddr:   dialAddr,
		serverName: serverName,
	}
	return &http.Client{Transport: globalTransportCache.getOrCreate(key, build)}
}

func dialSession(ctx context.Context, serverAddress string, opts TunnelDialOptions, mode TunnelMode) (*sessionDialInfo, error) {
	scheme, urlHost, dialAddr, serverName, err := normalizeHTTPDialTarget(serverAddress, opts.TLSEnabled, opts.HostOverride)
	if err != nil {
		return nil, err
	}
	headerHost := canonicalHeaderHost(urlHost, scheme)
	auth := newTunnelAuth(opts.AuthKey, 0)

	client := newHTTPClient(urlHost, dialAddr, serverName, scheme, 32, multiplexEnabled(opts.Multiplex))

	authorizeURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: joinPathRoot(opts.PathRoot, "/session")}).String()
	if opts.EarlyHandshake != nil && len(opts.EarlyHandshake.RequestPayload) > 0 {
		authorizeURL, err = setEarlyDataQuery(authorizeURL, opts.EarlyHandshake.RequestPayload)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizeURL, nil)
	if err != nil {
		return nil, err
	}
	req.Host = headerHost
	applyTunnelHeaders(req.Header, headerHost, mode)
	applyTunnelAuth(req, auth, mode, http.MethodGet, "/session")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s authorize bad status: %s (%s)", mode, resp.Status, strings.TrimSpace(string(bodyBytes)))
	}

	authResp, err := parseAuthorizeResponse(bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("%s authorize failed: %q", mode, strings.TrimSpace(string(bodyBytes)))
	}
	token := authResp.token
	if token == "" {
		return nil, fmt.Errorf("%s authorize empty token", mode)
	}
	if opts.EarlyHandshake != nil && len(authResp.earlyPayload) > 0 && opts.EarlyHandshake.HandleResponse != nil {
		if err := opts.EarlyHandshake.HandleResponse(authResp.earlyPayload); err != nil {
			return nil, err
		}
	}

	pushURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: joinPathRoot(opts.PathRoot, "/api/v1/upload"), RawQuery: "token=" + url.QueryEscape(token)}).String()
	pullURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: joinPathRoot(opts.PathRoot, "/stream"), RawQuery: "token=" + url.QueryEscape(token)}).String()
	finURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: joinPathRoot(opts.PathRoot, "/api/v1/upload"), RawQuery: "token=" + url.QueryEscape(token) + "&fin=1"}).String()
	closeURL := (&url.URL{Scheme: scheme, Host: urlHost, Path: joinPathRoot(opts.PathRoot, "/api/v1/upload"), RawQuery: "token=" + url.QueryEscape(token) + "&close=1"}).String()

	return &sessionDialInfo{
		client:     client,
		pushURL:    pushURL,
		pullURL:    pullURL,
		finURL:     finURL,
		closeURL:   closeURL,
		headerHost: headerHost,
		auth:       auth,
	}, nil
}

func bestEffortCloseSession(client *http.Client, closeURL, headerHost string, mode TunnelMode, auth *tunnelAuth) {
	if client == nil || closeURL == "" || headerHost == "" {
		return
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(closeCtx, http.MethodPost, closeURL, nil)
	if err != nil {
		return
	}
	req.Host = headerHost
	applyTunnelHeaders(req.Header, headerHost, mode)
	applyTunnelAuth(req, auth, mode, http.MethodPost, "/api/v1/upload")

	resp, err := client.Do(req)
	if err != nil || resp == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4*1024))
	_ = resp.Body.Close()
}

func normalizeHTTPDialTarget(serverAddress string, tlsEnabled bool, hostOverride string) (scheme, urlHost, dialAddr, serverName string, err error) {
	host, port, err := net.SplitHostPort(serverAddress)
	if err != nil {
		return "", "", "", "", fmt.Errorf("invalid server address %q: %w", serverAddress, err)
	}

	if hostOverride != "" {
		// Allow "example.com" or "example.com:443"
		if h, p, splitErr := net.SplitHostPort(hostOverride); splitErr == nil {
			if h != "" {
				hostOverride = h
			}
			if p != "" {
				port = p
			}
		}
		serverName = hostOverride
		urlHost = net.JoinHostPort(hostOverride, port)
	} else {
		serverName = host
		urlHost = net.JoinHostPort(host, port)
	}

	if tlsEnabled {
		scheme = "https"
	} else {
		scheme = "http"
	}

	dialAddr = net.JoinHostPort(host, port)
	return scheme, urlHost, dialAddr, trimPortForHost(serverName), nil
}
