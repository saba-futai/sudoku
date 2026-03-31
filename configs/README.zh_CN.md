# 配置说明（JSON）

[English Version](./README.md)

本项目使用 **两个** JSON 配置文件：
- 服务端模板：`configs/server.config.json`
- 客户端模板：`configs/client.config.json`

你可以把它们重命名为任意文件名，运行时通过 `-c` 指定即可：

```bash
./sudoku -c server.config.json
./sudoku -c client.config.json
```

## 快速开始

1. 生成密钥：
   ```bash
   ./sudoku -keygen
   ```
2. 将输出中的 **Master Public Key** 填入服务端配置的 `key`。
3. 将输出中的 **Available Private Key** 填入客户端配置的 `key`。

## 最小示例

服务端（基础 TCP）：
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
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true,
  "httpmask": { "disable": false, "mode": "legacy" }
}
```

客户端（基础 TCP）：
```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "example.com:8080",
  "key": "粘贴 Available Private Key",
  "aead": "chacha20-poly1305",
  "padding_min": 5,
  "padding_max": 15,
  "ascii": "prefer_entropy",
  "enable_pure_downlink": true,
  "httpmask": { "disable": false, "mode": "legacy" },
  "rule_urls": ["global"]
}
```

## 字段含义

### 基础字段
- `mode`：`"server"` 或 `"client"`。
- `local_port`：
  - 服务端：对外监听端口
  - 客户端：本地混合代理端口（HTTP + SOCKS）
- `server_address`（仅客户端）：服务端地址 `host:port`。
- `fallback_address`（仅服务端）：可疑流量回落的诱饵地址 `host:port`。
- `suspicious_action`（仅服务端）：
  - `"fallback"`：转发到诱饵地址
  - `"silent"`：静默不响应（保持连接）

### 密钥与加密
- `key`：
  - 服务端：填 **Master Public Key**
  - 客户端：填 **Available Private Key**
- `aead`：`"chacha20-poly1305"` / `"aes-128-gcm"` / `"none"`（仅测试）。

### Sudoku 编码与填充
- `ascii`：
  - `"prefer_entropy"`（推荐）：上下行都偏向 entropy
  - `"prefer_ascii"`：上下行都偏向可打印 ASCII
  - `"up_ascii_down_entropy"`：客户端上行偏向 ASCII，服务端下行偏向 entropy
  - `"up_entropy_down_ascii"`：客户端上行偏向 entropy，服务端下行偏向 ASCII
- `padding_min` / `padding_max`：0–100 的百分比；`padding_max` 必须 ≥ `padding_min`。
- `custom_table`：可选的布局字符串，例如 `"xpxvvpvv"`（2 个 `x`、2 个 `p`、4 个 `v`）。
- `custom_tables`：可选的布局列表；非空时会在每条连接中轮换布局。
- 方向模式补充说明：
  - `custom_table` 只会作用在 `ascii` 为 `entropy` 的那个方向上。
  - 只有当客户端上行是 `entropy`（`prefer_entropy` / `up_entropy_down_ascii`）时，`custom_tables` 才能完整轮换，因为服务端需要从客户端探测流量里识别所选布局。
  - 对于 `up_ascii_down_entropy`，下行仍会使用自定义布局，但 `custom_tables` 会自动收敛到第一项，因为仅作用于下行的多组布局无法通过上行探测区分。
- `enable_pure_downlink`：
  - `true`：上下行都用经典 Sudoku 编码
  - `false`：启用带宽优化下行（需要 AEAD 且两端必须一致）

### HTTP 伪装 / HTTP 隧道
所有与 HTTP 相关的字段都集中在 `httpmask` 对象中。

示例（更适合走 CDN/反代）：
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

- `disable`：设为 `true` 表示关闭所有 HTTP 伪装/隧道。
- `mode`：
  - `"legacy"`：先写一个伪造 HTTP/1.1 头再进入原始流（不适合 CDN）
  - `"stream"` / `"poll"` / `"auto"`：HTTP 隧道模式（更适合 CDN）
  - `"ws"`：WebSocket 隧道（更适合 CDN/反代）
- `tls`（仅客户端、且隧道模式）：设为 `true` 表示入口使用 HTTPS。
- `host`（仅客户端、且隧道模式）：覆盖 HTTP Host / SNI（可选）。
- `path_root`（可选）：双方约定的一级路径前缀。
- `multiplex`：
  - `"off"`：不复用
  - `"auto"`：尽可能复用底层 HTTP 连接（keep-alive / HTTP/2）
  - `"on"`：开启单隧道多目标复用（大量短连接时更省 RTT）
### 反向代理（把客户端服务暴露到服务端）
反向代理相关字段在 `reverse` 对象中。

服务端入口：
```json
{ "reverse": { "listen": ":8081" } }
```

客户端声明要暴露的服务：
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

- 服务端配置只需要设置 `reverse.listen` 来开启入口端口；要暴露哪些服务永远由客户端 `routes` 决定。
- 反向代理会默认把客户端上行切到 `packed`，以降低 reverse-proxy 场景的客户端上行带宽压力；普通正向代理流量仍保持 `pure` 上行。
- 服务端目前仍兼容旧版 `pure` 上行的 reverse 客户端，但这条兼容路径已经标记为废弃，后续会移除。
- `listen`（服务端）：对外暴露的入口地址。
- `client_id`（客户端）：可选的标识，用于多客户端的区分/管理。
- `routes`（客户端）：要暴露的服务列表。
  - `path`：HTTP 的路径前缀；或 TCP-over-WebSocket 的精确路径。
  - `target`：客户端本地 `host:port`。
  - `strip_prefix`：是否去掉前缀后再转发（默认 `true`）。
  - `host_header`：可选，覆盖转发时的 `Host`。

TCP-over-WebSocket（本地转发器）：
```bash
./sudoku -rev-dial wss://example.com:8081/ssh -rev-listen 127.0.0.1:2222
ssh -p 2222 127.0.0.1
```
注意：
- 隧道入口是 **精确路径** `/ssh`（不要带尾斜杠）。
- 内置转发器会协商 WebSocket 子协议 `sudoku-tcp-v1`。

纯 TCP 转发（每个入口仅支持 1 条）：
```json
{
  "reverse": {
    "client_id": "client-1",
    "routes": [{ "target": "10.0.0.1:25565" }]
  }
}
```

## 分流规则（客户端）
`rule_urls` 用于控制客户端分流（PAC/规则下载）：
- `["global"]`：全局代理
- `["direct"]`：全直连
- URL 列表：下载规则文件并按规则分流

中国大陆适用举例：
- 模板 `configs/client.config.json` 默认给了一组 PAC 规则 URL（BiliBili/WeChat/ChinaMaxNoIP + CN CIDR v4/v6），可直接用作示例再按需增删。
