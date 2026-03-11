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
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/protocol"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
	"github.com/SUDOKU-ASCII/sudoku/pkg/crypto"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/httpmask"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func probeHandshakeBytes(probe []byte, cfg *ProtocolConfig, table *sudoku.Table) error {
	rc := &connutil.ReadOnlyConn{Reader: bytes.NewReader(probe)}
	_, obfsConn := buildServerObfsConn(rc, cfg, table, false)
	pskC2S, pskS2C := tunnel.DerivePSKDirectionalBases(cfg.Key)
	// Server side: recv is client->server, send is server->client.
	cConn, err := crypto.NewRecordConn(obfsConn, cfg.AEADMethod, pskS2C, pskC2S)
	if err != nil {
		return err
	}

	msg, err := tunnel.ReadKIPMessage(cConn)
	if err != nil {
		return err
	}
	if msg.Type != tunnel.KIPTypeClientHello {
		return fmt.Errorf("unexpected handshake message: %d", msg.Type)
	}
	ch, err := tunnel.DecodeKIPClientHelloPayload(msg.Payload)
	if err != nil {
		return err
	}
	if connutil.AbsInt64(time.Now().Unix()-ch.Timestamp.Unix()) > 60 {
		return fmt.Errorf("time skew/replay")
	}
	return nil
}

// HandshakeResult bundles all outputs from the server-side handshake into a single struct
// so that callers can pick the fields they need without multiple function variants.
type HandshakeResult struct {
	Conn     net.Conn          // Upgraded tunnel connection (decrypted / de-obfuscated).
	UserHash string            // Stable per-user identifier (hex, 16 chars). May be empty.
	Fail     func(error) error // Wraps an error into HandshakeError with recorded data for fallback.
}

// ServerHandshakeCore performs the full server-side handshake and returns a HandshakeResult.
// It does NOT read the target address — the caller decides what to do with the tunnel.
func ServerHandshakeCore(rawConn net.Conn, cfg *ProtocolConfig) (*HandshakeResult, error) {
	conn, userHash, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, err
	}
	return &HandshakeResult{Conn: conn, UserHash: userHash, Fail: fail}, nil
}

// ServerHandshake performs the Sudoku server-side handshake.
//
// It completes the full handshake pipeline (HTTP mask → Sudoku decoding → AEAD decryption →
// timestamp verification) and then reads the target address from the tunnel.
//
// On failure, the returned error may be a *HandshakeError containing raw data for fallback handling.
func ServerHandshake(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, string, error) {
	conn, targetAddr, _, err := ServerHandshakeWithUserHash(rawConn, cfg)
	return conn, targetAddr, err
}

// ServerHandshakeWithUserHash is like ServerHandshake but also returns a stable per-user identifier extracted
// from the client handshake: hex(sha256(privateKey)[:8]) for official clients using split private keys.
func ServerHandshakeWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, string, string, error) {
	if cfg == nil {
		return nil, "", "", fmt.Errorf("config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, "", "", fmt.Errorf("invalid config: %w", err)
	}

	conn, userHash, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, "", "", err
	}

	msg, err := readFirstSessionMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, "", "", fail(fmt.Errorf("read session message failed: %w", err))
	}
	if msg.Type != tunnel.KIPTypeOpenTCP {
		_ = conn.Close()
		return nil, "", "", fail(fmt.Errorf("unexpected session message: %d", msg.Type))
	}

	targetAddr, _, _, err := protocol.ReadAddress(bytes.NewReader(msg.Payload))
	if err != nil {
		_ = conn.Close()
		return nil, "", "", fail(fmt.Errorf("decode target address failed: %w", err))
	}

	return conn, targetAddr, userHash, nil
}

// ServerHandshakeAuto upgrades the connection and detects whether it is a UoT (UDP-over-TCP) session.
//
// Returns:
//   - conn: the upgraded tunnel connection
//   - targetAddr: valid only when isUoT=false
//   - isUoT=true: caller should run HandleUoT(conn) instead of reading a target address
func ServerHandshakeAuto(rawConn net.Conn, cfg *ProtocolConfig) (conn net.Conn, targetAddr string, isUoT bool, err error) {
	conn, _, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, "", false, err
	}

	msg, err := readFirstSessionMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, "", false, fail(fmt.Errorf("read session message failed: %w", err))
	}
	switch msg.Type {
	case tunnel.KIPTypeStartUoT:
		return conn, "", true, nil
	case tunnel.KIPTypeOpenTCP:
		targetAddr, _, _, err = protocol.ReadAddress(bytes.NewReader(msg.Payload))
		if err != nil {
			_ = conn.Close()
			return nil, "", false, fail(fmt.Errorf("decode target address failed: %w", err))
		}
		return conn, targetAddr, false, nil
	default:
		_ = conn.Close()
		return nil, "", false, fail(fmt.Errorf("unexpected session message: %d", msg.Type))
	}
}

// ServerHandshakeAutoWithUserHash is like ServerHandshakeAuto but also returns the per-user handshake identifier.
func ServerHandshakeAutoWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (conn net.Conn, targetAddr string, isUoT bool, userHash string, err error) {
	conn, userHash, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	if err != nil {
		return nil, "", false, "", err
	}

	msg, err := readFirstSessionMessage(conn)
	if err != nil {
		_ = conn.Close()
		return nil, "", false, "", fail(fmt.Errorf("read session message failed: %w", err))
	}
	switch msg.Type {
	case tunnel.KIPTypeStartUoT:
		return conn, "", true, userHash, nil
	case tunnel.KIPTypeOpenTCP:
		targetAddr, _, _, err = protocol.ReadAddress(bytes.NewReader(msg.Payload))
		if err != nil {
			_ = conn.Close()
			return nil, "", false, "", fail(fmt.Errorf("decode target address failed: %w", err))
		}
		return conn, targetAddr, false, userHash, nil
	default:
		_ = conn.Close()
		return nil, "", false, "", fail(fmt.Errorf("unexpected session message: %d", msg.Type))
	}
}

// ServerHandshakeFlexible upgrades the connection and leaves payload parsing (address or UoT) to the caller.
// The returned fail function wraps errors into HandshakeError with recorded data for fallback handling.
func ServerHandshakeFlexible(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, func(error) error, error) {
	return serverHandshakeCore(rawConn, cfg)
}

// ServerHandshakeFlexibleWithUserHash is like ServerHandshakeFlexible but also returns the per-user handshake identifier.
func ServerHandshakeFlexibleWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, string, func(error) error, error) {
	return serverHandshakeCoreWithUserHash(rawConn, cfg)
}

func serverHandshakeCore(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, func(error) error, error) {
	conn, _, fail, err := serverHandshakeCoreWithUserHash(rawConn, cfg)
	return conn, fail, err
}

func readFirstSessionMessage(conn net.Conn) (*tunnel.KIPMessage, error) {
	for {
		msg, err := tunnel.ReadKIPMessage(conn)
		if err != nil {
			return nil, err
		}
		if msg.Type == tunnel.KIPTypeKeepAlive {
			continue
		}
		return msg, nil
	}
}

func serverHandshakeCoreWithUserHash(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, string, func(error) error, error) {
	if cfg == nil {
		return nil, "", nil, fmt.Errorf("config is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, "", nil, fmt.Errorf("invalid config: %w", err)
	}
	if userHash, ok := httpmask.EarlyHandshakeUserHash(rawConn); ok {
		fail := func(originalErr error) error { return originalErr }
		return rawConn, userHash, fail, nil
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
			return nil, "", nil, &HandshakeError{
				Err:            fmt.Errorf("invalid http header: %w", err),
				RawConn:        rawConn,
				HTTPHeaderData: httpHeaderData,
				ReadData:       nil,
			}
		}
	}

	tables := cfg.tableCandidates()
	selectedTable, preRead, err := tunnel.SelectTableByProbe(bufReader, tables, func(probe []byte, table *sudoku.Table) error {
		return probeHandshakeBytes(probe, cfg, table)
	})
	if err != nil {
		rawConn.SetReadDeadline(time.Time{})
		return nil, "", nil, &HandshakeError{
			Err:            err,
			RawConn:        rawConn,
			HTTPHeaderData: httpHeaderData,
			ReadData:       preRead,
		}
	}

	baseConn := tunnel.NewPreBufferedConn(rawConn, preRead)
	sConn, obfsConn := buildServerObfsConn(baseConn, cfg, selectedTable, true)

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

	pskC2S, pskS2C := tunnel.DerivePSKDirectionalBases(cfg.Key)
	cConn, err := crypto.NewRecordConn(obfsConn, cfg.AEADMethod, pskS2C, pskC2S)
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("crypto setup failed: %w", err))
	}

	msg, err := tunnel.ReadKIPMessage(cConn)
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("read client hello failed: %w", err))
	}
	if msg.Type != tunnel.KIPTypeClientHello {
		return nil, "", nil, fail(fmt.Errorf("unexpected handshake message: %d", msg.Type))
	}
	ch, err := tunnel.DecodeKIPClientHelloPayload(msg.Payload)
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("decode client hello failed: %w", err))
	}
	if connutil.AbsInt64(time.Now().Unix()-ch.Timestamp.Unix()) > 60 {
		return nil, "", nil, fail(fmt.Errorf("time skew/replay"))
	}

	userHash := hex.EncodeToString(ch.UserHash[:])

	curve := ecdh.X25519()
	serverEphemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("ecdh generate failed: %w", err))
	}
	shared, err := tunnel.X25519SharedSecret(serverEphemeral, ch.ClientPub[:])
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("ecdh failed: %w", err))
	}
	sessC2S, sessS2C, err := tunnel.DeriveSessionDirectionalBases(cfg.Key, shared, ch.Nonce)
	if err != nil {
		return nil, "", nil, fail(fmt.Errorf("derive session keys failed: %w", err))
	}

	var serverPub [32]byte
	copy(serverPub[:], serverEphemeral.PublicKey().Bytes())
	sh := &tunnel.KIPServerHello{
		Nonce:         ch.Nonce,
		ServerPub:     serverPub,
		SelectedFeats: ch.Features,
	}
	if err := tunnel.WriteKIPMessage(cConn, tunnel.KIPTypeServerHello, sh.EncodePayload()); err != nil {
		return nil, "", nil, fail(fmt.Errorf("write server hello failed: %w", err))
	}
	if err := cConn.Rekey(sessS2C, sessC2S); err != nil {
		return nil, "", nil, fail(fmt.Errorf("rekey failed: %w", err))
	}

	sConn.StopRecording()

	rawConn.SetReadDeadline(time.Time{})
	return cConn, userHash, fail, nil
}
