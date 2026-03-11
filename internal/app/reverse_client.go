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
package app

import (
	"time"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"github.com/SUDOKU-ASCII/sudoku/internal/reverse"
	"github.com/SUDOKU-ASCII/sudoku/internal/tunnel"
	"github.com/SUDOKU-ASCII/sudoku/pkg/logx"
)

func startReverseClient(cfg *config.Config, baseDialer *tunnel.BaseDialer) {
	if cfg == nil || cfg.Reverse == nil || len(cfg.Reverse.Routes) == 0 {
		return
	}
	if baseDialer == nil {
		logx.Warnf("Reverse", "disabled: missing dialer")
		return
	}

	clientID := ""
	if cfg.Reverse != nil {
		clientID = cfg.Reverse.ClientID
	}
	routes := append([]config.ReverseRoute(nil), cfg.Reverse.Routes...)

	go func() {
		backoff := 250 * time.Millisecond
		maxBackoff := 10 * time.Second

		for {
			conn, err := baseDialer.DialBase()
			if err != nil {
				logx.Warnf("Reverse", "dial base failed: %v", err)
				time.Sleep(backoff)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}

			backoff = 250 * time.Millisecond
			err = reverse.ServeClientSession(conn, clientID, routes)
			_ = conn.Close()
			if err != nil {
				logx.Warnf("Reverse", "session ended: %v", err)
			} else {
				logx.Infof("Reverse", "session ended")
			}
			time.Sleep(backoff)
		}
	}()
}
