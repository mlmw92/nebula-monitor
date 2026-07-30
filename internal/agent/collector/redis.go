package collector

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// RedisCollector 采集 Redis 实例指标，支持四种部署模式与 exporter 拉取模式。
// 每个 RedisInstanceConfig 对应一个采集目标，密码仅存本地，不上报 Server。
type RedisCollector struct {
	node     string
	instances []model.RedisInstanceConfig
}

// NewRedisCollector 创建 RedisCollector。
func NewRedisCollector(node string, instances []model.RedisInstanceConfig) *RedisCollector {
	return &RedisCollector{node: node, instances: instances}
}

// Collect 采集所有 Redis 实例指标，返回 redis_* 前缀指标与实例元信息。
func (c *RedisCollector) Collect() ([]model.Metric, []model.RedisInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.RedisInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, ri := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ri)
			continue
		}
		switch cfg.Topology {
		case "sentinel":
			m, ris := c.collectSentinel(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ris...)
		case "cluster":
			m, ris := c.collectCluster(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, ris...)
		default:
			// standalone / replication 复用单实例采集
			m, ri := c.collectStandalone(cfg, cfg.Addr, now)
			metrics = append(metrics, m...)
			instances = append(instances, ri)
		}
	}
	return metrics, instances
}

// ---- 直连采集 ----

// collectStandalone 采集单个 Redis 实例（standalone/replication 模式）。
// addr 为实际连接地址（sentinel 发现 master 时与 cfg.Addr 不同）。
func (c *RedisCollector) collectStandalone(cfg model.RedisInstanceConfig, addr string, now int64) ([]model.Metric, model.RedisInstance) {
	info, err := redisInfo(addr, cfg.Password, "all")
	if err != nil {
		slog.Warn("Redis 采集失败", "addr", addr, "err", err)
		return nil, model.RedisInstance{
			Instance: addr, Name: cfg.Name, Node: c.node,
			Role: "unknown", Topology: cfg.Topology, Up: false,
		}
	}
	repInfo, _ := redisInfo(addr, cfg.Password, "replication")
	for k, v := range repInfo {
		info[k] = v
	}
	labels := redisLabels(c.node, addr, info)
	labels["topology"] = cfg.Topology
	m := mapInfoToMetrics(info, labels, now)
	ri := model.RedisInstance{
		Instance: addr,
		Name:     cfg.Name,
		Node:     c.node,
		Role:     info["role"],
		Topology: cfg.Topology,
		Version:  info["redis_version"],
		Up:       true,
	}
	// 实例存活指标
	m = append(m, model.Metric{
		Node: c.node, Name: "redis_instance_up", Labels: labels, Value: 1, Timestamp: now,
	})
	return m, ri
}

// collectSentinel 采集哨兵自身 + 自动发现 master 并采集。
func (c *RedisCollector) collectSentinel(cfg model.RedisInstanceConfig, now int64) ([]model.Metric, []model.RedisInstance) {
	var metrics []model.Metric
	var instances []model.RedisInstance

	// 1. 采集哨兵自身
	sentInfo, err := redisInfo(cfg.Addr, cfg.Password, "sentinel")
	if err != nil {
		slog.Warn("Sentinel 采集失败", "addr", cfg.Addr, "err", err)
		instances = append(instances, model.RedisInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Role: "sentinel", Topology: cfg.Topology, Up: false,
		})
		return nil, instances
	}
	sentLabels := redisLabels(c.node, cfg.Addr, sentInfo)
	sentLabels["topology"] = cfg.Topology
	sentLabels["role"] = "sentinel"
	for k, v := range mapSentinelMetrics(sentInfo) {
		metrics = append(metrics, model.Metric{
			Node: c.node, Name: k, Labels: sentLabels, Value: v, Timestamp: now,
		})
	}
	metrics = append(metrics, model.Metric{
		Node: c.node, Name: "redis_instance_up", Labels: sentLabels, Value: 1, Timestamp: now,
	})
	instances = append(instances, model.RedisInstance{
		Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
		Role: "sentinel", Topology: cfg.Topology,
		Version: sentInfo["redis_version"], Up: true,
	})

	// 2. 发现 master 并采集
	if cfg.SentinelName == "" {
		return metrics, instances
	}
	masterAddr, err := sentinelGetMaster(cfg.Addr, cfg.Password, cfg.SentinelName)
	if err != nil {
		slog.Warn("Sentinel 发现 master 失败", "sentinel", cfg.Addr, "name", cfg.SentinelName, "err", err)
		return metrics, instances
	}
	m, ri := c.collectStandalone(cfg, masterAddr, now)
	// 覆盖 instance 标签为 master 地址，补充哨兵关联标签
	for i := range m {
		if m[i].Labels != nil {
			m[i].Labels["instance"] = masterAddr
			m[i].Labels["sentinel_master_of"] = cfg.SentinelName
		}
	}
	ri.Instance = masterAddr
	ri.Name = cfg.Name + "-master"
	ri.Topology = cfg.Topology
	metrics = append(metrics, m...)
	instances = append(instances, ri)
	return metrics, instances
}

// collectCluster 采集集群：解析拓扑 + 遍历所有 master + 集群级指标。
func (c *RedisCollector) collectCluster(cfg model.RedisInstanceConfig, now int64) ([]model.Metric, []model.RedisInstance) {
	var metrics []model.Metric
	var instances []model.RedisInstance

	// 1. CLUSTER INFO 获取集群级指标
	clusterInfo, err := redisClusterInfo(cfg.Addr, cfg.Password)
	if err != nil {
		slog.Warn("Cluster 采集失败", "addr", cfg.Addr, "err", err)
		instances = append(instances, model.RedisInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Role: "master", Topology: cfg.Topology, Up: false,
		})
		return nil, instances
	}
	clusterLabels := redisLabels(c.node, cfg.Addr, clusterInfo)
	clusterLabels["topology"] = "cluster"
	clusterLabels["role"] = "master"
	for k, v := range mapClusterMetrics(clusterInfo) {
		metrics = append(metrics, model.Metric{
			Node: c.node, Name: k, Labels: clusterLabels, Value: v, Timestamp: now,
		})
	}

	// 2. CLUSTER NODES 解析拓扑，遍历所有 master 与 replica
	masters, replicasByMaster, err := redisClusterNodes(cfg.Addr, cfg.Password)
	if err != nil {
		slog.Warn("CLUSTER NODES 解析失败", "addr", cfg.Addr, "err", err)
		return metrics, instances
	}
	for _, masterAddr := range masters {
		// 2.1 master 自身
		m, ri := c.collectStandalone(cfg, masterAddr, now)
		for i := range m {
			if m[i].Labels != nil {
				m[i].Labels["instance"] = masterAddr
				m[i].Labels["topology"] = "cluster"
			}
		}
		ri.Instance = masterAddr
		ri.Name = cfg.Name + "-" + masterAddr
		ri.Topology = cfg.Topology
		metrics = append(metrics, m...)
		instances = append(instances, ri)
		// 2.2 replicas（关联到当前 master）
		for _, replicaAddr := range replicasByMaster[masterAddr] {
			rm, rri := c.collectStandalone(cfg, replicaAddr, now)
			for i := range rm {
				if rm[i].Labels != nil {
					rm[i].Labels["instance"] = replicaAddr
					rm[i].Labels["topology"] = "cluster"
					rm[i].Labels["cluster_master_of"] = masterAddr
				}
			}
			rri.Instance = replicaAddr
			rri.Name = cfg.Name + "-slave-" + replicaAddr
			rri.Topology = cfg.Topology
			metrics = append(metrics, rm...)
			instances = append(instances, rri)
		}
	}
	return metrics, instances
}

// ---- exporter 模式 ----

// collectExporter 从 Prometheus exporter 拉取 /metrics 并解析。
func (c *RedisCollector) collectExporter(cfg model.RedisInstanceConfig, now int64) ([]model.Metric, model.RedisInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.ExporterURL)
	if err != nil {
		slog.Warn("Redis exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, model.RedisInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Role: "unknown", Topology: cfg.Topology, Up: false,
		}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("Redis exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, model.RedisInstance{
			Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
			Role: "unknown", Topology: cfg.Topology, Up: false,
		}
	}
	metrics := parsePrometheusText(string(body), c.node, cfg.Addr, now)
	ri := model.RedisInstance{
		Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
		Role: "master", Topology: cfg.Topology, Up: true,
	}
	// 从指标中提取 version 标签
	for _, m := range metrics {
		if m.Name == "redis_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				ri.Version = v
			}
			if r, ok := m.Labels["role"]; ok {
				ri.Role = r
			}
		}
	}
	return metrics, ri
}

// ---- 指标映射 ----

// redisLabels 构建基础标签集。
func redisLabels(node, addr string, info map[string]string) map[string]string {
	labels := map[string]string{
		"node":     node,
		"instance": addr,
		"role":     info["role"],
		"version":  info["redis_version"],
	}
	return labels
}

// mapInfoToMetrics 将 INFO 字段映射为 redis_* 指标。
func mapInfoToMetrics(info map[string]string, labels map[string]string, now int64) []model.Metric {
	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: labels["node"], Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	// 连接客户端
	out = append(out, mk("redis_connected_clients", parseFloat(info["connected_clients"])))
	out = append(out, mk("redis_blocked_clients", parseFloat(info["blocked_clients"])))
	// 内存
	out = append(out, mk("redis_used_memory", parseFloat(info["used_memory"])))
	out = append(out, mk("redis_used_memory_rss", parseFloat(info["used_memory_rss"])))
	out = append(out, mk("redis_used_memory_peak", parseFloat(info["used_memory_peak"])))
	out = append(out, mk("redis_maxmemory", parseFloat(info["maxmemory"])))
	if maxmem := parseFloat(info["maxmemory"]); maxmem > 0 {
		out = append(out, mk("redis_used_memory_percent", round2(parseFloat(info["used_memory"])/maxmem*100)))
	}
	out = append(out, mk("redis_memory_fragmentation_ratio", parseFloat(info["mem_fragmentation_ratio"])))
	// 命令
	out = append(out, mk("redis_ops_per_sec", parseFloat(info["instantaneous_ops_per_sec"])))
	out = append(out, mk("redis_total_commands_processed", parseFloat(info["total_commands_processed"])))
	out = append(out, mk("redis_rejected_connections", parseFloat(info["rejected_connections"])))
	// 键空间
	out = append(out, mk("redis_evicted_keys", parseFloat(info["evicted_keys"])))
	out = append(out, mk("redis_expired_keys", parseFloat(info["expired_keys"])))
	out = append(out, mk("redis_keys", parseFloat(extractDBKeys(info["db0"]))))
	// 命中率
	hits := parseFloat(info["keyspace_hits"])
	misses := parseFloat(info["keyspace_misses"])
	if hits+misses > 0 {
		out = append(out, mk("redis_hit_rate", round2(hits/(hits+misses)*100)))
	} else {
		out = append(out, mk("redis_hit_rate", 0))
	}
	// 运行时长
	out = append(out, mk("redis_uptime_in_seconds", parseFloat(info["uptime_in_seconds"])))
	// 网络
	out = append(out, mk("redis_total_connections_received", parseFloat(info["total_connections_received"])))
	out = append(out, mk("redis_net_input_bytes", parseFloat(info["total_net_input_bytes"])))
	out = append(out, mk("redis_net_output_bytes", parseFloat(info["total_net_output_bytes"])))
	// 主从复制
	if info["role"] == "master" {
		out = append(out, mk("redis_replication_offset", parseFloat(info["master_repl_offset"])))
		out = append(out, mk("redis_connected_slaves", parseFloat(info["connected_slaves"])))
	} else if info["role"] == "slave" {
		out = append(out, mk("redis_replication_offset", parseFloat(info["slave_repl_offset"])))
		out = append(out, mk("redis_replication_lag", parseFloat(info["master_last_io_seconds_ago"])))
	}
	return out
}

// mapSentinelMetrics 映射哨兵特有指标。
func mapSentinelMetrics(info map[string]string) map[string]float64 {
	return map[string]float64{
		"redis_sentinel_masters":   parseFloat(info["sentinel_masters"]),
		"redis_sentinel_slaves":    parseFloat(info["sentinel_slaves"]),
		"redis_sentinel_sentinels": parseFloat(info["sentinel_sentinels"]),
		"redis_sentinel_tilt":      parseFloat(info["sentinel_tilt"]),
	}
}

// mapClusterMetrics 映射集群级指标。
func mapClusterMetrics(info map[string]string) map[string]float64 {
	stateVal := 0.0
	if info["cluster_state"] == "ok" {
		stateVal = 1
	}
	return map[string]float64{
		"redis_cluster_state":           stateVal,
		"redis_cluster_slots_assigned":  parseFloat(info["cluster_slots_assigned"]),
		"redis_cluster_slots_ok":        parseFloat(info["cluster_slots_ok"]),
		"redis_cluster_slots_fail":      parseFloat(info["cluster_slots_fail"]),
		"redis_cluster_known_nodes":     parseFloat(info["cluster_known_nodes"]),
		"redis_cluster_size":            parseFloat(info["cluster_size"]),
	}
}

// extractDBKeys 从 INFO db0 字段（如 db0:keys=1000,expires=0,avg_ttl=0）提取键数量。
func extractDBKeys(dbField string) string {
	if dbField == "" {
		return "0"
	}
	// db0 字段格式：keys=1000,expires=0,avg_ttl=0
	parts := strings.Split(dbField, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "keys=") {
			return strings.TrimPrefix(p, "keys=")
		}
	}
	return "0"
}

// ---- 最小 RESP 协议客户端 ----

// redisInfo 连接 Redis 执行 INFO <section>，返回解析后的键值对。
func redisInfo(addr, password, section string) (map[string]string, error) {
	conn, err := dialRedis(addr, password)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := sendCommand(conn, "INFO", section)
	if err != nil {
		return nil, err
	}
	return parseInfoText(resp), nil
}

// redisClusterInfo 执行 CLUSTER INFO。
func redisClusterInfo(addr, password string) (map[string]string, error) {
	conn, err := dialRedis(addr, password)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	resp, err := sendCommand(conn, "CLUSTER", "INFO")
	if err != nil {
		return nil, err
	}
	return parseInfoText(resp), nil
}

// redisClusterNodes 执行 CLUSTER NODES，返回所有 master 节点地址列表以及 master→replicas 映射。
func redisClusterNodes(addr, password string) ([]string, map[string][]string, error) {
	conn, err := dialRedis(addr, password)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	resp, err := sendCommand(conn, "CLUSTER", "NODES")
	if err != nil {
		return nil, nil, err
	}
	masters, replicas := parseClusterTopology(resp)
	return masters, replicas, nil
}

// parseClusterTopology 解析 CLUSTER NODES 输出。
// 返回：master 节点地址列表（去重且忽略无效 addr），以及 masterAddr→[]replicaAddr 映射。
func parseClusterTopology(resp string) ([]string, map[string][]string) {
	type rawNode struct {
		id, addr, flags, masterID string
	}
	var nodes []rawNode
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Split(line, " ")
		if len(f) < 2 {
			continue
		}
		n := rawNode{id: f[0], addr: f[1]}
		if len(f) > 2 {
			n.flags = f[2]
		}
		if len(f) > 3 {
			n.masterID = f[3]
		}
		// addr 格式 host:port@cluster-port，去掉 @ 之后
		if idx := strings.Index(n.addr, "@"); idx >= 0 {
			n.addr = n.addr[:idx]
		}
		nodes = append(nodes, n)
	}
	// 先建立 master-id → master-addr 映射
	masterByID := map[string]string{}
	for _, n := range nodes {
		if !strings.Contains(n.flags, "master") {
			continue
		}
		if n.addr == "" || strings.HasPrefix(n.addr, ":") {
			continue
		}
		masterByID[n.id] = n.addr
	}
	var masters []string
	seen := map[string]bool{}
	replicasByMaster := map[string][]string{}
	for _, n := range nodes {
		if n.addr == "" || strings.HasPrefix(n.addr, ":") {
			continue
		}
		if strings.Contains(n.flags, "master") {
			if !seen[n.addr] {
				masters = append(masters, n.addr)
				seen[n.addr] = true
			}
		} else if strings.Contains(n.flags, "slave") && n.masterID != "" {
			if m, ok := masterByID[n.masterID]; ok {
				replicasByMaster[m] = append(replicasByMaster[m], n.addr)
			}
		}
	}
	return masters, replicasByMaster
}

// sentinelGetMaster 执行 SENTINEL get-master-addr-by-name <name>，返回 master 的 host:port。
func sentinelGetMaster(addr, password, name string) (string, error) {
	conn, err := dialRedis(addr, password)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	resp, err := sendCommand(conn, "SENTINEL", "get-master-addr-by-name", name)
	if err != nil {
		return "", err
	}
	// 响应为 Bulk Array：$6\r\nhost\r\n$4\r\nport\r\n
	// 解析出 host 和 port
	lines := strings.Split(resp, "\r\n")
	var parts []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "$") || strings.HasPrefix(l, "*") {
			continue
		}
		parts = append(parts, l)
	}
	if len(parts) >= 2 {
		return parts[0] + ":" + parts[1], nil
	}
	return "", fmt.Errorf("sentinel 响应格式异常")
}

// dialRedis 建立 TCP 连接并完成 AUTH。
func dialRedis(addr, password string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接 Redis 失败: %w", err)
	}
	if password != "" {
		resp, err := sendCommand(conn, "AUTH", password)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("AUTH 失败: %w", err)
		}
		// RESP 简单字符串 "+OK" 在 readResponse 中已剥去首字符 '+'，返回 "OK"。
		// AUTH 成功的唯一标准是响应为 "OK"；其余情况（如 "-ERR ..."）会在 sendCommand 处已返回 error。
		if resp != "OK" {
			conn.Close()
			return nil, fmt.Errorf("AUTH 被拒绝: %s", resp)
		}
	}
	return conn, nil
}

// sendCommand 编码 RESP 命令并读取响应。
func sendCommand(conn net.Conn, cmd ...string) (string, error) {
	// 编码：*N\r\n$len\r\ncmd\r\n...
	var sb strings.Builder
	fmt.Fprintf(&sb, "*%d\r\n", len(cmd))
	for _, c := range cmd {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(c), c)
	}
	_, err := conn.Write([]byte(sb.String()))
	if err != nil {
		return "", fmt.Errorf("发送命令失败: %w", err)
	}
	// 每次调用创建独立 reader，避免跨命令状态残留
	reader := bufio.NewReader(conn)
	return readResponse(reader)
}

// readResponse 读取 RESP 响应，返回可读字符串。
// 支持 Simple String(+)、Error(-)、Integer(:)、Bulk String($)、Array(*)。
func readResponse(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return "", fmt.Errorf("空响应")
	}
	switch line[0] {
	case '+': // Simple String
		return line[1:], nil
	case '-': // Error
		return "", fmt.Errorf("Redis 错误: %s", line[1:])
	case ':': // Integer
		return line[1:], nil
	case '$': // Bulk String
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return "", nil // nil bulk
		}
		buf := make([]byte, n+2) // +2 for \r\n
		_, err := io.ReadFull(reader, buf)
		if err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	case '*': // Array
		n, _ := strconv.Atoi(line[1:])
		if n < 0 {
			return "", nil
		}
		var parts []string
		for i := 0; i < n; i++ {
			part, err := readResponse(reader)
			if err != nil {
				return "", err
			}
			parts = append(parts, part)
		}
		return strings.Join(parts, "\r\n"), nil
	default:
		return line, nil
	}
}

// parseInfoText 解析 INFO 命令返回的文本为键值对。
func parseInfoText(text string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := line[:idx]
		val := line[idx+1:]
		out[key] = val
	}
	return out
}

// parsePrometheusText 解析 Prometheus 文本格式，仅保留 redis_ 前缀指标。
func parsePrometheusText(text, node, instance string, now int64) []model.Metric {
	var out []model.Metric
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := parsePromLine(line)
		if !ok || !strings.HasPrefix(name, "redis_") {
			continue
		}
		if labels == nil {
			labels = map[string]string{}
		}
		labels["node"] = node
		if _, exists := labels["instance"]; !exists {
			labels["instance"] = instance
		}
		out = append(out, model.Metric{
			Node: node, Name: name, Labels: labels, Value: value, Timestamp: now,
		})
	}
	return out
}

// parsePromLine 解析单行 Prometheus 指标：name{labels} value。
func parsePromLine(line string) (string, map[string]string, float64, bool) {
	// 分离 name{labels} 和 value
	var namePart, valuePart string
	braceIdx := strings.Index(line, "{")
	spaceIdx := strings.LastIndex(line, " ")
	if braceIdx >= 0 {
		// name{labels} value
		closeIdx := strings.Index(line, "}")
		if closeIdx < 0 || closeIdx >= spaceIdx {
			return "", nil, 0, false
		}
		namePart = line[:braceIdx]
		labelPart := line[braceIdx+1 : closeIdx]
		valuePart = strings.TrimSpace(line[closeIdx+1:])
		labels := parsePromLabels(labelPart)
		val, ok := parseFloatOK(valuePart)
		return namePart, labels, val, ok
	}
	// name value（无标签）
	if spaceIdx < 0 {
		return "", nil, 0, false
	}
	namePart = line[:spaceIdx]
	valuePart = strings.TrimSpace(line[spaceIdx+1:])
	val, ok := parseFloatOK(valuePart)
	return namePart, nil, val, ok
}

// parsePromLabels 解析 Prometheus 标签字符串 k1="v1",k2="v2"。
func parsePromLabels(s string) map[string]string {
	labels := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		idx := strings.Index(pair, "=")
		if idx < 0 {
			continue
		}
		key := pair[:idx]
		val := pair[idx+1:]
		// 去除引号
		val = strings.Trim(val, "\"")
		labels[key] = val
	}
	return labels
}

// parseFloat 安全解析浮点数，失败返回 0。
func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}

// parseFloatOK 解析浮点数并返回成功标志。
func parseFloatOK(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}
