package reverse

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

// ServeClientSession registers routes to the server and serves reverse mux streams until the session ends.
func ServeClientSession(conn net.Conn, clientID string, routes []config.ReverseRoute) error {
	if conn == nil {
		return fmt.Errorf("nil conn")
	}
	if len(routes) == 0 {
		return fmt.Errorf("no reverse routes")
	}

	clientID = strings.TrimSpace(clientID)

	// Preface.
	if _, err := conn.Write([]byte{tunnel.ReverseMagicByte, tunnel.ReverseVersion}); err != nil {
		return err
	}

	msg := helloMessage{
		ClientID: clientID,
		Routes:   append([]config.ReverseRoute(nil), routes...),
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxHelloBytes {
		return fmt.Errorf("reverse hello too large: %d", len(raw))
	}

	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
	if _, err := conn.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := conn.Write(raw); err != nil {
		return err
	}

	// Server must reply with a mux preface; consume magic then let mux handler read version.
	var magic [1]byte
	if _, err := io.ReadFull(conn, magic[:]); err != nil {
		return err
	}
	if magic[0] != tunnel.MuxMagicByte {
		return fmt.Errorf("unexpected server preface byte: %d", magic[0])
	}

	allowed := make(map[string]struct{}, len(routes))
	for _, r := range routes {
		target := strings.TrimSpace(r.Target)
		if target != "" {
			allowed[target] = struct{}{}
		}
	}

	dialTarget := func(targetAddr string) (net.Conn, error) {
		if _, ok := allowed[targetAddr]; !ok {
			return nil, fmt.Errorf("target not allowed")
		}
		return net.DialTimeout("tcp", targetAddr, 10*time.Second)
	}

	return tunnel.HandleMuxWithDialer(conn, nil, dialTarget)
}
