// cmd/sudoku-tunnel/main.go
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"filippo.io/edwards25519"
	"github.com/saba-futai/sudoku/internal/app"
	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

var (
	configPath  = flag.String("c", "config.json", "Path to configuration file")
	testConfig  = flag.Bool("test", false, "Test configuration file and exit")
	keygen      = flag.Bool("keygen", false, "Generate a new Ed25519 key pair")
	more        = flag.String("more", "", "Generate more Private key (hex) for split key generations")
	linkInput   = flag.String("link", "", "Start client directly from a sudoku:// short link")
	exportLink  = flag.Bool("export-link", false, "Print sudoku:// short link generated from the config")
	publicHost  = flag.String("public-host", "", "Advertised server host for short link generation (server mode)")
	setupWizard = flag.Bool("tui", false, "Launch interactive TUI to create config before starting")
)

func main() {
	flag.Parse()

	if *keygen {
		if *more != "" {

			// 1. Decode input
			keyBytes, err := hex.DecodeString(*more)
			if err != nil {
				log.Fatalf("Invalid private key hex: %v", err)
			}

			var x *edwards25519.Scalar
			if len(keyBytes) == 32 {
				x, err = edwards25519.NewScalar().SetCanonicalBytes(keyBytes)
				if err != nil {
					log.Fatalf("Invalid scalar: %v", err)
				}
			} else if len(keyBytes) == 64 {
				// Recover x from r, k
				r, err := edwards25519.NewScalar().SetCanonicalBytes(keyBytes[:32])
				if err != nil {
					log.Fatalf("Invalid scalar r: %v", err)
				}
				k, err := edwards25519.NewScalar().SetCanonicalBytes(keyBytes[32:])
				if err != nil {
					log.Fatalf("Invalid scalar k: %v", err)
				}
				x = new(edwards25519.Scalar).Add(r, k)
			} else {
				log.Fatal("Invalid key length. Must be 32 bytes (Master) or 64 bytes (Split)")
			}

			// 2. Generate new split key
			splitKey, err := crypto.SplitPrivateKey(x)
			if err != nil {
				log.Fatalf("Failed to split key: %v", err)
			}
			fmt.Printf("Split Private Key: %s\n", splitKey)
			return
		}

		// Generate new Master Key
		pair, err := crypto.GenerateMasterKey()
		if err != nil {
			log.Fatalf("Failed to generate key: %v", err)
		}
		keyBytes, err := hex.DecodeString(crypto.EncodeScalar(pair.Private))

		x, err := edwards25519.NewScalar().SetCanonicalBytes(keyBytes)
		splitKey, err := crypto.SplitPrivateKey(x)
		if err != nil {
			log.Fatalf("Failed to generate key: %v", err)
		}
		fmt.Printf("Available Private Key: %s\n", splitKey)
		fmt.Printf("Master Private Key: %s\n", crypto.EncodeScalar(pair.Private))
		fmt.Printf("Master Public Key:  %s\n", crypto.EncodePoint(pair.Public))
		return
	}

	if *linkInput != "" {
		cfg, err := config.BuildConfigFromShortLink(*linkInput)
		if err != nil {
			log.Fatalf("Failed to parse short link: %v", err)
		}
		tables, err := buildTables(cfg.Key, cfg.ASCII, cfg.CustomTable, cfg.CustomTables)
		if err != nil {
			log.Fatalf("Failed to build table: %v", err)
		}
		app.RunClient(cfg, tables)
		return
	}

	if *setupWizard {
		result, err := app.RunSetupWizard(*configPath, *publicHost)
		if err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		fmt.Printf("Server config saved to %s\n", result.ServerConfigPath)
		fmt.Printf("Client config saved to %s\n", result.ClientConfigPath)
		fmt.Printf("Short link: %s\n", result.ShortLink)

		tables, err := buildTables(result.ServerConfig.Key, result.ServerConfig.ASCII, result.ServerConfig.CustomTable, result.ServerConfig.CustomTables)
		if err != nil {
			log.Fatalf("Failed to build table: %v", err)
		}
		app.RunServer(result.ServerConfig, tables)
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config from %s: %v", *configPath, err)
	}

	if *testConfig {
		fmt.Printf("Configuration %s is valid.\n", *configPath)
		fmt.Printf("Mode: %s\n", cfg.Mode)
		if cfg.Mode == "client" {
			fmt.Printf("Rules: %d URLs configured\n", len(cfg.RuleURLs))
		}
		os.Exit(0)
	}

	if *exportLink {
		link, err := config.BuildShortLinkFromConfig(cfg, *publicHost)
		if err != nil {
			log.Fatalf("Export short link failed: %v", err)
		}
		fmt.Printf("Short link: %s\n", link)
		os.Exit(0)
	}

	tables, err := buildTables(cfg.Key, cfg.ASCII, cfg.CustomTable, cfg.CustomTables)
	if err != nil {
		log.Fatalf("Failed to build table: %v", err)
	}

	if cfg.Mode == "client" {
		app.RunClient(cfg, tables)
	} else {
		app.RunServer(cfg, tables)
	}
}

func buildTables(key string, ascii string, customTable string, customTables []string) ([]*sudoku.Table, error) {
	patterns := customTables
	if len(patterns) == 0 && strings.TrimSpace(customTable) != "" {
		patterns = []string{customTable}
	}
	if len(patterns) == 0 {
		patterns = []string{""}
	}
	tables := make([]*sudoku.Table, 0, len(patterns))
	for _, p := range patterns {
		t, err := sudoku.NewTableWithCustom(key, ascii, p)
		if err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	return tables, nil
}
