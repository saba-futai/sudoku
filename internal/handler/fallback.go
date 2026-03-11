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
package handler

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/logx"
)

func writeFullConn(conn net.Conn, data []byte) error {
	for len(data) > 0 {
		n, err := conn.Write(data)
		if n > 0 {
			data = data[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func HandleSuspicious(wrapper net.Conn, rawConn net.Conn, cfg *config.Config) {
	remoteAddr := rawConn.RemoteAddr().String()

	if cfg.SuspiciousAction == "silent" {
		logx.Warnf("Security", "Suspicious %s. Tarpit.", remoteAddr)
		io.Copy(io.Discard, rawConn)
		time.Sleep(5 * time.Second)
		rawConn.Close()
		return
	}

	if cfg.FallbackAddr == "" {
		rawConn.Close()
		return
	}

	logx.Warnf("Fallback", "%s -> %s", remoteAddr, cfg.FallbackAddr)
	dst, err := net.DialTimeout("tcp", cfg.FallbackAddr, 3*time.Second)
	if err != nil {
		rawConn.Close()
		return
	}

	var badData []byte
	if recorder, ok := wrapper.(interface{ GetBufferedAndRecorded() []byte }); ok {
		badData = recorder.GetBufferedAndRecorded()
	}

	if len(badData) > 0 {
		_ = dst.SetWriteDeadline(time.Now().Add(3 * time.Second))
		if err := writeFullConn(dst, badData); err != nil {
			dst.Close()
			rawConn.Close()
			return
		}
		_ = dst.SetWriteDeadline(time.Time{})
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer dst.Close()
		// 将剩余的 rawConn 数据转发给 dst
		io.Copy(dst, rawConn)
	}()
	go func() {
		defer wg.Done()
		defer rawConn.Close()
		io.Copy(rawConn, dst)
	}()
	wg.Wait()
}
