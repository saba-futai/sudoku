// internal/app/server.go
package app

import (
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/handler"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func RunServer(cfg *config.Config, tables []*sudoku.Table) {
	// 1. 监听 TCP 端口
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Server on :%d (Fallback: %s)", cfg.LocalPort, cfg.FallbackAddr)

	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go handleServerConn(c, cfg, tables)
	}
}

func handleServerConn(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table) {
	// Use Tunnel Abstraction for Handshake and Upgrade
	tunnelConn, err := tunnel.HandshakeAndUpgradeWithTables(rawConn, cfg, tables)
	if err != nil {
		if suspErr, ok := err.(*tunnel.SuspiciousError); ok {
			log.Printf("[Security] Suspicious connection: %v", suspErr.Err)
			handler.HandleSuspicious(suspErr.Conn, rawConn, cfg)
		} else {
			log.Printf("[Server] Handshake failed: %v", err)
			rawConn.Close()
		}
		return
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
		if err := tunnel.HandleUoTServer(tunnelConn); err != nil {
			log.Printf("[Server][UoT] session ended: %v", err)
		}
		return
	}

	// 非 UoT：将预读的字节放回流中以兼容旧协议
	prefixedConn := tunnel.NewPreBufferedConn(tunnelConn, firstByte)

	// 从上行连接读取目标地址
	destAddrStr, _, _, err := protocol.ReadAddress(prefixedConn)
	if err != nil {
		log.Printf("[Server] Failed to read target address: %v", err)
		return
	}

	log.Printf("[Server] Connecting to %s", destAddrStr)

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
