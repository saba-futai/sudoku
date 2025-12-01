/*
Copyright (C) 2025 by ふたい <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package apis

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/saba-futai/sudoku/internal/protocol"
	"github.com/saba-futai/sudoku/pkg/crypto"
	"github.com/saba-futai/sudoku/pkg/dnsutil"
	"github.com/saba-futai/sudoku/pkg/obfs/httpmask"
	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// Dial 建立一条到 Sudoku 服务器的隧道，并请求连接到 cfg.TargetAddress
//
// 参数:
//   - ctx: 用于控制连接建立的上下文（可以设置超时或取消）
//   - cfg: 协议配置，必须包含 Table、Key、ServerAddress、TargetAddress 等字段
//
// 返回值:
//   - net.Conn: 已经完成握手的加密隧道连接，可直接用于应用层数据传输
//   - error: 任何阶段失败都会返回错误
//
// 协议流程:
//  1. 建立到服务器的 TCP 连接
//  2. 发送 HTTP POST 伪装头
//  3. 包装 Sudoku 混淆层
//  4. 包装 AEAD 加密层
//  5. 发送握手数据（时间戳 + 随机数）
//  6. 发送目标地址
//
// 错误条件:
//   - TCP 连接失败
//   - 配置参数无效 (Table 为 nil 等)
//   - 写入 HTTP 伪装头失败
//   - 加密层初始化失败
//   - 握手数据发送失败
//   - 目标地址发送失败
//
// 使用示例:
//
//	cfg := &ProtocolConfig{
//	    ServerAddress: "0.0.0.0:8443",
//	    TargetAddress: "google.com:443",
//	    Key:           "my-secret-key",
//	    AEADMethod:    "chacha20-poly1305",
//	    Table:         sudoku.NewTable("my-seed", "prefer_ascii"),
//	    PaddingMin:    10,
//	    PaddingMax:    30,
//	}
//
//	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
//	defer cancel()
//
//	conn, err := apis.Dial(ctx, cfg)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer conn.Close()
//
//	// 现在可以直接使用 conn 进行读写
//	conn.Write([]byte("Hello"))
func buildHandshakePayload(key string) [16]byte {
	var payload [16]byte
	binary.BigEndian.PutUint64(payload[:8], uint64(time.Now().Unix()))
	hash := sha256.Sum256([]byte(key))
	copy(payload[8:], hash[:8])
	return payload
}

func wrapClientConn(rawConn net.Conn, cfg *ProtocolConfig) (net.Conn, error) {
	sConn := sudoku.NewConn(rawConn, cfg.Table, cfg.PaddingMin, cfg.PaddingMax, false)
	seed := cfg.Key
	if recoveredFromKey, err := crypto.RecoverPublicKey(cfg.Key); err == nil {
		seed = crypto.EncodePoint(recoveredFromKey)
	}
	cConn, err := crypto.NewAEADConn(sConn, seed, cfg.AEADMethod)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("setup crypto failed: %w", err)
	}
	return cConn, nil
}

func Dial(ctx context.Context, cfg *ProtocolConfig) (net.Conn, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := cfg.ValidateClient(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Resolve server address with DNS concurrency and optimistic cache.
	resolvedAddr, err := dnsutil.ResolveWithCache(ctx, cfg.ServerAddress)
	if err != nil {
		return nil, fmt.Errorf("resolve server address failed: %w", err)
	}

	var d net.Dialer
	// 1. 建立 TCP 连接
	rawConn, err := d.DialContext(ctx, "tcp", resolvedAddr)
	if err != nil {
		return nil, fmt.Errorf("dial tcp failed: %w", err)
	}

	// 遇到错误时确保关闭底层连接
	success := false
	defer func() {
		if !success {
			rawConn.Close()
		}
	}()

	// 2. 写入 HTTP POST 伪装头
	// 这层不在 Sudoku 编码内，是最外层的伪装
	if !cfg.DisableHTTPMask {
		if err := httpmask.WriteRandomRequestHeader(rawConn, cfg.ServerAddress); err != nil {
			return nil, fmt.Errorf("write http mask failed: %w", err)
		}
	}

	// 3. 包装 Sudoku 协议层
	// 所有写入 sConn 的数据都会被编码为 Sudoku 谜题
	cConn, err := wrapClientConn(rawConn, cfg)
	if err != nil {
		return nil, err
	}

	// 5. 内部握手 (Tunnel 协议)
	// 发送时间戳 (8 bytes) + 用户认证 (8 bytes) 防止重放
	handshake := buildHandshakePayload(cfg.Key)
	// 注意：这里直接写入 cConn，数据流向：
	// Handshake -> [AEAD Encrypt] -> [Sudoku Encode] -> [HTTP Body] -> Network
	if _, err := cConn.Write(handshake[:]); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("send handshake failed: %w", err)
	}

	// 6. 发送目标地址
	if err := protocol.WriteAddress(cConn, cfg.TargetAddress); err != nil {
		cConn.Close()
		return nil, fmt.Errorf("send target address failed: %w", err)
	}

	success = true
	return cConn, nil
}
