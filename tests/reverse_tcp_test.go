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
package tests

import (
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/SUDOKU-ASCII/sudoku/internal/config"
	"golang.org/x/crypto/ssh"
)

func TestReverseProxy_TCP_DefaultRoute(t *testing.T) {
	originLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen origin: %v", err)
	}
	defer originLn.Close()

	originAddr := originLn.Addr().String()
	go func() {
		for {
			c, err := originLn.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes: []config.ReverseRoute{
			// Path empty => raw TCP reverse on reverse.listen.
			{Target: originAddr},
		},
	}
	startSudokuClient(t, clientCfg)
	waitForReverseTCPRouteReady(t, reverseListen, func(conn net.Conn) error {
		if _, err := conn.Write([]byte("ping")); err != nil {
			return err
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return err
		}
		if string(buf) != "ping" {
			return fmt.Errorf("unexpected echo: %q", string(buf))
		}
		return nil
	})
}

func TestReverseProxy_TCP_DefaultRoute_SSH(t *testing.T) {
	sshSrv := startTestSSHServer(t, "127.0.0.1:0", "u", "p")
	defer sshSrv.Close()

	serverKey, clientKey := newTestKeys(t)

	ports, err := getFreePorts(3)
	if err != nil {
		t.Fatalf("ports: %v", err)
	}
	serverPort := ports[0]
	clientPort := ports[1]
	reversePort := ports[2]

	reverseListen := localServerAddr(reversePort)

	serverCfg := newTestServerConfig(serverPort, serverKey)
	serverCfg.Reverse = &config.ReverseConfig{Listen: reverseListen}
	startSudokuServer(t, serverCfg)
	waitForAddr(t, reverseListen)

	clientCfg := newTestClientConfig(clientPort, localServerAddr(serverPort), clientKey)
	clientCfg.Reverse = &config.ReverseConfig{
		ClientID: "r4s",
		Routes: []config.ReverseRoute{
			// Path empty => raw TCP reverse on reverse.listen.
			{Target: sshSrv.Addr()},
		},
	}
	startSudokuClient(t, clientCfg)

	sshCfg := &ssh.ClientConfig{
		User:            "u",
		Auth:            []ssh.AuthMethod{ssh.Password("p")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	waitForReverseTCPRouteReady(t, reverseListen, func(conn net.Conn) error {
		cconn, chans, reqs, err := ssh.NewClientConn(conn, reverseListen, sshCfg)
		if err != nil {
			return err
		}

		client := ssh.NewClient(cconn, chans, reqs)
		defer client.Close()

		sess, err := client.NewSession()
		if err != nil {
			return err
		}
		defer sess.Close()

		out, err := sess.CombinedOutput("echo hello")
		if err != nil {
			return fmt.Errorf("ssh exec: %w (out=%q)", err, string(out))
		}
		if string(out) != "echo hello" {
			return fmt.Errorf("unexpected output: %q", string(out))
		}
		return nil
	})
}
