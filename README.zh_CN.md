<p align="center">
  <img src="./assets/logo-cute.svg" width="100%">
    一种基于数独的代理协议，开启了明文 / 低熵代理时代
</p>

# Sudoku (ASCII)

> Sudoku 协议目前已被 [Mihomo](https://github.com/MetaCubeX/mihomo) 内核支持！

[![构建状态](https://img.shields.io/github/actions/workflow/status/saba-futai/sudoku/.github/workflows/release.yml?branch=main&style=for-the-badge)](https://github.com/saba-futai/sudoku/actions)
[![最新版本](https://img.shields.io/github/v/release/saba-futai/sudoku?style=for-the-badge)](https://github.com/saba-futai/sudoku/releases)
[![License](https://img.shields.io/badge/License-GPL%20v3-blue.svg?style=for-the-badge)](./LICENSE)

**SUDOKU** 是一个基于4x4数独设题解题的流量混淆协议。它通过将任意数据流（数据字节最多有256种可能，4x4数独的非同构体有288种）映射为以4个Clue为题目的唯一可解数独谜题，每种Puzzle有不少于一种的设题方案，随机选择的过程使得同一数据编码后有多种组合，产生了混淆性。

该项目的核心理念是利用数独网格的数学特性，实现对字节流的编解码，同时提供任意填充与抗主动探测能力。

## 安卓客户端：

**[Sudodroid](https://github.com/saba-futai/sudoku-android)**
## 核心特性

### 数独隐写算法
不同于传统的随机噪音混淆，本协议通过多种掩码方案，可以将数据流映射到完整的ASCII可打印字符中，抓包来看是完全的明文数据，亦或者利用其他掩码方案，使得数据流的熵足够低。
*   **动态填充**: 在任意时刻任意位置填充任意长度非数据字节，隐藏协议特征。
*   **数据隐藏**: 填充字节的分布特征与明文字节分布特征基本一致(65%~100%*的ASCII占比)，可避免通过数据分布特征识别明文。
*   **低信息熵**: 整体字节汉明重量约在3.0*（低熵模式下）,低于GFW Report提到的会被阻断的3.4~4.6。

---

> *注：100%的ASCII占比须在ASCII优先模式下，ENTROPY优先模式下为65%。 3.0的汉明重量须在ENTROPY优先模式下，ASCII优先模式下为4.0.

> 目前没有任何证据表明两种优先策略的任何一种有明显指纹。

---

### 上下行分离
#### ——基于[mieru](https://github.com/enfein/mieru/tree/main)提供的API的下行带宽解决尝试
> 在此特别感谢[mieru](https://github.com/enfein/mieru/tree/main)的开发者

由于sudoku协议对流的封装会导致包增大，对于流媒体和下载场景，可能出现带宽受限的问题（理论本地与VPS各有200mbps上下行不会出现瓶颈），因此采用同样为非TLS方案的mieru协议作(可选的)下行协议。
#### mieru配置
```json
"enable_mieru": true,
"mieru_config": {
    "port": 20123,
    "transport": "TCP",
    "mtu": 1400,
    "multiplexing": "HIGH"
}
```
**解释**：当仅开启`enable_mieru`但不配置`"mieru_config"`字段时，默认使用sudoku同端口的UDP协议。`"enable_mieru"`为`true`时即开启上下行分离，为`false`时可忽略`"mieru_config"`字段。`"mieru_config"`字段中必填项为`port`，指定下行端口，其他配置可直接删除。


**注意**：目前尚不确定这种配置方法带来的流量特征是否会被审查，暂列为`实验性功能`。

### 安全与加密
在混淆层之下，协议可选的采用 AEAD 保护数据完整性与机密性。
*   **算法支持**: AES-128-GCM 或 ChaCha20-Poly1305。
*   **防重放**: 握手阶段包含时间戳校验，有效防止重放攻击。

### 防御性回落 (Fallback)
当服务器检测到非法的握手请求、超时的连接或格式错误的数据包时，不直接断开连接，而是将连接无缝转发至指定的诱饵地址（如 Nginx 或 Apache 服务器）。探测者只会看到一个普通的网页服务器响应。

### 缺点（TODO）
1.  **数据包格式**: 仅支持 TCP 数据包。
2.  **带宽利用率**: 低于30%，推荐线路好的或者带宽高的用户使用，另外推荐机场主使用，可以有效增加用户的流量。
3.  **客户端代理**: 仅支持socks5/http。
4.  **协议普及度**: 暂仅有官方和Mihomo支持，






## 快速开始

### 编译

```bash
go build -o sudoku cmd/sudoku-tunnel/main.go
```

### 服务端配置 (config.json)

```json
{
  "mode": "server",
  "local_port": 1080,
  "server_address": "",
  "fallback_address": "127.0.0.1:80",
  "key": "见下面的运行步骤",
  "aead": "chacha20-poly1305",
  "suspicious_action": "fallback",
  "ascii": "prefer_entropy",
  "padding_min": 2,
  "padding_max": 7,
  "disable_http_mask": false,
  "enable_mieru": false,
  "mieru_config": {}
}
```

### 客户端配置

将 `mode` 改为 `client`，并设置 `server_address` 为服务端 IP，将`local_port` 设置为代理监听端口，添加 `rule_urls` 使用`configs/config.json`的模板填充，以及按照模板配置上下行分离配置即可。

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

指定 `config.json` 路径为参数运行程序
```bash
./sudoku -c config.json
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

[![Star History Chart](https://api.star-history.com/svg?repos=saba-futai/sudoku&type=Date)](https://star-history.com/#saba-futai/sudoku)
