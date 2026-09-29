package collector

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// errZKEmptyMntr 表示 mntr 无有效输出（四字命令可能被禁用）。
var errZKEmptyMntr = errors.New("mntr 返回为空（四字命令可能被禁用，需开启 4lw.commands.whitelist=mntr）")

// netDialer 统一的拨号器（5 秒超时）。
func netDialer() *net.Dialer { return &net.Dialer{Timeout: 5 * time.Second} }

// parseF 安全转 float，空串/非法值为 0（复用包内 parseFloatOK）。
func parseF(s string) float64 {
	v, _ := parseFloatOK(strings.TrimSpace(s))
	return v
}

// upMetric 生成实例存活指标。
func (c *ZooKeeperCollector) upMetric(cfg model.ZooKeeperInstanceConfig, instance string, up float64, now int64) model.Metric {
	return model.Metric{
		Node: c.node, Name: "zookeeper_instance_up", Value: up, Timestamp: now,
		Labels: map[string]string{
			"node":     c.node,
			"instance": instance,
			"name":     cfg.Name,
			"group":    cfg.Name,
			"role":     "",
		},
	}
}

// ZooKeeperCollector 采集 ZooKeeper 实例指标，走四字命令 mntr（TCP 直连，
// 无需第三方 exporter）。要求服务端未禁用四字命令（默认开启）。
//
// mntr 返回 zk_ 前缀的 key=value 行，如 zk_version / zk_server_state /
// zk_avg_latency / zk_followers 等。
type ZooKeeperCollector struct {
	node      string
	instances []model.ZooKeeperInstanceConfig
}

// NewZooKeeperCollector 创建 ZooKeeperCollector。
func NewZooKeeperCollector(node string, instances []model.ZooKeeperInstanceConfig) *ZooKeeperCollector {
	return &ZooKeeperCollector{node: node, instances: instances}
}

// Collect 采集所有 ZooKeeper 实例指标（等价于 CollectCtx(context.Background())）。
func (c *ZooKeeperCollector) Collect() ([]model.Metric, []model.ZooKeeperInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 ZooKeeper 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *ZooKeeperCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.ZooKeeperInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.ZooKeeperInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("ZooKeeper 采集被中断，跳过剩余实例", "err", err)
			break
		}
		m, zi := c.collectOne(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, zi)
	}
	return metrics, instances
}

func (c *ZooKeeperCollector) collectOne(ctx context.Context, cfg model.ZooKeeperInstanceConfig, now int64) ([]model.Metric, model.ZooKeeperInstance) {
	zi := model.ZooKeeperInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""),
		Name:     cfg.Name,
		Node:     c.node,
		Group:    cfg.Name,
		Up:       false,
	}
	down := func() ([]model.Metric, model.ZooKeeperInstance) {
		return []model.Metric{c.upMetric(cfg, zi.Instance, 0, now)}, zi
	}

	info, err := zookeeperMntr(ctx, cfg.Addr)
	if err != nil {
		slog.Warn("ZooKeeper mntr 失败", "addr", cfg.Addr, "err", err)
		return down()
	}

	zi.Version = info["zk_version"]
	zi.Role = info["zk_server_state"]
	zi.AvgLatency = parseF(info["zk_avg_latency"])
	zi.OutstandingRequests = parseF(info["zk_outstanding_requests"])
	zi.AliveConnections = parseF(info["zk_num_alive_connections"])
	zi.ZnodeCount = parseF(info["zk_znode_count"])
	zi.Followers = parseF(info["zk_followers"])
	zi.SyncedFollowers = parseF(info["zk_synced_followers"])
	zi.Up = true

	labels := map[string]string{
		"node":     c.node,
		"instance": zi.Instance,
		"name":     cfg.Name,
		"group":    cfg.Name,
		"role":     zi.Role,
		"version":  zi.Version,
	}
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	out := []model.Metric{
		mk("zookeeper_instance_up", 1),
		mk("zookeeper_avg_latency", zi.AvgLatency),
		mk("zookeeper_outstanding_requests", zi.OutstandingRequests),
		mk("zookeeper_alive_connections", zi.AliveConnections),
		mk("zookeeper_znode_count", zi.ZnodeCount),
	}
	// followers 类指标仅 leader/standalone 角色存在
	if _, ok := info["zk_followers"]; ok {
		out = append(out,
			mk("zookeeper_followers", zi.Followers),
			mk("zookeeper_synced_followers", zi.SyncedFollowers),
		)
	}
	return out, zi
}

// zookeeperMntr 建立 TCP 连接发送 mntr 四字命令，解析 zk_ 前缀 key=value 行。
func zookeeperMntr(ctx context.Context, addr string) (map[string]string, error) {
	d := netDialer()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	// 有界读取：mntr 输出很小，deadline 兜底防服务端不支持四字命令时挂住
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("mntr\r\n")); err != nil {
		return nil, err
	}
	info := map[string]string{}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "zk_") {
			continue
		}
		if eq := strings.Index(line, "="); eq > 0 {
			info[line[:eq]] = line[eq+1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(info) == 0 {
		return nil, errZKEmptyMntr
	}
	return info, nil
}
