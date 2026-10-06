package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// RocketMQCollector 采集 RocketMQ 实例指标，支持 HTTP API 直连与 exporter 双模式。
type RocketMQCollector struct {
	node      string
	instances []model.RocketMQInstanceConfig
}

// NewRocketMQCollector 创建 RocketMQCollector。
func NewRocketMQCollector(node string, instances []model.RocketMQInstanceConfig) *RocketMQCollector {
	return &RocketMQCollector{node: node, instances: instances}
}

// Collect 采集所有 RocketMQ 实例指标（等价于 CollectCtx(context.Background())）。
func (c *RocketMQCollector) Collect() ([]model.Metric, []model.RocketMQInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 RocketMQ 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *RocketMQCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.RocketMQInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.RocketMQInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("RocketMQ 采集被中断，跳过剩余实例", "err", err)
			break
		}
		if cfg.ExporterURL != "" {
			m, ri := c.collectExporter(ctx, cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ri)
			continue
		}
		m, ri := c.collectHTTP(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ri)
	}
	return metrics, instances
}

// collectHTTP 通过 RocketMQ HTTP API 采集。
func (c *RocketMQCollector) collectHTTP(ctx context.Context, cfg model.RocketMQInstanceConfig, now int64) ([]model.Metric, model.RocketMQInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + cfg.Addr

	labels := map[string]string{
		"node":     c.node,
		"instance": normalizeRemoteAddr(cfg.Addr, ""),
		"group":    cfg.Name,
		"name":     cfg.Name,
		"role":     "nameserver",
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	up := 1.0

	// 1. 集群信息
	clusterInfo, err := c.getRocketMQJSON(ctx, client, baseURL+"/rocketmq/httpapi/cluster/list.query")
	if err != nil {
		hint := "请确认 NameServer 已开启 HTTP API（RocketMQ 5.x 需启动参数 -Drocketmq.httpapi.enabled=true 或环境变量 ROCKETMQ_HTTPAPI_ENABLED=true；RocketMQ 4.x 无此 HTTP API，请改用 exporterURL 走 rocketmq-exporter 模式）"
		if strings.Contains(err.Error(), "EOF") {
			slog.Warn("RocketMQ 集群信息获取失败(连接被关闭, HTTP API 可能未开启)", "addr", cfg.Addr, "err", err, "hint", hint)
		} else {
			slog.Warn("RocketMQ 集群信息获取失败", "addr", cfg.Addr, "err", err, "hint", hint)
		}
		up = 0
	}
	out = append(out, mk("rocketmq_instance_up", up))

	if up == 0 {
		return out, model.RocketMQInstance{
			Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node,
			Group: cfg.Name, Role: "nameserver", Up: false,
		}
	}

	// 解析集群信息
	brokerCount := 0.0
	if data, ok := clusterInfo["data"].(map[string]interface{}); ok {
		if brokers, ok := data["brokerServer"].(map[string]interface{}); ok {
			brokerCount = float64(len(brokers))
		}
	}
	out = append(out, mk("rocketmq_broker_count", brokerCount))

	// 2. Topic 列表
	topicList, _ := c.getRocketMQJSON(ctx, client, baseURL+"/rocketmq/httpapi/topic/list.query")
	topicCount := 0.0
	if data, ok := topicList["data"].(map[string]interface{}); ok {
		if topics, ok := data["topicList"].([]interface{}); ok {
			topicCount = float64(len(topics))
		}
	}
	out = append(out, mk("rocketmq_topic_count", topicCount))

	// 3. Consumer Group 列表
	groupList, _ := c.getRocketMQJSON(ctx, client, baseURL+"/rocketmq/httpapi/consumerGroup/list.query")
	groupCount := 0.0
	if data, ok := groupList["data"].(map[string]interface{}); ok {
		if groups, ok := data["groupList"].([]interface{}); ok {
			groupCount = float64(len(groups))
		}
	}
	out = append(out, mk("rocketmq_consumer_group_count", groupCount))

	// 4. Broker TPS/QPS（从集群 stats 接口获取）
	stats, _ := c.getRocketMQJSON(ctx, client, baseURL+"/rocketmq/httpapi/cluster/stats.query")
	if data, ok := stats["data"].(map[string]interface{}); ok {
		out = append(out, mk("rocketmq_broker_tps", parseFloat(fmt.Sprintf("%v", data["brokerTps"]))))
		out = append(out, mk("rocketmq_producer_tps", parseFloat(fmt.Sprintf("%v", data["producerTps"]))))
		out = append(out, mk("rocketmq_consumer_tps", parseFloat(fmt.Sprintf("%v", data["consumerTps"]))))
	}

	// 5. 消息积压（从 Consumer Group stats 聚合）
	totalAccumulation := 0.0
	maxLag := 0.0
	if data, ok := groupList["data"].(map[string]interface{}); ok {
		if groups, ok := data["groupList"].([]interface{}); ok {
			for _, g := range groups {
				groupName, _ := g.(string)
				if groupName == "" {
					continue
				}
				groupStats, _ := c.getRocketMQJSON(ctx, client, baseURL+"/rocketmq/httpapi/consumer/stats.query?group="+groupName)
				if sd, ok := groupStats["data"].(map[string]interface{}); ok {
					diff := parseFloat(fmt.Sprintf("%v", sd["consumeDiff"]))
					totalAccumulation += diff
					if diff > maxLag {
						maxLag = diff
					}
				}
			}
		}
	}
	out = append(out, mk("rocketmq_message_accumulation", totalAccumulation))
	out = append(out, mk("rocketmq_consumer_lag", maxLag))

	version := ""
	if v, ok := clusterInfo["data"].(map[string]interface{}); ok {
		if vv, ok := v["rocketMQVersion"].(string); ok {
			version = vv
		}
	}

	ri := model.RocketMQInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Role:     "nameserver",
		Version:  version,
		Up:       true,
	}
	return out, ri
}

func (c *RocketMQCollector) collectExporter(ctx context.Context, cfg model.RocketMQInstanceConfig, now int64) ([]model.Metric, model.RocketMQInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	body, err := fetchMetrics(ctx, client, cfg.ExporterURL)
	if err != nil {
		slog.Warn("RocketMQ exporter 拉取失败", "target", safeExporterTarget(cfg.ExporterURL), "err", safeExporterError(err))
		return nil, model.RocketMQInstance{
			Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node,
			Group: cfg.Name, Role: "nameserver", Up: false,
		}
	}
	instance := normalizeRemoteAddr(cfg.Addr, "")
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, instance, "rocketmq_", now)
	// RocketMQ exporter 原生通常不暴露 rocketmq_instance_up；ExporterURL 可成功拉取
	// 且至少解析到一条 RocketMQ 指标时，补平台统一使用的实例存活指标，否则实例列表
	// 会因缺少 *_instance_up 而显示离线。
	hasUp := false
	for _, m := range metrics {
		if m.Name == "rocketmq_instance_up" {
			hasUp = true
			break
		}
	}
	if len(metrics) > 0 && !hasUp {
		metrics = append(metrics, model.Metric{
			Node: c.node, Name: "rocketmq_instance_up", Value: 1, Timestamp: now,
			Labels: map[string]string{
				"node": c.node, "instance": instance, "name": cfg.Name,
				"group": cfg.Name, "role": "nameserver",
			},
		})
	}
	up := exporterHealth(string(body), len(metrics) > 0, "rocketmq_up", "rocketmq_instance_up")
	ri := model.RocketMQInstance{
		Instance: instance, Name: cfg.Name, Node: c.node,
		Group: cfg.Name, Role: "nameserver", Up: up,
	}
	for _, m := range metrics {
		if m.Name == "rocketmq_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				ri.Version = v
			}
		}
	}
	return metrics, ri
}

// getRocketMQJSON 发送 GET 请求并解析 JSON。
func (c *RocketMQCollector) getRocketMQJSON(ctx context.Context, client *http.Client, url string) (map[string]interface{}, error) {
	body, err := fetchMetrics(ctx, client, url)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// formatRocketMQAddr 格式化 RocketMQ 地址。
func formatRocketMQAddr(addr string) string {
	return strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
}
