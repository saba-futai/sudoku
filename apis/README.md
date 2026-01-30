# Sudoku API (Standard)

面向其他开发者开放的纯 Sudoku 协议 API：HTTP 伪装 + 数独 ASCII/Entropy 混淆 + AEAD 加密。支持带宽优化下行（`enable_pure_downlink=false`）与 UoT（UDP over TCP）。

## 安装
- 推荐指定已有 tag：`go get github.com/saba-futai/sudoku@v0.2.0`
- 或者直接跟随最新提交：`go get github.com/saba-futai/sudoku`

## 配置要点
- 表格：`sudoku.NewTable("your-seed", "prefer_ascii"|"prefer_entropy")` 或 `sudoku.NewTableWithCustom("seed", "prefer_entropy", "xpxvvpvv")`（2 个 `x`、2 个 `p`、4 个 `v`，ASCII 优先）。
- 密钥：任意字符串即可，需两端一致，可用 `./sudoku -keygen` 或 `crypto.GenerateMasterKey` 生成。
- AEAD：`chacha20-poly1305`（默认）或 `aes-128-gcm`，`none` 仅测试用。
- 填充：`PaddingMin`/`PaddingMax` 为 0-100 的概率百分比。
- 客户端：设置 `ServerAddress`、`TargetAddress`。
- 链式代理：可选 `ChainHops`（多跳 `host:port` 列表），会对每一跳执行完整握手，最后一跳才连接 `TargetAddress`。
- 服务端：可设置 `HandshakeTimeoutSeconds` 限制握手耗时。

## 客户端示例
```go
package main

import (
	"context"
	"log"
	"time"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func main() {
	table, err := sudoku.NewTableWithCustom("seed-for-table", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		log.Fatal(err)
	}

	cfg := &apis.ProtocolConfig{
		ServerAddress: "1.2.3.4:8443",
		// Optional: ChainHops: []string{"mid.example.com:443", "exit.example.com:443"},
		TargetAddress: "example.com:443",
		Key:           "shared-key-hex-or-plain",
		AEADMethod:    "chacha20-poly1305",
		Table:         table,
		PaddingMin:    5,
		PaddingMax:    15,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := apis.Dial(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	// conn 即已完成握手的隧道，可直接读写应用层数据
}
```

## 服务端示例
```go
package main

import (
	"io"
	"log"
	"net"

	"github.com/saba-futai/sudoku/apis"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

func main() {
	table, err := sudoku.NewTableWithCustom("seed-for-table", "prefer_entropy", "xpxvvpvv")
	if err != nil {
		log.Fatal(err)
	}
	// For rotation, build multiple tables and set cfg.Tables instead of cfg.Table.

	cfg := &apis.ProtocolConfig{
		Key:                     "shared-key-hex-or-plain",
		AEADMethod:              "chacha20-poly1305",
		Table:                   table,
		PaddingMin:              5,
		PaddingMax:              15,
		HandshakeTimeoutSeconds: 5,
	}

	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		log.Fatal(err)
	}
	for {
		rawConn, err := ln.Accept()
		if err != nil {
			log.Println("accept:", err)
			continue
		}
		go func(c net.Conn) {
			defer c.Close()

			tunnel, target, userHash, err := apis.ServerHandshakeWithUserHash(c, cfg)
			if err != nil {
				// 握手失败时可按需 fallback；HandshakeError 携带已读数据
				log.Println("handshake:", err)
				return
			}
			defer tunnel.Close()

			_ = userHash // hex(sha256(privateKey)[:8]) for official split-key clients

			up, err := net.Dial("tcp", target)
			if err != nil {
				log.Println("dial target:", err)
				return
			}
			defer up.Close()

			go io.Copy(up, tunnel)
			io.Copy(tunnel, up)
		}(rawConn)
	}
}
```

## CDN/代理模式（stream / poll）
如需通过 CDN（例如 Cloudflare 小黄云）转发到服务端，设置 `cfg.DisableHTTPMask=false` 且 `cfg.HTTPMaskMode="auto"`（或 `"stream"` / `"poll"`），并在 accept 后使用 `apis.NewHTTPMaskTunnelServer(cfg).HandleConn`：

```go
srv := apis.NewHTTPMaskTunnelServer(cfg)
for {
	rawConn, _ := ln.Accept()
	go func(c net.Conn) {
		defer c.Close()
		tunnel, target, handled, err := srv.HandleConn(c)
		if err != nil || !handled || tunnel == nil {
			return
		}
		defer tunnel.Close()
		_ = target
		io.Copy(tunnel, tunnel)
	}(rawConn)
}
```

## HTTP 连接复用（HTTP/1.1 keep-alive / HTTP/2）
当开启 HTTP mask（`stream` / `poll` / `auto`）时，设置 `HTTPMaskMultiplex="auto"` 会复用底层 HTTP 连接：
- HTTP/1.1：keep-alive 复用连接池
- HTTPS + HTTP/2：同一条 h2 连接可并发承载多条隧道（多路复用）

示例（每次 Dial 仍会做一次 Sudoku 握手，但可复用 TCP/TLS 连接）：
```go
base := &apis.ProtocolConfig{
	ServerAddress:      "your.domain.com:443",
	Key:                "shared-key-hex-or-plain",
	AEADMethod:         "chacha20-poly1305",
	Table:              table,
	PaddingMin:         5,
	PaddingMax:         15,
	EnablePureDownlink: true,
	DisableHTTPMask:    false,
	HTTPMaskMode:       "auto",
	HTTPMaskTLSEnabled: true,
	HTTPMaskMultiplex:  "auto",
}

cfg := *base
cfg.TargetAddress = "example.com:443"
c1, _ := apis.Dial(ctx, &cfg)
defer c1.Close()
```

## 单 tunnel 多目标（MuxClient）
当需要真正省掉后续“建 tunnel/握手”的 RTT（单条隧道内并发多目标连接），使用 `apis.NewMuxClient`：

```go
base := &apis.ProtocolConfig{
	ServerAddress:      "your.domain.com:443",
	Key:                "shared-key-hex-or-plain",
	AEADMethod:         "chacha20-poly1305",
	Table:              table,
	PaddingMin:         5,
	PaddingMax:         15,
	EnablePureDownlink: true,
	// HTTPMask/HTTP tunnel is recommended for CDN/proxy scenarios, but mux itself works on any tunnel.
	DisableHTTPMask: false,
	HTTPMaskMode:    "auto",
	HTTPMaskTLSEnabled: true,
}

mux, _ := apis.NewMuxClient(base)
defer mux.Close()

c1, _ := mux.Dial(ctx, "example.com:443")
defer c1.Close()
```

## 服务端自动识别（Forward / UoT / Mux / Reverse）
当服务端需要对齐 CLI 的全部会话类型（普通转发 / UoT / 单 tunnel 多目标 mux / 反向代理注册），可使用 `ServerHandshakeSessionAutoWithUserHash`：

```go
tunnelConn, session, target, userHash, err := apis.ServerHandshakeSessionAutoWithUserHash(rawConn, cfg)
if err != nil {
	return
}
switch session {
case apis.SessionForward:
	_ = target
case apis.SessionUoT:
	_ = apis.HandleUoT(tunnelConn)
case apis.SessionMux:
	_ = apis.HandleMuxServer(tunnelConn, nil)
case apis.SessionReverse:
	_ = userHash
}
```

## 反向代理（Reverse Proxy over Sudoku）
服务端创建一个 `ReverseManager` 作为 `http.Handler`，并在隧道连接上调用 `HandleServerSession`；客户端使用 `DialBase` + `ServeReverseClientSession` 注册路由并长期保持会话：

```go
mgr := apis.NewReverseManager()
_ = mgr // use as http.Handler

// Server side (after ServerHandshakeSessionAutoWithUserHash returns SessionReverse):
_ = mgr.HandleServerSession(tunnelConn, userHash)

// Client side:
baseConn, _ := apis.DialBase(ctx, cfg)
_ = apis.ServeReverseClientSession(baseConn, "client-id", []apis.ReverseRoute{
	{Path: "/gitea", Target: "127.0.0.1:3000"},
})
```

## 说明
- `DefaultConfig()` 提供合理默认值，仍需设置 `Key`、`Table` 及对应的地址字段。
- 服务端如需回落（HTTP/原始 TCP），可从 `HandshakeError` 取出 `HTTPHeaderData` 与 `ReadData` 按顺序重放。
- 带宽优化模式：将 `enable_pure_downlink` 设为 `false`，需启用 AEAD。
- 如需 UoT，客户端调用 `DialUDPOverTCP`；服务端可用 `ServerHandshakeAuto`（或 `HTTPMaskTunnelServer.HandleConnAuto`）自动区分 TCP/UoT，随后对 UoT 连接调用 `HandleUoT`。
