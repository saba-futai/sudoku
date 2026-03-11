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
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/pkg/connutil"
)

const (
	kipMagic = "kip"

	KIPTypeClientHello byte = 0x01
	KIPTypeServerHello byte = 0x02

	KIPTypeOpenTCP   byte = 0x10
	KIPTypeStartMux  byte = 0x11
	KIPTypeStartUoT  byte = 0x12
	KIPTypeStartRev  byte = 0x13
	KIPTypeKeepAlive byte = 0x14
)

// KIP feature bits are advisory capability flags negotiated during the handshake.
// They are intentionally conservative and represent control-plane message families.
const (
	KIPFeatOpenTCP   uint32 = 1 << 0
	KIPFeatMux       uint32 = 1 << 1
	KIPFeatUoT       uint32 = 1 << 2
	KIPFeatReverse   uint32 = 1 << 3
	KIPFeatKeepAlive uint32 = 1 << 4

	KIPFeatAll = KIPFeatOpenTCP | KIPFeatMux | KIPFeatUoT | KIPFeatReverse | KIPFeatKeepAlive
)

const (
	kipHelloUserHashSize = 8
	kipHelloNonceSize    = 16
	kipHelloPubSize      = 32
)

const (
	kipMaxPayload = 64 * 1024
)

var errKIP = errors.New("kip protocol error")

type KIPMessage struct {
	Type    byte
	Payload []byte
}

func WriteKIPMessage(w io.Writer, typ byte, payload []byte) error {
	if w == nil {
		return fmt.Errorf("%w: nil writer", errKIP)
	}
	if len(payload) > kipMaxPayload {
		return fmt.Errorf("%w: payload too large: %d", errKIP, len(payload))
	}

	var hdr [3 + 1 + 2]byte
	copy(hdr[:3], []byte(kipMagic))
	hdr[3] = typ
	binary.BigEndian.PutUint16(hdr[4:], uint16(len(payload)))

	if err := connutil.WriteFull(w, hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	return connutil.WriteFull(w, payload)
}

func ReadKIPMessage(r io.Reader) (*KIPMessage, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", errKIP)
	}
	var hdr [3 + 1 + 2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if string(hdr[:3]) != kipMagic {
		return nil, fmt.Errorf("%w: bad magic", errKIP)
	}
	typ := hdr[3]
	n := int(binary.BigEndian.Uint16(hdr[4:]))
	if n < 0 || n > kipMaxPayload {
		return nil, fmt.Errorf("%w: invalid payload length: %d", errKIP, n)
	}
	var payload []byte
	if n > 0 {
		payload = make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	return &KIPMessage{Type: typ, Payload: payload}, nil
}

type KIPClientHello struct {
	Timestamp time.Time
	UserHash  [kipHelloUserHashSize]byte
	Nonce     [kipHelloNonceSize]byte
	ClientPub [kipHelloPubSize]byte
	Features  uint32
}

type KIPServerHello struct {
	Nonce         [kipHelloNonceSize]byte
	ServerPub     [kipHelloPubSize]byte
	SelectedFeats uint32
}

func kipUserHashFromPrivateKey(privateKey []byte, fallbackPSK string) [kipHelloUserHashSize]byte {
	var out [kipHelloUserHashSize]byte
	if len(privateKey) > 0 {
		h := sha256.Sum256(privateKey)
		copy(out[:], h[:kipHelloUserHashSize])
		return out
	}
	h := sha256.Sum256([]byte(strings.TrimSpace(fallbackPSK)))
	copy(out[:], h[:kipHelloUserHashSize])
	return out
}

func kipUserHashHex(userHash [kipHelloUserHashSize]byte) string {
	return hex.EncodeToString(userHash[:])
}

func (m *KIPClientHello) EncodePayload() []byte {
	var b bytes.Buffer
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(m.Timestamp.Unix()))
	b.Write(tmp[:])
	b.Write(m.UserHash[:])
	b.Write(m.Nonce[:])
	b.Write(m.ClientPub[:])
	var f [4]byte
	binary.BigEndian.PutUint32(f[:], m.Features)
	b.Write(f[:])
	return b.Bytes()
}

func DecodeKIPClientHelloPayload(payload []byte) (*KIPClientHello, error) {
	const minLen = 8 + kipHelloUserHashSize + kipHelloNonceSize + kipHelloPubSize + 4
	if len(payload) < minLen {
		return nil, fmt.Errorf("%w: client hello too short", errKIP)
	}
	var h KIPClientHello
	ts := int64(binary.BigEndian.Uint64(payload[:8]))
	h.Timestamp = time.Unix(ts, 0)
	off := 8
	copy(h.UserHash[:], payload[off:off+kipHelloUserHashSize])
	off += kipHelloUserHashSize
	copy(h.Nonce[:], payload[off:off+kipHelloNonceSize])
	off += kipHelloNonceSize
	copy(h.ClientPub[:], payload[off:off+kipHelloPubSize])
	off += kipHelloPubSize
	h.Features = binary.BigEndian.Uint32(payload[off : off+4])
	return &h, nil
}

func (m *KIPServerHello) EncodePayload() []byte {
	var b bytes.Buffer
	b.Write(m.Nonce[:])
	b.Write(m.ServerPub[:])
	var f [4]byte
	binary.BigEndian.PutUint32(f[:], m.SelectedFeats)
	b.Write(f[:])
	return b.Bytes()
}

func DecodeKIPServerHelloPayload(payload []byte) (*KIPServerHello, error) {
	const want = kipHelloNonceSize + kipHelloPubSize + 4
	if len(payload) != want {
		return nil, fmt.Errorf("%w: server hello bad len: %d", errKIP, len(payload))
	}
	var h KIPServerHello
	off := 0
	copy(h.Nonce[:], payload[off:off+kipHelloNonceSize])
	off += kipHelloNonceSize
	copy(h.ServerPub[:], payload[off:off+kipHelloPubSize])
	off += kipHelloPubSize
	h.SelectedFeats = binary.BigEndian.Uint32(payload[off : off+4])
	return &h, nil
}
