# Sudoku Tunnel Documentation / 使用手册

English | [中文](#zh)

Docs map:
- Beginner guide (EN): [doc/getting-started.en.md](./getting-started.en.md)
- 零基础上手指南（中文）：[doc/getting-started.zh.md](./getting-started.zh.md)
- 更新日志（中文）：[doc/CHANGELOG.zh.md](./CHANGELOG.zh.md)

## Overview
- **What it is**: TCP tunnel with HTTP masking, Sudoku ASCII/entropy obfuscation, and AEAD encryption; optional bandwidth-optimized downlink plus UoT (UDP-over-TCP).
- **Binaries**: single `sudoku` entry; configs in JSON; optional `sudoku://` short links for quick client bootstrap.
- **Ports**: server `local_port` is the public listening port; client `local_port` is the local mixed HTTP/SOCKS proxy port; UDP is relayed via the tunnel (UoT).

## Usage
- Generate keys: `./sudoku -keygen` (prints master public + client private). Use the server key on server side and client key on client side (see below).
- Run with config: `./sudoku -c server.config.json` (server) / `./sudoku -c client.config.json` (client)
- Test config only: `./sudoku -c server.config.json -test`
- Start client from link: `./sudoku -link "sudoku://..."` (PAC mode)
- Export short link from config:
  - client config: `./sudoku -c client.config.json -export-link`
  - server config: `./sudoku -c server.config.json -export-link -public-host host[:port]` (or set `server_address` in config)
- Interactive setup (creates server/client configs + link, then starts server): `./sudoku -tui [-public-host your.ip]`

## Key (most important)
`key` is the shared secret used to build the Sudoku mapping table and to derive the AEAD key (via SHA-256).

- Server config `key`: use `Master Public Key` (32-byte hex) printed by `./sudoku -keygen`.
- Client config `key`: use `Available Private Key` (64-byte hex) printed by `./sudoku -keygen`.
- The client will automatically derive the public key and use it as the shared `key` internally (you’ll see `Derived Public Key: ...` in logs).
- Keep both values private; anyone with the `key` can connect and decrypt AEAD traffic.

## Protocol (Layers & Principle)
- **HTTP mask**: random-looking HTTP request on connect.
- **Sudoku obfuscation**: bytes encoded as 4×4 Sudoku hints; `prefer_ascii` keeps output printable, `prefer_entropy` maximizes entropy.
- **AEAD**: `chacha20-poly1305` (default), `aes-128-gcm`, or `none` (test only); key hashed with SHA-256 to derive cipher key.
- **Handshake**: timestamp + nonce; server validates time skew and mode.
- **Downlink modes**: pure Sudoku (default) or packed 6-bit downlink (`enable_pure_downlink=false`, requires AEAD).

## Protocol (Wire Formats)
This section describes the **logical byte framing** used inside a tunnel connection.
On the real network, bytes are still transformed by the Sudoku obfuscation layer (and optionally wrapped by an HTTP mask).

### Record layer (AEAD framing)
Used when `aead` is not `none`.

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `body_len` | 2 | uint16, big-endian | Number of bytes that follow (`header` + `ciphertext`) |
| `header` | 12 | plaintext | `epoch` (4) + `seq` (8), both big-endian |
| `ciphertext` | `body_len - 12` | AEAD output | `AEAD.Seal(nonce=header, plaintext, aad=header)` |

Notes:
- `body_len` max is 65535 bytes.
- `epoch` is per-direction key rotation epoch; `seq` is a strict per-direction record counter.
- The 12-byte `header` is sent in plaintext and is also used as both **nonce** and **AAD**.

### KIP message (control plane)
All control messages are wrapped in a KIP message frame.

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `kip` | 3 | ASCII | Always `kip` |
| `type` | 1 | byte | Message type |
| `payload_len` | 2 | uint16, big-endian | Payload length |
| `payload` | variable | bytes | Payload |

KIP message types:

| Type | Value | Payload |
|---|---:|---|
| ClientHello | `0x01` | see below |
| ServerHello | `0x02` | see below |
| OpenTCP | `0x10` | SOCKS5-style address |
| StartMux | `0x11` | empty |
| StartUoT | `0x12` | empty |
| StartRev | `0x13` | JSON (reverse registration) |
| KeepAlive | `0x14` | empty |

### ClientHello payload

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `timestamp` | 8 | uint64, big-endian | Unix time (seconds) |
| `user_hash` | 8 | bytes | First 8 bytes of `SHA256(client_private_key)` (or `SHA256(key)` fallback) |
| `nonce` | 16 | bytes | Random nonce (client-generated) |
| `client_pub` | 32 | bytes | X25519 ephemeral public key |
| `features` | 4 | uint32, big-endian | Capability bits requested by client |

### ServerHello payload

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `nonce` | 16 | bytes | Must match the client nonce |
| `server_pub` | 32 | bytes | X25519 ephemeral public key |
| `selected_features` | 4 | uint32, big-endian | Capability bits accepted by server |

### Address encoding (SOCKS5 style)
Target addresses are encoded as:

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `atyp` | 1 | byte | `0x01` IPv4, `0x03` domain, `0x04` IPv6 |
| `addr` | variable | bytes | IPv4(4) / domain_len(1)+domain / IPv6(16) |
| `port` | 2 | uint16, big-endian | Destination port |

### Mux stream frames (in-tunnel multiplexing)
When multiplexing is enabled, a single tunnel carries multiple sub-streams.

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `frame_type` | 1 | byte | frame kind |
| `stream_id` | 4 | uint32, big-endian | sub-stream identifier |
| `payload_len` | 4 | uint32, big-endian | payload length |
| `payload` | variable | bytes | payload |

Frame types:

| Frame | Value | Payload |
|---|---:|---|
| Open | `0x01` | SOCKS5-style address |
| Data | `0x02` | raw bytes |
| Close | `0x03` | empty |
| Reset | `0x04` | UTF-8 text message (best-effort) |

### UoT datagram frames (UDP-over-TCP)
UoT sends datagrams over the reliable tunnel as:

| Field | Size | Encoding | Meaning |
|---|---:|---|---|
| `addr_len` | 2 | uint16, big-endian | address bytes length |
| `payload_len` | 2 | uint16, big-endian | datagram length |
| `addr` | `addr_len` | bytes | SOCKS5-style address bytes |
| `payload` | `payload_len` | bytes | UDP payload |

## How the Sudoku layer hides bytes (high-level)
- Both sides deterministically generate the same mapping table from `key` (+ optional `custom_table`).
- Each data byte is encoded into a small 4×4 Sudoku “puzzle hint” (a few clues) chosen from the table.
- Because there are multiple valid encodings for the same byte, the wire output varies even for identical input.
- `padding_min/max` inserts extra non-data hints to blur length/timing and stabilize byte distribution.
- The receiver filters padding, reconstructs hints, and decodes back to the original bytes.

## Config Templates
Minimal Server (standard):
```json
{
  "mode": "server",
  "local_port": 8080,
  "fallback_address": "127.0.0.1:80",
  "key": "MASTER_PUBLIC_KEY",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "padding_min": 5,
  "padding_max": 15,
  "custom_table": "xpxvvpvv",
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true
}
```

Client (standard):
```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "1.2.3.4:8080",
  "key": "AVAILABLE_PRIVATE_KEY",
  "aead": "chacha20-poly1305",
  "padding_min": 5,
  "padding_max": 15,
  "custom_table": "xpxvvpvv",
  "ascii": "prefer_entropy",
  "rule_urls": ["global"]
}
```

Prefer ASCII traffic: set `"ascii": "prefer_ascii"` on both ends. Toggle `"enable_pure_downlink": false` to enable packed downlink.
Need a custom byte fingerprint? Add `custom_table` with two `x`, two `p`, and four `v` (e.g. `xpxvvpvv`); all 420 permutations are accepted, and ASCII preference still wins if enabled.
Routing modes (client):
- `rule_urls: ["global"]` (default): proxy everything
- `rule_urls: ["direct"]`: proxy nothing (debug only)
- `rule_urls: [...]` (URLs): PAC mode, direct for CN/local rules and proxy the rest

## Cloudflare CDN (Orange Cloud)
Sudoku can run through a CDN/proxy only in real HTTP tunnel modes: `stream` / `poll` / `auto` (or `ws`). `legacy` mode is not CDN-compatible.

- Server: set `"httpmask": { "disable": false, "mode": "poll" }` (or `"auto"`).
- Client: same, and set `"server_address": "your.domain.com:443"` (or other Cloudflare-supported HTTP(S) ports like `8080`/`8443`).
- Recommended: set `"httpmask": { "multiplex": "auto" }` to reuse one tunnel connection for multiple target streams (lower RTT for subsequent connections).
- Set `"httpmask": { "tls": true }` to use HTTPS to the CDN edge (no port-based inference).

Notes:
- Cloudflare SSL mode `Flexible` is the simplest (Cloudflare → origin uses HTTP). For end-to-end TLS to your origin, use `Full (strict)` + a TLS terminator/reverse-proxy in front of Sudoku.

## Deployment & Persistence
- Build: `go build -o sudoku ./cmd/sudoku-tunnel`
- Systemd (example):
```ini
[Unit]
Description=Sudoku Tunnel Server
After=network.target

[Service]
ExecStart=/usr/local/bin/sudoku -c /etc/sudoku/server.config.json
Restart=on-failure
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
```
- Adjust paths/ports; for client, run as user service if desired.

## System Proxy (Client)
- Mixed proxy listens on `local_port` (default 1080).
- Browser: set SOCKS5 proxy to `127.0.0.1:<local_port>` (SwitchyOmega etc.).
- OS-wide: configure system SOCKS5/HTTP proxy to the same port (PAC rules handled inside client).

## Short Link Format
- Scheme: `sudoku://<base64url(json)>`
- JSON payload fields:
  - `h` host (server IP/FQDN) **required**
  - `p` port (server port) **required**
  - `k` key (public/shared) **required**
  - `a` ascii mode: `ascii` or `entropy` (default entropy)
  - `e` AEAD: `chacha20-poly1305` (default) / `aes-128-gcm` / `none`
  - `m` client mixed proxy port (default 1080 if missing)
  - `x` packed downlink (true enables bandwidth-optimized downlink)
  - `t` custom table pattern (optional, same as `custom_table` in config)
  - `ts` custom table patterns rotation (optional, same as `custom_tables` in config)
  - `hd` disable HTTP mask (optional)
  - `hm` HTTP mask mode: `legacy` / `stream` / `poll` / `auto` / `ws` (optional)
  - `ht` force HTTPS in tunnel modes (optional)
  - `hh` HTTP Host/SNI override in tunnel modes (optional)
  - `hx` HTTP mask multiplex: `off` / `auto` / `on` (optional)
  - `hy` HTTP mask path root prefix (optional)
- Example: `sudoku://eyJoIjoiZXhhbXBsZS5jb20iLCJwIjo4MDgwLCJrIjoiYWJjZCIsImEiOiJhc2NpaSIsIm0iOjEwODAsIm1wIjoyMDEyM30`
- Client bootstrap: `./sudoku -link "<link>"` (starts PAC proxy).
- Export from config: `./sudoku -c client.config.json -export-link` (or `./sudoku -c server.config.json -export-link -public-host host[:port]`)

---

<a name="zh"></a>

## 概览
- **功能**：HTTP 伪装 + 数独 ASCII/高熵混淆 + AEAD 加密，可选带宽优化下行。
- **形态**：单二进制 `sudoku`，JSON 配置；可用 `sudoku://` 短链接直接启动客户端。
- **端口**：服务端 `local_port` 是对外监听端口；客户端 `local_port` 是本地混合 HTTP/SOCKS 代理端口；UDP 通过隧道（UoT）转发。

## 使用方式
- 生成密钥：`./sudoku -keygen`（输出服务端公钥 + 客户端私钥，见下方说明）
- 配置运行：`./sudoku -c server.config.json`（服务端） / `./sudoku -c client.config.json`（客户端）
- 仅校验配置：`./sudoku -c server.config.json -test`
- 短链启动客户端：`./sudoku -link "sudoku://..."`（PAC 模式）
- 从配置导出短链：
  - 客户端配置：`./sudoku -c client.config.json -export-link`
  - 服务端配置：`./sudoku -c server.config.json -export-link -public-host 域名[:端口]`（或在配置里写 `server_address`）
- 交互式配置并启动服务端：`./sudoku -tui [-public-host 服务器IP]`

## key（最关键）
`key` 会用于生成数独映射表，同时也会被 SHA-256 后作为 AEAD 的派生密钥。

- 服务端配置 `key`：填 `./sudoku -keygen` 输出的 `Master Public Key`（32 字节 hex）。
- 客户端配置 `key`：填 `./sudoku -keygen` 输出的 `Available Private Key`（64 字节 hex）。
- 客户端会自动从私钥推导并在内部使用公钥作为共享 `key`（日志会打印 `Derived Public Key: ...`）。
- 两者都不要公开；拿到 `key` 的人可以连接并解密 AEAD 流量。

## 协议定义与原理
- **HTTP 伪装**：建立连接时先发随机化 HTTP 请求头。
- **数独混淆**：每字节编码为 4×4 数独提示；`prefer_ascii` 输出可打印字符，`prefer_entropy` 输出高熵字节。
- **AEAD 加密**：`chacha20-poly1305`（默认）/`aes-128-gcm`/`none`（仅测试）；密钥经 SHA-256 派生。
- **握手**：时间戳 + nonce；服务端校验时间偏差与模式是否一致。
- **下行模式**：默认纯数独下行；`enable_pure_downlink=false` 启用 6bit 拆分下行（需 AEAD）。

## 协议（更精确的字段定义）
本节描述的是隧道内部的**逻辑字节格式**。真实网络上的字节仍会先经过数独混淆层（并可选先走 HTTP 伪装）。

### Record 层（AEAD 分帧）
当 `aead` 不是 `none` 时启用。

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `body_len` | 2 | uint16，大端 | 后续字节长度（`header` + `ciphertext`） |
| `header` | 12 | 明文 | `epoch`（4）+ `seq`（8），均为大端 |
| `ciphertext` | `body_len - 12` | AEAD 输出 | `AEAD.Seal(nonce=header, plaintext, aad=header)` |

说明：
- `body_len` 最大为 65535 字节。
- `epoch` 是每个方向的密钥轮转代数；`seq` 是每个方向严格递增的记录序号。
- 12 字节的 `header` 会以明文发送，同时也会作为 **nonce** 和 **AAD** 使用。

### KIP 消息（控制面消息封装）
所有控制面消息都用 KIP 包一层：

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `kip` | 3 | ASCII | 固定为 `kip` |
| `type` | 1 | byte | 消息类型 |
| `payload_len` | 2 | uint16，大端 | payload 长度 |
| `payload` | 变长 | bytes | payload |

KIP 消息类型：

| 类型 | 值 | payload |
|---|---:|---|
| ClientHello | `0x01` | 见下表 |
| ServerHello | `0x02` | 见下表 |
| OpenTCP | `0x10` | SOCKS5 风格地址 |
| StartMux | `0x11` | 空 |
| StartUoT | `0x12` | 空 |
| StartRev | `0x13` | JSON（反代注册） |
| KeepAlive | `0x14` | 空 |

### ClientHello payload

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `timestamp` | 8 | uint64，大端 | Unix 秒级时间戳 |
| `user_hash` | 8 | bytes | `SHA256(客户端私钥)` 的前 8 字节（无私钥时用 `SHA256(key)` 回退） |
| `nonce` | 16 | bytes | 客户端随机数 |
| `client_pub` | 32 | bytes | X25519 临时公钥 |
| `features` | 4 | uint32，大端 | 客户端请求的能力位 |

### ServerHello payload

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `nonce` | 16 | bytes | 必须与客户端 `nonce` 相同 |
| `server_pub` | 32 | bytes | X25519 临时公钥 |
| `selected_features` | 4 | uint32，大端 | 服务端选择/接受的能力位 |

### 地址编码（SOCKS5 风格）

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `atyp` | 1 | byte | `0x01` IPv4、`0x03` 域名、`0x04` IPv6 |
| `addr` | 变长 | bytes | IPv4(4) / 域名长度(1)+域名 / IPv6(16) |
| `port` | 2 | uint16，大端 | 目标端口 |

### Mux 子流分帧（隧道内多路复用）

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `frame_type` | 1 | byte | 帧类型 |
| `stream_id` | 4 | uint32，大端 | 子流编号 |
| `payload_len` | 4 | uint32，大端 | payload 长度 |
| `payload` | 变长 | bytes | payload |

帧类型：

| 帧 | 值 | payload |
|---|---:|---|
| Open | `0x01` | SOCKS5 风格地址 |
| Data | `0x02` | 原始数据 |
| Close | `0x03` | 空 |
| Reset | `0x04` | UTF-8 文本（尽力而为） |

### UoT 报文（UDP-over-TCP）

| 字段 | 长度 | 编码 | 含义 |
|---|---:|---|---|
| `addr_len` | 2 | uint16，大端 | 地址字节长度 |
| `payload_len` | 2 | uint16，大端 | UDP 负载长度 |
| `addr` | `addr_len` | bytes | SOCKS5 风格地址字节 |
| `payload` | `payload_len` | bytes | UDP 数据 |

## 数独层如何“藏”字节（高层理解）
- 双端会用相同的 `key`（+ 可选 `custom_table`）确定性生成同一张映射表。
- 每个数据字节会被编码成一个很小的 4×4 数独“提示”（少量 clues），并从映射表中选取。
- 同一字节通常存在多种合法编码，因此即便输入相同，线上输出也会随机化变化。
- `padding_min/max` 会插入额外的非数据提示，用于模糊长度/时序并稳定字节分布。
- 接收端会过滤 padding、还原提示并解码回原始字节。

## 配置示例
服务端（标准）：
```json
{
  "mode": "server",
  "local_port": 8080,
  "fallback_address": "127.0.0.1:80",
  "key": "粘贴 Master Public Key",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "padding_min": 5,
  "padding_max": 15,
  "custom_table": "xpxvvpvv",
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true
}
```

客户端（标准）：
```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "1.2.3.4:8080",
  "key": "粘贴 Available Private Key",
  "aead": "chacha20-poly1305",
  "padding_min": 5,
  "padding_max": 15,
  "custom_table": "xpxvvpvv",
  "ascii": "prefer_entropy",
  "rule_urls": ["global"]
}
```

- ASCII 风格：`"ascii": "prefer_ascii"`（客户端/服务端一致）。
- 带宽优化：将 `"enable_pure_downlink"` 设为 `false` 启用带宽优化下行（需 AEAD）。
- 自定义字节特征：添加 `custom_table`（两个 `x`、两个 `p`、四个 `v`，如 `xpxvvpvv`，共 420 种排列），`ascii` 优先级最高。
客户端分流模式（路由）：
- `rule_urls: ["global"]`（默认）：全局代理
- `rule_urls: ["direct"]`：全直连（仅用于调试）
- `rule_urls: [...]`（URL 列表）：PAC 模式，命中 CN/local 规则走直连，其他走代理

## 过 Cloudflare CDN（小黄云）
Sudoku 只有在真实 HTTP 隧道模式下才能过 CDN/代理：`stream` / `poll` / `auto`（或 `ws`）；`legacy` 模式不兼容 CDN。

- 服务端：设置 `"httpmask": { "disable": false, "mode": "poll" }`（或 `"auto"`）。
- 客户端：同样开启 HTTP mask，并将 `"server_address"` 填成 Cloudflare 域名（如 `"your.domain.com:443"`；也可用 Cloudflare 支持的 `8080`/`8443` 等端口）。
- 建议同时开启 `"httpmask": { "multiplex": "auto" }`（复用单条隧道连接承载多条目标流，降低后续连接 RTT）。
- 如需使用 HTTPS，请显式设置 `"httpmask": { "tls": true }`（不再按端口自动推断）。

提示：
- Cloudflare 的 SSL 模式选 `Flexible` 最省事（Cloudflare → 源站走 HTTP）。如需源站也走 TLS，请用 `Full (strict)` 并在 Sudoku 前面加一层 TLS 终止/反代。

## 部署与守护
- 构建：`go build -o sudoku ./cmd/sudoku-tunnel`
- Systemd 示例见上（修改路径/端口）；客户端可用用户级服务。
- 确保 `LimitNOFILE` 足够大。

## 系统代理指向客户端
- 混合代理监听 `local_port`（默认 1080）。
- 浏览器：SOCKS5 指向 `127.0.0.1:<端口>`（可用 SwitchyOmega）。
- 系统级：在网络设置中填入同样的 SOCKS5/HTTP 代理；PAC 逻辑由客户端内部处理。

## 短链接格式
- 形式：`sudoku://<base64url(json)>`
- 字段：
  - `h` 主机（必填），`p` 端口（必填），`k` 密钥（必填，公钥/共享密钥）
  - `a` ASCII 模式：`ascii` / `entropy`（默认 entropy）
  - `e` AEAD：`chacha20-poly1305`（默认）/`aes-128-gcm`/`none`
  - `m` 客户端混合代理端口（缺省 1080）
  - `x` 带宽优化下行标记（true=启用）
  - `t` 自定义表型（可选，与 `custom_table` 一致）
  - `ts` 多表轮换（可选，与 `custom_tables` 一致）
  - `hd` 禁用 HTTP mask（可选）
  - `hm` HTTP mask 模式：`legacy` / `stream` / `poll` / `auto` / `ws`（可选）
  - `ht` 强制 HTTPS（可选）
  - `hh` tunnel 模式下的 Host/SNI 覆盖（可选）
  - `hx` HTTP mask 多路复用：`off` / `auto` / `on`（可选）
  - `hy` HTTP mask path root 前缀（可选）
- 启动：`./sudoku -link "<短链>"`；导出：`./sudoku -c client.config.json -export-link [-public-host]`
