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
	"github.com/saba-futai/sudoku/internal/reverse"
	"github.com/saba-futai/sudoku/internal/tunnel"
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

	var revMgr *reverse.Manager
	if cfg.Reverse != nil && strings.TrimSpace(cfg.Reverse.Listen) != "" {
		revMgr = reverse.NewManager()
		revListen := strings.TrimSpace(cfg.Reverse.Listen)
		go func() {
			log.Printf("[Reverse] entry on %s", revListen)
			if err := reverse.ServeEntry(revListen, revMgr); err != nil {
				log.Printf("[Reverse] entry error: %v", err)
			}
		}()
	}

	var tunnelSrv *httpmask.TunnelServer
	if cfg.HTTPMaskTunnelEnabled() {
		tunnelSrv = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
			Mode:     cfg.HTTPMask.Mode,
			PathRoot: cfg.HTTPMask.PathRoot,
			AuthKey:  cfg.Key,
			PassThroughOnReject: func() bool {
				if cfg.SuspiciousAction == "silent" {
					return true
				}
				return cfg.SuspiciousAction == "fallback" && strings.TrimSpace(cfg.FallbackAddr) != ""
			}(),
		})
	}

	for {
		c, err := l.Accept()
		if err != nil {
			continue
		}
		go handleServerConn(c, cfg, tables, tunnelSrv, revMgr)
	}
}

func handleServerConn(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, tunnelSrv *httpmask.TunnelServer, revMgr *reverse.Manager) {
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
			inner.HTTPMask.Disable = true
			handleSudokuServerConn(c, rawConn, &inner, tables, false, revMgr)
			return
		case httpmask.HandlePassThrough:
			if r, ok := c.(interface{ IsHTTPMaskRejected() bool }); ok && r.IsHTTPMaskRejected() {
				handler.HandleSuspicious(c, rawConn, cfg)
				return
			}
			handleSudokuServerConn(c, rawConn, cfg, tables, true, revMgr)
			return
		default:
			rawConn.Close()
			return
		}
	}

	handleSudokuServerConn(rawConn, rawConn, cfg, tables, true, revMgr)
}

func handleSudokuServerConn(handshakeConn net.Conn, rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, allowFallback bool, revMgr *reverse.Manager) {
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
		} else {
			log.Printf("[Server][UoT] session start")
		}
		if err := tunnel.HandleUoTServer(tunnelConn); err != nil {
			if userHash != "" {
				log.Printf("[Server][UoT][User:%s] session end: %v", userHash, err)
			} else {
				log.Printf("[Server][UoT] session end: %v", err)
			}
		} else {
			if userHash != "" {
				log.Printf("[Server][UoT][User:%s] session end", userHash)
			} else {
				log.Printf("[Server][UoT] session end")
			}
		}
		return
	}

	if firstByte[0] == tunnel.MuxMagicByte {
		if userHash != "" {
			log.Printf("[Server][Mux][User:%s] session start", userHash)
		} else {
			log.Printf("[Server][Mux] session start")
		}
		logConnect := func(addr string) {
			if userHash != "" {
				log.Printf("[Server][Mux][User:%s] Connecting to %s", userHash, addr)
			} else {
				log.Printf("[Server][Mux] Connecting to %s", addr)
			}
		}
		if err := tunnel.HandleMuxServer(tunnelConn, logConnect); err != nil {
			if userHash != "" {
				log.Printf("[Server][Mux][User:%s] session end: %v", userHash, err)
			} else {
				log.Printf("[Server][Mux] session end: %v", err)
			}
		} else {
			if userHash != "" {
				log.Printf("[Server][Mux][User:%s] session end", userHash)
			} else {
				log.Printf("[Server][Mux] session end")
			}
		}
		return
	}

	if firstByte[0] == tunnel.ReverseMagicByte {
		if revMgr == nil {
			log.Printf("[Server][Reverse] reverse proxy not enabled (missing reverse.listen)")
			return
		}
		if userHash != "" {
			log.Printf("[Server][Reverse][User:%s] session start", userHash)
		} else {
			log.Printf("[Server][Reverse] session start")
		}
		if err := reverse.HandleServerSession(tunnelConn, userHash, revMgr); err != nil {
			if userHash != "" {
				log.Printf("[Server][Reverse][User:%s] session end: %v", userHash, err)
			} else {
				log.Printf("[Server][Reverse] session end: %v", err)
			}
		} else {
			if userHash != "" {
				log.Printf("[Server][Reverse][User:%s] session end", userHash)
			} else {
				log.Printf("[Server][Reverse] session end")
			}
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
