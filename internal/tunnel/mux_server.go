package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/saba-futai/sudoku/internal/protocol"
)

// HandleMuxServer handles a multiplexed tunnel connection after the MuxMagicByte has been consumed.
//
// Wire format:
//   - [MuxMagicByte][muxVersion] (magic is consumed by caller; this function reads the version)
//   - then mux frames (open/data/close/reset)
func HandleMuxServer(conn net.Conn, onConnect func(targetAddr string)) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}

	var ver [1]byte
	if _, err := io.ReadFull(conn, ver[:]); err != nil {
		return err
	}
	if ver[0] != muxVersion {
		return fmt.Errorf("unsupported mux version: %d", ver[0])
	}

	sess := newMuxSession(conn, func(stream *muxStream, payload []byte) {
		sess := stream.session
		addr, err := decodeMuxOpenTarget(payload)
		if err != nil {
			sess.sendReset(stream.id, "bad address")
			stream.closeNoSend(err)
			sess.removeStream(stream.id)
			return
		}
		if onConnect != nil {
			onConnect(addr)
		}

		target, err := net.DialTimeout("tcp", addr, 10*time.Second)
		if err != nil {
			sess.sendReset(stream.id, err.Error())
			stream.closeNoSend(err)
			sess.removeStream(stream.id)
			return
		}

		pipeConn(stream, target)
	})

	<-sess.closed
	err := sess.closedErr()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func decodeMuxOpenTarget(payload []byte) (string, error) {
	addr, _, _, err := protocol.ReadAddress(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	if addr == "" {
		return "", fmt.Errorf("empty address")
	}
	return addr, nil
}
