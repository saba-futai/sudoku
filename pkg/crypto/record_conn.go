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
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	"github.com/saba-futai/sudoku/pkg/connutil"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeyUpdateAfterBytes controls automatic key rotation based on plaintext bytes.
// It is a package var (not config) to enable targeted tests with smaller thresholds.
var KeyUpdateAfterBytes int64 = 32 << 20 // 32 MiB

const (
	recordHeaderSize = 12 // epoch(uint32) + seq(uint64) - also used as nonce+AAD.
	maxFrameBodySize = 65535
)

type recordKeys struct {
	baseSend []byte
	baseRecv []byte
}

// RecordConn is a framed AEAD net.Conn with:
//   - deterministic per-record nonce (epoch+seq)
//   - per-direction key rotation (epoch), driven by plaintext byte counters
//   - replay/out-of-order protection within the connection (strict seq check)
//
// Wire format per record:
//   - uint16 bodyLen
//   - header[12] = epoch(uint32 BE) || seq(uint64 BE)  (plaintext)
//   - ciphertext = AEAD(header as nonce, plaintext, header as AAD)
type RecordConn struct {
	net.Conn
	method string

	writeMu sync.Mutex
	readMu  sync.Mutex

	keys recordKeys

	sendAEAD      cipher.AEAD
	sendAEADEpoch uint32

	recvAEAD      cipher.AEAD
	recvAEADEpoch uint32

	// Send direction state.
	sendEpoch        uint32
	sendSeq          uint64
	sendBytes        int64
	sendEpochUpdates uint32

	// Receive direction state.
	recvEpoch       uint32
	recvSeq         uint64
	recvInitialized bool

	readBuf bytes.Buffer

	// writeFrame is a reusable buffer for [len||header||ciphertext] on the wire.
	// Guarded by writeMu.
	writeFrame []byte
}

func (c *RecordConn) CloseWrite() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseWrite(c.Conn)
}

func (c *RecordConn) CloseRead() error {
	if c == nil {
		return nil
	}
	return connutil.TryCloseRead(c.Conn)
}

func NewRecordConn(c net.Conn, method string, baseSend, baseRecv []byte) (*RecordConn, error) {
	if c == nil {
		return nil, fmt.Errorf("nil conn")
	}
	method = normalizeAEADMethod(method)
	if method != "none" {
		if err := validateBaseKey(baseSend); err != nil {
			return nil, fmt.Errorf("invalid send base key: %w", err)
		}
		if err := validateBaseKey(baseRecv); err != nil {
			return nil, fmt.Errorf("invalid recv base key: %w", err)
		}
	}
	rc := &RecordConn{Conn: c, method: method}
	rc.keys = recordKeys{baseSend: cloneBytes(baseSend), baseRecv: cloneBytes(baseRecv)}
	if err := rc.resetTrafficState(); err != nil {
		return nil, err
	}
	return rc, nil
}

func (c *RecordConn) Rekey(baseSend, baseRecv []byte) error {
	if c == nil {
		return fmt.Errorf("nil conn")
	}
	if c.method != "none" {
		if err := validateBaseKey(baseSend); err != nil {
			return fmt.Errorf("invalid send base key: %w", err)
		}
		if err := validateBaseKey(baseRecv); err != nil {
			return fmt.Errorf("invalid recv base key: %w", err)
		}
	}
	c.writeMu.Lock()
	c.readMu.Lock()
	defer c.readMu.Unlock()
	defer c.writeMu.Unlock()

	c.keys = recordKeys{baseSend: cloneBytes(baseSend), baseRecv: cloneBytes(baseRecv)}
	if err := c.resetTrafficState(); err != nil {
		return err
	}
	c.readBuf.Reset()

	c.sendAEAD = nil
	c.recvAEAD = nil
	c.sendAEADEpoch = 0
	c.recvAEADEpoch = 0
	return nil
}

func (c *RecordConn) resetTrafficState() error {
	sendEpoch, sendSeq, err := randomRecordCounters()
	if err != nil {
		return fmt.Errorf("initialize record counters: %w", err)
	}
	c.sendEpoch = sendEpoch
	c.sendSeq = sendSeq
	c.sendBytes = 0
	c.sendEpochUpdates = 0
	c.recvEpoch = 0
	c.recvSeq = 0
	c.recvInitialized = false
	return nil
}

func normalizeAEADMethod(method string) string {
	switch method {
	case "", "chacha20-poly1305":
		return "chacha20-poly1305"
	case "aes-128-gcm", "none":
		return method
	default:
		return method
	}
}

func validateBaseKey(b []byte) error {
	if len(b) < 32 {
		return fmt.Errorf("need at least 32 bytes, got %d", len(b))
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return append([]byte(nil), b...)
}

func randomRecordCounters() (uint32, uint64, error) {
	epoch, err := randomNonZeroUint32()
	if err != nil {
		return 0, 0, err
	}
	seq, err := randomNonZeroUint64()
	if err != nil {
		return 0, 0, err
	}
	return epoch, seq, nil
}

func randomNonZeroUint32() (uint32, error) {
	var b [4]byte
	for {
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint32(b[:])
		if v != 0 && v != ^uint32(0) {
			return v, nil
		}
	}
}

func randomNonZeroUint64() (uint64, error) {
	var b [8]byte
	for {
		if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint64(b[:])
		if v != 0 && v != ^uint64(0) {
			return v, nil
		}
	}
}

func (c *RecordConn) newAEADFor(base []byte, epoch uint32) (cipher.AEAD, int, error) {
	method := c.method
	if method == "none" {
		return nil, 0, nil
	}
	key := deriveEpochKey(base, epoch, method)
	switch method {
	case "aes-128-gcm":
		block, err := aes.NewCipher(key[:16])
		if err != nil {
			return nil, 0, err
		}
		a, err := cipher.NewGCM(block)
		if err != nil {
			return nil, 0, err
		}
		if a.NonceSize() != recordHeaderSize {
			return nil, 0, fmt.Errorf("unexpected gcm nonce size: %d", a.NonceSize())
		}
		return a, 16, nil
	case "chacha20-poly1305":
		a, err := chacha20poly1305.New(key[:32])
		if err != nil {
			return nil, 0, err
		}
		if a.NonceSize() != recordHeaderSize {
			return nil, 0, fmt.Errorf("unexpected chacha nonce size: %d", a.NonceSize())
		}
		return a, 32, nil
	default:
		return nil, 0, fmt.Errorf("unsupported cipher: %s", method)
	}
}

func deriveEpochKey(base []byte, epoch uint32, method string) []byte {
	// Deterministic PRF-based KDF (per epoch):
	// key = HMAC-SHA256(base, "sudoku-record:" || method || epoch)
	//
	// Base is already derived from HKDF during handshake; this re-derivation is for per-epoch rotation
	// to reduce the value of long captures and limit impact of multi-target traffic analysis.
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], epoch)
	mac := hmac.New(sha256.New, base)
	_, _ = mac.Write([]byte("sudoku-record:"))
	_, _ = mac.Write([]byte(method))
	_, _ = mac.Write(b[:])
	return mac.Sum(nil)
}

func (c *RecordConn) maybeBumpSendEpochLocked(addedPlain int) error {
	ku := atomic.LoadInt64(&KeyUpdateAfterBytes)
	if ku <= 0 || c.method == "none" {
		return nil
	}
	c.sendBytes += int64(addedPlain)
	threshold := ku * int64(c.sendEpochUpdates+1)
	if c.sendBytes < threshold {
		return nil
	}
	c.sendEpoch++
	c.sendEpochUpdates++
	nextSeq, err := randomNonZeroUint64()
	if err != nil {
		return fmt.Errorf("rotate record seq: %w", err)
	}
	c.sendSeq = nextSeq
	return nil
}

func (c *RecordConn) Write(p []byte) (int, error) {
	if c == nil || c.Conn == nil {
		return 0, net.ErrClosed
	}
	if c.method == "none" {
		return c.Conn.Write(p)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	total := 0
	for len(p) > 0 {
		if c.sendAEAD == nil || c.sendAEADEpoch != c.sendEpoch {
			a, _, err := c.newAEADFor(c.keys.baseSend, c.sendEpoch)
			if err != nil {
				return total, err
			}
			c.sendAEAD = a
			c.sendAEADEpoch = c.sendEpoch
		}
		aead := c.sendAEAD

		maxPlain := maxFrameBodySize - recordHeaderSize - aead.Overhead()
		if maxPlain <= 0 {
			return total, errors.New("frame size too small")
		}
		n := len(p)
		if n > maxPlain {
			n = maxPlain
		}
		chunk := p[:n]
		p = p[n:]

		var header [recordHeaderSize]byte
		binary.BigEndian.PutUint32(header[:4], c.sendEpoch)
		binary.BigEndian.PutUint64(header[4:], c.sendSeq)
		c.sendSeq++

		cipherLen := n + aead.Overhead()
		bodyLen := recordHeaderSize + cipherLen
		frameLen := 2 + bodyLen
		if bodyLen > maxFrameBodySize {
			return total, errors.New("frame too large")
		}
		if cap(c.writeFrame) < frameLen {
			c.writeFrame = make([]byte, frameLen)
		}
		frame := c.writeFrame[:frameLen]
		binary.BigEndian.PutUint16(frame[:2], uint16(bodyLen))
		copy(frame[2:2+recordHeaderSize], header[:])

		dst := frame[2+recordHeaderSize : 2+recordHeaderSize : frameLen]
		_ = aead.Seal(dst[:0], header[:], chunk, header[:])

		if err := connutil.WriteFull(c.Conn, frame); err != nil {
			return total, err
		}

		total += n
		if err := c.maybeBumpSendEpochLocked(n); err != nil {
			return total, err
		}
	}
	return total, nil
}

func (c *RecordConn) Read(p []byte) (int, error) {
	if c == nil || c.Conn == nil {
		return 0, net.ErrClosed
	}
	if c.method == "none" {
		return c.Conn.Read(p)
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	if c.readBuf.Len() > 0 {
		return c.readBuf.Read(p)
	}

	var lenBuf [2]byte
	if _, err := io.ReadFull(c.Conn, lenBuf[:]); err != nil {
		return 0, err
	}
	bodyLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if bodyLen < recordHeaderSize {
		return 0, errors.New("frame too short")
	}
	if bodyLen > maxFrameBodySize {
		return 0, errors.New("frame too large")
	}

	body := make([]byte, bodyLen)
	if _, err := io.ReadFull(c.Conn, body); err != nil {
		return 0, err
	}
	header := body[:recordHeaderSize]
	ciphertext := body[recordHeaderSize:]

	epoch := binary.BigEndian.Uint32(header[:4])
	seq := binary.BigEndian.Uint64(header[4:])

	if c.recvInitialized {
		if epoch < c.recvEpoch {
			return 0, fmt.Errorf("replayed epoch: got %d want >=%d", epoch, c.recvEpoch)
		}
		if epoch == c.recvEpoch && seq != c.recvSeq {
			return 0, fmt.Errorf("out of order: epoch=%d got=%d want=%d", epoch, seq, c.recvSeq)
		}
		if epoch > c.recvEpoch {
			// Require strict monotonic epoch advance; allow some limited skipping to avoid hard deadlocks
			// if the peer rotates early due to its local byte counter. Large jumps are treated as suspicious.
			const maxJump = 8
			if epoch-c.recvEpoch > maxJump {
				return 0, fmt.Errorf("epoch jump too large: got=%d want<=%d", epoch-c.recvEpoch, maxJump)
			}
		}
	}

	if c.recvAEAD == nil || c.recvAEADEpoch != epoch {
		a, _, err := c.newAEADFor(c.keys.baseRecv, epoch)
		if err != nil {
			return 0, err
		}
		c.recvAEAD = a
		c.recvAEADEpoch = epoch
	}
	aead := c.recvAEAD

	plaintext, err := aead.Open(nil, header, ciphertext, header)
	if err != nil {
		return 0, fmt.Errorf("decryption failed: epoch=%d seq=%d: %w", epoch, seq, err)
	}
	c.recvEpoch = epoch
	c.recvSeq = seq + 1
	c.recvInitialized = true

	c.readBuf.Write(plaintext)
	return c.readBuf.Read(p)
}
