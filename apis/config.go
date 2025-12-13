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
	"fmt"

	"github.com/saba-futai/sudoku/pkg/obfs/sudoku"
)

// ProtocolConfig 定义了 Sudoku 协议栈所需的所有参数
//
// Sudoku 协议是一个多层的加密隧道协议：
//  1. HTTP 伪装层：伪装成 HTTP POST 请求
//  2. Sudoku 混淆层：使用数独谜题编码混淆流量特征
//  3. AEAD 加密层：提供机密性和完整性保护
//  4. 协议层：处理握手、地址传输等
type ProtocolConfig struct {
	// ============ 基础连接信息 ============

	// ServerAddress 服务器地址 (仅客户端使用)
	// 格式: "host:port" 或 "ip:port"
	// 例如: "example.com:443" 或 "1.2.3.4:8080"
	ServerAddress string

	// ============ 加密与混淆 ============

	// Key 预共享密钥，用于 AEAD 加密
	// 字符串两端一致即可；可直接使用 "./sudoku -keygen" 生成的密钥字符串或自行约定共享密钥
	Key string

	// AEADMethod 指定使用的 AEAD 加密算法
	// 有效值:
	//   - "aes-128-gcm": AES-128-GCM (较快，硬件加速支持好)
	//   - "chacha20-poly1305": ChaCha20-Poly1305 (纯软件实现性能好)
	//   - "none": 不加密 (仅用于测试，生产环境禁用)
	AEADMethod string

	// Table Sudoku 编码映射表 (客户端和服务端必须相同)
	// 使用 sudoku.NewTable(seed, "prefer_ascii"|"prefer_entropy") 或
	// sudoku.NewTableWithCustom(seed, "prefer_entropy", "<xpxvvpvv>") 创建
	// 不能为 nil
	Table *sudoku.Table

	// Tables is an optional candidate set for table rotation.
	// If provided (len>0), the client will pick one table per connection and the server will
	// probe the handshake to detect which one was used, keeping the handshake format unchanged.
	// When Tables is set, Table may be nil.
	Tables []*sudoku.Table

	// ============ Sudoku 填充参数 ============

	// PaddingMin 最小填充率 (0-100)
	// 在编码时随机插入填充字节的最小概率百分比
	PaddingMin int

	// PaddingMax 最大填充率 (0-100)
	// 在编码时随机插入填充字节的最大概率百分比
	// 必须 >= PaddingMin
	PaddingMax int

	// EnablePureDownlink 是否保持纯 Sudoku 下行
	// false 时启用带宽优化的 6bit 拆分下行，要求 AEAD 启用
	EnablePureDownlink bool

	// ============ 客户端特有字段 ============

	// TargetAddress 客户端想要访问的最终目标地址 (仅客户端使用)
	// 格式: "host:port"
	// 例如: "google.com:443" 或 "1.1.1.1:53"
	TargetAddress string

	// ============ 服务端特有字段 ============

	// HandshakeTimeoutSeconds 握手超时时间（秒）(仅服务端使用)
	// 推荐值: 5-10
	// 设置过小可能导致慢速网络握手失败
	// 设置过大可能使服务器容易受到慢速攻击
	HandshakeTimeoutSeconds int

	// ============ 通用开关 ============

	// DisableHTTPMask 是否禁用 HTTP 伪装层
	// 默认 false (启用伪装)
	// 如果为 true，客户端不发送伪装头，服务端也不检测伪装头
	// 注意：服务端支持自动检测，即使此项为 false，也能处理不带伪装头的客户端（前提是首字节不匹配 POST）
	DisableHTTPMask bool
}

// Validate 验证配置的有效性
// 返回第一个发现的错误，如果配置有效则返回 nil
func (c *ProtocolConfig) Validate() error {
	if c.Table == nil && len(c.Tables) == 0 {
		return fmt.Errorf("Table cannot be nil (or provide Tables)")
	}
	for i, t := range c.Tables {
		if t == nil {
			return fmt.Errorf("Tables[%d] cannot be nil", i)
		}
	}

	if c.Key == "" {
		return fmt.Errorf("Key cannot be empty")
	}

	switch c.AEADMethod {
	case "aes-128-gcm", "chacha20-poly1305", "none":
		// 有效值
	default:
		return fmt.Errorf("invalid AEADMethod: %s, must be one of: aes-128-gcm, chacha20-poly1305, none", c.AEADMethod)
	}

	if c.PaddingMin < 0 || c.PaddingMin > 100 {
		return fmt.Errorf("PaddingMin must be between 0 and 100, got %d", c.PaddingMin)
	}

	if c.PaddingMax < 0 || c.PaddingMax > 100 {
		return fmt.Errorf("PaddingMax must be between 0 and 100, got %d", c.PaddingMax)
	}

	if c.PaddingMax < c.PaddingMin {
		return fmt.Errorf("PaddingMax (%d) must be >= PaddingMin (%d)", c.PaddingMax, c.PaddingMin)
	}

	if !c.EnablePureDownlink && c.AEADMethod == "none" {
		return fmt.Errorf("bandwidth optimized downlink requires AEAD")
	}

	if c.HandshakeTimeoutSeconds < 0 {
		return fmt.Errorf("HandshakeTimeoutSeconds must be >= 0, got %d", c.HandshakeTimeoutSeconds)
	}

	return nil
}

// ValidateClient ensures the config carries the required client-side fields.
func (c *ProtocolConfig) ValidateClient() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.ServerAddress == "" {
		return fmt.Errorf("ServerAddress cannot be empty")
	}
	if c.TargetAddress == "" {
		return fmt.Errorf("TargetAddress cannot be empty")
	}
	return nil
}

// DefaultConfig 返回一个安全的默认配置
// 注意：返回的配置仍需设置 Key、Table、ServerAddress (客户端) 或 TargetAddress (服务端)
func DefaultConfig() *ProtocolConfig {
	return &ProtocolConfig{
		AEADMethod:              "chacha20-poly1305",
		PaddingMin:              10,
		PaddingMax:              30,
		EnablePureDownlink:      true,
		HandshakeTimeoutSeconds: 5,
	}
}

func (c *ProtocolConfig) tableCandidates() []*sudoku.Table {
	if c == nil {
		return nil
	}
	if len(c.Tables) > 0 {
		return c.Tables
	}
	if c.Table != nil {
		return []*sudoku.Table{c.Table}
	}
	return nil
}
