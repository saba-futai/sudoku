// internal/hybrid/manager.go
package hybrid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/enfein/mieru/v3/apis/model"
	"github.com/saba-futai/sudoku/internal/config"

	mieruClient "github.com/enfein/mieru/v3/apis/client"
	mieruServer "github.com/enfein/mieru/v3/apis/server"

	"github.com/enfein/mieru/v3/pkg/appctl/appctlpb"
)

const (
	BindingDomainSuffix = ".sudoku-binding"
	BindingTimeout      = 10 * time.Second
)

// === 辅助函数：解决 Protobuf 指针类型不匹配问题 ===
func toPtr[T any](v T) *T {
	return &v
}

// SplitConn 实现上下行分离的 net.Conn
type SplitConn struct {
	net.Conn              // 嵌入接口以满足其他方法
	Reader   net.Conn     // 下行 (Mieru)
	Writer   net.Conn     // 上行 (Sudoku)
	CloseFn  func() error // 关闭两个连接
}

func (s *SplitConn) Read(b []byte) (n int, err error) {
	if s.Reader == nil {
		return 0, errors.New("reader connection is nil")
	}
	return s.Reader.Read(b)
}

func (s *SplitConn) Write(b []byte) (n int, err error) {
	if s.Writer == nil {
		return 0, errors.New("writer connection is nil")
	}
	return s.Writer.Write(b)
}

func (s *SplitConn) Close() error {
	if s.CloseFn != nil {
		return s.CloseFn()
	}
	return nil
}

// Manager 管理 Mieru 实例和会话配对
type Manager struct {
	cfg      *config.Config
	mieruCli mieruClient.Client
	mieruSrv mieruServer.Server
	pending  sync.Map // map[string]chan net.Conn (UUID -> MieruConn Channel)
}

var instance *Manager
var once sync.Once

func GetInstance(cfg *config.Config) *Manager {
	once.Do(func() {
		instance = &Manager{
			cfg: cfg,
		}
	})
	// Update config for tests or reload
	instance.cfg = cfg
	return instance
}

// === Client Side ===

func (m *Manager) StartMieruClient() error {
	if !m.cfg.EnableMieru {
		return nil
	}

	mc := m.cfg.MieruConfig

	// 1. 转换 Transport 字符串为 Protobuf Enum
	var transportProto appctlpb.TransportProtocol
	switch strings.ToUpper(mc.Transport) {
	case "UDP":
		transportProto = appctlpb.TransportProtocol_UDP
	default:
		transportProto = appctlpb.TransportProtocol_TCP
	}

	// 2. 转换 Multiplexing Level
	var muxLevel appctlpb.MultiplexingLevel
	switch strings.ToUpper(mc.Multiplexing) {
	case "LOW":
		muxLevel = appctlpb.MultiplexingLevel_MULTIPLEXING_LOW
	case "MIDDLE":
		muxLevel = appctlpb.MultiplexingLevel_MULTIPLEXING_MIDDLE
	default:
		muxLevel = appctlpb.MultiplexingLevel_MULTIPLEXING_HIGH
	}

	// 构造 Mieru Client Profile
	profile := &appctlpb.ClientProfile{
		ProfileName: toPtr("sudoku_hybrid"),
		User: &appctlpb.User{
			Name:     toPtr(mc.Username),
			Password: toPtr(mc.Password),
		},
		Servers: []*appctlpb.ServerEndpoint{
			{
				IpAddress: toPtr(strings.Split(m.cfg.ServerAddress, ":")[0]), // Sudoku 的服务器 IP
				PortBindings: []*appctlpb.PortBinding{
					{
						Port:     toPtr(int32(mc.Port)),
						Protocol: toPtr(transportProto),
					},
				},
			},
		},
		Mtu: toPtr(int32(mc.MTU)),
		Multiplexing: &appctlpb.MultiplexingConfig{
			Level: toPtr(muxLevel),
		},
		// HandshakeMode 也需要指针
		HandshakeMode: toPtr(appctlpb.HandshakeMode_HANDSHAKE_NO_WAIT),
	}

	m.mieruCli = mieruClient.NewClient()
	clientCfg := &mieruClient.ClientConfig{
		Profile: profile,
	}

	if err := m.mieruCli.Store(clientCfg); err != nil {
		return fmt.Errorf("store mieru config fail: %v", err)
	}
	if err := m.mieruCli.Start(); err != nil {
		return fmt.Errorf("start mieru client fail: %v", err)
	}
	log.Printf("[Mieru] Client started, pairing with Sudoku Uplink")
	return nil
}

// DialMieruForDownlink 建立 Mieru 连接并发送绑定请求
func (m *Manager) DialMieruForDownlink(uuid string) (net.Conn, error) {
	if m.mieruCli == nil {
		return nil, errors.New("mieru client not initialized")
	}

	// 使用 UUID 构造一个伪造的域名，服务端通过这个域名识别并提取 UUID
	fakeAddr := fmt.Sprintf("%s%s:80", uuid, BindingDomainSuffix)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 调用 Mieru 的 Dial
	conn, err := m.mieruCli.DialContext(ctx, &NetAddr{NetworkStr: "tcp", AddrStr: fakeAddr})
	if err != nil {
		return nil, err
	}

	// 写入少量数据触发 SOCKS5 请求发送（对于 EarlyConn 模式）
	conn.Write([]byte("BIND"))

	return conn, nil
}

// === Server Side ===

func (m *Manager) StartMieruServer() error {
	if !m.cfg.EnableMieru {
		return nil
	}

	mc := m.cfg.MieruConfig

	// 转换 Transport
	var transportProto appctlpb.TransportProtocol
	switch strings.ToUpper(mc.Transport) {
	case "UDP":
		transportProto = appctlpb.TransportProtocol_UDP
	default:
		transportProto = appctlpb.TransportProtocol_TCP
	}

	// 构造 Server Config
	srvCfgPB := &appctlpb.ServerConfig{
		PortBindings: []*appctlpb.PortBinding{
			{
				Port:     toPtr(int32(mc.Port)),
				Protocol: toPtr(transportProto),
			},
		},
		Users: []*appctlpb.User{
			{
				Name:     toPtr(mc.Username),
				Password: toPtr(mc.Password),
			},
		},
		Mtu: toPtr(int32(mc.MTU)),
	}

	m.mieruSrv = mieruServer.NewServer()
	if err := m.mieruSrv.Store(&mieruServer.ServerConfig{Config: srvCfgPB}); err != nil {
		return err
	}
	if err := m.mieruSrv.Start(); err != nil {
		return err
	}

	log.Printf("[Mieru] Server listening on port %d (%s)", mc.Port, mc.Transport)

	// 启动接收循环
	go m.acceptLoop()
	return nil
}

func (m *Manager) acceptLoop() {
	for m.mieruSrv.IsRunning() {
		conn, req, err := m.mieruSrv.Accept()
		if err != nil {
			if !m.mieruSrv.IsRunning() {
				return
			}
			log.Printf("[Mieru] Accept error: %v", err)
			time.Sleep(time.Second)
			continue
		}

		// 异步处理每个连接
		go func(c net.Conn, r *model.Request) {
			// 获取目标地址 (UUID)
			dst := req.DstAddr.FQDN

			if strings.HasSuffix(dst, BindingDomainSuffix) {
				uuid := strings.TrimSuffix(dst, BindingDomainSuffix)

				// Mieru Client (EarlyConn) 在 Write 数据前会等待这个包
				// 0x05(Ver) 0x00(Success) 0x00(RSV) 0x01(IPv4) ... 0x00(Port)
				successResp := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
				if _, err := c.Write(successResp); err != nil {
					log.Printf("[Hybrid] Failed to send handshake resp: %v", err)
					c.Close()
					return
				}

				// 使用 LoadOrStore 避免轮询
				ch := make(chan net.Conn, 1)
				actual, _ := m.pending.LoadOrStore(uuid, ch)
				targetCh, ok := actual.(chan net.Conn)
				if !ok {
					log.Printf("[Hybrid] Type assertion failed for UUID: %s", uuid)
					c.Close()
					return
				}

				select {
				case targetCh <- c:
					log.Printf("[Hybrid] Paired Mieru downlink for UUID: %s", uuid)
				case <-time.After(5 * time.Second):
					log.Printf("[Hybrid] Timeout handing over conn for UUID: %s", uuid)
					c.Close()
					// 如果超时，尝试清理 map (虽然 RegisterSudokuConn 也会清理)
					m.pending.Delete(uuid)
				}

			} else {
				c.Close()
			}
		}(conn, req)
	}
}

// RegisterSudokuConn 注册一个 Sudoku 上行连接，等待 Mieru 下行连接
func (m *Manager) RegisterSudokuConn(uuid string) (net.Conn, error) {
	ch := make(chan net.Conn, 1)
	actual, _ := m.pending.LoadOrStore(uuid, ch)
	targetCh, ok := actual.(chan net.Conn)
	if !ok {
		return nil, fmt.Errorf("type assertion failed for UUID: %s", uuid)
	}

	defer m.pending.Delete(uuid)

	select {
	case mConn := <-targetCh:
		return mConn, nil
	case <-time.After(BindingTimeout):
		return nil, fmt.Errorf("timeout waiting for mieru downlink")
	}
}

func GenerateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// 辅助类型实现 net.Addr 接口
type NetAddr struct {
	NetworkStr string
	AddrStr    string
}

func (n *NetAddr) Network() string { return n.NetworkStr }
func (n *NetAddr) String() string  { return n.AddrStr }
