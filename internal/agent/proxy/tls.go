// tls.go 提供 mTLS 双向校验配置构建。
package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// buildClientTLSConfig 构建 Edge 作为 TLS 客户端的配置（双向校验）。
// cert/key 是 Edge 自身证书，ca 用于校验 Hub 证书。
//
// 注意 Go 的语义：InsecureSkipVerify=true 会让 crypto/tls 完全跳过证书校验，
// RootCAs 不再参与——等价于接受任何证书（此前的实现正是踩了这个坑）。
// 网闸场景按 IP 直连、证书 CN 不含 IP，无法做主机名校验；因此这里保留
// InsecureSkipVerify 只为跳过主机名，证书链校验在 VerifyPeerCertificate 中
// 手动按配置的 CA 完成。
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
		MinVersion:   tls.VersionTLS13,
		//nolint:gosec // 仅跳过主机名校验；证书链校验见 VerifyPeerCertificate
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("对端未出示证书")
			}
			certs := make([]*x509.Certificate, len(rawCerts))
			for i, raw := range rawCerts {
				c, err := x509.ParseCertificate(raw)
				if err != nil {
					return fmt.Errorf("解析对端证书失败: %w", err)
				}
				certs[i] = c
			}
			opts := x509.VerifyOptions{Roots: caPool, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			if _, err := certs[0].Verify(opts); err != nil {
				return fmt.Errorf("对端证书链校验失败: %w", err)
			}
			return nil
		},
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
