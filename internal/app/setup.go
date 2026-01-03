package app

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/saba-futai/sudoku/internal/config"
	"github.com/saba-futai/sudoku/pkg/crypto"
)

// WizardResult aggregates outputs from the interactive setup.
type WizardResult struct {
	ServerConfig     *config.Config
	ClientConfig     *config.Config
	ServerConfigPath string
	ClientConfigPath string
	ShortLink        string
}

type wizardInput struct {
	Host             string
	ServerPort       int
	MixPort          int
	FallbackAddr     string
	AEAD             string
	ASCIIMode        string
	SuspiciousAction string
	PaddingMin       int
	PaddingMax       int
	CustomTable      string
	EnablePureDown   bool

	DisableHTTPMask   bool
	HTTPMaskMode      string
	HTTPMaskTLS       bool
	HTTPMaskHost      string
	HTTPMaskMultiplex string

	Key string

	ServerPath string
	ClientPath string
}

// RunSetupWizard builds server/client configs interactively and exports a short link.
func RunSetupWizard(defaultServerPath, publicHost string) (*WizardResult, error) {
	if input, ok, err := runSetupWizardTUI(defaultServerPath, publicHost); ok {
		if err != nil {
			return nil, err
		}
		return finalizeWizard(input)
	}

	input, err := runSetupWizardPrompt(defaultServerPath, publicHost)
	if err != nil {
		return nil, err
	}
	return finalizeWizard(input)
}

func runSetupWizardPrompt(defaultServerPath, publicHost string) (wizardInput, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("== Sudoku Setup (fallback prompt mode) ==")
	host := promptString(reader, "Server public host/IP", publicHost, "127.0.0.1")
	serverPort := promptInt(reader, "Server port", 8080)
	mixPort := promptInt(reader, "Client mixed proxy port", 1080)
	fallback := promptString(reader, "Fallback address for suspicious traffic", "", "127.0.0.1:80")
	aead := promptString(reader, "AEAD (chacha20-poly1305 / aes-128-gcm / none)", "", "chacha20-poly1305")
	asciiMode := promptString(reader, "Encoding (ascii / entropy)", "", "entropy")
	suspiciousAction := promptString(reader, "Suspicious action (fallback / silent)", "", "fallback")
	paddingMin := promptInt(reader, "Padding min (%)", 5)
	paddingMax := promptInt(reader, "Padding max (%)", 15)
	customTable := promptString(reader, "Custom table layout (optional, e.g. xpxvvpvv)", "", "")
	pureDownlinkInput := strings.ToLower(strings.TrimSpace(promptString(reader, "Enable pure Sudoku downlink? (yes/no)", "yes", "yes")))
	enablePureDownlink := pureDownlinkInput != "no" && pureDownlinkInput != "n"

	disableHTTPMask := strings.ToLower(strings.TrimSpace(promptString(reader, "Disable HTTP mask? (yes/no)", "no", "no")))
	httpMaskDisabled := disableHTTPMask == "yes" || disableHTTPMask == "y"
	httpMaskMode := strings.TrimSpace(promptString(reader, "HTTP mask mode (legacy / auto / stream / poll)", "", "legacy"))
	httpMaskTLS := strings.ToLower(strings.TrimSpace(promptString(reader, "HTTP mask TLS (https)? (yes/no)", "no", "no")))
	httpMaskTLSEnabled := httpMaskTLS == "yes" || httpMaskTLS == "y"
	httpMaskHost := strings.TrimSpace(promptString(reader, "HTTP mask Host override (optional)", "", ""))
	httpMaskMux := strings.TrimSpace(promptString(reader, "HTTP mask multiplex (off / auto / on)", "", "off"))

	keyInput := promptString(reader, "Shared key (leave empty to auto-generate)", "", "")

	serverPath := promptString(reader, "Server config output path", defaultServerPath, defaultServerPath)
	if serverPath == "" {
		serverPath = "config.server.json"
	}
	clientPath := promptString(reader, "Client config output path", "client.config.json", "client.config.json")
	if clientPath == "" {
		clientPath = "client.config.json"
	}

	return wizardInput{
		Host:              host,
		ServerPort:        serverPort,
		MixPort:           mixPort,
		FallbackAddr:      fallback,
		AEAD:              aead,
		ASCIIMode:         asciiMode,
		SuspiciousAction:  suspiciousAction,
		PaddingMin:        paddingMin,
		PaddingMax:        paddingMax,
		CustomTable:       customTable,
		EnablePureDown:    enablePureDownlink,
		DisableHTTPMask:   httpMaskDisabled,
		HTTPMaskMode:      httpMaskMode,
		HTTPMaskTLS:       httpMaskTLSEnabled,
		HTTPMaskHost:      httpMaskHost,
		HTTPMaskMultiplex: httpMaskMux,
		Key:               keyInput,
		ServerPath:        serverPath,
		ClientPath:        clientPath,
	}, nil
}

func finalizeWizard(in wizardInput) (*WizardResult, error) {
	host := strings.TrimSpace(in.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	if in.ServerPort <= 0 {
		in.ServerPort = 8080
	}
	if in.MixPort <= 0 {
		in.MixPort = 1080
	}

	aead := strings.ToLower(strings.TrimSpace(in.AEAD))
	if aead == "" {
		aead = "chacha20-poly1305"
	}
	asciiMode := resolveASCII(in.ASCIIMode)
	suspiciousAction := strings.ToLower(strings.TrimSpace(in.SuspiciousAction))
	if suspiciousAction == "" {
		suspiciousAction = "fallback"
	}

	paddingMin := in.PaddingMin
	paddingMax := in.PaddingMax
	if paddingMin < 0 {
		paddingMin = 0
	}
	if paddingMax < 0 {
		paddingMax = 0
	}
	if paddingMax < paddingMin {
		paddingMax = paddingMin
	}

	enablePureDownlink := in.EnablePureDown
	if !enablePureDownlink && aead == "none" {
		fmt.Println("Bandwidth-optimized downlink requires AEAD. Forcing chacha20-poly1305.")
		aead = "chacha20-poly1305"
	}

	key := strings.TrimSpace(in.Key)
	if key == "" {
		// Use public key as the shared secret to avoid accidental private key exposure.
		pair, err := crypto.GenerateMasterKey()
		if err != nil {
			return nil, fmt.Errorf("generate key failed: %w", err)
		}
		key = crypto.EncodePoint(pair.Public)
		fmt.Printf("Generated shared key: %s\n", key)
	}

	httpMaskMode := strings.ToLower(strings.TrimSpace(in.HTTPMaskMode))
	if httpMaskMode == "" {
		httpMaskMode = "legacy"
	}
	httpMaskMux := strings.ToLower(strings.TrimSpace(in.HTTPMaskMultiplex))
	if httpMaskMux == "" {
		httpMaskMux = "off"
	}

	serverCfg := &config.Config{
		Mode:               "server",
		Transport:          "tcp",
		LocalPort:          in.ServerPort,
		FallbackAddr:       strings.TrimSpace(in.FallbackAddr),
		Key:                key,
		AEAD:               aead,
		SuspiciousAction:   suspiciousAction,
		PaddingMin:         paddingMin,
		PaddingMax:         paddingMax,
		ASCII:              asciiMode,
		CustomTable:        strings.TrimSpace(in.CustomTable),
		EnablePureDownlink: enablePureDownlink,
		DisableHTTPMask:    in.DisableHTTPMask,
		HTTPMaskMode:       httpMaskMode,
		HTTPMaskTLS:        in.HTTPMaskTLS,
		HTTPMaskHost:       strings.TrimSpace(in.HTTPMaskHost),
		HTTPMaskMultiplex:  httpMaskMux,
	}

	clientCfg := &config.Config{
		Mode:               "client",
		Transport:          "tcp",
		LocalPort:          in.MixPort,
		ServerAddress:      fmt.Sprintf("%s:%d", host, in.ServerPort),
		Key:                key,
		AEAD:               aead,
		PaddingMin:         paddingMin,
		PaddingMax:         paddingMax,
		ASCII:              asciiMode,
		CustomTable:        strings.TrimSpace(in.CustomTable),
		ProxyMode:          "pac",
		RuleURLs:           nil,
		EnablePureDownlink: enablePureDownlink,
		DisableHTTPMask:    in.DisableHTTPMask,
		HTTPMaskMode:       httpMaskMode,
		HTTPMaskTLS:        in.HTTPMaskTLS,
		HTTPMaskHost:       strings.TrimSpace(in.HTTPMaskHost),
		HTTPMaskMultiplex:  httpMaskMux,
	}

	serverPath := strings.TrimSpace(in.ServerPath)
	if serverPath == "" {
		serverPath = "config.server.json"
	}
	clientPath := strings.TrimSpace(in.ClientPath)
	if clientPath == "" {
		clientPath = "client.config.json"
	}

	if err := config.Save(serverPath, serverCfg); err != nil {
		return nil, fmt.Errorf("save server config: %w", err)
	}
	if err := config.Save(clientPath, clientCfg); err != nil {
		return nil, fmt.Errorf("save client config: %w", err)
	}

	shortLink, err := config.BuildShortLinkFromConfig(clientCfg, "")
	if err != nil {
		return nil, fmt.Errorf("build short link: %w", err)
	}

	return &WizardResult{
		ServerConfig:     serverCfg,
		ClientConfig:     clientCfg,
		ServerConfigPath: serverPath,
		ClientConfigPath: clientPath,
		ShortLink:        shortLink,
	}, nil
}

func promptString(r *bufio.Reader, label, current, fallback string) string {
	displayDefault := current
	if displayDefault == "" {
		displayDefault = fallback
	}
	fmt.Printf("%s [%s]: ", label, displayDefault)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return displayDefault
	}
	return line
}

func promptInt(r *bufio.Reader, label string, def int) int {
	fmt.Printf("%s [%d]: ", label, def)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	val, err := strconv.Atoi(line)
	if err != nil {
		fmt.Printf("Invalid number, using %d\n", def)
		return def
	}
	return val
}

func resolveASCII(val string) string {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "ascii", "prefer_ascii":
		return "prefer_ascii"
	default:
		return "prefer_entropy"
	}
}
