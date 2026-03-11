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
package sudoku

import (
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/pkg/connutil"
)

// DirectionalConn wires separate reader/writer streams onto a single net.Conn.
// It is useful for asymmetric obfuscation (e.g. packed downlink, sudoku uplink).
type DirectionalConn struct {
	net.Conn
	reader  io.Reader
	writer  io.Writer
	closers []func() error
}

func NewDirectionalConn(base net.Conn, reader io.Reader, writer io.Writer, closers ...func() error) *DirectionalConn {
	return &DirectionalConn{
		Conn:    base,
		reader:  reader,
		writer:  writer,
		closers: closers,
	}
}

func (c *DirectionalConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *DirectionalConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

func (c *DirectionalConn) CloseRead() error {
	if err := connutil.TryCloseRead(c.reader); err != nil {
		return err
	}
	return connutil.TryCloseRead(c.Conn)
}

func (c *DirectionalConn) CloseWrite() error {
	firstErr := connutil.RunClosers(c.closers...)
	if err := connutil.TryCloseWrite(c.writer); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := connutil.TryCloseWrite(c.Conn); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (c *DirectionalConn) Close() error {
	firstErr := connutil.RunClosers(c.closers...)
	if err := c.Conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
