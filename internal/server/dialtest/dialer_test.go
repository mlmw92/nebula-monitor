package dialtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

func TestDialICMPUsesICMPProbe(t *testing.T) {
	var gotHost string
	var gotTimeout time.Duration
	d := &Dialer{icmpProbe: func(host string, timeout time.Duration) (time.Duration, error) {
		gotHost, gotTimeout = host, timeout
		return 12*time.Millisecond + 345*time.Microsecond, nil
	}}

	result := d.Run(Task{ID: "ping", Type: TaskTypeICMP, Target: "192.0.2.1", Timeout: 3})
	if !result.Up || result.Latency != 12.35 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if gotHost != "192.0.2.1" || gotTimeout != 3*time.Second {
		t.Fatalf("probe args = %q, %s", gotHost, gotTimeout)
	}
}

func TestProbeICMPPermissionErrorIsExplicit(t *testing.T) {
	oldResolve, oldListen := resolveICMPAddr, listenICMPPacket
	defer func() { resolveICMPAddr, listenICMPPacket = oldResolve, oldListen }()
	resolveICMPAddr = func(string, string) (*net.IPAddr, error) { return &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, nil }
	listenICMPPacket = func(string, string) (icmpPacketConn, error) { return nil, errors.New("permission denied") }
	_, err := probeICMP("127.0.0.1", time.Second)
	if err == nil || !strings.Contains(err.Error(), "ICMP 探测不可执行") {
		t.Fatalf("unexpected permission error: %v", err)
	}
}

func TestProbeICMPBuildsIPv4EchoAndClosesSocket(t *testing.T) {
	oldResolve, oldListen := resolveICMPAddr, listenICMPPacket
	defer func() { resolveICMPAddr, listenICMPPacket = oldResolve, oldListen }()
	resolveICMPAddr = func(string, string) (*net.IPAddr, error) { return &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, nil }
	fake := &fakeICMPConn{}
	listenICMPPacket = func(network, address string) (icmpPacketConn, error) {
		if network != "ip4:icmp" || address != "" {
			t.Fatalf("listen %q %q", network, address)
		}
		return fake, nil
	}
	if _, err := probeICMP("127.0.0.1", time.Second); err != nil {
		t.Fatal(err)
	}
	if !fake.closed {
		t.Fatal("ICMP socket was not closed")
	}
	message, err := icmp.ParseMessage(1, fake.written)
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != ipv4.ICMPTypeEcho {
		t.Fatalf("sent type = %v", message.Type)
	}
}

func TestProbeICMPBuildsIPv6Echo(t *testing.T) {
	oldResolve, oldListen := resolveICMPAddr, listenICMPPacket
	defer func() { resolveICMPAddr, listenICMPPacket = oldResolve, oldListen }()
	resolveICMPAddr = func(string, string) (*net.IPAddr, error) { return &net.IPAddr{IP: net.ParseIP("::1")}, nil }
	fake := &fakeICMPConn{protocol: 58, replyType: ipv6.ICMPTypeEchoReply}
	listenICMPPacket = func(network, address string) (icmpPacketConn, error) {
		if network != "ip6:ipv6-icmp" || address != "" {
			t.Fatalf("listen %q %q", network, address)
		}
		return fake, nil
	}
	if _, err := probeICMP("::1", time.Second); err != nil {
		t.Fatal(err)
	}
	message, err := icmp.ParseMessage(58, fake.written)
	if err != nil {
		t.Fatal(err)
	}
	if message.Type != ipv6.ICMPTypeEchoRequest {
		t.Fatalf("sent type = %v", message.Type)
	}
}

type fakeICMPConn struct {
	written   []byte
	closed    bool
	protocol  int
	replyType icmp.Type
}

func (f *fakeICMPConn) SetDeadline(time.Time) error { return nil }
func (f *fakeICMPConn) WriteTo(data []byte, _ net.Addr) (int, error) {
	f.written = append([]byte(nil), data...)
	return len(data), nil
}
func (f *fakeICMPConn) ReadFrom(buf []byte) (int, net.Addr, error) {
	protocol := f.protocol
	if protocol == 0 {
		protocol = 1
	}
	request, err := icmp.ParseMessage(protocol, f.written)
	if err != nil {
		return 0, nil, err
	}
	echo := request.Body.(*icmp.Echo)
	replyType := f.replyType
	if replyType == nil {
		replyType = ipv4.ICMPTypeEchoReply
	}
	reply, err := (&icmp.Message{Type: replyType, Body: &icmp.Echo{ID: echo.ID, Seq: echo.Seq}}).Marshal(nil)
	if err != nil {
		return 0, nil, err
	}
	return copy(buf, reply), &net.IPAddr{IP: net.ParseIP("127.0.0.1")}, nil
}
func (f *fakeICMPConn) Close() error { f.closed = true; return nil }

func TestHTTPSExpiredCertificateStillProducesExpiryMetric(t *testing.T) {
	cert := expiredCertificate(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	pool := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool.AddCert(parsed)
	dialer := NewDialer()
	dialer.tlsRoots = pool
	task := Task{ID: "expired", Name: "expired", Type: TaskTypeHTTPS, Target: strings.TrimPrefix(srv.URL, "https://"), Timeout: 2}
	result := dialer.Run(task)
	if result.Up || result.CertNotAfter == 0 || result.CertExpiry >= 0 {
		t.Fatalf("expired certificate result = %+v", result)
	}
	metrics := ResultToMetrics(result, task, 123)
	for _, metric := range metrics {
		if metric.Name == "dial_test_cert_expiry" {
			if metric.Value >= 0 {
				t.Fatalf("expiry metric must be negative: %+v", metric)
			}
			return
		}
	}
	t.Fatalf("missing expiry metric: %+v", metrics)
}

func TestHTTPSRecentlyExpiredCertificateStaysNegative(t *testing.T) {
	now := time.Now()
	cert := certificateForPeriod(t, now.Add(-time.Hour), now.Add(-2*time.Minute))
	result := runTLSTest(t, cert, "127.0.0.1")
	if result.CertNotAfter == 0 || result.CertExpiry >= 0 {
		t.Fatalf("recently expired certificate must remain negative: %+v", result)
	}
}

func TestHTTPSWrongHostnameRemainsRejected(t *testing.T) {
	cert := expiredCertificate(t)
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	d := NewDialer()
	d.tlsRoots = pool
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	target := net.JoinHostPort("localhost", port)
	result := d.Run(Task{ID: "wrong-host", Type: TaskTypeHTTPS, Target: target, Timeout: 2})
	if result.Up || result.CertNotAfter != 0 {
		t.Fatalf("wrong hostname must remain rejected: %+v", result)
	}
}

func runTLSTest(t *testing.T, cert tls.Certificate, host string) Result {
	t.Helper()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return runTLSWithRoots(t, cert, pool, host)
}

func runTLSWithRoots(t *testing.T, cert tls.Certificate, roots *x509.CertPool, host string) Result {
	t.Helper()
	d := NewDialer()
	d.tlsRoots = roots
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	return d.Run(Task{ID: "tls", Type: TaskTypeHTTPS, Target: net.JoinHostPort(host, port), Timeout: 2})
}

func TestHTTPSFutureCertificateRemainsRejected(t *testing.T) {
	now := time.Now()
	cert := certificateForPeriod(t, now.Add(24*time.Hour), now.Add(48*time.Hour))
	result := runTLSTest(t, cert, "127.0.0.1")
	if result.Up || result.CertNotAfter != 0 {
		t.Fatalf("not-yet-valid certificate must remain rejected: %+v", result)
	}
}

func TestHTTPSExpiredIntermediateRemainsRejected(t *testing.T) {
	cert, roots := expiredIntermediateChain(t)
	result := runTLSWithRoots(t, cert, roots, "127.0.0.1")
	if result.Up || result.CertNotAfter != 0 {
		t.Fatalf("expired intermediate must reject the chain: %+v", result)
	}
}

func expiredCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	now := time.Now()
	return certificateForPeriod(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour))
}

func expiredIntermediateChain(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	now := time.Now()
	rootKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := x509.Certificate{SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "root"},
		NotBefore: now.Add(-72 * time.Hour), NotAfter: now.Add(72 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, &rootTemplate, &rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}

	intermediateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	intermediateTemplate := x509.Certificate{SerialNumber: big.NewInt(11), Subject: pkix.Name{CommonName: "intermediate"},
		NotBefore: now.Add(-72 * time.Hour), NotAfter: now.Add(-24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	intermediateDER, err := x509.CreateCertificate(rand.Reader, &intermediateTemplate, rootCert, &intermediateKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	intermediateCert, err := x509.ParseCertificate(intermediateDER)
	if err != nil {
		t.Fatal(err)
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := x509.Certificate{SerialNumber: big.NewInt(12), Subject: pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(-36 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, intermediateCert, &leafKey.PublicKey, intermediateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediateDER})...)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(leafKey)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	return cert, roots
}

func certificateForPeriod(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}
