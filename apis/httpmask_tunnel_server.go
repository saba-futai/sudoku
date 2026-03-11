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
package apis

import (
	"net"
	"strings"

	"github.com/saba-futai/sudoku/internal/tunnel"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
)

// HTTPMaskTunnelServer bridges raw TCP accepts with the optional CDN-capable HTTP tunnel transports (stream/poll).
//
// Typical usage:
//
//	srv := apis.NewHTTPMaskTunnelServer(serverCfg)
//	for {
//	  c, _ := ln.Accept()
//	  go func(raw net.Conn) {
//	    defer raw.Close()
//	    tunnel, target, handled, err := srv.HandleConn(raw)
//	    if err != nil || !handled || tunnel == nil {
//	      return
//	    }
//	    defer tunnel.Close()
//	    io.Copy(tunnel, tunnel)
//	  }(c)
//	}
type HTTPMaskTunnelServer struct {
	cfg *ProtocolConfig
	ts  *httpmask.TunnelServer
}

func NewHTTPMaskTunnelServer(cfg *ProtocolConfig) *HTTPMaskTunnelServer {
	if cfg == nil {
		return &HTTPMaskTunnelServer{}
	}

	var ts *httpmask.TunnelServer
	if !cfg.DisableHTTPMask {
		switch strings.ToLower(strings.TrimSpace(cfg.HTTPMaskMode)) {
		case "stream", "poll", "auto", "ws":
			ts = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{
				Mode:     cfg.HTTPMaskMode,
				PathRoot: cfg.HTTPMaskPathRoot,
				AuthKey:  cfg.Key,
				EarlyHandshake: tunnel.NewHTTPMaskServerEarlyHandshake(tunnel.EarlyCodecConfig{
					PSK:                cfg.Key,
					AEAD:               cfg.AEADMethod,
					EnablePureDownlink: cfg.EnablePureDownlink,
					PaddingMin:         cfg.PaddingMin,
					PaddingMax:         cfg.PaddingMax,
				}, cfg.tableCandidates(), nil),
			})
		}
	}

	return &HTTPMaskTunnelServer{
		cfg: cfg,
		ts:  ts,
	}
}

// HandleConn handles a single accepted TCP connection.
//
// Returns:
//   - tunnelConn/targetAddr if a Sudoku tunnel handshake has been completed (handled=true and tunnelConn != nil)
//   - handled=true, tunnelConn=nil for HTTP tunnel control requests (e.g., poll push/pull)
//   - handled=false if this server is not configured to handle HTTP tunnel transports (legacy-only)
func (s *HTTPMaskTunnelServer) HandleConn(rawConn net.Conn) (tunnelConn net.Conn, targetAddr string, handled bool, err error) {
	tunnelConn, targetAddr, _, handled, err = s.HandleConnWithUserHash(rawConn)
	return tunnelConn, targetAddr, handled, err
}

// HandleConnWithUserHash is like HandleConn but also returns the per-user handshake identifier when a Sudoku
// tunnel handshake has been completed.
func (s *HTTPMaskTunnelServer) HandleConnWithUserHash(rawConn net.Conn) (tunnelConn net.Conn, targetAddr string, userHash string, handled bool, err error) {
	if s == nil || s.cfg == nil {
		return nil, "", "", false, nil
	}

	// Legacy-only server behavior.
	if s.ts == nil {
		tunnelConn, targetAddr, userHash, err = ServerHandshakeWithUserHash(rawConn, s.cfg)
		return tunnelConn, targetAddr, userHash, true, err
	}

	res, c, err := s.ts.HandleConn(rawConn)
	if err != nil {
		return nil, "", "", true, err
	}

	switch res {
	case httpmask.HandleDone:
		return nil, "", "", true, nil
	case httpmask.HandlePassThrough:
		tunnelConn, targetAddr, userHash, err = ServerHandshakeWithUserHash(c, s.cfg)
		return tunnelConn, targetAddr, userHash, true, err
	case httpmask.HandleStartTunnel:
		inner := *s.cfg
		inner.DisableHTTPMask = true
		tunnelConn, targetAddr, userHash, err = ServerHandshakeWithUserHash(c, &inner)
		return tunnelConn, targetAddr, userHash, true, err
	default:
		return nil, "", "", true, nil
	}
}

// HandleConnAuto handles a single accepted TCP connection, and supports both TCP target connections
// and UoT (UDP-over-TCP) sessions.
//
// Returns:
//   - tunnelConn/targetAddr if a TCP target address has been read (handled=true, isUoT=false)
//   - tunnelConn with isUoT=true if this is a UoT session (caller should run HandleUoT)
//   - handled=true, tunnelConn=nil for HTTP tunnel control requests (e.g., poll push/pull)
func (s *HTTPMaskTunnelServer) HandleConnAuto(rawConn net.Conn) (tunnelConn net.Conn, targetAddr string, isUoT bool, handled bool, err error) {
	tunnelConn, targetAddr, isUoT, _, handled, err = s.HandleConnAutoWithUserHash(rawConn)
	return tunnelConn, targetAddr, isUoT, handled, err
}

// HandleConnAutoWithUserHash is like HandleConnAuto but also returns the per-user handshake identifier when a
// Sudoku tunnel handshake has been completed.
func (s *HTTPMaskTunnelServer) HandleConnAutoWithUserHash(rawConn net.Conn) (tunnelConn net.Conn, targetAddr string, isUoT bool, userHash string, handled bool, err error) {
	if s == nil || s.cfg == nil {
		return nil, "", false, "", false, nil
	}

	// Legacy-only server behavior.
	if s.ts == nil {
		tunnelConn, targetAddr, isUoT, userHash, err = ServerHandshakeAutoWithUserHash(rawConn, s.cfg)
		return tunnelConn, targetAddr, isUoT, userHash, true, err
	}

	res, c, err := s.ts.HandleConn(rawConn)
	if err != nil {
		return nil, "", false, "", true, err
	}

	switch res {
	case httpmask.HandleDone:
		return nil, "", false, "", true, nil
	case httpmask.HandlePassThrough:
		tunnelConn, targetAddr, isUoT, userHash, err = ServerHandshakeAutoWithUserHash(c, s.cfg)
		return tunnelConn, targetAddr, isUoT, userHash, true, err
	case httpmask.HandleStartTunnel:
		inner := *s.cfg
		inner.DisableHTTPMask = true
		tunnelConn, targetAddr, isUoT, userHash, err = ServerHandshakeAutoWithUserHash(c, &inner)
		return tunnelConn, targetAddr, isUoT, userHash, true, err
	default:
		return nil, "", false, "", true, nil
	}
}
