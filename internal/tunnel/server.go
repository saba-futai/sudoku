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
package tunnel

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/connutil"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

const (
	HandshakeTimeout = 5 * time.Second
)

// BufferedConn wraps net.Conn and bufio.Reader
type BufferedConn struct {
	net.Conn
	r          *bufio.Reader
	recorder   *bytes.Buffer
	recordLock sync.Mutex
}

func (bc *BufferedConn) CloseWrite() error {
	if bc == nil {
		return nil
	}
	return connutil.TryCloseWrite(bc.Conn)
}

func (bc *BufferedConn) CloseRead() error {
	if bc == nil {
		return nil
	}
	return connutil.TryCloseRead(bc.Conn)
}

func (bc *BufferedConn) Read(p []byte) (n int, err error) {
	n, err = bc.r.Read(p)
	if n > 0 && bc.recorder != nil {
		bc.recordLock.Lock()
		bc.recorder.Write(p[:n])
		bc.recordLock.Unlock()
	}
	return n, err
}

// PreBufferedConn for Split detection peek
type PreBufferedConn struct {
	net.Conn
	buf []byte
}

func (p *PreBufferedConn) CloseWrite() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseWrite(p.Conn)
}

func (p *PreBufferedConn) CloseRead() error {
	if p == nil {
		return nil
	}
	return connutil.TryCloseRead(p.Conn)
}

// NewPreBufferedConn replays the provided bytes before reading from the underlying connection.
func NewPreBufferedConn(conn net.Conn, preRead []byte) net.Conn {
	return &PreBufferedConn{Conn: conn, buf: preRead}
}

func (p *PreBufferedConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	if p.Conn == nil {
		return 0, io.EOF
	}
	return p.Conn.Read(b)
}

// GetBufferedAndRecorded returns all data that has been consumed and buffered
func (bc *BufferedConn) GetBufferedAndRecorded() []byte {
	if bc == nil {
		return nil
	}

	bc.recordLock.Lock()
	defer bc.recordLock.Unlock()

	var recorded []byte
	if bc.recorder != nil {
		recorded = bc.recorder.Bytes()
	}

	// Also get any buffered data that hasn't been read yet
	buffered := bc.r.Buffered()
	if buffered > 0 {
		peeked, _ := bc.r.Peek(buffered)
		full := make([]byte, len(recorded)+len(peeked))
		copy(full, recorded)
		copy(full[len(recorded):], peeked)
		return full
	}
	return recorded
}

// SuspiciousError indicates a potential attack or protocol violation
type SuspiciousError struct {
	Err  error
	Conn net.Conn // The connection at the state where error occurred (for fallback/logging)
}

func (e *SuspiciousError) Error() string {
	return e.Err.Error()
}

// HandshakeAndUpgrade wraps the raw connection with Sudoku/Crypto and performs handshake.
func HandshakeAndUpgrade(rawConn net.Conn, cfg *config.Config, table *sudoku.Table) (net.Conn, error) {
	return HandshakeAndUpgradeWithTables(rawConn, cfg, []*sudoku.Table{table})
}

// HandshakeMeta carries optional, per-connection identity hints extracted from the client handshake.
//
// UserHash is a hex-encoded 8-byte value derived from the client's private key (when the client uses one):
// sha256(privateKey)[:8]. For clients without a private key, it is derived from the handshake nonce bytes.
type HandshakeMeta struct {
	// UserHash is a hex-encoded 8-byte client identifier.
	// When the client has a private key, it is sha256(privateKey)[:8].
	UserHash string
}

type recordedConn struct {
	net.Conn
	recorded []byte
}

func (rc *recordedConn) CloseWrite() error {
	if rc == nil {
		return nil
	}
	return connutil.TryCloseWrite(rc.Conn)
}

func (rc *recordedConn) CloseRead() error {
	if rc == nil {
		return nil
	}
	return connutil.TryCloseRead(rc.Conn)
}

func (rc *recordedConn) GetBufferedAndRecorded() []byte {
	return rc.recorded
}

type prefixedRecorderConn struct {
	net.Conn
	prefix []byte
}

func (pc *prefixedRecorderConn) CloseWrite() error {
	if pc == nil {
		return nil
	}
	return connutil.TryCloseWrite(pc.Conn)
}

func (pc *prefixedRecorderConn) CloseRead() error {
	if pc == nil {
		return nil
	}
	return connutil.TryCloseRead(pc.Conn)
}

func (pc *prefixedRecorderConn) GetBufferedAndRecorded() []byte {
	var rest []byte
	if r, ok := pc.Conn.(interface{ GetBufferedAndRecorded() []byte }); ok {
		rest = r.GetBufferedAndRecorded()
	}
	out := make([]byte, 0, len(pc.prefix)+len(rest))
	out = append(out, pc.prefix...)
	out = append(out, rest...)
	return out
}

func probeHandshakeBytes(probe []byte, cfg *config.Config, table *sudoku.Table) error {
	rc := &connutil.ReadOnlyConn{Reader: bytes.NewReader(probe)}
	_, obfsConn := buildObfsConnForServer(rc, table, cfg, false)
	pskC2S, pskS2C := derivePSKDirectionalBases(cfg.Key)
	// Server side: recv is client->server, send is server->client.
	cConn, err := crypto.NewRecordConn(obfsConn, cfg.AEAD, pskS2C, pskC2S)
	if err != nil {
		return err
	}

	msg, err := ReadKIPMessage(cConn)
	if err != nil {
		return err
	}
	if msg.Type != KIPTypeClientHello {
		return fmt.Errorf("unexpected handshake message: %d", msg.Type)
	}
	ch, err := DecodeKIPClientHelloPayload(msg.Payload)
	if err != nil {
		return err
	}
	if connutil.AbsInt64(time.Now().Unix()-ch.Timestamp.Unix()) > int64(kipHandshakeSkew.Seconds()) {
		return fmt.Errorf("time skew/replay")
	}
	return nil
}

// HandshakeAndUpgradeWithTables performs the handshake by probing one of multiple tables.
// This enables per-connection table rotation without adding a plaintext table selector.
func HandshakeAndUpgradeWithTables(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table) (net.Conn, error) {
	conn, _, err := HandshakeAndUpgradeWithTablesMeta(rawConn, cfg, tables)
	return conn, err
}

// HandshakeAndUpgradeWithTablesMeta is like HandshakeAndUpgradeWithTables but also returns handshake metadata
// that can be used for multi-user accounting (e.g., per-split-private-key identification).
func HandshakeAndUpgradeWithTablesMeta(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table) (net.Conn, *HandshakeMeta, error) {
	if rawConn == nil {
		return nil, nil, fmt.Errorf("nil conn")
	}
	if cfg == nil {
		return nil, nil, fmt.Errorf("nil config")
	}
	if userHash, ok := httpmask.EarlyHandshakeUserHash(rawConn); ok {
		return rawConn, &HandshakeMeta{UserHash: userHash}, nil
	}

	// 0) Byte-level prelude handling (legacy HTTP mask + buffered probe bytes).
	bufReader := bufio.NewReader(rawConn)
	_ = rawConn.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	defer func() { _ = rawConn.SetReadDeadline(time.Time{}) }()

	httpHeaderData, susp := maybeConsumeLegacyHTTPMask(rawConn, bufReader, cfg)
	if susp != nil {
		return nil, nil, susp
	}

	// 1. Sudoku Layer
	if !cfg.EnablePureDownlink && cfg.AEAD == "none" {
		return nil, nil, fmt.Errorf("enable_pure_downlink=false requires AEAD")
	}

	selectedTable, preRead, err := SelectTableByProbe(bufReader, tables, func(probe []byte, table *sudoku.Table) error {
		return probeHandshakeBytes(probe, cfg, table)
	})
	if err != nil {
		combined := make([]byte, 0, len(httpHeaderData)+len(preRead))
		combined = append(combined, httpHeaderData...)
		combined = append(combined, preRead...)
		return nil, nil, &SuspiciousError{Err: err, Conn: &recordedConn{Conn: rawConn, recorded: combined}}
	}

	baseConn := NewPreBufferedConn(rawConn, preRead)
	sConn, obfsConn := buildObfsConnForServer(baseConn, selectedTable, cfg, true)

	// 2. Crypto Layer
	pskC2S, pskS2C := derivePSKDirectionalBases(cfg.Key)
	// Server side: recv is client->server, send is server->client.
	cConn, err := crypto.NewRecordConn(obfsConn, cfg.AEAD, pskS2C, pskC2S)
	if err != nil {
		return nil, nil, fmt.Errorf("crypto setup failed: %w", err)
	}

	// 3. Handshake
	_ = rawConn.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	msg, err := ReadKIPMessage(cConn)
	if err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("handshake read failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	if msg.Type != KIPTypeClientHello {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("unexpected handshake message: %d", msg.Type), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	ch, err := DecodeKIPClientHelloPayload(msg.Payload)
	if err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("decode client hello failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	if connutil.AbsInt64(time.Now().Unix()-ch.Timestamp.Unix()) > int64(kipHandshakeSkew.Seconds()) {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("time skew/replay"), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	meta := &HandshakeMeta{UserHash: kipUserHashHex(ch.UserHash)}
	if !globalHandshakeReplay.allow(meta.UserHash, ch.Nonce, time.Now()) {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("replay"), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	curve := ecdh.X25519()
	serverEphemeral, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("ecdh generate failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	shared, err := x25519SharedSecret(serverEphemeral, ch.ClientPub[:])
	if err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("ecdh failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	sessC2S, sessS2C, err := deriveSessionDirectionalBases(cfg.Key, shared, ch.Nonce)
	if err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("derive session keys failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	var serverPub [kipHelloPubSize]byte
	copy(serverPub[:], serverEphemeral.PublicKey().Bytes())
	sh := &KIPServerHello{
		Nonce:         ch.Nonce,
		ServerPub:     serverPub,
		SelectedFeats: ch.Features & KIPFeatAll,
	}
	if err := WriteKIPMessage(cConn, KIPTypeServerHello, sh.EncodePayload()); err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("write server hello failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	if err := cConn.Rekey(sessS2C, sessC2S); err != nil {
		return nil, nil, &SuspiciousError{Err: fmt.Errorf("rekey failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	sConn.StopRecording()
	return cConn, meta, nil
}

func maybeConsumeLegacyHTTPMask(rawConn net.Conn, r *bufio.Reader, cfg *config.Config) ([]byte, *SuspiciousError) {
	if rawConn == nil || r == nil || cfg == nil || cfg.HTTPMask.Disable {
		return nil, nil
	}

	peekBytes, _ := r.Peek(4) // Ignore error; if peek fails, let subsequent read handle it.
	if !httpmask.LooksLikeHTTPRequestStart(peekBytes) {
		return nil, nil
	}

	consumed, err := httpmask.ConsumeHeader(r)
	if err == nil {
		return consumed, nil
	}

	// Return rawConn wrapped in BufferedConn so caller can handle fallback.
	recorder := new(bytes.Buffer)
	if len(consumed) > 0 {
		recorder.Write(consumed)
	}
	badConn := &BufferedConn{
		Conn:     rawConn,
		r:        r,
		recorder: recorder,
	}
	return consumed, &SuspiciousError{Err: fmt.Errorf("invalid http header: %w", err), Conn: badConn}
}
