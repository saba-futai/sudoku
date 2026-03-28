package dnsutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestTLSNameServer_LookupIP(t *testing.T) {
	prevTLSConfig := newDOTTLSConfig
	newDOTTLSConfig = func(serverName string) *tls.Config {
		return &tls.Config{
			ServerName:         serverName,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: true,
		}
	}
	t.Cleanup(func() {
		newDOTTLSConfig = prevTLSConfig
	})

	cert := generateTestTLSCert(t, "localhost")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listen tls: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveTestDoTConn(conn)
		}
	}()

	ns, err := newTLSNameServer(ServerOptions{
		Type:      "dot",
		Address:   "localhost:" + portOfAddr(t, ln.Addr().String()),
		Bootstrap: []string{"127.0.0.1"},
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("newTLSNameServer: %v", err)
	}

	ips, err := ns.LookupIP(context.Background(), "ip4", "example.com")
	if err != nil {
		t.Fatalf("LookupIP: %v", err)
	}
	if len(ips) != 1 || ips[0].String() != "1.2.3.4" {
		t.Fatalf("unexpected ips: %v", ips)
	}

	_ = ln.Close()
	<-done
}

func TestRecommendedClientOptions_PrefersAliDoH(t *testing.T) {
	opts := RecommendedClientOptions()
	if len(opts.Servers) == 0 {
		t.Fatalf("expected default servers")
	}
	first := opts.Servers[0]
	if got := normalizeServerType(first.Type); got != "https" {
		t.Fatalf("first server type = %q, want %q", got, "https")
	}
	if first.Address != "dns.alidns.com" {
		t.Fatalf("first server address = %q", first.Address)
	}
	if first.Path != "/dns-query" {
		t.Fatalf("first server path = %q", first.Path)
	}
	if len(first.Bootstrap) != 2 || first.Bootstrap[0] != "223.5.5.5" || first.Bootstrap[1] != "223.6.6.6" {
		t.Fatalf("unexpected bootstrap: %v", first.Bootstrap)
	}
}

func serveTestDoTConn(conn net.Conn) {
	defer conn.Close()

	var prefix [2]byte
	if _, err := io.ReadFull(conn, prefix[:]); err != nil {
		return
	}
	queryLen := int(binary.BigEndian.Uint16(prefix[:]))
	if queryLen <= 0 {
		return
	}
	query := make([]byte, queryLen)
	if _, err := io.ReadFull(conn, query); err != nil {
		return
	}
	resp, err := buildTestDNSResponse(query, net.ParseIP("1.2.3.4"))
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(prefix[:], uint16(len(resp)))
	if _, err := conn.Write(prefix[:]); err != nil {
		return
	}
	_, _ = conn.Write(resp)
}

func buildTestDNSResponse(query []byte, ip net.IP) ([]byte, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, io.ErrUnexpectedEOF
	}
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := parser.Question()
	if err != nil {
		return nil, err
	}

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		RecursionDesired:   true,
		RecursionAvailable: true,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(q); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	if err := builder.AResource(dnsmessage.ResourceHeader{
		Name:  q.Name,
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
		TTL:   60,
	}, dnsmessage.AResource{A: [4]byte{ip4[0], ip4[1], ip4[2], ip4[3]}}); err != nil {
		return nil, err
	}
	return builder.Finish()
}

func generateTestTLSCert(t *testing.T, dnsName string) tls.Certificate {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: dnsName,
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{dnsName},
	}

	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("load key pair: %v", err)
	}
	return cert
}

func portOfAddr(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	return port
}
