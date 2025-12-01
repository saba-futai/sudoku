# Sudoku Tunnel Beginner Guide

Step-by-step instructions for absolute beginners to get a working client/server pair.

## 1) What you need
- OS: Linux / macOS / Windows.
- Either download the release binary or install Go 1.22+ to build it yourself.
- Ports: one public port on the server (example: 8080), one local proxy port on the client (default 1080).

## 2) Get the binary
Pick one:
1) Download the prebuilt archive from GitHub Releases and extract the `sudoku` executable.
2) Build locally:
```bash
git clone https://github.com/saba-futai/sudoku.git
cd sudoku
go build -o sudoku ./cmd/sudoku-tunnel
```

## 3) Generate keys
```bash
./sudoku -keygen
```
- Put the `Master Public Key` into the server config `key`.
- Put the `Available Private Key` into the client config `key`.
- Need more private keys for the same public key? Run `./sudoku -keygen -more <master-private-key>`.

## 4) Server config (`server.json`)
```json
{
  "mode": "server",
  "local_port": 8080,
  "fallback_address": "127.0.0.1:80",
  "key": "Master Public Key here",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "padding_min": 5,
  "padding_max": 15,
  "ascii": "prefer_entropy",
  "enable_mieru": false
}
```

## 5) Client config (`client.json`)
```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "1.2.3.4:8080",
  "key": "Available Private Key here",
  "aead": "chacha20-poly1305",
  "padding_min": 5,
  "padding_max": 15,
  "ascii": "prefer_entropy",
  "disable_http_mask": false,
  "proxy_mode": "pac",
  "rule_urls": []
}
```
- Want plaintext-looking traffic? Set `ascii` to `prefer_ascii` on both sides.
- Want split uplink/downlink (Mieru)? Set `enable_mieru` to `true` on both sides and add a matching `mieru_config.port`.

## 6) Run
```bash
# Server
./sudoku -c server.json

# Client (starts a mixed HTTP/SOCKS5 proxy on port 1080)
./sudoku -c client.json
```

## 7) Verify it works
- Terminal check: `curl -x socks5h://127.0.0.1:1080 https://ipinfo.io/ip` should show the server’s public IP.
- Browser: set a SOCKS5 proxy to `127.0.0.1:1080` (or the port you chose) and browse the web.

## 8) Use or share a short link
- Start the client directly from a link: `./sudoku -link "sudoku://..."`.
- Export a link from your config to share: `./sudoku -c client.json -export-link -public-host your.server.com`.

## 9) Quick troubleshooting
- Port in use: change `local_port` or free the port.
- Handshake or 403 errors: verify the client `key` matches the server public key; ensure `ascii` and `aead` settings match.
- Slow transfer: lower padding (`padding_min/max`) and confirm server bandwidth/firewall rules.
- Validate configs without running: `./sudoku -c server.json -test`.

## 10) Run in the background and update
- Linux persistence: see the systemd example in `doc/README.md`.
- Upgrading: replace the binary and restart; configs stay the same if keys do not change.
- Want an interactive setup? Try `./sudoku -tui` and follow the prompts.
