package tunnel

import (
	"io"
	"net"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

const (
	DownlinkModePure   byte = 0x01
	DownlinkModePacked byte = 0x02
)

type directionalConn struct {
	net.Conn
	reader  io.Reader
	writer  io.Writer
	closers []func() error
}

func (c *directionalConn) CloseWrite() error {
	var firstErr error
	for _, fn := range c.closers {
		if fn == nil {
			continue
		}
		if err := fn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	if cw, ok := c.writer.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		if err := cw.CloseWrite(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *directionalConn) CloseRead() error {
	if cr, ok := c.reader.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	if cr, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return cr.CloseRead()
	}
	return nil
}

func newDirectionalConn(base net.Conn, reader io.Reader, writer io.Writer, closers ...func() error) net.Conn {
	return &directionalConn{
		Conn:    base,
		reader:  reader,
		writer:  writer,
		closers: closers,
	}
}

func (c *directionalConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *directionalConn) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

func (c *directionalConn) Close() error {
	var firstErr error
	for _, fn := range c.closers {
		if fn == nil {
			continue
		}
		if err := fn(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := c.Conn.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

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
	return newDirectionalConn(raw, packed, baseSudoku)
}

// buildObfsConnForServer builds the obfuscation layer for server side, keeping Sudoku on uplink.
// It returns the reader Sudoku connection (for fallback recording) and the composed net.Conn.
func buildObfsConnForServer(raw net.Conn, table *sudoku.Table, cfg *config.Config, record bool) (*sudoku.Conn, net.Conn) {
	uplinkSudoku := sudoku.NewConn(raw, table, cfg.PaddingMin, cfg.PaddingMax, record)
	if cfg.EnablePureDownlink {
		return uplinkSudoku, uplinkSudoku
	}
	packed := sudoku.NewPackedConn(raw, table, cfg.PaddingMin, cfg.PaddingMax)
	return uplinkSudoku, newDirectionalConn(raw, uplinkSudoku, packed, packed.Flush)
}
