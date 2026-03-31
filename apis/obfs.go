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
	"io"
	"net"

	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/obfs/sudoku"
)

func buildClientObfsConn(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table) net.Conn {
	return buildClientObfsConnForUplinkMode(raw, cfg, table, tunnel.ObfsUplinkPure)
}

func buildReverseClientObfsConn(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table) net.Conn {
	return buildClientObfsConnForUplinkMode(raw, cfg, table, tunnel.ObfsUplinkPacked)
}

func buildClientObfsConnForUplinkMode(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table, uplinkMode tunnel.ObfsUplinkMode) net.Conn {
	downlinkTable := table.OppositeDirection()

	var reader io.Reader
	if cfg.EnablePureDownlink {
		reader = sudoku.NewConn(raw, downlinkTable, cfg.PaddingMin, cfg.PaddingMax, false)
	} else {
		reader = sudoku.NewPackedConn(raw, downlinkTable, cfg.PaddingMin, cfg.PaddingMax)
	}

	var writer io.Writer
	var closers []func() error
	switch uplinkMode {
	case tunnel.ObfsUplinkPacked:
		packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
		writer = packed
		closers = append(closers, packed.Flush)
	default:
		writer = sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, false)
	}

	return sudoku.NewDirectionalConn(raw, reader, writer, closers...)
}

type recordingObfsConn interface {
	net.Conn
	StopRecording()
	GetBufferedAndRecorded() []byte
}

func buildServerObfsConn(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table, uplinkMode tunnel.ObfsUplinkMode, record bool) (recordingObfsConn, net.Conn) {
	var uplink recordingObfsConn
	switch uplinkMode {
	case tunnel.ObfsUplinkPacked:
		uplink = sudoku.NewPackedConnWithRecord(raw, table, cfg.PaddingMin, cfg.PaddingMax, record)
	default:
		uplink = sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, record)
	}
	downlink, closers := sudoku.NewServerDownlinkWriter(raw, table, cfg.PaddingMin, cfg.PaddingMax, cfg.EnablePureDownlink)
	return uplink, sudoku.NewDirectionalConn(raw, uplink, downlink, closers...)
}
