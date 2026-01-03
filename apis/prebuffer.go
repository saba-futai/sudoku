package apis

import "net"

// NewPreBufferedConn returns a net.Conn that replays preRead before reading from conn.
//
// This is useful when you need to peek one byte to decide between UoT / multiplex / legacy payloads
// and still keep the stream consumable by the next parser.
func NewPreBufferedConn(conn net.Conn, preRead []byte) net.Conn {
	if conn == nil || len(preRead) == 0 {
		return conn
	}
	buf := make([]byte, len(preRead))
	copy(buf, preRead)
	return &preBufferedConn{Conn: conn, buf: buf}
}

