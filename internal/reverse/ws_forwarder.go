package reverse

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/logx"
)

// ServeLocalWSForward listens on listenAddr and forwards each accepted TCP connection through
// a WebSocket tunnel at dialURL (ws:// or wss://). The WebSocket connection must negotiate
// the "sudoku-tcp-v1" subprotocol (see sudokuTCPSubprotocol).
func ServeLocalWSForward(listenAddr, dialURL string, insecure bool) error {
	listenAddr = strings.TrimSpace(listenAddr)
	dialURL = strings.TrimSpace(dialURL)
	if listenAddr == "" {
		return fmt.Errorf("missing listen address")
	}
	if dialURL == "" {
		return fmt.Errorf("missing dial url")
	}

	u, err := url.Parse(dialURL)
	if err != nil || u == nil {
		return fmt.Errorf("invalid dial url: %q", dialURL)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	default:
		return fmt.Errorf("dial url must be ws:// or wss:// (got %q)", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return fmt.Errorf("dial url missing host: %q", dialURL)
	}

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()

	logx.Infof("Reverse/Forward", "TCP %s -> %s", ln.Addr().String(), dialURL)

	var wsHTTPClient *http.Client
	if insecure && strings.EqualFold(u.Scheme, "wss") {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		wsHTTPClient = &http.Client{Transport: tr}
	}

	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		go func(local net.Conn) {
			if local == nil {
				return
			}
			defer local.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ws, _, err := websocket.Dial(ctx, dialURL, &websocket.DialOptions{
				Subprotocols:    []string{sudokuTCPSubprotocol},
				CompressionMode: websocket.CompressionDisabled,
				HTTPClient:      wsHTTPClient,
			})
			cancel()
			if err != nil {
				logx.Warnf("Reverse/Forward", "dial ws failed: %v", err)
				return
			}
			if ws.Subprotocol() != sudokuTCPSubprotocol {
				_ = ws.Close(websocket.StatusPolicyViolation, "subprotocol required")
				logx.Warnf("Reverse/Forward", "server did not accept subprotocol %q", sudokuTCPSubprotocol)
				return
			}

			wsConn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
			tunnel.PipeConn(local, wsConn)
		}(c)
	}
}
