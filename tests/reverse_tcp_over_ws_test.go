package tests

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/saba-futai/sudoku/internal/config"
)

func TestReverseProxy_TCPOverWebSocket_Subpath(t *testing.T) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen origin: %v", err)
	}
	defer originLn.Close()

	originAddr := originLn.Addr().String()
	go func() {
		for {
			c, err := originLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes:   []config.ReverseRoute{{Path: "/ssh", Target: originAddr}},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseRouteReady(t, reverseListen, "/ssh")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, "ws://"+reverseListen+"/ssh", &websocket.DialOptions{
		Subprotocols:    []string{"sudoku-tcp-v1"},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		t.Fatalf("reverse tcp ws dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	if ws.Subprotocol() != "sudoku-tcp-v1" {
		t.Fatalf("expected negotiated subprotocol, got %q", ws.Subprotocol())
	}

	wsConn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	if _, err := wsConn.Write([]byte("ping")); err != nil {
		t.Fatalf("reverse tcp ws write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(wsConn, buf); err != nil {
		t.Fatalf("reverse tcp ws read: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("unexpected echo: %q", string(buf))
	}
}
