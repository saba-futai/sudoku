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
package app

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/handler"
	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/internal/reverse"
	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/logx"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// logUserInfo logs an info message, prepending [User:hash] when userHash is non-empty.
func logUserInfo(tag, userHash, format string, args ...interface{}) {
	if userHash != "" {
		format = "[User:" + userHash + "] " + format
	}
	logx.Infof(tag, format, args...)
}

// logUserWarn logs a warning message, prepending [User:hash] when userHash is non-empty.
func logUserWarn(tag, userHash, format string, args ...interface{}) {
	if userHash != "" {
		format = "[User:" + userHash + "] " + format
	}
	logx.Warnf(tag, format, args...)
}

func RunServer(cfg *config.Config, tables []*sudoku.Table) {
	logx.InstallStd()

	// Listen on TCP port.
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.LocalPort))
	if err != nil {
		logx.Fatalf("Server", "%v", err)
	}
	logx.Infof("Server", "Server on :%d (Fallback: %s)", cfg.LocalPort, cfg.FallbackAddr)

	var revMgr *reverse.Manager
	if cfg.Reverse != nil && strings.TrimSpace(cfg.Reverse.Listen) != "" {
		revMgr = reverse.NewManager()
		revListen := strings.TrimSpace(cfg.Reverse.Listen)
		go func() {
			logx.Infof("Reverse", "entry on %s", revListen)
			if err := reverse.ServeEntry(revListen, revMgr); err != nil {
				logx.Warnf("Reverse", "entry error: %v", err)
			}
		}()
	}

	var tunnelSrv *httpmask.TunnelServer
	if cfg.HTTPMaskTunnelEnabled() {
		tunnelSrv = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
			Mode:     cfg.HTTPMask.Mode,
			PathRoot: cfg.HTTPMask.PathRoot,
			AuthKey:  cfg.Key,
			EarlyHandshake: tunnel.NewHTTPMaskServerEarlyHandshake(tunnel.EarlyCodecConfig{
				PSK:                cfg.Key,
				AEAD:               cfg.AEAD,
				EnablePureDownlink: cfg.EnablePureDownlink,
				PaddingMin:         cfg.PaddingMin,
				PaddingMax:         cfg.PaddingMax,
			}, tables, tunnel.AllowHandshakeReplay),
			PassThroughOnReject: func() bool {
				if cfg.SuspiciousAction == "silent" {
					return true
				}
				return cfg.SuspiciousAction == "fallback" && strings.TrimSpace(cfg.FallbackAddr) != ""
			}(),
		})
	}

	// Graceful shutdown on SIGINT / SIGTERM.
	sigCh := make(chan os.Signal, 1)
	stopCh := make(chan struct{})
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logx.Infof("Server", "Shutting down...")
		close(stopCh)
		_ = l.Close()
	}()

	for {
		c, err := l.Accept()
		if err != nil {
			// If the listener was closed by signal, exit gracefully.
			select {
			case <-stopCh:
				return
			default:
				continue
			}
		}
		go handleServerConn(c, cfg, tables, tunnelSrv, revMgr)
	}
}

func handleServerConn(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table, tunnelSrv *httpmask.TunnelServer, revMgr *reverse.Manager) {
	if tunnelSrv != nil {
		res, c, err := tunnelSrv.HandleConn(rawConn)
		if err != nil {
			logx.Warnf("Server/HTTP", "tunnel prelude failed: %v", err)
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
				// Use the pass-through conn for fallback proxying so any replay prefix is preserved.
				handler.HandleSuspicious(c, c, cfg)
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
			logx.Warnf("Security", "Suspicious connection: %v", suspErr.Err)
			// Only meaningful for direct TCP/legacy mask connections.
			if allowFallback {
				// Use handshakeConn for proxying so we preserve any pre-read/replayed bytes (e.g. HTTP tunnel pass-through prefixes),
				// but still use suspErr.Conn to extract any consumed/recorded bytes for the fallback.
				handler.HandleSuspicious(suspErr.Conn, handshakeConn, cfg)
			} else {
				rawConn.Close()
			}
		} else {
			logx.Warnf("Server", "Handshake failed: %v", err)
			rawConn.Close()
		}
		return
	}

	userHash := ""
	if meta != nil {
		userHash = meta.UserHash
	}

	// Read the first KIP control message to detect session type (UoT / Mux / Reverse / normal).
	var msg *tunnel.KIPMessage
	for {
		m, err := tunnel.ReadKIPMessage(tunnelConn)
		if err != nil {
			logx.Warnf("Server", "Failed to read KIP message: %v", err)
			return
		}
		if m.Type == tunnel.KIPTypeKeepAlive {
			continue
		}
		msg = m
		break
	}

	switch msg.Type {
	case tunnel.KIPTypeStartUoT:
		logUserInfo("Server/UoT", userHash, "session start")
		if err := tunnel.HandleUoTServer(tunnelConn); err != nil {
			logUserWarn("Server/UoT", userHash, "session end: %v", err)
		} else {
			logUserInfo("Server/UoT", userHash, "session end")
		}
		return
	case tunnel.KIPTypeStartMux:
		logUserInfo("Server/Mux", userHash, "session start")
		logConnect := func(addr string) {
			logUserInfo("Server/Mux", userHash, "Connecting to %s", addr)
		}
		if err := tunnel.HandleMuxServer(tunnelConn, logConnect); err != nil {
			logUserWarn("Server/Mux", userHash, "session end: %v", err)
		} else {
			logUserInfo("Server/Mux", userHash, "session end")
		}
		return
	case tunnel.KIPTypeStartRev:
		if revMgr == nil {
			logx.Warnf("Server/Reverse", "reverse proxy not enabled (missing reverse.listen)")
			return
		}
		logUserInfo("Server/Reverse", userHash, "session start")
		if err := reverse.HandleServerSession(tunnelConn, userHash, revMgr, msg.Payload); err != nil {
			logUserWarn("Server/Reverse", userHash, "session end: %v", err)
		} else {
			logUserInfo("Server/Reverse", userHash, "session end")
		}
		return
	case tunnel.KIPTypeOpenTCP:
		destAddrStr, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
		if err != nil {
			logx.Warnf("Server", "Failed to decode target address: %v", err)
			return
		}

		logUserInfo("Server", userHash, "Connecting to %s", destAddrStr)

		target, err := net.DialTimeout("tcp", destAddrStr, 10*time.Second)
		if err != nil {
			logx.Warnf("Server", "Connect target failed: %v", err)
			return
		}

		connutil.PipeConn(tunnelConn, target)
		return
	default:
		logx.Warnf("Server", "Unknown KIP message: %d", msg.Type)
		return
	}
}
