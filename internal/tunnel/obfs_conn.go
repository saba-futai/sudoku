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
