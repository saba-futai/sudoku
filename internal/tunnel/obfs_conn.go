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
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

type ObfsUplinkMode byte

const (
	ObfsUplinkPure ObfsUplinkMode = iota
	ObfsUplinkPacked
)

type RecordingObfsConn interface {
	net.Conn
	StopRecording()
	GetBufferedAndRecorded() []byte
}

type obfsMetaConn interface {
	SudokuUplinkPacked() bool
}

type connWithObfsMeta struct {
	net.Conn
	uplinkPacked bool
}

func (c *connWithObfsMeta) SudokuUplinkPacked() bool {
	if c == nil {
		return false
	}
	return c.uplinkPacked
}

func WrapConnWithObfsMeta(conn net.Conn, uplinkMode ObfsUplinkMode) net.Conn {
	if conn == nil {
		return nil
	}
	return &connWithObfsMeta{Conn: conn, uplinkPacked: uplinkMode == ObfsUplinkPacked}
}

func wrapConnWithObfsMeta(conn net.Conn, uplinkMode ObfsUplinkMode) net.Conn {
	return WrapConnWithObfsMeta(conn, uplinkMode)
}

type downlinkWriterSwitcher interface {
	ReplaceWriter(writer io.Writer, closers ...func() error)
}

func ConnUplinkPacked(conn net.Conn) (bool, bool) {
	if conn == nil {
		return false, false
	}
	v, ok := conn.(obfsMetaConn)
	if !ok {
		return false, false
	}
	return v.SudokuUplinkPacked(), true
}

func SwitchConnDownlinkWriter(conn net.Conn, writer io.Writer, closers ...func() error) bool {
	switch v := conn.(type) {
	case downlinkWriterSwitcher:
		v.ReplaceWriter(writer, closers...)
		return true
	case *connWithObfsMeta:
		if inner, ok := v.Conn.(downlinkWriterSwitcher); ok {
			inner.ReplaceWriter(writer, closers...)
			return true
		}
	}
	return false
}

func buildClientObfsConnForMode(raw net.Conn, table *sudoku.Table, paddingMin, paddingMax int, pureDownlink bool, uplinkMode ObfsUplinkMode) net.Conn {
	uplinkTable := table
	downlinkTable := table.OppositeDirection()

	var reader io.Reader
	if pureDownlink {
		reader = sudoku.NewConn(raw, downlinkTable, paddingMin, paddingMax, false)
	} else {
		reader = sudoku.NewPackedConn(raw, downlinkTable, paddingMin, paddingMax)
	}

	var writer io.Writer
	var closers []func() error
	switch uplinkMode {
	case ObfsUplinkPacked:
		packed := sudoku.NewPackedConn(raw, uplinkTable, paddingMin, paddingMax)
		writer = packed
		closers = append(closers, packed.Flush)
	default:
		writer = sudoku.NewConn(raw, uplinkTable, paddingMin, paddingMax, false)
	}

	return wrapConnWithObfsMeta(sudoku.NewDirectionalConn(raw, reader, writer, closers...), uplinkMode)
}

func buildServerObfsConnForMode(raw net.Conn, table *sudoku.Table, paddingMin, paddingMax int, pureDownlink bool, uplinkMode ObfsUplinkMode, record bool) (RecordingObfsConn, net.Conn) {
	var uplink RecordingObfsConn
	switch uplinkMode {
	case ObfsUplinkPacked:
		uplink = sudoku.NewPackedConnWithRecord(raw, table, paddingMin, paddingMax, record)
	default:
		uplink = sudoku.NewConn(raw, table, paddingMin, paddingMax, record)
	}
	downlink, closers := sudoku.NewServerDownlinkWriter(raw, table.OppositeDirection(), paddingMin, paddingMax, pureDownlink)
	return uplink, wrapConnWithObfsMeta(sudoku.NewDirectionalConn(raw, uplink, downlink, closers...), uplinkMode)
}

// buildObfsConnForClient builds the obfuscation layer for client side, keeping Sudoku on uplink.
func buildObfsConnForClient(raw net.Conn, table *sudoku.Table, cfg *config.Config) net.Conn {
	return buildClientObfsConnForMode(raw, table, cfg.PaddingMin, cfg.PaddingMax, cfg.EnablePureDownlink, ObfsUplinkPure)
}

func buildReverseObfsConnForClient(raw net.Conn, table *sudoku.Table, cfg *config.Config) net.Conn {
	return buildClientObfsConnForMode(raw, table, cfg.PaddingMin, cfg.PaddingMax, cfg.EnablePureDownlink, ObfsUplinkPacked)
}

// buildObfsConnForServer builds the obfuscation layer for server side, keeping Sudoku on uplink.
// It returns the reader Sudoku connection (for fallback recording) and the composed net.Conn.
func buildObfsConnForServer(raw net.Conn, table *sudoku.Table, cfg *config.Config, uplinkMode ObfsUplinkMode, record bool) (RecordingObfsConn, net.Conn) {
	return buildServerObfsConnForMode(raw, table, cfg.PaddingMin, cfg.PaddingMax, cfg.EnablePureDownlink, uplinkMode, record)
}
