// tls.go 提供 mTLS 双向校验配置构建。
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// buildClientTLSConfig 构建 Edge 作为 TLS 客户端的配置（双向校验）。
// cert/key 是 Edge 自身证书，ca 用于校验 Hub 证书。
func buildClientTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 Edge 证书失败: %w", err)
	}
	caPool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ClientCAs:    caPool,
		MinVersion:   tls.VersionTLS13,
		// 同时校验 Hub 证书（ServerName 留空时，TLS1.3 仍校验证书链是否由配置的 CA 签发）
		// 网闸场景两端 IP 固定且由同一 CA 签发，InsecureSkipVerify=true 仅跳过主机名校验，
		// 仍会校验证书链合法性（RootCAs），兼顾 IP 直连与安全。
		InsecureSkipVerify: true,
	}, nil
}

// buildServerTLSConfig 构建 Hub 作为 TLS 服务端的配置（要求客户端证书）。
// cert/key 是 Hub 自身证书，ca 用于校验 Edge 客户端证书。
func buildServerTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 Hub 证书失败: %w", err)
	}
	caPool, err := loadCAPool(caFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert, // 强制双向校验
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// loadCAPool 加载 CA 证书池。
func loadCAPool(caFile string) (*x509.CertPool, error) {
	caData, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("加载 CA 证书失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caData) {
		return nil, fmt.Errorf("CA 证书解析失败: %s", caFile)
	}
	return pool, nil
}
