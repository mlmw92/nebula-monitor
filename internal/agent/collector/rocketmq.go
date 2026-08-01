package collector

import (
	"encoding/json"
	"fmt"
	"io"
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

// Collect 采集所有 RocketMQ 实例指标。
func (c *RocketMQCollector) Collect() ([]model.Metric, []model.RocketMQInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.RocketMQInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, ri := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ri)
			continue
		}
		m, ri := c.collectHTTP(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ri)
	}
	return metrics, instances
}

// collectHTTP 通过 RocketMQ HTTP API 采集。
func (c *RocketMQCollector) collectHTTP(cfg model.RocketMQInstanceConfig, now int64) ([]model.Metric, model.RocketMQInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + cfg.Addr

	labels := map[string]string{
		"node":     c.node,
		"instance": cfg.Addr,
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
	clusterInfo, err := c.getRocketMQJSON(client, baseURL+"/rocketmq/httpapi/cluster/list.query")
	if err != nil {
		slog.Warn("RocketMQ 集群信息获取失败", "addr", cfg.Addr, "err", err)
		up = 0
	}
	out = append(out, mk("rocketmq_instance_up", up))

	if up == 0 {
		return out, model.RocketMQInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
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
	topicList, _ := c.getRocketMQJSON(client, baseURL+"/rocketmq/httpapi/topic/list.query")
	topicCount := 0.0
	if data, ok := topicList["data"].(map[string]interface{}); ok {
		if topics, ok := data["topicList"].([]interface{}); ok {
			topicCount = float64(len(topics))
		}
	}
	out = append(out, mk("rocketmq_topic_count", topicCount))

	// 3. Consumer Group 列表
	groupList, _ := c.getRocketMQJSON(client, baseURL+"/rocketmq/httpapi/consumerGroup/list.query")
	groupCount := 0.0
	if data, ok := groupList["data"].(map[string]interface{}); ok {
		if groups, ok := data["groupList"].([]interface{}); ok {
			groupCount = float64(len(groups))
		}
	}
	out = append(out, mk("rocketmq_consumer_group_count", groupCount))

	// 4. Broker TPS/QPS（从集群 stats 接口获取）
	stats, _ := c.getRocketMQJSON(client, baseURL+"/rocketmq/httpapi/cluster/stats.query")
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
				groupStats, _ := c.getRocketMQJSON(client, baseURL+"/rocketmq/httpapi/consumer/stats.query?group="+groupName)
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
		Instance: cfg.Addr,
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Role:     "nameserver",
		Version:  version,
		Up:       true,
	}
	return out, ri
}

func (c *RocketMQCollector) collectExporter(cfg model.RocketMQInstanceConfig, now int64) ([]model.Metric, model.RocketMQInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.ExporterURL)
	if err != nil {
		slog.Warn("RocketMQ exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, model.RocketMQInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Group: cfg.Name, Role: "nameserver", Up: false,
		}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("RocketMQ exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, model.RocketMQInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Group: cfg.Name, Role: "nameserver", Up: false,
		}
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, cfg.Addr, "rocketmq_", now)
	ri := model.RocketMQInstance{
		Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
		Group: cfg.Name, Role: "nameserver", Up: true,
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
func (c *RocketMQCollector) getRocketMQJSON(client *http.Client, url string) (map[string]interface{}, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
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
