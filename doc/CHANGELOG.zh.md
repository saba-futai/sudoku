# 更新日志（main 分支）

根据 `main` 分支全部提交记录整理，时间范围覆盖 2025-11-20 ~ 2025-11-26（当前 HEAD：5890267）。

## 版本概览
- **未发布（main）**：新增 UoT（UDP over TCP）与 SOCKS5 UDP 支持，完善极端场景测试与 PR 自动化验证。
- **未发布（main）**：优化数独连接性能与资源管理（ab6f00b）；补充文档入口（5890267）。
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
