package apis

import (
	"net"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

const (
	downlinkModePure   byte = 0x01
	downlinkModePacked byte = 0x02
)

func downlinkMode(cfg *ProtocolConfig) byte {
	if cfg.EnablePureDownlink {
		return downlinkModePure
	}
	return downlinkModePacked
}

func buildClientObfsConn(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table) net.Conn {
	base := sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, false)
	if cfg.EnablePureDownlink {
		return base
	}
	packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
	return sudoku.NewDirectionalConn(raw, packed, base)
}

func buildServerObfsConn(raw net.Conn, cfg *ProtocolConfig, table *sudoku.Table, record bool) (*sudoku.Conn, net.Conn) {
	uplink := sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, record)
	if cfg.EnablePureDownlink {
		return uplink, uplink
	}
	packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
	return uplink, sudoku.NewDirectionalConn(raw, uplink, packed, packed.Flush)
}
