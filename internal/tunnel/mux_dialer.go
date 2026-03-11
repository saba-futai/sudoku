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
	"bytes"
	"fmt"
	"net"
	"sync"

	"github.com/saba-futai/sudoku/internal/protocol"
)

// MuxDialer multiplexes multiple target connections over a single upgraded Sudoku tunnel.
//
// It is intended to reduce per-connection RTT when using HTTPMask tunnel modes by keeping one long-lived
// tunnel and opening lightweight sub-streams for each destination.
type MuxDialer struct {
	BaseDialer

	mu       sync.Mutex
	cond     *sync.Cond
	creating bool
	session  *muxSession
}

func (d *MuxDialer) Dial(destAddrStr string) (net.Conn, error) {
	sess, err := d.getOrCreateSession()
	if err != nil {
		return nil, err
	}

	var addrBuf bytes.Buffer
	if err := protocol.WriteAddress(&addrBuf, destAddrStr); err != nil {
		return nil, fmt.Errorf("encode address failed: %w", err)
	}

	streamID := sess.nextStreamID()
	st := newMuxStream(sess, streamID)
	sess.registerStream(st)

	if err := sess.sendFrame(muxFrameOpen, streamID, addrBuf.Bytes()); err != nil {
		st.closeNoSend(err)
		sess.removeStream(streamID)
		return nil, fmt.Errorf("mux open failed: %w", err)
	}
	return st, nil
}

func (d *MuxDialer) DialUDPOverTCP() (net.Conn, error) {
	// UoT uses a dedicated tunnel because it already multiplexes at the packet layer.
	return d.dialUoT()
}

func (d *MuxDialer) getOrCreateSession() (*muxSession, error) {
	d.mu.Lock()
	if d.cond == nil {
		d.cond = sync.NewCond(&d.mu)
	}
	for {
		if sess := d.session; sess != nil && !sess.isClosed() {
			d.mu.Unlock()
			return sess, nil
		}
		if !d.creating {
			d.creating = true
			break
		}
		d.cond.Wait()
	}
	d.mu.Unlock()

	if d.Config == nil {
		d.mu.Lock()
		d.creating = false
		d.cond.Broadcast()
		d.mu.Unlock()
		return nil, fmt.Errorf("missing config")
	}
	if !d.Config.HTTPMaskTunnelEnabled() {
		d.mu.Lock()
		d.creating = false
		d.cond.Broadcast()
		d.mu.Unlock()
		if d.Config.HTTPMask.Disable {
			return nil, fmt.Errorf("mux requires httpmask.disable=false")
		}
		return nil, fmt.Errorf("mux requires httpmask.mode=stream/poll/auto/ws (got %q)", d.Config.HTTPMask.Mode)
	}
	if d.Config.HTTPMask.Multiplex != "on" {
		d.mu.Lock()
		d.creating = false
		d.cond.Broadcast()
		d.mu.Unlock()
		return nil, fmt.Errorf("mux requires httpmask.multiplex=on (got %q)", d.Config.HTTPMask.Multiplex)
	}

	baseConn, err := d.dialBase()
	if err != nil {
		d.mu.Lock()
		d.creating = false
		d.cond.Broadcast()
		d.mu.Unlock()
		return nil, err
	}

	if err := WriteKIPMessage(baseConn, KIPTypeStartMux, nil); err != nil {
		_ = baseConn.Close()
		d.mu.Lock()
		d.creating = false
		d.cond.Broadcast()
		d.mu.Unlock()
		return nil, fmt.Errorf("mux start failed: %w", err)
	}

	d.mu.Lock()
	sess := newMuxSession(baseConn, nil)
	d.session = sess
	d.creating = false
	d.cond.Broadcast()
	d.mu.Unlock()
	return sess, nil
}
