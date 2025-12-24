# Sudoku Tunnel 零基础上手指南

适合从未接触过代理/隧道的新手，带你从下载到验证一路跑通。

## 0. 服务端一键脚本

**[easy-install](https://github.com/SUDOKU-ASCII/easy-install)**


## 1. 准备工作
- 一台可被客户端访问到的服务器（有公网 IP / 域名，或在同一网络环境可直连）。
- 电脑：Linux / macOS / Windows 均可。
- 依赖：已下载发布版二进制，或安装 Go 1.22+ 准备自行编译。
- 端口：服务端需要一个外网可访问的 TCP 端口（示例用 8080），客户端本地代理端口默认 1080；同时确认服务器防火墙/安全组已放行该端口。

## 2. 获取程序
二选一：
1) 直接下载：从 GitHub Releases 页面获取与你平台匹配的压缩包并解压出 `sudoku` 可执行文件。
2) 自行编译：
```bash
git clone https://github.com/saba-futai/sudoku.git
cd sudoku
go build -o sudoku ./cmd/sudoku-tunnel
```

## 3. 生成密钥
```bash
./sudoku -keygen
```
输出中：
- `Master Public Key`：填到服务端配置里的 `key`。
- `Available Private Key`：填到客户端配置里的 `key`。
- 需要更多私钥对应同一个公钥时，使用 `./sudoku -keygen -more <master-private-key>`。

## 4. 准备服务端配置（server.json）
将以下内容保存为 `server.json`（按需修改端口和回落地址）：
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
提示：如果你没有在 `fallback_address` 上准备诱饵网页服务，可以把 `"suspicious_action"` 设为 `"silent"`，对可疑连接直接丢弃。

## 5. 准备客户端配置（client.json）
将以下内容保存为 `client.json`，把 `server_address` 改成你的服务器地址和端口，把 `key` 换成 Available Private Key：
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
  "disable_http_mask": false,
  "rule_urls": ["global"]
}
```
- 想要看起来更像纯文本：把 `ascii` 改成 `prefer_ascii`，客户端和服务端需一致。
- 想要自定义字节指纹：添加 `custom_table`（两个 `x`、两个 `p`、四个 `v`，如 `xpxvvpvv`，共 420 种全排列）；若同时配置 ASCII，则 ASCII 优先生效。
- 想要更好的下行带宽：两端都将 `enable_pure_downlink` 设为 `false`，开启带宽优化下行（需 AEAD）。
- 分流提示：`rule_urls: ["global"]` 表示全局代理（最省心）。如需 PAC 分流，请配置规则 URL（见 `doc/README.md`），或直接用短链启动（`./sudoku -link ...`）。

## 5.1（可选）过 Cloudflare CDN（小黄云）
如需走 Cloudflare CDN/反代，请使用真实 HTTP 隧道模式（`stream` / `poll` / `auto`），不要用 `legacy`。

- 服务端：`"disable_http_mask": false`，并将 `"http_mask_mode"` 设为 `"poll"`（或 `"auto"`）。
- 客户端：同样开启 HTTP mask，并把 `"server_address"` 填成 Cloudflare 域名（例如 `"your.domain.com:443"`；也可用 Cloudflare 支持的 `8080`/`8443` 等端口）。
- `443` 会自动走 HTTPS；如需强制 HTTPS，可设 `"http_mask_tls": true`。

## 6. 启动
```bash
# 服务端
./sudoku -c server.json

# 客户端（本机开启 HTTP/SOCKS5 混合代理，默认 1080 端口）
./sudoku -c client.json
```

## 7. 验证是否成功
- 终端测试：`curl -x socks5h://127.0.0.1:1080 https://ipinfo.io/ip` 应返回服务器的出口 IP。
- 浏览器：在代理插件或系统网络里填上 SOCKS5 `127.0.0.1` 端口 `1080`，访问网页确认可用。

## 8. 使用/导出短链
- 启动客户端并直接用短链：`./sudoku -link "sudoku://..."`。
- 从配置导出短链（分享给别人）：
  - 客户端配置：`./sudoku -c client.json -export-link`
  - 服务端配置：`./sudoku -c server.json -export-link -public-host 域名[:端口]`
短链可让对方免编辑配置，直接运行即可。
提示：短链接支持 `custom_table` 以及 `custom_tables`（多表轮换），并可携带 CDN 相关的 HTTP mask 选项；如需兼容旧版本客户端，请至少保留 `custom_table`。

## 9. 常见问题速查
- **端口占用**：更换 `local_port` 或释放冲突程序。
- **握手失败/403**：确认客户端 `key` 与服务端公钥匹配；确保双方 `ascii`、`aead` 设置一致。
- **连得上但很慢**：检查 `padding_min/max` 是否设置过大；确认服务器出口带宽与防火墙放行。
- **配置是否生效**：使用 `-test` 选项，例如 `./sudoku -c server.json -test`，仅校验配置不真正启动。

## 10. 后台运行与更新
- Linux 持久化：可参考 `doc/README.md` 里的 systemd 示例编写服务。
- 更新：替换二进制后重启进程；如密钥未变，无需改配置。
- 想快速重新生成配置：尝试 `./sudoku -tui`，按提示一步步选择，会自动生成并启动。
