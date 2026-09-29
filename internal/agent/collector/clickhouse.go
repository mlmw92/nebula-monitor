package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// ClickHouseCollector 采集 ClickHouse 实例指标，直连 HTTP 端口（默认 8123）执行
// SQL 查询，无需 exporter：
//   - SELECT version()
//   - SELECT metric, value FROM system.metrics（连接数 / 运行中查询 / 合并）
//   - SELECT uptime()
type ClickHouseCollector struct {
	node      string
	instances []model.ClickHouseInstanceConfig
}

// NewClickHouseCollector 创建 ClickHouseCollector。
func NewClickHouseCollector(node string, instances []model.ClickHouseInstanceConfig) *ClickHouseCollector {
	return &ClickHouseCollector{node: node, instances: instances}
}

// Collect 采集所有 ClickHouse 实例指标（等价于 CollectCtx(context.Background())）。
func (c *ClickHouseCollector) Collect() ([]model.Metric, []model.ClickHouseInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 ClickHouse 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *ClickHouseCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.ClickHouseInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.ClickHouseInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("ClickHouse 采集被中断，跳过剩余实例", "err", err)
			break
		}
		m, ci := c.collectOne(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ci)
	}
	return metrics, instances
}

func (c *ClickHouseCollector) collectOne(ctx context.Context, cfg model.ClickHouseInstanceConfig, now int64) ([]model.Metric, model.ClickHouseInstance) {
	ci := model.ClickHouseInstance{
		Instance: urlHostPort(cfg.Addr),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	down := func() ([]model.Metric, model.ClickHouseInstance) {
		return []model.Metric{c.upMetric(cfg, ci, 0, now)}, ci
	}

	query := func(q string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+cfg.Addr+"/", nil)
		if err != nil {
			return "", err
		}
		qf := req.URL.Query()
		qf.Set("query", q)
		req.URL.RawQuery = qf.Encode()
		if cfg.Username != "" {
			req.SetBasicAuth(cfg.Username, cfg.Password)
		} else if cfg.Password != "" {
			// 仅密码（default 用户）：X-ClickHouse-User 缺省为 default，用 Basic Auth default:password
			req.SetBasicAuth("default", cfg.Password)
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return "", err
		}
		if resp.StatusCode/100 != 2 {
			return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return string(body), nil
	}

	version, err := query("SELECT version() FORMAT TSV")
	if err != nil {
		slog.Warn("ClickHouse 连接失败", "addr", cfg.Addr, "err", err)
		return down()
	}
	version = strings.TrimSpace(version)
	ci.Version = version

	uptime, _ := strconv.ParseFloat(strings.TrimSpace(mustQuery(query, "SELECT uptime() FORMAT TSV")), 64)
	ci.UptimeSeconds = uptime

	// system.metrics：metric/value 两列 TSV
	metricMap := map[string]float64{}
	if raw, err := query("SELECT metric, value FROM system.metrics FORMAT TSV"); err == nil {
		for _, line := range stringsSplitLines(raw) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
					metricMap[fields[0]] = v
				}
			}
		}
	}
	ci.TCPConnections = metricMap["TCPConnection"]
	ci.HTTPConnections = metricMap["HTTPConnection"]
	ci.QueriesRunning = metricMap["Query"]
	ci.MergesRunning = metricMap["Merge"]
	ci.Up = true

	labels := map[string]string{
		"node":     c.node,
		"instance": ci.Instance,
		"name":     cfg.Name,
		"group":    cfg.Name,
		"role":     "server",
		"version":  ci.Version,
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	out := []model.Metric{
		mk("clickhouse_instance_up", 1),
		mk("clickhouse_tcp_connections", ci.TCPConnections),
		mk("clickhouse_http_connections", ci.HTTPConnections),
		mk("clickhouse_queries_running", ci.QueriesRunning),
		mk("clickhouse_merges_running", ci.MergesRunning),
		mk("clickhouse_uptime_seconds", ci.UptimeSeconds),
	}
	return out, ci
}

// mustQuery 查询失败返回空串（错误已在调用处日志中体现， uptime 等非关键指标可缺失）。
func mustQuery(q func(string) (string, error), query string) string {
	s, err := q(query)
	if err != nil {
		return "0"
	}
	return s
}

// upMetric 生成实例存活指标。
func (c *ClickHouseCollector) upMetric(cfg model.ClickHouseInstanceConfig, ci model.ClickHouseInstance, up float64, now int64) model.Metric {
	return model.Metric{
		Node: c.node, Name: "clickhouse_instance_up", Value: up, Timestamp: now,
		Labels: map[string]string{
			"node":     c.node,
			"instance": ci.Instance,
			"name":     cfg.Name,
			"group":    cfg.Name,
			"role":     "server",
		},
	}
}
