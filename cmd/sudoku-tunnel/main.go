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
package main

import (
	"flag"
	"os"

	"github.com/saba-futai/sudoku/internal/app"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/reverse"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/logx"
)

var (
	configPath  = flag.String("c", "server.config.json", "Path to configuration file")
	testConfig  = flag.Bool("test", false, "Test configuration file and exit")
	keygen      = flag.Bool("keygen", false, "Generate a new Ed25519 key pair")
	more        = flag.String("more", "", "Generate more Private key (hex) for split key generations")
	linkInput   = flag.String("link", "", "Start client directly from a sudoku:// short link")
	exportLink  = flag.Bool("export-link", false, "Print sudoku:// short link generated from the config")
	publicHost  = flag.String("public-host", "", "Advertised server host for short link generation (server mode); supports host or host:port")
	setupWizard = flag.Bool("tui", false, "Launch interactive TUI to create config before starting")

	revDial     = flag.String("rev-dial", "", "Dial a reverse TCP-over-WebSocket endpoint (ws:// or wss://) and forward from rev-listen")
	revListen   = flag.String("rev-listen", "", "Local TCP listen address for reverse forwarder (e.g., 127.0.0.1:2222)")
	revInsecure = flag.Bool("rev-insecure", false, "Skip TLS verification for wss reverse dial (testing only)")
)

func main() {
	flag.Parse()
	logx.InstallStd()

	if *revDial != "" || *revListen != "" {
		if *revDial == "" || *revListen == "" {
			logx.Fatalf("CLI", "reverse forwarder requires both -rev-dial and -rev-listen")
		}
		if err := reverse.ServeLocalWSForward(*revListen, *revDial, *revInsecure); err != nil {
			logx.Fatalf("CLI", "%v", err)
		}
		return
	}

	if *keygen {
		if *more != "" {
			x, err := crypto.ParsePrivateScalar(*more)
			if err != nil {
				logx.Fatalf("CLI", "Invalid private key: %v", err)
			}

			// 2. Generate new split key
			splitKey, err := crypto.SplitPrivateKey(x)
			if err != nil {
				logx.Fatalf("CLI", "Failed to split key: %v", err)
			}
			logx.Infof("CLI", "Split Private Key: %s", splitKey)
			return
		}

		// Generate new Master Key
		pair, err := crypto.GenerateMasterKey()
		if err != nil {
			logx.Fatalf("CLI", "Failed to generate key: %v", err)
		}
		splitKey, err := crypto.SplitPrivateKey(pair.Private)
		if err != nil {
			logx.Fatalf("CLI", "Failed to generate key: %v", err)
		}
		logx.Infof("CLI", "Available Private Key: %s", splitKey)
		logx.Infof("CLI", "Master Private Key: %s", crypto.EncodeScalar(pair.Private))
		logx.Infof("CLI", "Master Public Key:  %s", crypto.EncodePoint(pair.Public))
		return
	}

	if *linkInput != "" {
		cfg, err := config.BuildConfigFromShortLink(*linkInput)
		if err != nil {
			logx.Fatalf("CLI", "Failed to parse short link: %v", err)
		}
		tables, err := app.BuildTables(cfg)
		if err != nil {
			logx.Fatalf("CLI", "Failed to build table: %v", err)
		}
		app.RunClient(cfg, tables)
		return
	}

	if *setupWizard {
		result, err := app.RunSetupWizard(*configPath, *publicHost)
		if err != nil {
			logx.Fatalf("CLI", "Setup failed: %v", err)
		}
		logx.Infof("CLI", "Server config saved to %s", result.ServerConfigPath)
		logx.Infof("CLI", "Client config saved to %s", result.ClientConfigPath)
		logx.Infof("CLI", "Short link: %s", result.ShortLink)

		tables, err := app.BuildTables(result.ServerConfig)
		if err != nil {
			logx.Fatalf("CLI", "Failed to build table: %v", err)
		}
		app.RunServer(result.ServerConfig, tables)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		logx.Fatalf("CLI", "Failed to load config from %s: %v", *configPath, err)
	}

	if *testConfig {
		logx.Infof("CLI", "Configuration %s is valid.", *configPath)
		logx.Infof("CLI", "Mode: %s", cfg.Mode)
		if cfg.Mode == "client" {
			logx.Infof("CLI", "Rules: %d URLs configured", len(cfg.RuleURLs))
		}
		os.Exit(0)
	}

	if *exportLink {
		link, err := config.BuildShortLinkFromConfig(cfg, *publicHost)
		if err != nil {
			logx.Fatalf("CLI", "Export short link failed: %v", err)
		}
		logx.Infof("CLI", "Short link: %s", link)
		os.Exit(0)
	}

	tables, err := app.BuildTables(cfg)
	if err != nil {
		logx.Fatalf("CLI", "Failed to build table: %v", err)
	}

	if cfg.Mode == "client" {
		app.RunClient(cfg, tables)
	} else {
		app.RunServer(cfg, tables)
	}
}
