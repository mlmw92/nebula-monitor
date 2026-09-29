package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// ElasticsearchCollector 采集 Elasticsearch 实例指标，直连原生 JSON 接口：
//   - GET /                 → 集群名与版本
//   - GET /_cluster/health  → 集群状态 / 节点数 / 分片分布
//
// 无 exporter 依赖。Addr 形如 http://127.0.0.1:9200。
type ElasticsearchCollector struct {
	node      string
	instances []model.ElasticsearchInstanceConfig
}

// NewElasticsearchCollector 创建 ElasticsearchCollector。
func NewElasticsearchCollector(node string, instances []model.ElasticsearchInstanceConfig) *ElasticsearchCollector {
	return &ElasticsearchCollector{node: node, instances: instances}
}

// Collect 采集所有 Elasticsearch 实例指标（等价于 CollectCtx(context.Background())）。
func (c *ElasticsearchCollector) Collect() ([]model.Metric, []model.ElasticsearchInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 Elasticsearch 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *ElasticsearchCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.ElasticsearchInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.ElasticsearchInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("Elasticsearch 采集被中断，跳过剩余实例", "err", err)
			break
		}
		m, ei := c.collectOne(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ei)
	}
	return metrics, instances
}

func (c *ElasticsearchCollector) collectOne(ctx context.Context, cfg model.ElasticsearchInstanceConfig, now int64) ([]model.Metric, model.ElasticsearchInstance) {
	ei := model.ElasticsearchInstance{
		Instance: urlHostPort(cfg.Addr),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	down := func() ([]model.Metric, model.ElasticsearchInstance) {
		return []model.Metric{c.upMetric(cfg, ei, 0, now)}, ei
	}

	// 根路径：集群名与版本
	var root struct {
		ClusterName string `json:"cluster_name"`
		Version     struct {
			Number string `json:"number"`
		} `json:"version"`
	}
	if err := c.getJSON(ctx, cfg, cfg.Addr+"/", &root); err != nil {
		slog.Warn("Elasticsearch 连接失败", "addr", cfg.Addr, "err", err)
		return down()
	}

	var health struct {
		Status              string `json:"status"`
		NumberOfNodes       int    `json:"number_of_nodes"`
		NumberOfDataNodes   int    `json:"number_of_data_nodes"`
		ActiveShards        int    `json:"active_shards"`
		ActivePrimaryShards int    `json:"active_primary_shards"`
		UnassignedShards    int    `json:"unassigned_shards"`
	}
	if err := c.getJSON(ctx, cfg, cfg.Addr+"/_cluster/health", &health); err != nil {
		slog.Warn("Elasticsearch 集群健康查询失败", "addr", cfg.Addr, "err", err)
		return down()
	}

	ei.ClusterName = root.ClusterName
	ei.Version = root.Version.Number
	ei.Status = health.Status
	ei.Nodes = float64(health.NumberOfNodes)
	ei.DataNodes = float64(health.NumberOfDataNodes)
	ei.ActiveShards = float64(health.ActiveShards)
	ei.PrimaryShards = float64(health.ActivePrimaryShards)
	ei.UnassignedShards = float64(health.UnassignedShards)
	ei.Up = true

	labels := map[string]string{
		"node":        c.node,
		"instance":    ei.Instance,
		"name":        cfg.Name,
		"group":       cfg.Name,
		"role":        "coordinator",
		"version":     ei.Version,
		"cluster":     ei.ClusterName,
		"es_status":   ei.Status,
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	statusVal := 2.0
	switch health.Status {
	case "green":
		statusVal = 0
	case "yellow":
		statusVal = 1
	}

	out := []model.Metric{
		mk("es_instance_up", 1),
		mk("es_cluster_status", statusVal),
		mk("es_nodes", ei.Nodes),
		mk("es_data_nodes", ei.DataNodes),
		mk("es_active_shards", ei.ActiveShards),
		mk("es_primary_shards", ei.PrimaryShards),
		mk("es_unassigned_shards", ei.UnassignedShards),
	}
	return out, ei
}

// getJSON 带 Basic Auth 的 GET 请求并解析 JSON。
func (c *ElasticsearchCollector) getJSON(ctx context.Context, cfg model.ElasticsearchInstanceConfig, url string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// upMetric 生成实例存活指标。
func (c *ElasticsearchCollector) upMetric(cfg model.ElasticsearchInstanceConfig, ei model.ElasticsearchInstance, up float64, now int64) model.Metric {
	return model.Metric{
		Node: c.node, Name: "es_instance_up", Value: up, Timestamp: now,
		Labels: map[string]string{
			"node":     c.node,
			"instance": ei.Instance,
			"name":     cfg.Name,
			"group":    cfg.Name,
			"role":     "coordinator",
		},
	}
}
