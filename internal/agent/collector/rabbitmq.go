package collector

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// RabbitMQCollector 采集 RabbitMQ 实例指标，走 rabbitmq_prometheus 插件的
// /metrics 端点（默认端口 15692，无需 exporter）。
//
// 解析策略：优先取**无标签的全局聚合序列**。rabbitmq_prometheus 会同时输出
// 全局聚合序列与 per-queue / per-channel 的带标签序列，且部分指标名相同
// （如 rabbitmq_queue_messages 既有全局也有带 queue 标签的样本）；实例级监控
// 关注整机总量，因此同名时以无标签样本为准，带标签样本忽略。
type RabbitMQCollector struct {
	node      string
	instances []model.RabbitMQInstanceConfig
}

// NewRabbitMQCollector 创建 RabbitMQCollector。
func NewRabbitMQCollector(node string, instances []model.RabbitMQInstanceConfig) *RabbitMQCollector {
	return &RabbitMQCollector{node: node, instances: instances}
}

// Collect 采集所有 RabbitMQ 实例指标（等价于 CollectCtx(context.Background())）。
func (c *RabbitMQCollector) Collect() ([]model.Metric, []model.RabbitMQInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 RabbitMQ 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *RabbitMQCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.RabbitMQInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.RabbitMQInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("RabbitMQ 采集被中断，跳过剩余实例", "err", err)
			break
		}
		m, ri := c.collectOne(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ri)
	}
	return metrics, instances
}

func (c *RabbitMQCollector) collectOne(ctx context.Context, cfg model.RabbitMQInstanceConfig, now int64) ([]model.Metric, model.RabbitMQInstance) {
	ri := model.RabbitMQInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	down := func() ([]model.Metric, model.RabbitMQInstance) {
		return []model.Metric{c.upMetric(cfg, ri.Instance, 0, now)}, ri
	}

	url := "http://" + cfg.Addr + "/metrics"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Warn("RabbitMQ 请求构造失败", "addr", cfg.Addr, "err", err)
		return down()
	}
	if cfg.Username != "" || cfg.Password != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("RabbitMQ 连接失败", "addr", cfg.Addr, "err", err)
		return down()
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		slog.Warn("RabbitMQ /metrics 返回非 2xx", "addr", cfg.Addr, "status", resp.StatusCode)
		return down()
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("RabbitMQ metrics 读取失败", "addr", cfg.Addr, "err", err)
		return down()
	}

	// 逐行解析，同名时优先无标签（全局聚合）样本
	values := map[string]float64{}
	isGlobal := map[string]bool{}
	version := ""
	hasRabbitMetrics := false
	for _, line := range stringsSplitLines(string(body)) {
		name, labels, value, ok := parsePromLine(line)
		if !ok || !stringsHasPrefix(name, "rabbitmq_") {
			continue
		}
		hasRabbitMetrics = true
		if name == "rabbitmq_build_info" {
			if v := labels["version"]; v != "" && version == "" {
				version = v
			}
			continue
		}
		if prev, seen := isGlobal[name]; seen && prev && len(labels) > 0 {
			continue // 已有全局样本，忽略带标签样本
		}
		if _, seen := values[name]; !seen || len(labels) == 0 {
			values[name] = value
			if len(labels) == 0 {
				isGlobal[name] = true
			}
		}
	}
	_ = isGlobal

	get := func(name string) float64 { return values[name] }

	labels := map[string]string{
		"node":     c.node,
		"instance": ri.Instance,
		"name":     cfg.Name,
		"group":    cfg.Name,
		"role":     "broker",
		"version":  version,
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	ri.Version = version
	ri.Connections = get("rabbitmq_connections")
	ri.Queues = get("rabbitmq_queues")
	ri.Messages = get("rabbitmq_queue_messages")
	ri.Consumers = get("rabbitmq_consumers")
	ri.Publishers = get("rabbitmq_publishers")
	ri.MemoryBytes = get("rabbitmq_process_memory_bytes")
	ri.FdUsed = get("rabbitmq_fd_used")
	ri.Up = exporterHealth(string(body), hasRabbitMetrics, "rabbitmq_up", "rabbitmq_instance_up")

	upValue := 0.0
	if ri.Up {
		upValue = 1
	}
	out := []model.Metric{
		mk("rabbitmq_instance_up", upValue),
		mk("rabbitmq_connections", ri.Connections),
		mk("rabbitmq_queues", ri.Queues),
		mk("rabbitmq_queue_messages", ri.Messages),
		mk("rabbitmq_consumers", ri.Consumers),
		mk("rabbitmq_publishers", ri.Publishers),
		mk("rabbitmq_process_memory_bytes", ri.MemoryBytes),
		mk("rabbitmq_fd_used", ri.FdUsed),
	}
	return out, ri
}

// upMetric 生成实例存活指标（带 name/group/instance 标签，与其它内置类型一致）。
func (c *RabbitMQCollector) upMetric(cfg model.RabbitMQInstanceConfig, instance string, up float64, now int64) model.Metric {
	return model.Metric{
		Node: c.node, Name: "rabbitmq_instance_up", Value: up, Timestamp: now,
		Labels: map[string]string{
			"node":     c.node,
			"instance": instance,
			"name":     cfg.Name,
			"group":    cfg.Name,
			"role":     "broker",
		},
	}
}
