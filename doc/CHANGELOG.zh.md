# 更新日志

## Unreleased
- TBD

## v0.3.6（2026-03-15）
- `client/lb`: 客户端新增多节点负载均衡：`-c` / `-link` 支持多值同时生效，mixed 监听端口固定取首个配置的 `local_port`，同站点目标按 Rendezvous Hashing 粘性选路，并在节点失败时按次优节点回退。
- `cli`: 交互式 setup wizard 与客户端配置装配逻辑迁移到独立 `internal/cli`，入口职责更清晰；新增 `internal/cliutil` 复用多值 flag 解析，减少 `cmd/sudoku-tunnel` 主入口样板代码。
- `tests`: 将 CLI flag、负载均衡与原 `cmd/matrix-smoke` 的 HTTPMask 冒烟场景统一迁入 `tests/`，减少重复测试代码并保持全量回归覆盖。

## v0.3.4（2026-03-12）
- `cleanup`: 删除仓库内已不再使用的旧 `AEADConn` 实现及其测试，统一收敛到当前记录层 `RecordConn` 路径，减少重复加密封装。
- `cleanup`: 移除内部未使用的握手包装函数、下行模式辅助函数与 HTTPMask 认证辅助死代码，收紧实现表面并降低维护噪音。
- `tests`: 清理未引用的中间人测试辅助逻辑，保持测试代码与当前覆盖路径一致。

## v0.3.3（2026-03-10）
- `httpmask`: 将 KIP 握手折叠进首个 HTTP/WS 往返，消除启用 `httpmask` 时相对关闭状态多出的 1 RTT，同时保持抓包仍为 HTTP/WS、认证与 CDN 兼容路径不变。
- `httpmask`: 新增统一的 early-handshake 构建逻辑，收敛 client/server 在 `apis`、CLI 与测试入口的重复实现，降低后续维护成本。
- `tests`: 本地 smoke 新增固定链路时延下的 RTT 对照测试；`cmd/remote-matrix` 新增启用/关闭 `httpmask` 的 RTT 一致性校验。
- `crypto`: 记录层 epoch/seq 轮换逻辑改为随机初始化与更稳健的接收侧推进，补充对应测试，降低长连接与重协商边界场景下的脆弱性。
- `sudoku/packed`: 为前缀字节增加受保护的 padding 扰动，并补充前缀回归测试，降低前缀模式过于固定的问题。

## v0.3.2（2026-03-07）
- `dns`: 内置客户端侧硬编码 DNS 能力：DoH（阿里/腾讯）、缓存、过期兜底与 bogus IP（`198.18.0.0/15`）过滤；不新增配置字段，保持内核级默认策略。
- `dns`: DNS 查询保持 `IPv4 only`，并仅用于客户端目标 / PAC / DIRECT 解析；代理服务器引导路径（`server_address` / tunnel bootstrap）不走这套解析链，避免自循环与引导污染。
- `proxy/pac`: 在 FakeIP / OpenClash 一类环境下，PAC 与直连目标解析改用内置 resolver，避免系统 DNS 返回 `198.18.0.0/15` 导致误分流或误直连。
- `proxy/tun`: 内核新增跨平台主动出站 bypass 能力，消费 `SUDOKU_OUTBOUND_*` 环境变量；规则下载、DoH、DIRECT、服务端 TCP 拨号与 HTTPMask 主动出站在 TUN 场景下可绕开系统默认隧道路由，避免自循环导致的个别 PAC 规则源下载失败。
- `socks5/uot`: 修复 `UDP ASSOCIATE` 在 loopback / 多网卡 / point-to-point 接口场景下的本机地址通告与客户端地址学习逻辑，提升本地与虚拟网卡环境下的健壮性。
- `tests`: 补充解析与 `UDP ASSOCIATE` 相关单测、集成测试，并让测试地址选择跳过 point-to-point 接口，避免 `utun` 等本机特殊地址导致误报。

## v0.2.6（2026-02-23）
- `refactor`: 大规模模块化重构（不改协议/配置语义）：拆分 HTTPMask tunnel client/server 逻辑、拆分客户端 mixed proxy（SOCKS4/5/HTTP/路由/拨号）、拆分 reverse 重写逻辑并补单测，整体显著降低单文件复杂度与重复代码，便于后续维护与定位问题。
- `httpmask`: masker 的 User-Agent/paths/content-types 列表改为 embed 资源，代码更精简且便于后续按需调整指纹数据集。
- `sudoku/table`: `NewTable` 构建失败不再 `panic`，改为记录错误并返回 `nil`（仅影响作为 Go 库被调用的异常分支；正常配置不会触发）。

## v0.2.3（2026-02-14）
- `proxy/pac`: 修复 PAC 路由中的 DNS 缓存过期实现，移除 `time.AfterFunc` 删除策略导致的“旧定时器误删新值”与高并发定时器膨胀风险；改为按过期时间惰性回收，提升稳定性与可维护性。
- `proxy/pac`: PAC 的回查 DNS 从 `ip4` 扩展为 `ip`，允许同时利用 A/AAAA 结果进行规则判断。
- `geodata`: 重构规则解析路径，统一为构建态解析模型；在保持行为兼容的前提下收敛参数传递、减少耦合，使解析流程更易读、更易扩展。
- `geodata`: 新增原生 IPv6 规则区间存储与匹配（`IP-CIDR6`/IPv6 单 IP），并避免 IPv6 规则污染 IPv4 匹配集合，修复 PAC 在 IPv6 场景的潜在误分流。
- `proxy/http`: 修复 HTTP/CONNECT 请求中 `Host: [IPv6]` 无端口时的默认端口补全与路由 IP 提取逻辑，避免该场景下的错误路由/连接失败。
- `tests`: 新增/完善回归测试覆盖 DNS 缓存刷新、IPv6 规则匹配与 IPv6 Host 无端口解析，确保重构后行为稳定。

## v0.2.2（2026-02-05）
- `reverse`: 子路径反代彻底可用：修复资源 404、SVG/图片无法加载、WebSocket 无法建立等问题；增强 `Location`/`Set-Cookie Path` 重写，并支持在 `Content-Type` 缺失时按路径后缀推断是否可重写。
- `reverse`: 新增反代前缀 Cookie 回写与路由回退：当浏览器发起根路径请求（如 `/api/*`、`/ws`）但无 `Referer` 时，仍能正确路由到最近访问的子路径应用，彻底解决“资源 404 / WS 连不上”类问题。
- `reverse`: 避免对子路径场景的 JS/HTML 重写误伤正则字面量导致页面卡死/报错（如 `Invalid regular expression flags`）：重写时自动跳过 `<script>` 内联脚本内容（不再尝试改写脚本）。
- `reverse`: 避免将纯分隔符 `"/"` 误当作 URL 重写（修复 Navidrome 等应用出现 `#/navi//navi/...` 这类路径爆炸）；并在 `Content-Type` 不准确时按 `.js/.mjs` 后缀识别为 JS，从而禁用文本重写以避免破坏 bundle。
- `reverse`: 新增 TCP-over-WebSocket（CDN/反代友好）：在每个路由的精确路径 `/<route>`（无尾斜杠）上协商子协议 `sudoku-tcp-v1` 并转发任意 TCP 到客户端 `target`。
- `reverse`: 反向代理支持纯 TCP 转发：当 `reverse.routes[].path` 为空时，`reverse.listen` 上的非 HTTP 入站连接会被当作原始 TCP 流量转发到客户端目标（每个入口仅支持 1 条 TCP 路由）。
- `cli`: 新增内置本地端口转发器：`-rev-dial` / `-rev-listen` / `-rev-insecure`（用于把本地 TCP 通过 TCP-over-WebSocket 隧道转发）。
- `tunnel`: 修复双向 Pipe 在对端不支持 half-close 时的关闭行为，避免隧道连接“卡住不退出”，使内置转发器（含 SSH）稳定可用。
- `tunnel`: 修复 mux 会话在对端异常断开（`ECONNRESET`）时被误判为错误导致的偶发失败，提升 CI 与压力测试稳定性。
- `tests`: 新增 BDD 场景测试，模拟 TLS Edge/CDN、内置转发器与 HTTPMask 隧道，验证 HTTP 子路径与 TCP-over-WS 可用性。
- `deps`: 替换弃用的 `nhooyr.io/websocket` 为 `github.com/coder/websocket`。
- `ci`: release workflow 在构建发布产物前先跑严格测试，避免未验证版本被发布。
- `docs`: 补充反向代理与 TCP-over-WebSocket 的使用说明（含配置与命令示例）。

## v0.2.1（2026-01-30）
- `reverse`: 修复子路径挂载场景（如 `/gitea`）下静态资源/跳转路径丢前缀的问题：在 `strip_prefix=true` 时自动回写 `Location`、`Set-Cookie Path` 并重写 HTML/CSS/JS 中的根路径引用（如 `"/assets"`、`url(/assets)`）。

## v0.2.0（2026-01-29）
- `config`: `config.json` 新增 `httpmask` 对象统一管理 HTTP 相关字段，并保持对旧字段的向后兼容。
- `proxy`: 新增链式代理（多跳嵌套隧道）。
- `reverse`: 新增反向代理（服务端按路径前缀访问客户端本地 HTTP 服务）。
- `httpmask`: 过 CDN 默认使用 split-stream；移除 stream-one 路径。
- `perf`: Sudoku padding 概率逻辑移除 float；新增 `make bench` 与 `make pprof-sudoku`。
- `docker`: 新增服务端 Docker 镜像构建（Dockerfile）并在 tag 发布时推送到 GHCR。
- `apis`: 新增链式代理配置与更严格的校验测试。

## 版本概览
- v0.2.0：链式代理、反向代理、配置结构化（httpmask）、CDN 默认 split-stream、性能/benchmark 工具、Docker 镜像、API 对齐增强。
- v0.0.7：移除旧的分离下行实现，新增 `enable_pure_downlink` 开关（默认纯数独下行，可关闭以启用 6bit 拆分下行并提升带宽）；API/CLI 同步支持 UoT；改进 HTTP 伪装与回落。
- v0.0.6：初版 Sudoku 混淆 + AEAD 加密 + HTTP 伪装，支持 PAC/HTTP/SOCKS 混合代理。
- **v0.0.5**：新增 UoT（UDP over TCP）与 SOCKS5 UDP 支持，完善极端场景测试与 PR 自动化验证。
- **v old）**：优化数独连接性能与资源管理（ab6f00b）；补充文档入口（5890267）。
- **v0.1.3（2025-11-25）**：CLI 增强支持拆分密钥生成；握手新增 SHA-256 鉴权与错误细分；Ed25519 推导与拆分；配置描述/指引优化。
- **v0.1.2（2025-11-24）**：默认 Mieru 配置与修复初始化；连接缓冲/回放能力增强；HTTP 头处理与性能优化；SOCKS4、DNS 缓存、配置默认值加载与更多健壮性修复；新增标准模式测试。
- **v0.1.1（2025-11-24）**：新增协议 API；修复缓冲接口的空指针风险。
- **v0.0.ι（2025-11-24）**：HTTP 伪装与分离隧道支持；Mieru 下行隧道实现。
- **v0.0.γ（2025-11-23）**：Mieru 分离隧道初版，完善文档。
- **v0.0.α（2025-11-22）**：发布流程拆分；PAC 调试；YAML 规则、代理模式默认值与配置清理。
- **v0.0.9 / v0.0.8 / v0.0.7 / v0.0.5 / v0.0.4 / v0.0.3 / v0.0.2 / v0.0.1**：核心 Sudoku ASCII 协议、SOCKS5+PAC，逐步加入 ASCII 模式、多协议混合代理与规则下载。

## 完整提交时间线
- 2025-11-26 5890267 docs: add initial project README.
- 2025-11-26 ab6f00b refactor(sudoku): 重构数独连接以提高性能和资源管理
- 2025-11-25 7177bf1 (v0.1.3) feat(cli): enhance key generation with split key support
- 2025-11-25 ba07aed feat(security): enhance handshake authentication with SHA-256 hashing
- 2025-11-25 1677cb6 feat(config): update key generation instructions and improve mieru integration
- 2025-11-25 a27b3d9 feat(crypto): implement Ed25519 key derivation and splitting
- 2025-11-25 3b2c7c7 feat(api): enhance Sudoku protocol handshake with detailed error handling
- 2025-11-25 fe8915e refactor(config): clarify ASCII mode description and optimize logic
- 2025-11-24 7fec754 (v0.1.2) fix(config): correct mieru config initialization logic
- 2025-11-24 ab3a69d feat(config): implement default mieru config when enabled but not set
- 2025-11-24 5ff9eb4 feat(obfs): improve http header consumption and fallback handling
- 2025-11-24 db26ba8 feat(tunnel): enhance BufferedConn with data recording and retrieval
- 2025-11-24 7dee241 refactor(obfs/sudoku): reimplement connection management and buffering
- 2025-11-24 ace9e6b fix(obfs/sudoku): add nil pointer checks to prevent panics
- 2025-11-24 ee0e103 fix: Enhance connection safety and prevent panics with nil and type assertion checks across various connection types.
- 2025-11-24 c7a28d7 perf: improve obfuscation performance by reducing allocations and adding benchmarks.
- 2025-11-24 8e1c4cf feat: introduce configuration loading with default value application and remove specific HTTP masker content types.
- 2025-11-24 6c19c88 feat: Add SOCKS4 proxy support, implement DNS caching, and include unit tests for protocol handlers.
- 2025-11-24 716ac89 test: add `SudokuTunnel_Standard` test case for standard mode operation.
- 2025-11-24 8bd57ec feat: Abstract client proxy connection logic with a new `tunnel.Dialer` interface, improve hybrid manager's connection
- 2025-11-24 0cfc93a Antigravaty changed
- 2025-11-24 b7d9b0b (v0.1.1) fix(obfs): handle nil pointer in GetBufferedAndRecorded method
- 2025-11-24 c61b38e feat(api): implement Sudoku protocol client and server APIs
- 2025-11-24 57b783e (v0.0.ι) feat(proxy): implement HTTP masking and split tunneling support
- 2025-11-23 843b040 feat(hybrid): implement mieru-based downlink tunneling
- 2025-11-23 9686484 (v0.0.γ) feat(hybrid): implement split tunneling with Mieru integration
- 2025-11-22 806011e docs(readme): add link to Chinese documentation
- 2025-11-22 45f5f07 docs(readme): refine documentation and clarify protocol features
- 2025-11-22 b5a2c25 (v0.0.α) chore(release): split build and release workflows
- 2025-11-22 2c831b1 debug(config): add pac proxy mode support
- 2025-11-22 f61506f (v0.0.9) feat(geodata): support YAML format for rule parsing
- 2025-11-22 14ee4d6 refactor(config): remove legacy geoip_url and update default proxy mode
- 2025-11-22 65bc6a0 (v0.0.8) feat(client): implement mixed protocol proxy with HTTP/SOCKS5 support
- 2025-11-22 45c5e81 feat(client): implement mixed protocol proxy with HTTP/SOCKS5 support
- 2025-11-21 1f2130e docs(readme): translate and restructure documentation content
- 2025-11-21 a87cf9e (v0.0.7) feat(obfs): implement ASCII mode for Sudoku obfuscation
- 2025-11-21 9d3ac27 feat(obfs): implement ASCII mode for Sudoku obfuscation
- 2025-11-21 fec2ad4 (v0.0.5) feat(obfs): implement ASCII mode for Sudoku obfuscation
- 2025-11-21 8cb8d3a docs(readme): update README with badges, TODO section, and running instructions
- 2025-11-21 5d40e57 (v0.0.4, v0.0.3) feat(proxy): implement SOCKS5 proxy with PAC routing support
- 2025-11-20 aee2734 (v0.0.2, v0.0.1) feat(core): implement sudoku ascii traffic obfuscation protocol
- 2025-11-20 067240f Initial commit
