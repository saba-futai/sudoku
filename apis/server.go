/*
Copyright (C) 2025 by ふたい <contact me via issue>

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
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// bufferedConn 这是一个内部辅助结构，用于将 bufio 多读的数据传递给后续层
// 必须实现 net.Conn
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (bc *bufferedConn) Read(p []byte) (int, error) {
	return bc.r.Read(p)
}

type preBufferedConn struct {
	net.Conn
	buf []byte
}

func (p *preBufferedConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

type readOnlyConn struct {
	*bytes.Reader
}

func (c *readOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (c *readOnlyConn) Close() error                     { return nil }
func (c *readOnlyConn) LocalAddr() net.Addr              { return nil }
func (c *readOnlyConn) RemoteAddr() net.Addr             { return nil }
func (c *readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (c *readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *readOnlyConn) SetWriteDeadline(time.Time) error { return nil }

func drainBuffered(r *bufio.Reader) ([]byte, error) {
	n := r.Buffered()
	if n <= 0 {
		return nil, nil
	}
	out := make([]byte, n)
	_, err := io.ReadFull(r, out)
	return out, err
}

func probeHandshakeBytes(probe []byte, cfg *ProtocolConfig, table *sudoku.Table) error {
	rc := &readOnlyConn{Reader: bytes.NewReader(probe)}
	_, obfsConn := buildServerObfsConn(rc, cfg, table, false)
	cConn, err := crypto.NewAEADConn(obfsConn, cfg.Key, cfg.AEADMethod)
	if err != nil {
		return err
	}

	handshakeBuf := make([]byte, 16)
	if _, err := io.ReadFull(cConn, handshakeBuf); err != nil {
		return err
	}
	ts := int64(binary.BigEndian.Uint64(handshakeBuf[:8]))
	now := time.Now().Unix()
	if abs(now-ts) > 60 {
		return fmt.Errorf("timestamp skew/replay detected: server_time=%d client_time=%d", now, ts)
	}

	modeBuf := []byte{0}
	if _, err := io.ReadFull(cConn, modeBuf); err != nil {
		return err
	}
	if modeBuf[0] != downlinkMode(cfg) {
		return fmt.Errorf("downlink mode mismatch: client=%d server=%d", modeBuf[0], downlinkMode(cfg))
	}
	return nil
}

func selectTableByProbe(r *bufio.Reader, cfg *ProtocolConfig, tables []*sudoku.Table) (*sudoku.Table, []byte, error) {
	const (
		maxProbeBytes = 64 * 1024
		readChunk     = 4 * 1024
	)
	if len(tables) == 0 {
		return nil, nil, fmt.Errorf("no table candidates")
	}
	if len(tables) > 255 {
		return nil, nil, fmt.Errorf("too many table candidates: %d", len(tables))
	}

	probe, err := drainBuffered(r)
	if err != nil {
		return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
	}

	tmp := make([]byte, readChunk)
	for {
		if len(tables) == 1 {
			tail, err := drainBuffered(r)
			if err != nil {
				return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
			}
			probe = append(probe, tail...)
			return tables[0], probe, nil
		}

		needMore := false
		for _, table := range tables {
			err := probeHandshakeBytes(probe, cfg, table)
			if err == nil {
				tail, err := drainBuffered(r)
				if err != nil {
					return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
				}
				probe = append(probe, tail...)
				return table, probe, nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				needMore = true
			}
		}

		if !needMore {
			return nil, probe, fmt.Errorf("handshake table selection failed")
		}
		if len(probe) >= maxProbeBytes {
			return nil, probe, fmt.Errorf("handshake probe exceeded %d bytes", maxProbeBytes)
		}

		n, err := r.Read(tmp)
		if n > 0 {
			probe = append(probe, tmp[:n]...)
		}
		if err != nil {
			return nil, probe, fmt.Errorf("handshake probe read failed: %w", err)
		}
	}
}

// ServerHandshake 执行 Sudoku 服务端握手逻辑
// 输入: rawConn (刚 Accept 的 TCP 连接)
// 输出:
//  1. tunnelConn: 解密后的透明连接，可直接用于应用层数据传输
//  2. targetAddr: 客户端想要访问的目标地址
//  3. error: 如果是 *HandshakeError 类型，包含了用于 Fallback 的完整数据
//
// 握手过程分为多个层次：
//  1. HTTP 伪装层：验证 HTTP POST 请求头
//  2. Sudoku 混淆层：解码 Sudoku 谜题
//  3. AEAD 加密层：解密并验证数据
//  4. 协议层：验证时间戳握手、读取目标地址
//
// 任何层次失败都会返回 HandshakeError，其中包含该层及之前所有层读取的数据
func ServerHandshake(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, string, error) {
	if cfg == nil {
		return nil, "", fmt.Errorf("config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, "", fmt.Errorf("invalid config: %w", err)
	}

	conn, fail, err := serverHandshakeCore(rawConn, cfg)
	if err != nil {
		return nil, "", err
	}

	// 4. 读取目标地址
	targetAddr, _, _, err := protocol.ReadAddress(conn)
	if err != nil {
		conn.Close()
		return nil, "", fail(fmt.Errorf("read target address failed: %w", err))
	}

	return conn, targetAddr, nil
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ServerHandshakeFlexible upgrades the connection and leaves payload parsing (address or UoT) to the caller.
// The returned fail function wraps errors into HandshakeError with recorded data for fallback handling.
func ServerHandshakeFlexible(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, func(error) error, error) {
	return serverHandshakeCore(rawConn, cfg)
}

func serverHandshakeCore(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, func(error) error, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid config: %w", err)
	}

	deadline := time.Now().Add(time.Duration(cfg.HandshakeTimeoutSeconds) * time.Second)
	rawConn.SetReadDeadline(deadline)

	bufReader := bufio.NewReader(rawConn)
	shouldConsumeMask := false
	var httpHeaderData []byte

	if !cfg.DisableHTTPMask {
		if peekBytes, err := bufReader.Peek(4); err == nil && httpmask.LooksLikeHTTPRequestStart(peekBytes) {
			shouldConsumeMask = true
		}
	}

	if shouldConsumeMask {
		var err error
		httpHeaderData, err = httpmask.ConsumeHeader(bufReader)
		if err != nil {
			rawConn.SetReadDeadline(time.Time{})
			return nil, nil, &HandshakeError{
				Err:            fmt.Errorf("invalid http header: %w", err),
				RawConn:        rawConn,
				HTTPHeaderData: httpHeaderData,
				ReadData:       nil,
			}
		}
	}

	tables := cfg.tableCandidates()
	selectedTable, preRead, err := selectTableByProbe(bufReader, cfg, tables)
	if err != nil {
		rawConn.SetReadDeadline(time.Time{})
		return nil, nil, &HandshakeError{
			Err:            err,
			RawConn:        rawConn,
			HTTPHeaderData: httpHeaderData,
			ReadData:       preRead,
		}
	}

	baseConn := &preBufferedConn{Conn: rawConn, buf: preRead}
	bConn := &bufferedConn{Conn: baseConn, r: bufio.NewReader(baseConn)}
	sConn, obfsConn := buildServerObfsConn(bConn, cfg, selectedTable, true)

	fail := func(originalErr error) error {
		rawConn.SetReadDeadline(time.Time{})
		badData := sConn.GetBufferedAndRecorded()
		return &HandshakeError{
			Err:            originalErr,
			RawConn:        rawConn,
			HTTPHeaderData: httpHeaderData,
			ReadData:       badData,
		}
	}

	cConn, err := crypto.NewAEADConn(obfsConn, cfg.Key, cfg.AEADMethod)
	if err != nil {
		return nil, nil, fail(fmt.Errorf("crypto setup failed: %w", err))
	}

	handshakeBuf := make([]byte, 16)
	if _, err := io.ReadFull(cConn, handshakeBuf); err != nil {
		cConn.Close()
		return nil, nil, fail(fmt.Errorf("read handshake failed: %w", err))
	}

	ts := int64(binary.BigEndian.Uint64(handshakeBuf[:8]))
	now := time.Now().Unix()
	if abs(now-ts) > 60 {
		cConn.Close()
		return nil, nil, fail(fmt.Errorf("timestamp skew/replay detected: server_time=%d client_time=%d", now, ts))
	}

	sConn.StopRecording()

	modeBuf := []byte{0}
	if _, err := io.ReadFull(cConn, modeBuf); err != nil {
		cConn.Close()
		return nil, nil, fail(fmt.Errorf("read downlink mode failed: %w", err))
	}
	if modeBuf[0] != downlinkMode(cfg) {
		cConn.Close()
		return nil, nil, fail(fmt.Errorf("downlink mode mismatch: client=%d server=%d", modeBuf[0], downlinkMode(cfg)))
	}

	rawConn.SetReadDeadline(time.Time{})
	return cConn, fail, nil
}
