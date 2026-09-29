package collector

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// NacosCollector 采集 Nacos 实例存活状态，走控制台健康检查接口：
//   - GET {addr}{contextPath}/v1/console/health/readiness → 200 即就绪
//
// Nacos 的丰富指标需开启 2.x actuator prometheus（可后续经 exporter 模式扩展），
// 此处先保证「存活监控 + 告警」这一核心诉求。
type NacosCollector struct {
	node      string
	instances []model.NacosInstanceConfig
}

// NewNacosCollector 创建 NacosCollector。
func NewNacosCollector(node string, instances []model.NacosInstanceConfig) *NacosCollector {
	return &NacosCollector{node: node, instances: instances}
}

// Collect 采集所有 Nacos 实例指标（等价于 CollectCtx(context.Background())）。
func (c *NacosCollector) Collect() ([]model.Metric, []model.NacosInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 Nacos 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *NacosCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.NacosInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.NacosInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("Nacos 采集被中断，跳过剩余实例", "err", err)
			break
		}
		m, ni := c.collectOne(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, ni)
	}
	return metrics, instances
}

func (c *NacosCollector) collectOne(ctx context.Context, cfg model.NacosInstanceConfig, now int64) ([]model.Metric, model.NacosInstance) {
	cp := cfg.ContextPath
	if cp == "" {
		cp = "/nacos"
	}
	if !strings.HasPrefix(cp, "/") {
		cp = "/" + cp
	}
	cp = strings.TrimSuffix(cp, "/")

	ni := model.NacosInstance{
		Instance: urlHostPort(cfg.Addr),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	up := 0.0
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+cfg.Addr+cp+"/v1/console/health/readiness", nil)
	if err == nil {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				up = 1
			}
		} else {
			slog.Warn("Nacos 健康检查失败", "addr", cfg.Addr, "err", err)
		}
	} else {
		slog.Warn("Nacos 请求构造失败", "addr", cfg.Addr, "err", err)
	}
	ni.Up = up == 1

	metric := model.Metric{
		Node: c.node, Name: "nacos_instance_up", Value: up, Timestamp: now,
		Labels: map[string]string{
			"node":     c.node,
			"instance": ni.Instance,
			"name":     cfg.Name,
			"group":    cfg.Name,
			"role":     "server",
		},
	}
	return []model.Metric{metric}, ni
}
