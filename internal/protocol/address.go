package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
)

// AddrType 定义
const (
	AddrTypeIPv4   = 0x01
	AddrTypeDomain = 0x03
	AddrTypeIPv6   = 0x04
)

// ReadAddress 读取 SOCKS5 格式的目标地址
// 返回: 完整地址字符串 (host:port), 地址类型, IP(如果是域名则为nil), error
func ReadAddress(r io.Reader) (string, byte, net.IP, error) {
	buf := make([]byte, 262) // Max domain length + overhead

	// 1. 读取地址类型
	if _, err := io.ReadFull(r, buf[:1]); err != nil {
		return "", 0, nil, err
	}
	addrType := buf[0]

	var host string
	var ip net.IP

	switch addrType {
	case AddrTypeIPv4:
		if _, err := io.ReadFull(r, buf[:4]); err != nil {
			return "", 0, nil, err
		}
		ip = net.IP(buf[:4])
		host = ip.String()
	case AddrTypeDomain:
		if _, err := io.ReadFull(r, buf[:1]); err != nil {
			return "", 0, nil, err
		}
		domainLen := int(buf[0])
		if _, err := io.ReadFull(r, buf[:domainLen]); err != nil {
			return "", 0, nil, err
		}
		host = string(buf[:domainLen])
	case AddrTypeIPv6:
		if _, err := io.ReadFull(r, buf[:16]); err != nil {
			return "", 0, nil, err
		}
		ip = net.IP(buf[:16])
		host = fmt.Sprintf("[%s]", ip.String())
	default:
		return "", 0, nil, fmt.Errorf("unknown address type: %d", addrType)
	}

	// 2. 读取端口
	if _, err := io.ReadFull(r, buf[:2]); err != nil {
		return "", 0, nil, err
	}
	port := binary.BigEndian.Uint16(buf[:2])

	return fmt.Sprintf("%s:%d", host, port), addrType, ip, nil
}

// WriteAddress 将地址写入 Writer (SOCKS5 格式)
// 输入 rawAddr 为 "host:port"
func WriteAddress(w io.Writer, rawAddr string) error {
	host, portStr, err := net.SplitHostPort(rawAddr)
	if err != nil {
		return err
	}
	portInt, _ := net.LookupPort("tcp", portStr)

	ip := net.ParseIP(host)

	// 构建缓冲
	buf := make([]byte, 0, 300)

	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			buf = append(buf, AddrTypeIPv4)
			buf = append(buf, ip4...)
		} else {
			buf = append(buf, AddrTypeIPv6)
			buf = append(buf, ip...)
		}
	} else {
		buf = append(buf, AddrTypeDomain)
		if len(host) > 255 {
			return errors.New("domain too long")
		}
		buf = append(buf, byte(len(host)))
		buf = append(buf, []byte(host)...)
	}

	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(portInt))
	buf = append(buf, portBytes...)

	_, err = w.Write(buf)
	return err
}
