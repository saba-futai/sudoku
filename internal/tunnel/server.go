package tunnel

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

const (
	HandshakeTimeout = 5 * time.Second
)

var (
	// bufferPool for general IO operations
	bufferPool = sync.Pool{
		New: func() interface{} {
			return make([]byte, 32*1024)
		},
	}
)

// BufferedConn wraps net.Conn and bufio.Reader
type BufferedConn struct {
	net.Conn
	r          *bufio.Reader
	recorder   *bytes.Buffer
	recordLock sync.Mutex
}

func (bc *BufferedConn) Read(p []byte) (n int, err error) {
	n, err = bc.r.Read(p)
	if n > 0 && bc.recorder != nil {
		bc.recordLock.Lock()
		bc.recorder.Write(p[:n])
		bc.recordLock.Unlock()
	}
	return n, err
}

// PreBufferedConn for Split detection peek
type PreBufferedConn struct {
	net.Conn
	buf []byte
}

// NewPreBufferedConn replays the provided bytes before reading from the underlying connection.
func NewPreBufferedConn(conn net.Conn, preRead []byte) net.Conn {
	return &PreBufferedConn{Conn: conn, buf: preRead}
}

func (p *PreBufferedConn) Read(b []byte) (int, error) {
	if len(p.buf) > 0 {
		n := copy(b, p.buf)
		p.buf = p.buf[n:]
		return n, nil
	}
	if p.Conn == nil {
		return 0, io.EOF
	}
	return p.Conn.Read(b)
}

// GetBufferedAndRecorded returns all data that has been consumed and buffered
func (bc *BufferedConn) GetBufferedAndRecorded() []byte {
	if bc == nil {
		return nil
	}

	bc.recordLock.Lock()
	defer bc.recordLock.Unlock()

	var recorded []byte
	if bc.recorder != nil {
		recorded = bc.recorder.Bytes()
	}

	// Also get any buffered data that hasn't been read yet
	buffered := bc.r.Buffered()
	if buffered > 0 {
		peeked, _ := bc.r.Peek(buffered)
		full := make([]byte, len(recorded)+len(peeked))
		copy(full, recorded)
		copy(full[len(recorded):], peeked)
		return full
	}
	return recorded
}

// SuspiciousError indicates a potential attack or protocol violation
type SuspiciousError struct {
	Err  error
	Conn net.Conn // The connection at the state where error occurred (for fallback/logging)
}

func (e *SuspiciousError) Error() string {
	return e.Err.Error()
}

// HandshakeAndUpgrade wraps the raw connection with Sudoku/Crypto and performs handshake.
func HandshakeAndUpgrade(rawConn net.Conn, cfg *config.Config, table *sudoku.Table) (net.Conn, error) {
	return HandshakeAndUpgradeWithTables(rawConn, cfg, []*sudoku.Table{table})
}

type recordedConn struct {
	net.Conn
	recorded []byte
}

func (rc *recordedConn) GetBufferedAndRecorded() []byte {
	return rc.recorded
}

type prefixedRecorderConn struct {
	net.Conn
	prefix []byte
}

func (pc *prefixedRecorderConn) GetBufferedAndRecorded() []byte {
	var rest []byte
	if r, ok := pc.Conn.(interface{ GetBufferedAndRecorded() []byte }); ok {
		rest = r.GetBufferedAndRecorded()
	}
	out := make([]byte, 0, len(pc.prefix)+len(rest))
	out = append(out, pc.prefix...)
	out = append(out, rest...)
	return out
}

type readOnlyConn struct {
	*bytes.Reader
}

func (c *readOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (c *readOnlyConn) Close() error                     { return nil }
func (c *readOnlyConn) LocalAddr() net.Addr              { return nil }
func (c *readOnlyConn) RemoteAddr() net.Addr             { return nil }
func (c *readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (c *readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (c *readOnlyConn) SetWriteDeadline(time.Time) error { return nil }

func probeHandshakeBytes(probe []byte, cfg *config.Config, table *sudoku.Table) error {
	rc := &readOnlyConn{Reader: bytes.NewReader(probe)}
	_, obfsConn := buildObfsConnForServer(rc, table, cfg, false)
	cConn, err := crypto.NewAEADConn(obfsConn, cfg.Key, cfg.AEAD)
	if err != nil {
		return err
	}

	handshakeBuf := make([]byte, 16)
	if _, err := io.ReadFull(cConn, handshakeBuf); err != nil {
		return err
	}
	ts := int64(binary.BigEndian.Uint64(handshakeBuf[:8]))
	if abs(time.Now().Unix()-ts) > 60 {
		return fmt.Errorf("time skew/replay")
	}

	modeBuf := make([]byte, 1)
	if _, err := io.ReadFull(cConn, modeBuf); err != nil {
		return err
	}
	if modeBuf[0] != downlinkModeByte(cfg) {
		return fmt.Errorf("downlink mode mismatch: client=%d server=%d", modeBuf[0], downlinkModeByte(cfg))
	}
	return nil
}

func drainBuffered(r *bufio.Reader) ([]byte, error) {
	n := r.Buffered()
	if n <= 0 {
		return nil, nil
	}
	out := make([]byte, n)
	_, err := io.ReadFull(r, out)
	return out, err
}

func selectTableByProbe(r *bufio.Reader, cfg *config.Config, tables []*sudoku.Table) (*sudoku.Table, []byte, error) {
	const (
		maxProbeBytes = 64 * 1024
		readChunk     = 4 * 1024
	)
	if len(tables) == 0 {
		return nil, nil, fmt.Errorf("no table candidates")
	}
	if len(tables) > 255 {
		return nil, nil, fmt.Errorf("too many table candidates: %d", len(tables))
	}

	probe, err := drainBuffered(r)
	if err != nil {
		return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
	}

	tmp := make([]byte, readChunk)
	for {
		if len(tables) == 1 {
			tail, err := drainBuffered(r)
			if err != nil {
				return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
			}
			probe = append(probe, tail...)
			return tables[0], probe, nil
		}

		needMore := false
		for _, table := range tables {
			err := probeHandshakeBytes(probe, cfg, table)
			if err == nil {
				tail, err := drainBuffered(r)
				if err != nil {
					return nil, nil, fmt.Errorf("drain buffered bytes failed: %w", err)
				}
				probe = append(probe, tail...)
				return table, probe, nil
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				needMore = true
			}
		}

		if !needMore {
			return nil, probe, fmt.Errorf("handshake table selection failed")
		}
		if len(probe) >= maxProbeBytes {
			return nil, probe, fmt.Errorf("handshake probe exceeded %d bytes", maxProbeBytes)
		}

		n, err := r.Read(tmp)
		if n > 0 {
			probe = append(probe, tmp[:n]...)
		}
		if err != nil {
			return nil, probe, fmt.Errorf("handshake probe read failed: %w", err)
		}
	}
}

// HandshakeAndUpgradeWithTables performs the handshake by probing one of multiple tables.
// This enables per-connection table rotation without adding a plaintext table selector.
func HandshakeAndUpgradeWithTables(rawConn net.Conn, cfg *config.Config, tables []*sudoku.Table) (net.Conn, error) {
	// 0. HTTP Header Check
	bufReader := bufio.NewReader(rawConn)
	rawConn.SetReadDeadline(time.Now().Add(HandshakeTimeout))

	shouldConsumeMask := false
	var httpHeaderData []byte

	if !cfg.DisableHTTPMask {
		peekBytes, _ := bufReader.Peek(4) // Ignore error; if peek fails, let subsequent read handle it.
		if httpmask.LooksLikeHTTPRequestStart(peekBytes) {
			shouldConsumeMask = true
		}
	}

	if shouldConsumeMask {
		consumed, err := httpmask.ConsumeHeader(bufReader)
		httpHeaderData = consumed
		if err != nil {
			rawConn.SetReadDeadline(time.Time{})
			// Return rawConn wrapped in BufferedConn so caller can handle fallback.
			recorder := new(bytes.Buffer)
			if len(consumed) > 0 {
				recorder.Write(consumed)
			}
			badConn := &BufferedConn{
				Conn:     rawConn,
				r:        bufReader,
				recorder: recorder,
			}
			return nil, &SuspiciousError{Err: fmt.Errorf("invalid http header: %w", err), Conn: badConn}
		}
	}

	// 1. Sudoku Layer
	if !cfg.EnablePureDownlink && cfg.AEAD == "none" {
		rawConn.SetReadDeadline(time.Time{})
		return nil, fmt.Errorf("enable_pure_downlink=false requires AEAD")
	}

	selectedTable, preRead, err := selectTableByProbe(bufReader, cfg, tables)
	rawConn.SetReadDeadline(time.Time{})
	if err != nil {
		combined := make([]byte, 0, len(httpHeaderData)+len(preRead))
		combined = append(combined, httpHeaderData...)
		combined = append(combined, preRead...)
		return nil, &SuspiciousError{Err: err, Conn: &recordedConn{Conn: rawConn, recorded: combined}}
	}

	baseConn := NewPreBufferedConn(rawConn, preRead)
	sConn, obfsConn := buildObfsConnForServer(baseConn, selectedTable, cfg, true)

	// 2. Crypto Layer
	cConn, err := crypto.NewAEADConn(obfsConn, cfg.Key, cfg.AEAD)
	if err != nil {
		return nil, fmt.Errorf("crypto setup failed: %w", err)
	}

	// 3. Handshake
	handshakeBuf := make([]byte, 16)
	rawConn.SetReadDeadline(time.Now().Add(HandshakeTimeout))
	_, err = io.ReadFull(cConn, handshakeBuf)
	if err != nil {
		rawConn.SetReadDeadline(time.Time{})
		return nil, &SuspiciousError{Err: fmt.Errorf("handshake read failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	ts := int64(binary.BigEndian.Uint64(handshakeBuf[:8]))
	if abs(time.Now().Unix()-ts) > 60 {
		rawConn.SetReadDeadline(time.Time{})
		return nil, &SuspiciousError{Err: fmt.Errorf("time skew/replay"), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	// 4. Downlink mode negotiation
	modeBuf := make([]byte, 1)
	if _, err := io.ReadFull(cConn, modeBuf); err != nil {
		rawConn.SetReadDeadline(time.Time{})
		return nil, &SuspiciousError{Err: fmt.Errorf("read downlink mode failed: %w", err), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}
	rawConn.SetReadDeadline(time.Time{})
	if modeBuf[0] != downlinkModeByte(cfg) {
		return nil, &SuspiciousError{Err: fmt.Errorf("downlink mode mismatch: client=%d server=%d", modeBuf[0], downlinkModeByte(cfg)), Conn: &prefixedRecorderConn{Conn: sConn, prefix: httpHeaderData}}
	}

	sConn.StopRecording()
	return cConn, nil
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
