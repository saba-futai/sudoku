package tunnel

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	muxFrameOpen  byte = 0x01
	muxFrameData  byte = 0x02
	muxFrameClose byte = 0x03
	muxFrameReset byte = 0x04
)

const (
	muxHeaderSize     = 1 + 4 + 4
	muxMaxFrameSize   = 256 * 1024
	muxMaxDataPayload = 32 * 1024
)

type muxSession struct {
	conn net.Conn

	writeMu sync.Mutex

	streamsMu sync.Mutex
	streams   map[uint32]*muxStream
	nextID    uint32

	closed    chan struct{}
	closeOnce sync.Once
	closeErr  error

	onOpen func(stream *muxStream, payload []byte)
}

func newMuxSession(conn net.Conn, onOpen func(stream *muxStream, payload []byte)) *muxSession {
	s := &muxSession{
		conn:    conn,
		streams: make(map[uint32]*muxStream),
		closed:  make(chan struct{}),
		onOpen:  onOpen,
	}
	go s.readLoop()
	return s
}

func (s *muxSession) isClosed() bool {
	select {
	case <-s.closed:
		return true
	default:
		return false
	}
}

func (s *muxSession) closedErr() error {
	s.streamsMu.Lock()
	err := s.closeErr
	s.streamsMu.Unlock()
	if err == nil {
		return io.ErrClosedPipe
	}
	return err
}

func (s *muxSession) closeWithError(err error) {
	if err == nil {
		err = io.ErrClosedPipe
	}
	s.closeOnce.Do(func() {
		s.streamsMu.Lock()
		if s.closeErr == nil {
			s.closeErr = err
		}
		streams := make([]*muxStream, 0, len(s.streams))
		for _, st := range s.streams {
			streams = append(streams, st)
		}
		s.streams = make(map[uint32]*muxStream)
		s.streamsMu.Unlock()

		for _, st := range streams {
			st.closeNoSend(err)
		}

		close(s.closed)
		_ = s.conn.Close()
	})
}

func (s *muxSession) registerStream(st *muxStream) {
	s.streamsMu.Lock()
	s.streams[st.id] = st
	s.streamsMu.Unlock()
}

func (s *muxSession) getStream(id uint32) *muxStream {
	s.streamsMu.Lock()
	st := s.streams[id]
	s.streamsMu.Unlock()
	return st
}

func (s *muxSession) removeStream(id uint32) {
	s.streamsMu.Lock()
	delete(s.streams, id)
	s.streamsMu.Unlock()
}

func (s *muxSession) nextStreamID() uint32 {
	s.streamsMu.Lock()
	s.nextID++
	id := s.nextID
	if id == 0 {
		s.nextID++
		id = s.nextID
	}
	s.streamsMu.Unlock()
	return id
}

func (s *muxSession) sendFrame(frameType byte, streamID uint32, payload []byte) error {
	if s.isClosed() {
		return s.closedErr()
	}
	if len(payload) > muxMaxFrameSize {
		return fmt.Errorf("mux payload too large: %d", len(payload))
	}

	var header [muxHeaderSize]byte
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], streamID)
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := writeFull(s.conn, header[:]); err != nil {
		s.closeWithError(err)
		return err
	}
	if len(payload) > 0 {
		if err := writeFull(s.conn, payload); err != nil {
			s.closeWithError(err)
			return err
		}
	}
	return nil
}

func (s *muxSession) sendReset(streamID uint32, msg string) {
	// Best-effort: ignore errors (session is probably already failing).
	if msg == "" {
		msg = "reset"
	}
	_ = s.sendFrame(muxFrameReset, streamID, []byte(msg))
	_ = s.sendFrame(muxFrameClose, streamID, nil)
}

func (s *muxSession) readLoop() {
	var header [muxHeaderSize]byte
	for {
		if _, err := io.ReadFull(s.conn, header[:]); err != nil {
			s.closeWithError(err)
			return
		}
		frameType := header[0]
		streamID := binary.BigEndian.Uint32(header[1:5])
		n := int(binary.BigEndian.Uint32(header[5:9]))
		if n < 0 || n > muxMaxFrameSize {
			s.closeWithError(fmt.Errorf("invalid mux frame length: %d", n))
			return
		}

		var payload []byte
		if n > 0 {
			payload = make([]byte, n)
			if _, err := io.ReadFull(s.conn, payload); err != nil {
				s.closeWithError(err)
				return
			}
		}

		switch frameType {
		case muxFrameOpen:
			if s.onOpen == nil {
				s.sendReset(streamID, "unexpected open")
				continue
			}
			if streamID == 0 {
				s.sendReset(streamID, "invalid stream id")
				continue
			}
			if existing := s.getStream(streamID); existing != nil {
				s.sendReset(streamID, "stream already exists")
				continue
			}
			st := newMuxStream(s, streamID)
			s.registerStream(st)
			// Avoid blocking the demux loop on dial/IO.
			go s.onOpen(st, payload)

		case muxFrameData:
			st := s.getStream(streamID)
			if st == nil {
				// Unknown stream; ignore to avoid killing the whole session.
				continue
			}
			if len(payload) == 0 {
				continue
			}
			st.enqueue(payload)

		case muxFrameClose:
			st := s.getStream(streamID)
			if st == nil {
				continue
			}
			st.closeNoSend(io.EOF)
			s.removeStream(streamID)

		case muxFrameReset:
			st := s.getStream(streamID)
			if st == nil {
				continue
			}
			msg := stringsTrimASCII(payload)
			if msg == "" {
				msg = "reset"
			}
			st.closeNoSend(errors.New(msg))
			s.removeStream(streamID)

		default:
			s.closeWithError(fmt.Errorf("unknown mux frame type: %d", frameType))
			return
		}
	}
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func stringsTrimASCII(b []byte) string {
	// Avoid importing strings in this low-level file; keep a tiny ASCII trim.
	i := 0
	j := len(b)
	for i < j {
		c := b[i]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			break
		}
		i++
	}
	for j > i {
		c := b[j-1]
		if c != ' ' && c != '\n' && c != '\r' && c != '\t' {
			break
		}
		j--
	}
	if i >= j {
		return ""
	}
	out := make([]byte, j-i)
	copy(out, b[i:j])
	return string(out)
}

type muxStream struct {
	session *muxSession
	id      uint32

	mu       sync.Mutex
	cond     *sync.Cond
	closed   bool
	closeErr error
	readBuf  []byte
	queue    [][]byte

	localAddr  net.Addr
	remoteAddr net.Addr
}

func newMuxStream(session *muxSession, id uint32) *muxStream {
	st := &muxStream{
		session:    session,
		id:         id,
		localAddr:  &net.TCPAddr{},
		remoteAddr: &net.TCPAddr{},
	}
	st.cond = sync.NewCond(&st.mu)
	return st
}

func (c *muxStream) closeNoSend(err error) {
	if err == nil {
		err = io.EOF
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if c.closeErr == nil {
		c.closeErr = err
	}
	c.cond.Broadcast()
	c.mu.Unlock()
}

func (c *muxStream) closedErr() error {
	c.mu.Lock()
	err := c.closedErrLocked()
	c.mu.Unlock()
	return err
}

func (c *muxStream) closedErrLocked() error {
	if c.closeErr == nil {
		return io.ErrClosedPipe
	}
	return c.closeErr
}

func (c *muxStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for len(c.readBuf) == 0 && len(c.queue) == 0 && !c.closed {
		c.cond.Wait()
	}
	if len(c.readBuf) == 0 && len(c.queue) > 0 {
		c.readBuf = c.queue[0]
		c.queue = c.queue[1:]
	}
	if len(c.readBuf) == 0 && c.closed {
		return 0, c.closedErrLocked()
	}

	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *muxStream) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.session.isClosed() {
		return 0, c.session.closedErr()
	}
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, c.closedErr()
	}

	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > muxMaxDataPayload {
			chunk = p[:muxMaxDataPayload]
		}
		if err := c.session.sendFrame(muxFrameData, c.id, chunk); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

func (c *muxStream) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.closeErr == nil {
		c.closeErr = io.ErrClosedPipe
	}
	c.cond.Broadcast()
	c.mu.Unlock()

	_ = c.session.sendFrame(muxFrameClose, c.id, nil)
	c.session.removeStream(c.id)
	return nil
}

func (c *muxStream) LocalAddr() net.Addr  { return c.localAddr }
func (c *muxStream) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *muxStream) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	_ = c.SetWriteDeadline(t)
	return nil
}
func (c *muxStream) SetReadDeadline(time.Time) error  { return nil }
func (c *muxStream) SetWriteDeadline(time.Time) error { return nil }

func (c *muxStream) enqueue(payload []byte) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if len(c.readBuf) == 0 && len(c.queue) == 0 {
		c.readBuf = payload
	} else {
		c.queue = append(c.queue, payload)
	}
	c.cond.Signal()
	c.mu.Unlock()
}
