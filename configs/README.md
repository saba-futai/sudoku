# 配置文件说明（`configs/config.json`）

本项目使用单个 JSON 配置文件同时支持服务端与客户端。你可以直接复制 `configs/config.json` 作为模板手动修改。

## 最小示例

**服务端（`mode: "server"`）**

```json
{
  "mode": "server",
  "local_port": 8080,
  "fallback_address": "127.0.0.1:80",
  "suspicious_action": "fallback",
  "key": "<服务端使用公钥（hex）>",
  "aead": "chacha20-poly1305",
  "ascii": "prefer_entropy",
  "padding_min": 5,
  "padding_max": 15,
  "enable_pure_downlink": true,
  "httpmask": {
    "disable": false,
    "mode": "legacy",
    "tls": false,
    "host": "",
    "path_root": "",
    "multiplex": "off"
  }
}
```

**客户端（`mode: "client"`）**

```json
{
  "mode": "client",
  "local_port": 1080,
  "server_address": "example.com:8080",
  "key": "<客户端可填私钥或公钥（hex）>",
  "aead": "chacha20-poly1305",
  "ascii": "prefer_entropy",
  "padding_min": 5,
  "padding_max": 15,
  "enable_pure_downlink": true,
  "httpmask": {
    "disable": false,
    "mode": "legacy",
    "tls": false,
    "host": "",
    "path_root": "",
    "multiplex": "off"
  },
  "rule_urls": ["global"]
}
```

## 字段语义

### 基础字段
- `mode`：`"server"` 或 `"client"`。
- `local_port`：服务端监听端口（server）；客户端本地混合代理监听端口（client）。
- `server_address`：仅客户端使用，形如 `"host:port"`。
- `fallback_address`：仅服务端使用，形如 `"127.0.0.1:80"`；用于可疑流量回落到本机/同机的 Web 服务。
- `suspicious_action`：仅服务端使用：
  - `"fallback"`：回落转发到 `fallback_address`
  - `"silent"`：静默吞掉（tarpit）

### 密钥 / 加密
- `key`：共享密钥（推荐填公钥 hex）。生成方式：
  - `./sudoku -keygen`：会输出 split 私钥、master 私钥与 master 公钥（推荐把 **公钥** 放到服务端）。
  - 客户端为了多用户识别/分离密钥，可填 split 私钥；程序会自动推导公钥并在启动日志打印 `Derived Public Key`。
- `aead`：`"chacha20-poly1305"` / `"aes-128-gcm"` / `"none"`（不建议生产使用）。

### Sudoku 编码 / 填充
- `ascii`：`"prefer_entropy"`（默认）或 `"prefer_ascii"`。
- `padding_min` / `padding_max`：0–100 的概率百分比，表示在编码流中插入 padding 的概率范围（`max` 必须 >= `min`）。
- `custom_table`：自定义布局（8 个字符，包含 2 个 `x`、2 个 `p`、4 个 `v`），例如 `"xpxvvpvv"`。
- `custom_tables`：多套布局轮换（数组）。当配置多表时，客户端每条连接随机选择，服务端通过握手探测选择。
- `enable_pure_downlink`：
  - `true`：上下行都走纯 Sudoku 编码
  - `false`：下行启用 6-bit 拆分以提升带宽（要求 `aead != "none"`，且客户端/服务端必须一致）

### HTTP 伪装 / HTTP Tunnel
- 推荐使用新的 `httpmask` 对象统一管理 HTTP 伪装/隧道相关字段：
  - `httpmask.disable`：`true` 禁用所有 HTTP 伪装。
  - `httpmask.mode`：
    - `"legacy"`：写一个伪造 HTTP/1.1 头后切到原始流（默认，非 CDN 模式）
    - `"stream"` / `"poll"` / `"auto"`：CDN 友好的 HTTP tunnel 模式（通过 CDN 时建议用这些）
  - HTTP tunnel 模式（`stream`/`poll`/`auto`）会强制启用基于 `key` 的短期 HMAC/Token 校验以减少主动探测，无需额外字段（强制更新）。
  - `httpmask.tls`：仅客户端 tunnel 模式生效；`true` 表示使用 HTTPS。
  - `httpmask.host`：仅客户端 tunnel 模式生效；覆盖 HTTP Host/SNI（可留空）。
  - `httpmask.path_root`：可选，作为 **一级路径前缀**（双方需一致）。示例：填 `"aabbcc"` 后，端点会变为：
    - `/<path_root>/session`
    - `/<path_root>/stream`
    - `/<path_root>/api/v1/upload`
  - `httpmask.multiplex`：
    - `"off"`：不复用（每个目标单独建 tunnel）
    - `"auto"`：复用底层 HTTP 连接（keep-alive / h2）
    - `"on"`：开启“单隧道多目标”的 mux（客户端减少 RTT；服务端会看到 mux session）

兼容性：仍兼容旧版顶层字段 `disable_http_mask` / `http_mask_mode` / `http_mask_tls` / `http_mask_host` / `path_root` / `http_mask_multiplex` / `http_mask_path_root`，但建议迁移到 `httpmask`。

### 链式代理（Chain Proxy）
- `chain.hops`：仅客户端使用。多跳 Sudoku 代理列表（`host:port`），按顺序嵌套握手/建隧道，最后一跳才连接真正目标地址。

示例：
```json
{
  "server_address": "entry.example.com:443",
  "chain": { "hops": ["mid.example.com:443", "exit.example.com:443"] }
}
```

### 反向代理（Reverse Proxy over Sudoku）
用于让 NAT 后的客户端把本地 HTTP 服务通过隧道暴露给服务端，再由服务端通过不同路径前缀访问。

- 服务端：`reverse.listen`（如 `":8081"`）开启一个 HTTP 入口；通过 `http://<server>:8081/<path>/...` 访问。
- 客户端：`reverse.routes` 声明要暴露的本地服务：
  - `reverse.routes[].path`：对外路径前缀（如 `"/gitea"`）
  - `reverse.routes[].target`：客户端本地 `host:port`（如 `"127.0.0.1:3000"`）
  - `reverse.routes[].strip_prefix`：是否去掉前缀后再转发（默认 `true`）
  - `reverse.routes[].host_header`：可选，覆盖转发时的 `Host`

### 其他
- `rule_urls`：仅客户端 `proxy_mode=pac` 时使用；支持：
  - `["global"]`：全局代理
  - `["direct"]`：全直连
  - 或填 URL 列表（PAC/规则下载）
- `transport`：当前版本保留字段，建议保持 `"tcp"`。
