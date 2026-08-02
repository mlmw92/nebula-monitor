package collector

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/IBM/sarama"

	"github.com/nebula/monitor/internal/model"
)

// KafkaCollector 采集 Kafka 实例指标，支持 sarama 直连与 JMX exporter 双模式。
type KafkaCollector struct {
	node      string
	instances []model.KafkaInstanceConfig
}

// NewKafkaCollector 创建 KafkaCollector。
func NewKafkaCollector(node string, instances []model.KafkaInstanceConfig) *KafkaCollector {
	return &KafkaCollector{node: node, instances: instances}
}

// Collect 采集所有 Kafka 实例指标。
func (c *KafkaCollector) Collect() ([]model.Metric, []model.KafkaInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.KafkaInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, ki := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ki)
			continue
		}
		m, ki := c.collectDirect(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ki)
	}
	return metrics, instances
}

// collectDirect 通过 sarama 直连 Kafka Broker 采集。
func (c *KafkaCollector) collectDirect(cfg model.KafkaInstanceConfig, now int64) ([]model.Metric, model.KafkaInstance) {
	config := sarama.NewConfig()
	config.Net.DialTimeout = 5 * time.Second
	config.Net.ReadTimeout = 5 * time.Second
	config.Version = sarama.V2_8_0_0 // 兼容大多数 Kafka 版本

	// 1. 创建 Client
	client, err := sarama.NewClient([]string{cfg.Addr}, config)
	if err != nil {
		slog.Warn("Kafka 连接失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg)
	}
	defer client.Close()

	// 2. 创建 AdminClient 获取集群元信息
	admin, err := sarama.NewClusterAdminFromClient(client)
	if err != nil {
		slog.Warn("Kafka AdminClient 创建失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg)
	}
	defer admin.Close()

	brokers := client.Brokers()
	topics, err := client.Topics()
	if err != nil {
		slog.Warn("Kafka 获取 Topics 失败", "addr", cfg.Addr, "err", err)
		topics = nil
	}

	labels := map[string]string{
		"node":     c.node,
		"instance": normalizeRemoteAddr(cfg.Addr, ""),
		"group":    cfg.Name,
		"role":     "broker",
		"version":  cfg.Version,
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	out = append(out, mk("kafka_instance_up", 1))
	out = append(out, mk("kafka_broker_count", float64(len(brokers))))
	out = append(out, mk("kafka_topic_count", float64(len(topics))))

	// 统计分区数和未充分复制分区
	totalPartitions := 0
	underReplicated := 0
	offlinePartitions := 0
	for _, topic := range topics {
		topicsMeta, err := admin.DescribeTopics([]string{topic})
		if err != nil || len(topicsMeta) == 0 {
			continue
		}
		tm := topicsMeta[0]
		totalPartitions += len(tm.Partitions)
		for _, p := range tm.Partitions {
			if p.Err == sarama.ErrReplicaNotAvailable {
				underReplicated++
			}
			if p.Leader == -1 {
				offlinePartitions++
			}
		}
	}
	out = append(out, mk("kafka_partition_count", float64(totalPartitions)))
	out = append(out, mk("kafka_under_replicated_partitions", float64(underReplicated)))
	out = append(out, mk("kafka_offline_partitions", float64(offlinePartitions)))

	// Consumer Group 数量
	if groups, err := admin.ListConsumerGroups(); err == nil {
		out = append(out, mk("kafka_consumer_group_count", float64(len(groups))))
	}

	// 控制器数
	if controller, err := admin.Controller(); err == nil && controller != nil {
		out = append(out, mk("kafka_active_controller_count", 1))
	} else {
		out = append(out, mk("kafka_active_controller_count", 0))
	}

	// Consumer Lag（按 group/topic 汇总）
	totalLag := 0.0
	maxLag := 0.0
	if groups, err := admin.ListConsumerGroups(); err == nil {
		for groupName := range groups {
			groupLag, err := getConsumerGroupLag(admin, client, groupName)
			if err != nil {
				continue
			}
			totalLag += groupLag
			if groupLag > maxLag {
				maxLag = groupLag
			}
		}
	}
	out = append(out, mk("kafka_consumer_lag", totalLag))
	out = append(out, mk("kafka_consumer_lag_max", maxLag))

	ki := model.KafkaInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Role:     "broker",
		Version:  cfg.Version,
		Up:       true,
	}
	return out, ki
}

func (c *KafkaCollector) downInstance(cfg model.KafkaInstanceConfig) model.KafkaInstance {
	return model.KafkaInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node, Group: cfg.Name, Role: "broker", Version: cfg.Version, Up: false,
	}
}

// getConsumerGroupLag 计算 Consumer Group 的总 lag。
func getConsumerGroupLag(admin sarama.ClusterAdmin, client sarama.Client, group string) (float64, error) {
	partitionOffsets, err := admin.ListConsumerGroupOffsets(group, nil)
	if err != nil {
		return 0, err
	}
	var totalLag float64
	if partitionOffsets == nil {
		return 0, nil
	}
	for topic, partitions := range partitionOffsets.Blocks {
		for partitionID, block := range partitions {
			if block == nil {
				continue
			}
			latestOffset, err := client.GetOffset(topic, partitionID, sarama.OffsetNewest)
			if err != nil {
				continue
			}
			if block.Offset >= 0 && latestOffset > block.Offset {
				totalLag += float64(latestOffset - block.Offset)
			}
		}
	}
	return totalLag, nil
}

func (c *KafkaCollector) collectExporter(cfg model.KafkaInstanceConfig, now int64) ([]model.Metric, model.KafkaInstance) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", cfg.ExporterURL, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("Kafka exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("Kafka exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg)
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, normalizeRemoteAddr(cfg.Addr, ""), "kafka_", now)
	ki := model.KafkaInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node, Group: cfg.Name, Role: "broker", Version: cfg.Version, Up: true,
	}
	for _, m := range metrics {
		if m.Name == "kafka_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				ki.Version = v
			}
		}
	}
	return metrics, ki
}

// formatAddr 格式化地址用于日志。
func formatAddr(addr string) string {
	return fmt.Sprintf("kafka://%s", addr)
}
