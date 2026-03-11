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
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/saba-futai/sudoku/pkg/connutil"

	"golang.org/x/crypto/chacha20poly1305"
)

type AEADConn struct {
	net.Conn
	aead      cipher.AEAD
	readBuf   bytes.Buffer
	nonceSize int
}

func (cc *AEADConn) CloseWrite() error {
	if cc == nil {
		return nil
	}
	return connutil.TryCloseWrite(cc.Conn)
}

func (cc *AEADConn) CloseRead() error {
	if cc == nil {
		return nil
	}
	return connutil.TryCloseRead(cc.Conn)
}

func NewAEADConn(c net.Conn, key string, method string) (*AEADConn, error) {
	if method == "none" {
		return &AEADConn{Conn: c, aead: nil}, nil
	}

	h := sha256.New()
	h.Write([]byte(key))
	keyBytes := h.Sum(nil)

	var aead cipher.AEAD
	var err error

	switch method {
	case "aes-128-gcm":
		block, _ := aes.NewCipher(keyBytes[:16])
		aead, err = cipher.NewGCM(block)
	case "chacha20-poly1305":
		aead, err = chacha20poly1305.New(keyBytes)
	default:
		return nil, fmt.Errorf("unsupported cipher: %s", method)
	}
	if err != nil {
		return nil, err
	}

	return &AEADConn{
		Conn:      c,
		aead:      aead,
		nonceSize: aead.NonceSize(),
	}, nil
}

func (cc *AEADConn) Write(p []byte) (int, error) {
	if cc.aead == nil {
		return cc.Conn.Write(p)
	}

	maxPayload := 65535 - cc.nonceSize - cc.aead.Overhead()
	totalWritten := 0
	var frameBuf bytes.Buffer
	header := make([]byte, 2)
	nonce := make([]byte, cc.nonceSize)

	for len(p) > 0 {
		chunkSize := len(p)
		if chunkSize > maxPayload {
			chunkSize = maxPayload
		}
		chunk := p[:chunkSize]
		p = p[chunkSize:]

		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return totalWritten, err
		}

		ciphertext := cc.aead.Seal(nil, nonce, chunk, nil)
		frameLen := len(nonce) + len(ciphertext)
		binary.BigEndian.PutUint16(header, uint16(frameLen))

		frameBuf.Reset()
		frameBuf.Write(header)
		frameBuf.Write(nonce)
		frameBuf.Write(ciphertext)

		if err := connutil.WriteFull(cc.Conn, frameBuf.Bytes()); err != nil {
			return totalWritten, err
		}
		totalWritten += chunkSize
	}
	return totalWritten, nil
}

func (cc *AEADConn) Read(p []byte) (int, error) {
	if cc.aead == nil {
		return cc.Conn.Read(p)
	}

	if cc.readBuf.Len() > 0 {
		return cc.readBuf.Read(p)
	}

	header := make([]byte, 2)
	if _, err := io.ReadFull(cc.Conn, header); err != nil {
		return 0, err
	}
	frameLen := int(binary.BigEndian.Uint16(header))

	body := make([]byte, frameLen)
	if _, err := io.ReadFull(cc.Conn, body); err != nil {
		return 0, err
	}

	if len(body) < cc.nonceSize {
		return 0, errors.New("frame too short")
	}
	nonce := body[:cc.nonceSize]
	ciphertext := body[cc.nonceSize:]

	plaintext, err := cc.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return 0, errors.New("decryption failed")
	}

	cc.readBuf.Write(plaintext)
	return cc.readBuf.Read(p)
}
