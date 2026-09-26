package collector

import (
	"context"
	"log/slog"
	"net"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// FastDFSCollector 采集 FastDFS 实例，支持两种模式：
//   - exporter 模式：抓取 fastdfs_exporter 的 /metrics（ExporterURL 非空）
//   - 存活探测模式：直连 TCP 端口（tracker 22122 / storage 23000）判定可达性
//
// FastDFS 无标准直连接口，完整指标（空间/IO/Storage 状态）依赖 fastdfs_exporter。
type FastDFSCollector struct {
	node      string
	instances []model.FastDFSInstanceConfig
}

// NewFastDFSCollector 构造 FastDFS 采集器。
func NewFastDFSCollector(node string, instances []model.FastDFSInstanceConfig) *FastDFSCollector {
	return &FastDFSCollector{node: node, instances: instances}
}

// Collect 遍历所有实例采集指标与实例元信息（等价于 CollectCtx(context.Background())）。
func (c *FastDFSCollector) Collect() ([]model.Metric, []model.FastDFSInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 遍历所有实例采集指标与实例元信息；ctx 取消或超时后停止采集剩余实例。
func (c *FastDFSCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.FastDFSInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.FastDFSInstance
	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("FastDFS 采集被中断，跳过剩余实例", "err", err)
			break
		}
		if cfg.ExporterURL != "" {
			m, fi := c.collectExporter(ctx, cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, fi)
			continue
		}
		m, fi := c.collectLiveness(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, fi)
	}
	return metrics, instances
}

func fastDFSInstanceMeta(c *FastDFSCollector, cfg model.FastDFSInstanceConfig) model.FastDFSInstance {
	return model.FastDFSInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, cfgRolePort(cfg.Role)),
		Name:     cfg.Name,
		Node:     c.node,
		Role:     cfg.Role,
		Group:    cfg.Group,
	}
}

func cfgRolePort(role string) string {
	if role == "storage" {
		return "23000"
	}
	return "22122"
}

func (c *FastDFSCollector) collectExporter(ctx context.Context, cfg model.FastDFSInstanceConfig, now int64) ([]model.Metric, model.FastDFSInstance) {
	inst := fastDFSInstanceMeta(c, cfg)
	body, err := fetchMetricsText(ctx, nil, cfg.ExporterURL)
	if err != nil {
		slog.Warn("FastDFS exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		inst.Up = false
		return nil, inst
	}
	metrics := parsePrometheusTextWithPrefix(body, c.node, inst.Instance, "fastdfs_", now)
	inst.Up = true
	for _, m := range metrics {
		switch m.Name {
		case "fastdfs_up":
			inst.Up = m.Value > 0.5
		case "fastdfs_storage_count":
			inst.StorageTotal = m.Value
		case "fastdfs_storage_online_count":
			inst.StorageOnline = m.Value
		case "fastdfs_storage_offline_count":
			inst.StorageOffline = m.Value
		case "fastdfs_group_count":
			inst.GroupTotal = m.Value
		case "fastdfs_total_space":
			inst.TotalSpaceMB = m.Value / 1024 / 1024
		case "fastdfs_free_space":
			inst.FreeSpaceMB = m.Value / 1024 / 1024
		case "fastdfs_used_space":
			inst.UsedSpaceMB = m.Value / 1024 / 1024
		case "fastdfs_trunk_free_space":
			inst.TrunkFreeMB = m.Value / 1024 / 1024
		case "fastdfs_disk_read_bytes":
			inst.DiskReadMB = m.Value / 1024 / 1024
		case "fastdfs_disk_write_bytes":
			inst.DiskWriteMB = m.Value / 1024 / 1024
		case "fastdfs_net_recv_bytes":
			inst.NetRecvMB = m.Value / 1024 / 1024
		case "fastdfs_net_sent_bytes":
			inst.NetSentMB = m.Value / 1024 / 1024
		}
	}
	return metrics, inst
}

func (c *FastDFSCollector) collectLiveness(ctx context.Context, cfg model.FastDFSInstanceConfig, now int64) ([]model.Metric, model.FastDFSInstance) {
	inst := fastDFSInstanceMeta(c, cfg)
	base := map[string]string{
		"node":     c.node,
		"instance": inst.Instance,
		"addr":     cfg.Addr,
		"role":     cfg.Role,
		"group":    cfg.Group,
	}
	up := 0.0
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", cfg.Addr)
	if err == nil {
		_ = conn.Close()
		up = 1
		inst.Up = true
	} else {
		slog.Warn("FastDFS 存活探测失败", "addr", cfg.Addr, "err", err)
		inst.Up = false
	}
	return []model.Metric{fastdfsMetric(c.node, "fastdfs_up", up, base, now)}, inst
}

func fastdfsMetric(node, name string, value float64, labels map[string]string, now int64) model.Metric {
	if labels == nil {
		labels = map[string]string{}
	}
	return model.Metric{Node: node, Name: name, Labels: labels, Value: value, Timestamp: now}
}
