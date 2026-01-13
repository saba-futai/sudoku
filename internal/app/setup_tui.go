package app

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

func runSetupWizardTUI(defaultServerPath, publicHost string) (wizardInput, bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return wizardInput{}, false, nil
	}

	host := strings.TrimSpace(publicHost)
	if host == "" {
		host = "127.0.0.1"
	}

	serverPort := "8080"
	mixPort := "1080"
	fallback := "127.0.0.1:80"

	aead := "chacha20-poly1305"
	encoding := "entropy"
	suspiciousAction := "fallback"
	paddingMin := "5"
	paddingMax := "15"
	customTable := ""
	enablePureDownlink := true

	disableHTTPMask := false
	httpMaskMode := "legacy"
	httpMaskTLS := false
	httpMaskHost := ""
	httpMaskPathRoot := ""
	httpMaskMultiplex := "off"

	key := ""

	serverPath := strings.TrimSpace(defaultServerPath)
	if serverPath == "" {
		serverPath = "config.json"
	}
	clientPath := "client.config.json"

	validatePort := func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("invalid number")
		}
		if n <= 0 || n > 65535 {
			return fmt.Errorf("must be 1..65535")
		}
		return nil
	}

	validatePercent := func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("invalid number")
		}
		if n < 0 || n > 100 {
			return fmt.Errorf("must be 0..100")
		}
		return nil
	}

	validateHost := func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("cannot be empty")
		}
		return nil
	}

	validateFallback := func(v string) error {
		v = strings.TrimSpace(v)
		if v == "" {
			return fmt.Errorf("cannot be empty")
		}
		if _, _, err := net.SplitHostPort(v); err != nil {
			return fmt.Errorf("must be host:port")
		}
		return nil
	}

	validateHTTPMaskMode := func(v string) error {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "legacy", "auto", "stream", "poll":
			return nil
		default:
			return fmt.Errorf("must be legacy/auto/stream/poll")
		}
	}

	validateHTTPMaskMultiplex := func(v string) error {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "off", "auto", "on":
			return nil
		default:
			return fmt.Errorf("must be off/auto/on")
		}
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Sudoku Setup Wizard").
				Description("Generate server & client configs, print a short link, then start the server.\n\nPress ↑/↓ to move, Enter to confirm, Esc to cancel."),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Server public host/IP").
				Description("Used in client config + short link").
				Value(&host).
				Validate(validateHost),
			huh.NewInput().
				Title("Server port").
				Value(&serverPort).
				Validate(validatePort),
			huh.NewInput().
				Title("Client mixed proxy port").
				Value(&mixPort).
				Validate(validatePort),
			huh.NewInput().
				Title("Fallback address").
				Description("Server-side fallback for suspicious traffic").
				Value(&fallback).
				Validate(validateFallback),
			huh.NewSelect[string]().
				Title("Suspicious action").
				Options(
					huh.NewOption("fallback", "fallback"),
					huh.NewOption("silent", "silent"),
				).
				Value(&suspiciousAction),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("AEAD").
				Options(
					huh.NewOption("chacha20-poly1305 (recommended)", "chacha20-poly1305"),
					huh.NewOption("aes-128-gcm", "aes-128-gcm"),
					huh.NewOption("none (testing only)", "none"),
				).
				Value(&aead),
			huh.NewSelect[string]().
				Title("Encoding").
				Options(
					huh.NewOption("entropy (recommended)", "entropy"),
					huh.NewOption("ascii", "ascii"),
				).
				Value(&encoding),
			huh.NewInput().
				Title("Padding min (%)").
				Value(&paddingMin).
				Validate(validatePercent),
			huh.NewInput().
				Title("Padding max (%)").
				Value(&paddingMax).
				Validate(validatePercent),
			huh.NewInput().
				Title("Custom table layout (optional)").
				Description("Example: xpxvvpvv").
				Value(&customTable),
			huh.NewConfirm().
				Title("Enable pure Sudoku downlink").
				Description("Disable to use bandwidth-optimized downlink (requires AEAD)").
				Value(&enablePureDownlink),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Disable HTTP mask").
				Description("Disable HTTP masking completely for direct TCP").
				Value(&disableHTTPMask),
			huh.NewInput().
				Title("HTTP mask mode (legacy/auto/stream/poll)").
				Value(&httpMaskMode).
				Validate(validateHTTPMaskMode),
			huh.NewConfirm().
				Title("Use HTTPS for HTTP tunnel (TLS)").
				Value(&httpMaskTLS),
			huh.NewInput().
				Title("HTTP mask Host override (optional)").
				Value(&httpMaskHost),
			huh.NewInput().
				Title("HTTP mask path root (optional)").
				Description("Example: aabbcc => /aabbcc/session, /aabbcc/api/v1/upload, ...").
				Value(&httpMaskPathRoot),
			huh.NewSelect[string]().
				Title("HTTP mask multiplex").
				Options(
					huh.NewOption("off", "off"),
					huh.NewOption("auto (try once, then remember)", "auto"),
					huh.NewOption("on (require)", "on"),
				).
				Value(&httpMaskMultiplex).
				Validate(validateHTTPMaskMultiplex),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Shared key (leave empty to auto-generate)").
				Value(&key),
			huh.NewInput().
				Title("Server config output path").
				Value(&serverPath),
			huh.NewInput().
				Title("Client config output path").
				Value(&clientPath),
		),
	)

	if err := form.Run(); err != nil {
		return wizardInput{}, true, err
	}

	serverPortInt, _ := strconv.Atoi(strings.TrimSpace(serverPort))
	mixPortInt, _ := strconv.Atoi(strings.TrimSpace(mixPort))
	paddingMinInt, _ := strconv.Atoi(strings.TrimSpace(paddingMin))
	paddingMaxInt, _ := strconv.Atoi(strings.TrimSpace(paddingMax))

	return wizardInput{
		Host:              host,
		ServerPort:        serverPortInt,
		MixPort:           mixPortInt,
		FallbackAddr:      fallback,
		AEAD:              aead,
		ASCIIMode:         encoding,
		SuspiciousAction:  suspiciousAction,
		PaddingMin:        paddingMinInt,
		PaddingMax:        paddingMaxInt,
		CustomTable:       customTable,
		EnablePureDown:    enablePureDownlink,
		DisableHTTPMask:   disableHTTPMask,
		HTTPMaskMode:      httpMaskMode,
		HTTPMaskTLS:       httpMaskTLS,
		HTTPMaskHost:      httpMaskHost,
		HTTPMaskPathRoot:  httpMaskPathRoot,
		HTTPMaskMultiplex: httpMaskMultiplex,
		Key:               key,
		ServerPath:        serverPath,
		ClientPath:        clientPath,
	}, true, nil
}
