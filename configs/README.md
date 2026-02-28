# Configuration Guide (JSON)

[中文说明](./README.zh_CN.md)

This project uses **two** JSON configuration files:
- Server template: `configs/server.config.json`
- Client template: `configs/client.config.json`

You can rename them to any filename; start the program with `-c`.

```bash
./sudoku -c server.config.json
./sudoku -c client.config.json
```

## Quick Start

1. Generate keys:
   ```bash
   ./sudoku -keygen
   ```
2. Put **Master Public Key** into the server config `key`.
3. Put **Available Private Key** into the client config `key`.

## Minimal Examples

Server (basic TCP):
```json
{
  "mode": "server",
  "local_port": 8080,
  "fallback_address": "127.0.0.1:80",
  "key": "MASTER_PUBLIC_KEY_HEX",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "padding_min": 5,
  "padding_max": 15,
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true,
  "httpmask": { "disable": false, "mode": "legacy" }
}
```

Client (basic TCP):
```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "example.com:8080",
  "key": "AVAILABLE_PRIVATE_KEY_HEX",
  "aead": "chacha20-poly1305",
  "padding_min": 5,
  "padding_max": 15,
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true,
  "httpmask": { "disable": false, "mode": "legacy" },
  "rule_urls": ["global"]
}
```

## Field Guide

### Basics
- `mode`: `"server"` or `"client"`.
- `local_port`:
  - server: public listening port
  - client: local mixed proxy port (HTTP + SOCKS)
- `server_address` (client only): server `host:port`.
- `fallback_address` (server only): decoy `host:port` for suspicious traffic.
- `suspicious_action` (server only):
  - `"fallback"`: forward to the decoy
  - `"silent"`: keep the connection but do not respond

### Keys & AEAD
- `key`:
  - server: **Master Public Key**
  - client: **Available Private Key**
- `aead`: `"chacha20-poly1305"` / `"aes-128-gcm"` / `"none"` (testing only).

### Sudoku encoding & padding
- `ascii`: `"prefer_entropy"` (recommended) or `"prefer_ascii"`.
- `padding_min` / `padding_max`: 0–100 percentage; `padding_max` must be ≥ `padding_min`.
- `custom_table`: optional layout string like `"xpxvvpvv"` (2×`x`, 2×`p`, 4×`v`).
- `custom_tables`: optional array of layouts; if non-empty it rotates layouts per connection.
- `enable_pure_downlink`:
  - `true`: both directions use classic Sudoku encoding
  - `false`: packed downlink mode to reduce overhead (requires AEAD, and both ends must match)

### HTTP masking / HTTP tunnel
All HTTP-related knobs live under the `httpmask` object.

Example (CDN/proxy friendly mode):
```json
{
  "httpmask": {
    "disable": false,
    "mode": "auto",
    "tls": true,
    "host": "example.com",
    "path_root": "aabbcc",
    "multiplex": "auto"
  }
}
```

- `disable`: set `true` to turn off all HTTP masking/tunneling.
- `mode`:
  - `"legacy"`: send one fake HTTP/1.1 header then switch to raw stream (not CDN-friendly)
  - `"stream"` / `"poll"` / `"auto"`: HTTP tunnel modes (CDN-friendly)
  - `"ws"`: WebSocket tunnel mode (CDN/reverse-proxy friendly)
- `tls` (client only, tunnel modes): set `true` to use HTTPS to the entry.
- `host` (client only, tunnel modes): override HTTP Host / SNI (optional).
- `path_root` (optional): first-level path prefix shared by both ends.
- `multiplex`:
  - `"off"`: do not reuse connections
  - `"auto"`: reuse HTTP connections when possible (keep-alive / HTTP/2)
  - `"on"`: enable a single-tunnel multi-target mux (lower RTT for many connections)

### Reverse proxy (expose client services)
Reverse proxy settings live under the `reverse` object.

Server entry:
```json
{ "reverse": { "listen": ":8081" } }
```

Client routes:
```json
{
  "reverse": {
    "client_id": "client-1",
    "routes": [
      { "path": "/gitea", "target": "127.0.0.1:3000", "strip_prefix": true },
      { "path": "/ssh", "target": "127.0.0.1:22" }
    ]
  }
}
```

- Server config only needs `reverse.listen` to open the entry port. Routes are always defined on the client.
- `listen` (server): the public reverse entry address.
- `client_id` (client): optional identifier for multi-client routing/management.
- `routes` (client): array of services to expose.
  - `path`: URL path prefix (for HTTP) or exact path (for TCP-over-WebSocket).
  - `target`: client-side `host:port`.
  - `strip_prefix`: whether to remove the prefix before proxying (default `true`).
  - `host_header`: optional override for upstream `Host`.

TCP-over-WebSocket (client-side local forwarder):
```bash
./sudoku -rev-dial wss://example.com:8081/ssh -rev-listen 127.0.0.1:2222
ssh -p 2222 127.0.0.1
```
Notes:
- The tunnel endpoint is the **exact path** `/ssh` (no trailing slash).
- The WebSocket subprotocol is `sudoku-tcp-v1` (handled by the built-in forwarder).

Raw TCP forwarding (one per server entry):
```json
{
  "reverse": {
    "client_id": "client-1",
    "routes": [{ "target": "10.0.0.1:25565" }]
  }
}
```

## Routing rules (client)
`rule_urls` controls proxy routing (PAC/rule download):
- `["global"]`: proxy everything
- `["direct"]`: proxy nothing
- URL list: download rule files and route accordingly

Mainland China example:
- The default `configs/client.config.json` uses a PAC URL list suitable as an example for Mainland China users (BiliBili/WeChat/ChinaMaxNoIP + CN CIDR v4/v6).
