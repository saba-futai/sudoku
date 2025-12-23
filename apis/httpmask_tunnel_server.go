package apis

import (
	"net"
	"strings"

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
		case "stream", "poll", "auto":
			ts = httpmask.NewTunnelServer(httpmask.TunnelServerOptions{Mode: cfg.HTTPMaskMode})
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
	if s == nil || s.cfg == nil {
		return nil, "", false, nil
	}

	// Legacy-only server behavior.
	if s.ts == nil {
		tunnelConn, targetAddr, err = ServerHandshake(rawConn, s.cfg)
		return tunnelConn, targetAddr, true, err
	}

	res, c, err := s.ts.HandleConn(rawConn)
	if err != nil {
		return nil, "", true, err
	}

	switch res {
	case httpmask.HandleDone:
		return nil, "", true, nil
	case httpmask.HandlePassThrough:
		tunnelConn, targetAddr, err = ServerHandshake(c, s.cfg)
		return tunnelConn, targetAddr, true, err
	case httpmask.HandleStartTunnel:
		inner := *s.cfg
		inner.DisableHTTPMask = true
		tunnelConn, targetAddr, err = ServerHandshake(c, &inner)
		return tunnelConn, targetAddr, true, err
	default:
		return nil, "", true, nil
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
	if s == nil || s.cfg == nil {
		return nil, "", false, false, nil
	}

	// Legacy-only server behavior.
	if s.ts == nil {
		tunnelConn, targetAddr, isUoT, err = ServerHandshakeAuto(rawConn, s.cfg)
		return tunnelConn, targetAddr, isUoT, true, err
	}

	res, c, err := s.ts.HandleConn(rawConn)
	if err != nil {
		return nil, "", false, true, err
	}

	switch res {
	case httpmask.HandleDone:
		return nil, "", false, true, nil
	case httpmask.HandlePassThrough:
		tunnelConn, targetAddr, isUoT, err = ServerHandshakeAuto(c, s.cfg)
		return tunnelConn, targetAddr, isUoT, true, err
	case httpmask.HandleStartTunnel:
		inner := *s.cfg
		inner.DisableHTTPMask = true
		tunnelConn, targetAddr, isUoT, err = ServerHandshakeAuto(c, &inner)
		return tunnelConn, targetAddr, isUoT, true, err
	default:
		return nil, "", false, true, nil
	}
}
