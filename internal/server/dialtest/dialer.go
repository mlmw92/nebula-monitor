package dialtest

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// TaskType 拨测类型。
type TaskType string

const (
	TaskTypeHTTP  TaskType = "http"
	TaskTypeHTTPS TaskType = "https"
	TaskTypeTCP   TaskType = "tcp"
	TaskTypeICMP  TaskType = "icmp"
)

// Task 拨测任务定义。
type Task struct {
	ID       string   `json:"id" yaml:"id"`
	Name     string   `json:"name" yaml:"name"`
	Type     TaskType `json:"type" yaml:"type"`
	Target   string   `json:"target" yaml:"target"`     // URL（HTTP/HTTPS）或 host:port（TCP）或 host（ICMP）
	Interval int      `json:"interval" yaml:"interval"` // 拨测间隔（秒）
	Timeout  int      `json:"timeout" yaml:"timeout"`   // 超时（秒）
	Enabled  bool     `json:"enabled" yaml:"enabled"`
	Severity string   `json:"severity" yaml:"severity"` // 告警严重级别: critical/warning/info，默认 warning
	Notify   []string `json:"notify" yaml:"notify"`     // 通知渠道：email/webhook/dingtalk/feishu/wecom，空表示仅平台展示、不推送外部渠道
}

// Result 拨测结果。
type Result struct {
	TaskID     string
	Up         bool
	Latency    float64 // 毫秒
	CertExpiry float64 // SSL 证书剩余天数（仅 HTTPS）
	StatusCode int     // HTTP 状态码（仅 HTTP/HTTPS）
	Error      string  // 异常原因（仅 Up=false 时有效，如连接拒绝/超时/DNS失败/HTTP状态文本）
}

// Dialer 执行拨测。
type Dialer struct{}

// NewDialer 创建拨测器。
func NewDialer() *Dialer {
	return &Dialer{}
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
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
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
		TaskID:    task.ID,
		Up:        resp.StatusCode >= 200 && resp.StatusCode < 400,
		Latency:   round2(latency),
		StatusCode: resp.StatusCode,
	}
	// 非 2xx/3xx 视为异常，记录 HTTP 状态文本作为原因
	if !result.Up {
		result.Error = http.StatusText(resp.StatusCode)
		if result.Error == "" {
			result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
	}
	// HTTPS 证书到期检测
	if task.Type == TaskTypeHTTPS && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		cert := resp.TLS.PeerCertificates[0]
		days := cert.NotAfter.Sub(time.Now()).Hours() / 24
		result.CertExpiry = round2(days)
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

// dialICMP 执行 ICMP 拨测（简化实现，使用 TCP echo 替代）。
// 注意：ICMP 需要 CAP_NET_RAW 权限，这里降级为 TCP connect 检测端口 7（echo）或直接 ping。
func (d *Dialer) dialICMP(task Task) Result {
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	// 简化：使用 TCP 连接目标主机的 echo 端口（7），失败则认为不可达
	start := time.Now()
	conn, err := net.DialTimeout("tcp", task.Target+":7", timeout)
	latency := float64(time.Since(start).Microseconds()) / 1000.0
	if err != nil {
		// echo 端口未开放不代表主机不可达，尝试连接 80 端口
		conn2, err2 := net.DialTimeout("tcp", task.Target+":80", timeout)
		if err2 != nil {
			return Result{TaskID: task.ID, Up: false, Latency: round2(latency), Error: err2.Error()}
		}
		conn2.Close()
		return Result{TaskID: task.ID, Up: true, Latency: round2(latency)}
	}
	conn.Close()
	return Result{TaskID: task.ID, Up: true, Latency: round2(latency)}
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
	if task.Type == TaskTypeHTTPS && r.CertExpiry > 0 {
		out = append(out, model.Metric{Name: "dial_test_cert_expiry", Labels: labels, Value: r.CertExpiry, Timestamp: now})
	}
	return out
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
