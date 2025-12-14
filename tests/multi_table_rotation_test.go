package tests

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func TestMultiTableRotation_ServerProbesTables(t *testing.T) {
	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer serverListener.Close()

	serverAddr := serverListener.Addr().String()
	key := "test-key-rotate"

	t1, err := sudoku.NewTableWithCustom("seed-1", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		t.Fatalf("build t1: %v", err)
	}
	t2, err := sudoku.NewTableWithCustom("seed-2", "prefer_entropy", "vxpvxvvp")
	if err != nil {
		t.Fatalf("build t2: %v", err)
	}

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Tables:                  []*sudoku.Table{t1, t2},
		PaddingMin:              5,
		PaddingMax:              15,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := serverListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tunnelConn, _, err := apis.ServerHandshake(c, serverCfg)
				if err != nil {
					return
				}
				defer tunnelConn.Close()
				io.Copy(tunnelConn, tunnelConn)
			}(conn)
		}
	}()

	clientBase := func(table *sudoku.Table, tables []*sudoku.Table) *apis.ProtocolConfig {
		return &apis.ProtocolConfig{
			ServerAddress:      serverAddr,
			TargetAddress:      "example.com:80",
			Key:                key,
			AEADMethod:         "chacha20-poly1305",
			Table:              table,
			Tables:             tables,
			PaddingMin:         5,
			PaddingMax:         15,
			EnablePureDownlink: true,
			DisableHTTPMask:    false,
		}
	}

	t.Run("ClientUsesTable1", func(t *testing.T) {
		conn, err := apis.Dial(context.Background(), clientBase(t1, nil))
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer conn.Close()

		msg := []byte("hello t1")
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if string(buf) != string(msg) {
			t.Fatalf("expected %q, got %q", msg, buf)
		}
	})

	t.Run("ClientUsesTable2", func(t *testing.T) {
		conn, err := apis.Dial(context.Background(), clientBase(t2, nil))
		if err != nil {
			t.Fatalf("dial failed: %v", err)
		}
		defer conn.Close()

		msg := []byte("hello t2")
		if _, err := conn.Write(msg); err != nil {
			t.Fatalf("write failed: %v", err)
		}
		buf := make([]byte, len(msg))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("read failed: %v", err)
		}
		if string(buf) != string(msg) {
			t.Fatalf("expected %q, got %q", msg, buf)
		}
	})

	t.Run("ClientPicksFromTables", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		cfg := clientBase(nil, []*sudoku.Table{t1, t2})
		for i := 0; i < 10; i++ {
			conn, err := apis.Dial(ctx, cfg)
			if err != nil {
				t.Fatalf("dial failed: %v", err)
			}
			msg := []byte("hello rotate")
			if _, err := conn.Write(msg); err != nil {
				conn.Close()
				t.Fatalf("write failed: %v", err)
			}
			buf := make([]byte, len(msg))
			if _, err := io.ReadFull(conn, buf); err != nil {
				conn.Close()
				t.Fatalf("read failed: %v", err)
			}
			conn.Close()
		}
	})
}

func TestMultiTableRotation_Stress(t *testing.T) {
	serverListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer serverListener.Close()

	serverAddr := serverListener.Addr().String()
	key := "test-key-stress"

	t1, err := sudoku.NewTableWithCustom("seed-a", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		t.Fatalf("build t1: %v", err)
	}
	t2, err := sudoku.NewTableWithCustom("seed-b", "prefer_entropy", "vxpvxvvp")
	if err != nil {
		t.Fatalf("build t2: %v", err)
	}

	serverCfg := &apis.ProtocolConfig{
		Key:                     key,
		AEADMethod:              "chacha20-poly1305",
		Tables:                  []*sudoku.Table{t1, t2},
		PaddingMin:              0,
		PaddingMax:              0,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
		DisableHTTPMask:         false,
	}

	go func() {
		for {
			conn, err := serverListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tunnelConn, _, err := apis.ServerHandshake(c, serverCfg)
				if err != nil {
					return
				}
				defer tunnelConn.Close()
				io.Copy(tunnelConn, tunnelConn)
			}(conn)
		}
	}()

	clientCfg := &apis.ProtocolConfig{
		ServerAddress:      serverAddr,
		TargetAddress:      "example.com:80",
		Key:                key,
		AEADMethod:         "chacha20-poly1305",
		Tables:             []*sudoku.Table{t1, t2},
		PaddingMin:         0,
		PaddingMax:         0,
		EnablePureDownlink: true,
		DisableHTTPMask:    false,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const clients = 50
	var wg sync.WaitGroup
	wg.Add(clients)
	for i := 0; i < clients; i++ {
		go func() {
			defer wg.Done()
			conn, err := apis.Dial(ctx, clientCfg)
			if err != nil {
				return
			}
			defer conn.Close()
			msg := []byte("ping")
			conn.Write(msg)
			buf := make([]byte, len(msg))
			io.ReadFull(conn, buf)
		}()
	}
	wg.Wait()
}
