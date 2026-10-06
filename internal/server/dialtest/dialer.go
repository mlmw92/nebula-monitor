package dialtest

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/nebula/monitor/internal/model"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// TaskType 拨测类型。
type TaskType string

const (
	// TaskTypeHTTP 普通 HTTP 拨测。
	TaskTypeHTTP TaskType = "http"
	// TaskTypeHTTPS 带 TLS 的 HTTPS 拨测。
	TaskTypeHTTPS TaskType = "https"
	// TaskTypeTCP TCP 端口连通性拨测。
	TaskTypeTCP TaskType = "tcp"
	// TaskTypeICMP ICMP 存活探测（ping）。
	TaskTypeICMP TaskType = "icmp"
)

// Task 拨测任务定义。
type Task struct {
	ID       string   `json:"id" yaml:"id"`
	Name     string   `json:"name" yaml:"name"`
	Type     TaskType `json:"type" yaml:"type"`
	Target   string   `json:"target" yaml:"target"`     // URL（HTTP/HTTPS）或 host:port（TCP）或 host（ICMP）
	Interval int      `json:"interval" yaml:"interval"` // 拨测间隔（秒）
	Timeout  int      `json:"timeout" yaml:"timeout"`   // 超时（秒）
	// FailThreshold 连续失败达到该次数才判定为故障并触发告警，用于抑制单次网络抖动
	// 产生的“故障→恢复”邮件对。≤ 0 表示使用默认阈值（3 次）。
	FailThreshold int  `json:"fail_threshold,omitempty" yaml:"fail_threshold,omitempty"`
	Enabled       bool `json:"enabled" yaml:"enabled"`
	// Public 是否在「对外状态页」展示（默认 false = 不展示）。
	//
	// 这是「对外发布」的开关：状态页是**无需登录**的页面，因此默认不暴露任何任务，
	// 由运维显式勾选哪些可以对外公开（C3）。
	Public   bool     `json:"public,omitempty" yaml:"public,omitempty"`
	Severity string   `json:"severity" yaml:"severity"` // 告警严重级别: critical/warning/info，默认 warning
	Notify   []string `json:"notify" yaml:"notify"`     // 通知渠道：email/webhook/dingtalk/feishu/wecom，空表示仅平台展示、不推送外部渠道

	// SSL 证书过期预警阈值（天）：仅 HTTPS 任务生效；≤ 0 表示沿用默认（预警 30 / 告警 7）。
	// 证书剩余天数 ≤ CertWarnDays 触发「警告」，≤ CertCritDays 触发「紧急」。
	CertWarnDays int `json:"cert_warn,omitempty" yaml:"cert_warn,omitempty"`
	CertCritDays int `json:"cert_crit,omitempty" yaml:"cert_crit,omitempty"`
}

// Result 拨测结果。
type Result struct {
	TaskID       string
	Up           bool
	Latency      float64 // 毫秒
	CertExpiry   float64 // SSL 证书剩余天数（仅 HTTPS，可为负表示已过期）
	CertNotAfter int64   // SSL 证书到期时间戳（毫秒，仅 HTTPS 且成功解析到证书时 > 0）
	StatusCode   int     // HTTP 状态码（仅 HTTP/HTTPS）
	Error        string  // 异常原因（仅 Up=false 时有效，如连接拒绝/超时/DNS失败/HTTP状态文本）
}

// Dialer 执行拨测。
type Dialer struct {
	icmpProbe func(host string, timeout time.Duration) (time.Duration, error)
	tlsRoots  *x509.CertPool
}

// NewDialer 创建拨测器。
func NewDialer() *Dialer {
	return &Dialer{icmpProbe: probeICMP}
}

// Run 执行单个拨测任务。
func (d *Dialer) Run(task Task) Result {
	switch task.Type {
	case TaskTypeHTTP, TaskTypeHTTPS:
		return d.dialHTTP(task)
	case TaskTypeTCP:
		return d.dialTCP(task)
	case TaskTypeICMP:
		return d.dialICMP(task)
	default:
		return Result{TaskID: task.ID, Up: false}
	}
}

// dialHTTP 执行 HTTP/HTTPS 拨测。
func (d *Dialer) dialHTTP(task Task) Result {
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// 自定义完整校验只为允许读取已过期证书；签发链、主机名和未来生效仍必须通过。
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, VerifyConnection: func(state tls.ConnectionState) error {
				if len(state.PeerCertificates) == 0 {
					return errors.New("对端未提供证书")
				}
				verify := func(at time.Time) ([][]*x509.Certificate, error) {
					opts := x509.VerifyOptions{DNSName: state.ServerName, Roots: d.tlsRoots, Intermediates: x509.NewCertPool(), CurrentTime: at}
					for _, cert := range state.PeerCertificates[1:] {
						opts.Intermediates.AddCert(cert)
					}
					return state.PeerCertificates[0].Verify(opts)
				}
				now := time.Now()
				_, err := verify(now)
				if err == nil {
					return nil
				}
				leaf := state.PeerCertificates[0]
				var invalid x509.CertificateInvalidError
				if !errors.As(err, &invalid) || invalid.Reason != x509.Expired || now.Before(leaf.NotBefore) || !now.After(leaf.NotAfter) {
					return err
				}
				chains, historicalErr := verify(leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) / 2))
				if historicalErr != nil {
					return err
				}
				for _, chain := range chains {
					valid := true
					for _, cert := range chain[1:] {
						if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
							valid = false
							break
						}
					}
					if valid {
						return nil
					}
				}
				return err
			}},
		},
	}
	scheme := string(task.Type)
	if scheme == "" {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s", scheme, task.Target)

	start := time.Now()
	resp, err := client.Get(url)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Result{TaskID: task.ID, Up: false, Latency: round2(latency), Error: err.Error()}
	}
	defer resp.Body.Close()

	result := Result{
		TaskID:     task.ID,
		Up:         resp.StatusCode >= 200 && resp.StatusCode < 400,
		Latency:    round2(latency),
		StatusCode: resp.StatusCode,
	}
	// 非 2xx/3xx 视为异常，记录 HTTP 状态文本作为原因
	if !result.Up {
		result.Error = http.StatusText(resp.StatusCode)
		if result.Error == "" {
			result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}
	if task.Type == TaskTypeHTTPS && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert := resp.TLS.PeerCertificates[0]
		days := cert.NotAfter.Sub(time.Now()).Hours() / 24
		result.CertExpiry = round2(days)
		if days < 0 && result.CertExpiry == 0 {
			result.CertExpiry = -0.01
		}
		result.CertNotAfter = cert.NotAfter.UnixMilli()
		if days < 0 {
			result.Up = false
			result.Error = "SSL 证书已过期"
		}
	}
	return result
}

// dialTCP 执行 TCP 拨测。
func (d *Dialer) dialTCP(task Task) Result {
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()
	conn, err := net.DialTimeout("tcp", task.Target, timeout)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		return Result{TaskID: task.ID, Up: false, Latency: round2(latency), Error: err.Error()}
	}
	conn.Close()
	return Result{TaskID: task.ID, Up: true, Latency: round2(latency)}
}

// dialICMP 执行真实 ICMP Echo 探测；权限不足时返回明确失败，不退化为 TCP 端口探测。
func (d *Dialer) dialICMP(task Task) Result {
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	probe := d.icmpProbe
	if probe == nil {
		probe = probeICMP
	}
	latency, err := probe(task.Target, timeout)
	if err != nil {
		return Result{TaskID: task.ID, Up: false, Latency: round2(float64(latency.Microseconds()) / 1000), Error: err.Error()}
	}
	return Result{TaskID: task.ID, Up: true, Latency: round2(float64(latency.Microseconds()) / 1000)}
}

type icmpPacketConn interface {
	SetDeadline(time.Time) error
	WriteTo([]byte, net.Addr) (int, error)
	ReadFrom([]byte) (int, net.Addr, error)
	Close() error
}

var resolveICMPAddr = net.ResolveIPAddr
var listenICMPPacket = func(network, address string) (icmpPacketConn, error) {
	return icmp.ListenPacket(network, address)
}

func probeICMP(host string, timeout time.Duration) (time.Duration, error) {
	addr, err := resolveICMPAddr("ip", host)
	if err != nil {
		return 0, err
	}
	network, protocol := "ip4:icmp", 1
	var echoType, replyType icmp.Type = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	if addr.IP.To4() == nil {
		network, protocol, echoType, replyType = "ip6:ipv6-icmp", 58, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	conn, err := listenICMPPacket(network, "")
	if err != nil {
		return 0, fmt.Errorf("ICMP 探测不可执行: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, err
	}
	message := icmp.Message{Type: echoType, Code: 0, Body: &icmp.Echo{ID: os.Getpid() & 0xffff, Seq: 1, Data: []byte("nebula-monitor")}}
	data, err := message.Marshal(nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	if _, err := conn.WriteTo(data, addr); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return time.Since(start), err
		}
		reply, err := icmp.ParseMessage(protocol, buf[:n])
		if err != nil || reply.Type != replyType {
			continue
		}
		echo, ok := reply.Body.(*icmp.Echo)
		if ok && echo.ID == os.Getpid()&0xffff && echo.Seq == 1 {
			return time.Since(start), nil
		}
	}
}

// ResultToMetrics 将拨测结果转为指标。
func ResultToMetrics(r Result, task Task, now int64) []model.Metric {
	labels := map[string]string{
		"name":   task.Name,
		"type":   string(task.Type),
		"target": task.Target,
	}
	var out []model.Metric
	upVal := 0.0
	if r.Up {
		upVal = 1
	}
	out = append(out, model.Metric{Name: "dial_test_up", Labels: labels, Value: upVal, Timestamp: now})
	out = append(out, model.Metric{Name: "dial_test_latency", Labels: labels, Value: r.Latency, Timestamp: now})
	if task.Type == TaskTypeHTTPS && r.CertNotAfter != 0 {
		out = append(out, model.Metric{Name: "dial_test_cert_expiry", Labels: labels, Value: r.CertExpiry, Timestamp: now})
	}
	return out
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
