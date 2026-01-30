package app

import (
	"log"
	"time"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/internal/reverse"
	"github.com/saba-futai/sudoku/internal/tunnel"
)

func startReverseClient(cfg *config.Config, baseDialer *tunnel.BaseDialer) {
	if cfg == nil || cfg.Reverse == nil || len(cfg.Reverse.Routes) == 0 {
		return
	}
	if baseDialer == nil {
		log.Printf("[Reverse] disabled: missing dialer")
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
				log.Printf("[Reverse] dial base failed: %v", err)
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
				log.Printf("[Reverse] session ended: %v", err)
			} else {
				log.Printf("[Reverse] session ended")
			}
			time.Sleep(backoff)
		}
	}()
}
