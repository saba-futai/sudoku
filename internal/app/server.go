// internal/app/server.go
package app

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/handler"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/multiplex"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func RunServer(cfg *config.Config, tables []*sudoku.Table) {
	// 1. 监听 TCP 端口
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Server on :%d (Fallback: %s)", cfg.LocalPort, cfg.FallbackAddr)

	var tunnelSrv *httpmask.TunnelServer
	if !cfg.DisableHTTPMask {
		switch strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMode)) {
		case "stream", "poll", "auto":
			tunnelSrv = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
				Mode: cfg.HTTPMaskMode,
			})
		}
	}

	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go handleServerConn(c, cfg, tables, tunnelSrv)
	}
}

func handleServerConn(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, tunnelSrv *httpmask.TunnelServer) {
	if tunnelSrv != nil {
		res, c, err := tunnelSrv.HandleConn(rawConn)
		if err != nil {
			log.Printf("[Server][HTTP] tunnel prelude failed: %v", err)
			rawConn.Close()
			return
		}
		switch res {
		case httpmask.HandleDone:
			return
		case httpmask.HandleStartTunnel:
			inner := *cfg
			inner.DisableHTTPMask = true
			handleSudokuServerConn(c, rawConn, &inner, tables, false)
			return
		case httpmask.HandlePassThrough:
			handleSudokuServerConn(c, rawConn, cfg, tables, true)
			return
		default:
			rawConn.Close()
			return
		}
	}

	handleSudokuServerConn(rawConn, rawConn, cfg, tables, true)
}

func handleSudokuServerConn(handshakeConn net.Conn, rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, allowFallback bool) {
	// Use Tunnel Abstraction for Handshake and Upgrade
	tunnelConn, meta, err := tunnel.HandshakeAndUpgradeWithTablesMeta(handshakeConn, cfg, tables)
	if err != nil {
		if suspErr, ok := err.(*tunnel.SuspiciousError); ok {
			log.Printf("[Security] Suspicious connection: %v", suspErr.Err)
			// Only meaningful for direct TCP/legacy mask connections.
			if allowFallback {
				handler.HandleSuspicious(suspErr.Conn, rawConn, cfg)
			} else {
				rawConn.Close()
			}
		} else {
			log.Printf("[Server] Handshake failed: %v", err)
			rawConn.Close()
		}
		return
	}

	userHash := ""
	if meta != nil {
		userHash = meta.UserHash
	}

	// ==========================================
	// 5. 连接目标地址
	// ==========================================

	// 判断是否为 UoT (UDP over TCP) 会话
	firstByte := make([]byte, 1)
	if _, err := io.ReadFull(tunnelConn, firstByte); err != nil {
		log.Printf("[Server] Failed to read first byte: %v", err)
		return
	}

	if firstByte[0] == tunnel.UoTMagicByte {
		if userHash != "" {
			log.Printf("[Server][UoT][User:%s] session start", userHash)
		}
		if err := tunnel.HandleUoTServer(tunnelConn); err != nil {
			log.Printf("[Server][UoT] session ended: %v", err)
		}
		return
	}

	// Multiplex session: one tunnel carries multiple target streams.
	if firstByte[0] == multiplex.MagicByte {
		v, err := multiplex.ReadVersion(tunnelConn)
		if err != nil {
			log.Printf("[Server][MUX] read version failed: %v", err)
			return
		}
		if err := multiplex.ValidateVersion(v); err != nil {
			log.Printf("[Server][MUX] %v", err)
			return
		}

		sess, err := multiplex.NewServerSession(tunnelConn)
		if err != nil {
			log.Printf("[Server][MUX] start session failed: %v", err)
			return
		}
		defer sess.Close()

		if userHash != "" {
			log.Printf("[Server][MUX][User:%s] session start", userHash)
		} else {
			log.Printf("[Server][MUX] session start")
		}

		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go handleSudokuServerStream(stream, userHash)
		}
	}

	// 非 UoT：将预读的字节放回流中以兼容旧协议
	prefixedConn := tunnel.NewPreBufferedConn(tunnelConn, firstByte)

	// 从上行连接读取目标地址
	destAddrStr, _, _, err := protocol.ReadAddress(prefixedConn)
	if err != nil {
		log.Printf("[Server] Failed to read target address: %v", err)
		return
	}

	if userHash != "" {
		log.Printf("[Server][User:%s] Connecting to %s", userHash, destAddrStr)
	} else {
		log.Printf("[Server] Connecting to %s", destAddrStr)
	}

	target, err := net.DialTimeout("tcp", destAddrStr, 10*time.Second)
	if err != nil {
		log.Printf("[Server] Connect target failed: %v", err)
		return
	}

	// ==========================================
	// 6. 转发数据
	// ==========================================
	pipeConn(prefixedConn, target)
}

func handleSudokuServerStream(stream net.Conn, userHash string) {
	defer stream.Close()

	firstByte := make([]byte, 1)
	if _, err := io.ReadFull(stream, firstByte); err != nil {
		return
	}

	if firstByte[0] == tunnel.UoTMagicByte {
		if userHash != "" {
			log.Printf("[Server][MUX][UoT][User:%s] session start", userHash)
		}
		if err := tunnel.HandleUoTServer(stream); err != nil {
			log.Printf("[Server][MUX][UoT] session ended: %v", err)
		}
		return
	}

	prefixed := tunnel.NewPreBufferedConn(stream, firstByte)
	destAddrStr, _, _, err := protocol.ReadAddress(prefixed)
	if err != nil {
		return
	}

	if userHash != "" {
		log.Printf("[Server][MUX][User:%s] Connecting to %s", userHash, destAddrStr)
	} else {
		log.Printf("[Server][MUX] Connecting to %s", destAddrStr)
	}

	target, err := net.DialTimeout("tcp", destAddrStr, 10*time.Second)
	if err != nil {
		log.Printf("[Server][MUX] Connect target failed: %v", err)
		return
	}
	pipeConn(prefixed, target)
}
