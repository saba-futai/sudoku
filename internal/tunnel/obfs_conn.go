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
	"net"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

const (
	DownlinkModePure   byte = 0x01
	DownlinkModePacked byte = 0x02
)

func downlinkModeByte(cfg *config.Config) byte {
	if cfg.EnablePureDownlink {
		return DownlinkModePure
	}
	return DownlinkModePacked
}

// buildObfsConnForClient builds the obfuscation layer for client side, keeping Sudoku on uplink.
func buildObfsConnForClient(raw net.Conn, table *sudoku.Table, cfg *config.Config) net.Conn {
	baseSudoku := sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, false)
	if cfg.EnablePureDownlink {
		return baseSudoku
	}
	packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
	return sudoku.NewDirectionalConn(raw, packed, baseSudoku)
}

// buildObfsConnForServer builds the obfuscation layer for server side, keeping Sudoku on uplink.
// It returns the reader Sudoku connection (for fallback recording) and the composed net.Conn.
func buildObfsConnForServer(raw net.Conn, table *sudoku.Table, cfg *config.Config, record bool) (*sudoku.Conn, net.Conn) {
	uplinkSudoku := sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, record)
	if cfg.EnablePureDownlink {
		return uplinkSudoku, uplinkSudoku
	}
	packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
	return uplinkSudoku, sudoku.NewDirectionalConn(raw, uplinkSudoku, packed, packed.Flush)
}
