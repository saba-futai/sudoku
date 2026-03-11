<p align="center">
  <img src="./assets/logo-cute.svg" width="100%">
    一种抛弃随机数基于数独的代理协议，开启了明文 / 低熵 / 用户自定义特征代理时代
</p>

# Sudoku (ASCII)

> Sudoku 协议目前已被 [Mihomo](https://github.com/MetaCubeX/mihomo) 内核支持！

[![构建状态](https://img.shields.io/github/actions/workflow/status/SUDOKU-ASCII/sudoku/.github/workflows/release.yml?branch=main&style=for-the-badge)](https://github.com/SUDOKU-ASCII/sudoku/actions)
[![最新版本](https://img.shields.io/github/v/release/SUDOKU-ASCII/sudoku?style=for-the-badge)](https://github.com/SUDOKU-ASCII/sudoku/releases)
[![License](https://img.shields.io/badge/License-GPL%20v3-blue.svg?style=for-the-badge)](./LICENSE)

**SUDOKU** 是一个基于4x4数独设题解题的流量混淆协议。它通过将任意数据流（数据字节最多有256种可能，4x4数独的非同构体有288种）映射为以4个Clue为题目的唯一可解数独谜题，每种Puzzle有不少于一种的设题方案，随机选择的过程使得同一数据编码后有多种组合，产生了混淆性。

> **以防有些人看不懂README的中文，特此澄清**：在enable_pure_downlink false时下行带宽利用率在**80%**，而非网传的30%，并且下行不是随机数，上行是什么字节格式，下行就是什么，你有422种选择。请不要诋毁sudoku了可以吗，这又不是什么利益竞争。有问题提issue。

该项目的核心理念是利用数独网格的数学特性，实现对字节流的编解码，同时提供任意填充与抗主动探测能力。

## 客户端支持

版本要求：
- 基于 Mihomo 内核的客户端：**Mihomo > v1.19.21**
- 官方安卓客户端 Sudodroid：**Sudodroid >= v0.2.4**

### Android
- **CMFA（Clash Meta for Android / Mihomo 内核）**：https://github.com/MetaCubeX/ClashMetaForAndroid
- **Sudodroid（官方 Sudoku 客户端）**：https://github.com/SUDOKU-ASCII/sudoku-android

### iOS
- **Clash Mi（Mihomo 内核）**：https://github.com/KaringX/clashmi （App Store： https://apps.apple.com/us/app/clash-mi/id6744321968 ）

### 桌面端（Windows/macOS/Linux）
- **Sudoku Desktop（官方 Sudoku 桌面客户端）**：https://github.com/SUDOKU-ASCII/sudoku-desktop
- **任意 Mihomo 内核的套壳 UI**（例如 Clash Verge Rev）：https://github.com/Clash-Verge-rev/clash-verge-rev

### 软路由（OpenWrt）
- **OpenClash**：https://github.com/vernesong/OpenClash

### 一键安装脚本（服务端/工具）
- https://github.com/SUDOKU-ASCII/easy-install


## 核心特性

### 数独隐写算法
不同于传统的随机噪音混淆，本协议通过多种掩码方案，可以将数据流映射到完整的ASCII可打印字符（这只是微不足道的、可选的一种罢了，你的特征你来决定）中，抓包来看是完全的明文数据（特指在这种情况下，而非全部情况下，sudoku不是专为明文而生，明文只是附带的一个选择罢了），亦或者利用其他掩码方案，使得数据流的熵足够低。
*   **动态填充**: 在任意时刻任意位置填充任意长度非数据字节，隐藏协议特征。
*   **数据隐藏**: 填充字节的分布特征与明文字节分布特征基本一致(65%~100%*的ASCII占比)，可避免通过数据分布特征识别明文。
*   **低信息熵**: 整体字节汉明重量约在3.0/5.0*（低熵模式下）,低于GFW Report提到的会被阻断的3.4~4.6。
*   **自由暖暖**: 用户可以随意定义想要的字节样式，我们不推荐某一种，正是大家混着用才能更好规避审查。

---

> *注：100%的ASCII占比须在ASCII优先模式下，ENTROPY优先模式下为65%。 3.0的汉明重量须在ENTROPY优先模式下，ASCII优先模式下为4.0。目前没有证据表明任一优先策略有明显指纹。

### 下行模式
* **纯 Sudoku 下行**：默认模式，上下行都使用经典的数独谜题编码。
* **带宽优化下行**：将 `enable_pure_downlink` 设为 `false` 后，下行会把 AEAD 密文拆成 6bit 片段，复用原有的填充池与 ASCII/entropy/customised 偏好，降低下行开销；上行保持sudoku本身协议，下行特征此时与上行保持一致。此模式必须开启 AEAD。


### 安全与加密
在混淆层之下，协议可选的采用 AEAD 保护数据完整性与机密性。
*   **算法支持**: AES-128-GCM 或 ChaCha20-Poly1305。
*   **PFS + Key Update**：每条连接派生独立会话密钥，并在长连接中自动轮转子密钥。
*   **防重放**：握手阶段做短 TTL 的 nonce 严格去重（并包含时间戳偏移校验），阻断窗口内重放。

### 防御性回落 (Fallback)
当服务器检测到非法的握手请求、超时的连接或格式错误的数据包时，不直接断开连接，而是将连接无缝转发至指定的诱饵地址（如 Nginx 或 Apache 服务器）。探测者只会看到一个普通的网页服务器响应。

#### 用作链式代理 / 端口复用
Fallback 的主要价值不止是“回落到网页”，更常见的是把它当作**链式代理/中转**：用一个“外层 Sudoku 服务端”做入口（伪装/承压/抗探测），在握手判定失败时把连接转发到 `fallback_address`；而 `fallback_address` 指向的是**内层的另一个 Sudoku 服务端**，由它来接收并完成真实的 Sudoku 握手与转发。

推荐搭法（两层 Sudoku）：
1. **内层（真实）Sudoku 服务端**：监听一个内网端口（例如 `0.0.0.0:8443`），配置为你真正要用的与真实客户端配套的 `key` / `aead` / `httpmask` 等。
2. **外层（入口）Sudoku 服务端**（跳板）：监听公网端口，并设置：
   - `"suspicious_action": "fallback"`
   - `"fallback_address": "x.x.x.x:8443"`（指向内层`ip:port`）
   - `key` 填一个**不同于内层的“假 key”**（这样正常客户端连到外层时会握手失败，从而触发 fallback 中转到内层），或者相同的key不同的ascii偏好。
3. **客户端**：`server_address` 填外层公网地址，但 `key` 使用**内层的真实 key**且与内层配套。

这样客户端看起来“连的是外层”，但实际握手与数据通道都在内层完成；外层只负责在握手失败时把已读到的前缀字节按顺序重放给内层，然后做双向转发。
（~~当然中间跳板也是完整的 sudoku 服务器~~）

### 缺点（TODO）
1.  **数据包格式**: 原生 TCP，UDP 通过 UoT（UDP-over-TCP）隧道支持，暂不暴露原生 UDP 监听。
2.  **客户端代理**: 仅支持 socks5/http。无原生 TUN。
3.  **协议普及度**: 暂仅有官方和 Mihomo 支持。






## 快速开始

### 编译

```bash
go build -o sudoku cmd/sudoku-tunnel/main.go
```

### 配置

配置模板与字段说明请看：
- [configs/README.zh_CN.md](./configs/README.zh_CN.md)
- [configs/README.md](./configs/README.md)

模板：
- `configs/server.config.json`
- `configs/client.config.json`

### Docker（服务端）
本地构建：
```bash
docker build -t sudoku:local .
```
运行（首次启动自动生成配置）：
```bash
docker run --rm -p 8080:8080 -p 8081:8081 -v sudoku-data:/etc/sudoku sudoku:local
```

容器在 `/etc/sudoku/server.config.json` / `/etc/sudoku/keys.env` 不存在时会自动生成，并在日志里输出客户端所需的私钥（`AVAILABLE_PRIVATE_KEY`）。

如果你想自带配置，也可以直接挂载：
```bash
docker run --rm -p 8080:8080 -p 8081:8081 -v "$PWD/server.config.json:/etc/sudoku/server.config.json:ro" sudoku:local
```

**注意**：Key一定要用sudoku专门生成

### 运行

> 务必先生成KeyPair
```bash
$ ./sudoku -keygen
Available Private Key: b1ec294d5dba60a800e1ef8c3423d5a176093f0d8c432e01bc24895d6828140aac81776fc0b44c3c08e418eb702b5e0a4c0a2dd458f8284d67f0d8d2d4bfdd0e
Master Private Key: 709aab5f030c9b8c322811d5c6545497c2136ce1e43b574e231562303de8f108
Master Public Key:  6e5c05c3f7f5d45fcd2f6a5a7f4700f94ff51db376c128c581849feb71ccc58b
```
你需要将`Master Public Key`填入服务端配置的`key`，然后复制`Available Private Key`，填入客户端的`key`。

如果你需要生成更多与此公钥相对的私钥，请使用`-more`参数 + 已有的私钥/'Master Private Key'：
```bash
$ ./sudoku -keygen -more 709aab5f030c9b8c322811d5c6545497c2136ce1e43b574e231562303de8f108
Split Private Key: 89acb9663cfd3bd04adf0001cc7000a8eb312903088b33a847d7e5cf102f1d0ad4c1e755e1717114bee50777d9dd3204d7e142dedcb023a6db3d7c602cb9d40e
```
将此处的`Split Private Key`填入客户端配置的`key`。

指定配置文件路径运行程序
```bash
./sudoku -c server.config.json
./sudoku -c client.config.json
```

## 协议流程

1.  **初始化**: 客户端与服务端根据预共享密钥（Key）生成相同的数独映射表。
2.  **握手**: 客户端发送加密的时间戳与随机数。
3.  **传输**: 数据 -> AEAD 加密 -> 切片 -> 映射为数独提示 -> 添加填充 -> 发送。
4.  **接收**: 接收数据 -> 过滤填充 -> 还原数独提示 -> 查表解码 -> AEAD 解密。

---


## 声明
> [!NOTE]\
> 此软件仅用于教育和研究目的。用户需自行遵守当地网络法规。

## 鸣谢

- [链接1](https://gfw.report/publications/usenixsecurity23/zh/)
- [链接2](https://github.com/enfein/mieru/issues/8)
- [链接3](https://github.com/zhaohuabing/lightsocks)
- [链接4](https://imciel.com/2020/08/27/create-custom-tunnel/)
- [链接5](https://oeis.org/A109252)
- [链接6](https://pi.math.cornell.edu/~mec/Summer2009/Mahmood/Four.html)


## Star History

[![Stargazers over time](https://starchart.cc/SUDOKU-ASCII/sudoku.svg?variant=adaptive)](https://starchart.cc/SUDOKU-ASCII/sudoku)
