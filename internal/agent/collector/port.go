package collector

import (
	"net"
	"strconv"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// PortCollector 执行 TCP 端口存活检测。
type PortCollector struct {
	node  string
	ports []string
}

// NewPortCollector 创建 PortCollector。
func NewPortCollector(node string, ports []string) *PortCollector {
	return &PortCollector{node: node, ports: ports}
}

// Collect 对每个配置端口执行 TCP connect 检测，产出 port_up 与 port_latency 指标。
func (c *PortCollector) Collect() []model.Metric {
	if len(c.ports) == 0 {
		return nil
	}
	now := model.NowMillis()
	var out []model.Metric
	for _, port := range c.ports {
		labels := map[string]string{
			"node": c.node,
			"port": port,
		}
		up := 0.0
		latency := 0.0
		// 尝试连接 127.0.0.1:port（端口检测针对本机）
		addr := net.JoinHostPort("127.0.0.1", port)
		start := time.Now()
		conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
		latency = float64(time.Since(start).Microseconds()) / 1000.0
		if err == nil {
			conn.Close()
			up = 1
		}
		out = append(out, model.Metric{
			Node: c.node, Name: "port_up", Labels: labels, Value: up, Timestamp: now,
		})
		out = append(out, model.Metric{
			Node: c.node, Name: "port_latency", Labels: labels, Value: round2(latency), Timestamp: now,
		})
	}
	return out
}

// isValidPort 校验端口号字符串是否合法。
func isValidPort(s string) bool {
	p, err := strconv.Atoi(s)
	return err == nil && p > 0 && p <= 65535
}
