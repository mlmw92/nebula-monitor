package collector

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/nebula/monitor/internal/model"
)

// MySQLCollector 采集 MySQL 实例指标，支持直连与 exporter 双模式。
// 密码仅存本地不上报。
type MySQLCollector struct {
	node      string
	instances []model.MySQLInstanceConfig
}

// NewMySQLCollector 创建 MySQLCollector。
func NewMySQLCollector(node string, instances []model.MySQLInstanceConfig) *MySQLCollector {
	return &MySQLCollector{node: node, instances: instances}
}

// Collect 采集所有 MySQL 实例指标。
func (c *MySQLCollector) Collect() ([]model.Metric, []model.MySQLInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.MySQLInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, mi := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, mi)
			continue
		}
		m, mi := c.collectDirect(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, mi)
	}
	return metrics, instances
}

// collectDirect 直连 MySQL 采集。
func (c *MySQLCollector) collectDirect(cfg model.MySQLInstanceConfig, now int64) ([]model.Metric, model.MySQLInstance) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/?timeout=5s&readTimeout=5s", cfg.User, cfg.Password, cfg.Addr)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		slog.Warn("MySQL 连接失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		slog.Warn("MySQL ping 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}

	// 1. SHOW GLOBAL STATUS
	status, err := queryGlobalStatus(db)
	if err != nil {
		slog.Warn("MySQL SHOW STATUS 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	// 2. SHOW GLOBAL VARIABLES（max_connections / version 等）
	vars, err := queryGlobalVariables(db)
	if err != nil {
		slog.Warn("MySQL SHOW VARIABLES 失败", "addr", cfg.Addr, "err", err)
	}
	// 3. SHOW SLAVE STATUS（复制信息）
	slave, err := querySlaveStatus(db)

	// 规范化实例地址：回环地址（127.0.0.1/localhost 等）替换为 Agent 本机真实 IP，
	// 保留端口；非回环地址（用户配置的真实 IP/域名）原样保留，与 Redis/Nginx 行为一致。
	realAddr := normalizeInstanceAddr(cfg.Addr)

	labels := map[string]string{
		"node":      c.node,
		"instance":  realAddr,
		"topology":  cfg.Topology,
		"group":     cfg.Name,
		"name":      cfg.Name,
		"version":   vars["version"],
	}
	role := "master"
	replicaOf := ""
	if slave != nil {
		if ioRunning, ok := slave["Slave_IO_Running"]; ok && ioRunning == "Yes" {
			role = "slave"
		if masterHost, ok := slave["Master_Host"]; ok {
			replicaOf = masterHost
			if masterPort, ok2 := slave["Master_Port"]; ok2 {
				replicaOf = masterHost + ":" + masterPort
			}
			replicaOf = normalizeInstanceAddr(replicaOf)
		}
		}
	}
	// Group Replication：优先使用成员真实角色（cluster 拓扑）。
	// 非 GR 实例无本机记录或权限不足，queryGroupReplicationRole 返回空，不影响主从判定。
	if grRole := queryGroupReplicationRole(db); grRole != "" {
		role = grRole
		replicaOf = "" // GR 由前端 group 视图呈现，不依赖 replicaOf
	}
	labels["role"] = role
	if replicaOf != "" {
		labels["replica_of"] = replicaOf
	}

	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	out = append(out, mk("mysql_instance_up", 1))
	out = append(out, mk("mysql_threads_connected", parseFloat(status["Threads_connected"])))
	out = append(out, mk("mysql_threads_running", parseFloat(status["Threads_running"])))
	out = append(out, mk("mysql_max_connections", parseFloat(vars["max_connections"])))
	out = append(out, mk("mysql_connection_errors_total", parseFloat(status["Connection_errors_max_connections"])))
	// QPS = Questions / Uptime
	questions := parseFloat(status["Questions"])
	uptime := parseFloat(status["Uptime"])
	if uptime > 0 {
		out = append(out, mk("mysql_queries_per_sec", round2(questions/uptime)))
	}
	out = append(out, mk("mysql_slow_queries", parseFloat(status["Slow_queries"])))
	// InnoDB 缓冲池命中率
	readReq := parseFloat(status["Innodb_buffer_pool_read_requests"])
	reads := parseFloat(status["Innodb_buffer_pool_reads"])
	if readReq > 0 {
		out = append(out, mk("mysql_innodb_buffer_pool_hit_rate", round2((1-reads/readReq)*100)))
	}
	out = append(out, mk("mysql_innodb_buffer_pool_size", parseFloat(vars["innodb_buffer_pool_size"])))
	out = append(out, mk("mysql_innodb_row_lock_waits", parseFloat(status["Innodb_row_lock_waits"])))
	out = append(out, mk("mysql_innodb_deadlocks", parseFloat(status["Innodb_deadlocks"])))
	// 复制
	if slave != nil {
		ioVal := 0.0
		if slave["Slave_IO_Running"] == "Yes" {
			ioVal = 1
		}
		sqlVal := 0.0
		if slave["Slave_SQL_Running"] == "Yes" {
			sqlVal = 1
		}
		out = append(out, mk("mysql_slave_io_running", ioVal))
		out = append(out, mk("mysql_slave_sql_running", sqlVal))
		out = append(out, mk("mysql_seconds_behind_master", parseFloat(slave["Seconds_Behind_Master"])))
	}
	// 事务
	out = append(out, mk("mysql_com_commit", parseFloat(status["Com_commit"])))
	out = append(out, mk("mysql_com_rollback", parseFloat(status["Com_rollback"])))
	// InnoDB 行操作
	out = append(out, mk("mysql_innodb_rows_read", parseFloat(status["Innodb_rows_read"])))
	out = append(out, mk("mysql_innodb_rows_inserted", parseFloat(status["Innodb_rows_inserted"])))
	out = append(out, mk("mysql_innodb_rows_updated", parseFloat(status["Innodb_rows_updated"])))
	out = append(out, mk("mysql_innodb_rows_deleted", parseFloat(status["Innodb_rows_deleted"])))
	// 网络
	out = append(out, mk("mysql_bytes_received", parseFloat(status["Bytes_received"])))
	out = append(out, mk("mysql_bytes_sent", parseFloat(status["Bytes_sent"])))
	// 临时表
	out = append(out, mk("mysql_created_tmp_disk_tables", parseFloat(status["Created_tmp_disk_tables"])))
	// 运行时长
	out = append(out, mk("mysql_uptime", uptime))

	mi := model.MySQLInstance{
		Instance:  realAddr,
		Name:      cfg.Name,
		Node:      c.node,
		Role:      role,
		Topology:  cfg.Topology,
		Group:     cfg.Name,
		ReplicaOf: replicaOf,
		Version:   vars["version"],
		Up:        true,
	}
	return out, mi
}

// downInstance 构造一个不可达的实例元信息。
func (c *MySQLCollector) downInstance(cfg model.MySQLInstanceConfig, role string) model.MySQLInstance {
	return model.MySQLInstance{
		Instance: normalizeInstanceAddr(cfg.Addr), Name: cfg.Name, Node: c.node,
		Role: role, Topology: cfg.Topology, Group: cfg.Name, Up: false,
	}
}

// collectExporter 从 Prometheus exporter 拉取 /metrics。
func (c *MySQLCollector) collectExporter(cfg model.MySQLInstanceConfig, now int64) ([]model.Metric, model.MySQLInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.ExporterURL)
	if err != nil {
		slog.Warn("MySQL exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("MySQL exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, normalizeInstanceAddr(cfg.Addr), "mysql_", now)
	mi := model.MySQLInstance{
		Instance: normalizeInstanceAddr(cfg.Addr), Name: cfg.Name, Node: c.node,
		Role: "master", Topology: cfg.Topology, Group: cfg.Name, Up: true,
	}
	for _, m := range metrics {
		if m.Name == "mysql_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				mi.Version = v
			}
			if r, ok := m.Labels["role"]; ok {
				mi.Role = r
			}
		}
	}
	return metrics, mi
}

// queryGroupReplicationRole 查询本节点在 Group Replication 中的角色（PRIMARY/SECONDARY）。
// 非 GR 实例无本机记录或权限不足，返回空字符串。
func queryGroupReplicationRole(db *sql.DB) string {
	rows, err := db.Query(`SELECT MEMBER_ROLE FROM performance_schema.replication_group_members WHERE MEMBER_ID = (SELECT @@server_uuid)`)
	if err != nil {
		return ""
	}
	defer rows.Close()
	if rows.Next() {
		var r string
		if err := rows.Scan(&r); err == nil {
			return strings.ToLower(strings.TrimSpace(r))
		}
	}
	return ""
}

// queryGlobalStatus 执行 SHOW GLOBAL STATUS，返回 Variable_name→Value 映射。
func queryGlobalStatus(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SHOW GLOBAL STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// queryGlobalVariables 执行 SHOW GLOBAL VARIABLES。
func queryGlobalVariables(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SHOW GLOBAL VARIABLES")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			continue
		}
		out[k] = v
	}
	return out, nil
}

// querySlaveStatus 执行 SHOW SLAVE STATUS，返回第一行的列名→值映射。
// 非 slave 或无复制时返回 nil。
func querySlaveStatus(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SHOW SLAVE STATUS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return nil, nil // 非 slave
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i, col := range cols {
		if vals[i].Valid {
			out[col] = vals[i].String
		}
	}
	return out, nil
}

// parsePrometheusTextWithPrefix 解析 Prometheus 文本，仅保留指定前缀指标。
func parsePrometheusTextWithPrefix(text, node, instance, prefix string, now int64) []model.Metric {
	var out []model.Metric
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, labels, value, ok := parsePromLine(line)
		if !ok || !strings.HasPrefix(name, prefix) {
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

// normalizeInstanceAddr 规范化 MySQL 实例地址：
//   - host 为回环地址（127.0.0.1/localhost/::1 等）时，替换为 Agent 本机真实 IP，保留端口；
//   - 非回环地址（用户配置的真实 IP/域名）原样保留，与 Redis/Nginx 行为一致。
// 这样即使 Agent 用 127.0.0.1 连接本机 MySQL，监控面板也展示可识别的真实地址。
// normalizeInstanceAddr 规范化 MySQL 实例地址，默认端口 3306。
func normalizeInstanceAddr(addr string) string {
	return normalizeRemoteAddr(addr, "3306")
}
