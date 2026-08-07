package collector

import (
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// NginxCollector 采集 Nginx 实例指标，支持 stub_status 解析与 VTS exporter 双模式。
type NginxCollector struct {
	node      string
	instances []model.NginxInstanceConfig
}

// NewNginxCollector 创建 NginxCollector。
func NewNginxCollector(node string, instances []model.NginxInstanceConfig) *NginxCollector {
	return &NginxCollector{node: node, instances: instances}
}

// Collect 采集所有 Nginx 实例指标。
func (c *NginxCollector) Collect() ([]model.Metric, []model.NginxInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.NginxInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, ni := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ni)
			continue
		}
		m, ni := c.collectStubStatus(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ni)
	}
	return metrics, instances
}

// collectStubStatus 通过 HTTP GET stub_status 端点采集。
func (c *NginxCollector) collectStubStatus(cfg model.NginxInstanceConfig, now int64) ([]model.Metric, model.NginxInstance) {
	scheme := "http"
	if strings.HasPrefix(cfg.Addr, "https://") {
		scheme = "https"
	}
	host := strings.TrimPrefix(strings.TrimPrefix(cfg.Addr, "https://"), "http://")
	statusPath := cfg.StatusPath
	if statusPath == "" {
		statusPath = "/nginx_status"
	}
	url := fmt.Sprintf("%s://%s%s", scheme, host, statusPath)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(url)
	if err != nil {
		slog.Warn("Nginx stub_status 拉取失败", "url", url, "err", err)
		return nil, c.downInstance(cfg)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 非 200 通常是 stub_status 未在该路径启用（如返回 404/403），
		// 属被监控机 Nginx 配置问题，给出明确提示而非打印整段响应体。
		slog.Warn("Nginx stub_status 返回非 200",
			"url", url, "status", resp.StatusCode,
			"hint", "请在目标 Nginx 配置中启用 stub_status：location "+statusPath+" { stub_status; allow 127.0.0.1; } 然后 nginx -s reload")
		return nil, c.downInstance(cfg)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("Nginx stub_status 读取失败", "url", url, "err", err)
		return nil, c.downInstance(cfg)
	}

	parsed := parseNginxStubStatus(string(body))
	if parsed == nil {
		slog.Warn("Nginx stub_status 解析失败", "url", url,
			"hint", "响应内容非标准 stub_status 格式，请确认该 location 指向 stub_status 而非静态页/反向代理")
		return nil, c.downInstance(cfg)
	}

	labels := map[string]string{
		"node":     c.node,
		"instance": normalizeRemoteAddr(cfg.Addr, ""),
		"group":    cfg.Name,
		"name":     cfg.Name,
		"version":  resp.Header.Get("Server"),
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	out = append(out, mk("nginx_instance_up", 1))
	out = append(out, mk("nginx_active_connections", parsed["active"]))
	out = append(out, mk("nginx_accepts", parsed["accepts"]))
	out = append(out, mk("nginx_handled", parsed["handled"]))
	out = append(out, mk("nginx_requests", parsed["requests"]))
	out = append(out, mk("nginx_reading", parsed["reading"]))
	out = append(out, mk("nginx_writing", parsed["writing"]))
	out = append(out, mk("nginx_waiting", parsed["waiting"]))
	// 派生指标
	accepts := parsed["accepts"]
	handled := parsed["handled"]
	if accepts > 0 {
		out = append(out, mk("nginx_connection_drop_rate", round2((accepts-handled)/accepts*100)))
	} else {
		out = append(out, mk("nginx_connection_drop_rate", 0))
	}

	ni := model.NginxInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Version:  resp.Header.Get("Server"),
		Up:       true,
	}
	return out, ni
}

func (c *NginxCollector) downInstance(cfg model.NginxInstanceConfig) model.NginxInstance {
	return model.NginxInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node, Group: cfg.Name, Up: false,
	}
}

func (c *NginxCollector) collectExporter(cfg model.NginxInstanceConfig, now int64) ([]model.Metric, model.NginxInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.ExporterURL)
	if err != nil {
		slog.Warn("Nginx exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("Nginx exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg)
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, normalizeRemoteAddr(cfg.Addr, ""), "nginx_", now)
	ni := model.NginxInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node, Group: cfg.Name, Up: true,
	}
	for _, m := range metrics {
		if m.Name == "nginx_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				ni.Version = v
			}
		}
	}
	return metrics, ni
}

// parseNginxStubStatus 解析 nginx stub_status 输出。
// 典型格式：
//
//	Active connections: 15
//	server accepts handled requests
//	8456 8456 32891
//	Reading: 0 Writing: 3 Waiting: 12
var (
	reActive = regexp.MustCompile(`Active connections:\s*(\d+)`)
	reNumbers = regexp.MustCompile(`\s+(\d+)\s+(\d+)\s+(\d+)`)
	reRWWait  = regexp.MustCompile(`Reading:\s*(\d+)\s+Writing:\s*(\d+)\s+Waiting:\s*(\d+)`)
)

func parseNginxStubStatus(text string) map[string]float64 {
	out := map[string]float64{}
	if m := reActive.FindStringSubmatch(text); len(m) >= 2 {
		out["active"] = parseFloat(m[1])
	}
	if m := reNumbers.FindStringSubmatch(text); len(m) >= 4 {
		out["accepts"] = parseFloat(m[1])
		out["handled"] = parseFloat(m[2])
		out["requests"] = parseFloat(m[3])
	}
	if m := reRWWait.FindStringSubmatch(text); len(m) >= 4 {
		out["reading"] = parseFloat(m[1])
		out["writing"] = parseFloat(m[2])
		out["waiting"] = parseFloat(m[3])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
