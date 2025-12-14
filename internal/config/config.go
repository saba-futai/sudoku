// internal/config/config.go
package config

type Config struct {
	Mode               string   `json:"mode"`      // "client" or "server"
	Transport          string   `json:"transport"` // "tcp" or "udp"
	LocalPort          int      `json:"local_port"`
	ServerAddress      string   `json:"server_address"`
	FallbackAddr       string   `json:"fallback_address"`
	Key                string   `json:"key"`
	AEAD               string   `json:"aead"`              // "aes-128-gcm", "chacha20-poly1305", "none"
	SuspiciousAction   string   `json:"suspicious_action"` // "fallback" or "silent"
	PaddingMin         int      `json:"padding_min"`
	PaddingMax         int      `json:"padding_max"`
	RuleURLs           []string `json:"rule_urls"`            // 留空则使用默认，支持 "global", "direct" 关键字
	ProxyMode          string   `json:"proxy_mode"`           // 运行时状态，非JSON字段，由Load解析逻辑填充
	ASCII              string   `json:"ascii"`                // "prefer_entropy" (默认): 低熵, "prefer_ascii": 纯ASCII字符，高熵
	CustomTable        string   `json:"custom_table"`         // 可选，定义 X/P/V 布局，如 "xpxvvpvv"
	CustomTables       []string `json:"custom_tables"`        // 可选，多套 X/P/V 布局轮换
	EnablePureDownlink bool     `json:"enable_pure_downlink"` // 启用纯 Sudoku 下行；false 时使用带宽优化下行编码
	DisableHTTPMask    bool     `json:"disable_http_mask"`
}
