# Sudoku API (Standard)

面向其他开发者开放的纯 Sudoku 协议 API：HTTP 伪装 + 数独 ASCII/Entropy 混淆 + AEAD 加密。支持带宽优化下行（`enable_pure_downlink=false`）与 UoT（UDP over TCP）。

## 安装
- 推荐指定已有 tag：`go get github.com/saba-futai/sudoku@v0.1.0`
- 或者直接跟随最新提交：`go get github.com/saba-futai/sudoku`

## 配置要点
- 表格：`sudoku.NewTable("your-seed", "prefer_ascii"|"prefer_entropy")` 或 `sudoku.NewTableWithCustom("seed", "prefer_entropy", "xpxvvpvv")`（2 个 `x`、2 个 `p`、4 个 `v`，ASCII 优先）。
- 密钥：任意字符串即可，需两端一致，可用 `./sudoku -keygen` 或 `crypto.GenerateMasterKey` 生成。
- AEAD：`chacha20-poly1305`（默认）或 `aes-128-gcm`，`none` 仅测试用。
- 填充：`PaddingMin`/`PaddingMax` 为 0-100 的概率百分比。
- 客户端：设置 `ServerAddress`、`TargetAddress`。
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

			_ = userHash // hex(sha256(privateKey)[1:8]) for official split-key clients

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

## 多路复用（降低后续连接 RTT）
当开启 HTTP mask（尤其是 `stream`/`poll`）时，可通过多路复用复用单条隧道连接，后续每次 Dial 仅创建逻辑流（减少 RTT 与连接抖动）。

客户端（建立一条复用会话，然后多次 Dial）：
```go
sess, err := apis.DialMultiplex(ctx, &apis.ProtocolConfig{
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
})
if err != nil {
	log.Fatal(err)
}
defer sess.Close()

c1, _ := sess.Dial(ctx, "example.com:443")
defer c1.Close()
```

服务端（握手后判断是否为复用连接；复用流内仍使用目标地址前置帧）：
```go
tunnel, userHash, fail, err := apis.ServerHandshakeFlexibleWithUserHash(rawConn, cfg)
if err != nil {
	log.Println(fail(err))
	return
}

peek := make([]byte, 1)
if _, err := io.ReadFull(tunnel, peek); err != nil {
	_ = tunnel.Close()
	return
}

if peek[0] == apis.MultiplexMagicByte {
	mux, err := apis.AcceptMultiplexServer(tunnel)
	if err != nil {
		_ = tunnel.Close()
		return
	}
	defer mux.Close()

	for {
		stream, target, err := mux.AcceptTCP()
		if err != nil {
			return
		}
		go func(s net.Conn, dst string) {
			defer s.Close()
			_ = userHash
			// dial dst, then io.Copy both ways...
		}(stream, target)
	}
}

// 非复用：把预读的 1 字节放回去，再按旧流程读 target
tuned := apis.NewPreBufferedConn(tunnel, peek)
target, err := apis.ReadTargetAddress(tuned)
if err != nil {
	_ = tunnel.Close()
	return
}
_ = target
```

## 说明
- `DefaultConfig()` 提供合理默认值，仍需设置 `Key`、`Table` 及对应的地址字段。
- 服务端如需回落（HTTP/原始 TCP），可从 `HandshakeError` 取出 `HTTPHeaderData` 与 `ReadData` 按顺序重放。
- 带宽优化模式：将 `enable_pure_downlink` 设为 `false`，需启用 AEAD。
- 如需 UoT，客户端调用 `DialUDPOverTCP`；服务端可用 `ServerHandshakeAuto`（或 `HTTPMaskTunnelServer.HandleConnAuto`）自动区分 TCP/UoT，随后对 UoT 连接调用 `HandleUoT`。
