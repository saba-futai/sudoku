package tunnel

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/connutil"
)

// HandleMuxServer handles a multiplexed tunnel connection after the control plane has selected mux mode.
func HandleMuxServer(conn net.Conn, onConnect func(targetAddr string)) error {
	dial := func(addr string) (net.Conn, error) {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	return HandleMuxWithDialer(conn, onConnect, dial)
}

// HandleMuxWithDialer is like HandleMuxServer but allows the caller to control how targets are dialed.
//
// The dialTarget callback must establish a TCP connection to targetAddr (host:port). It may enforce
// allow-lists (useful for reverse proxy clients).
func HandleMuxWithDialer(conn net.Conn, onConnect func(targetAddr string), dialTarget func(targetAddr string) (net.Conn, error)) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if dialTarget == nil {
		return fmt.Errorf("nil dialTarget")
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

		target, err := dialTarget(addr)
		if err != nil {
			sess.sendReset(stream.id, err.Error())
			stream.closeNoSend(err)
			sess.removeStream(stream.id)
			return
		}

		connutil.PipeConn(stream, target)
	})

	<-sess.closed
	err := sess.closedErr()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) {
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
