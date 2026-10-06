package collector

import (
	"context"
	"database/sql"
	"fmt"
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

// Collect 采集所有 MySQL 实例指标（等价于 CollectCtx(context.Background())）。
func (c *MySQLCollector) Collect() ([]model.Metric, []model.MySQLInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 MySQL 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *MySQLCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.MySQLInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.MySQLInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("MySQL 采集被中断，跳过剩余实例", "err", err)
			break
		}
		if cfg.ExporterURL != "" {
			m, mi := c.collectExporter(ctx, cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, mi)
			continue
		}
		m, mi := c.collectDirect(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, mi)
	}
	return metrics, instances
}

// collectDirect 直连 MySQL 采集。
func (c *MySQLCollector) collectDirect(ctx context.Context, cfg model.MySQLInstanceConfig, now int64) ([]model.Metric, model.MySQLInstance) {
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/?timeout=5s&readTimeout=5s", cfg.User, cfg.Password, cfg.Addr)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		slog.Warn("MySQL 连接失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		slog.Warn("MySQL ping 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}

	// 1. SHOW GLOBAL STATUS
	status, err := queryGlobalStatus(ctx, db)
	if err != nil {
		slog.Warn("MySQL SHOW STATUS 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	// 2. SHOW GLOBAL VARIABLES（max_connections / version 等）
	vars, err := queryGlobalVariables(ctx, db)
	if err != nil {
		slog.Warn("MySQL SHOW VARIABLES 失败", "addr", cfg.Addr, "err", err)
	}
	// 3. SHOW SLAVE STATUS（复制信息）
	slave, err := querySlaveStatus(ctx, db)

	// 规范化实例地址：回环地址（127.0.0.1/localhost 等）替换为 Agent 本机真实 IP，
	// 保留端口；非回环地址（用户配置的真实 IP/域名）原样保留，与 Redis/Nginx 行为一致。
	realAddr := normalizeInstanceAddr(cfg.Addr)
	// 实例别名/分组名：未配置 name 时回退到实例地址，避免分组聚合落入 "default"。
	labelName := cfg.Name
	if labelName == "" {
		labelName = realAddr
	}

	labels := map[string]string{
		"node":     c.node,
		"instance": realAddr,
		"topology": cfg.Topology,
		"group":    labelName,
		"name":     labelName,
		"version":  vars["version"],
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
	// Group Replication：cluster 拓扑下角色只能来自组复制成员表。
	var gr grInfo
	if strings.EqualFold(cfg.Topology, "cluster") {
		// 模式与组视图先取：它决定「是否在组内」「成员是否就绪」，与角色查询相互独立。
		gr = queryGroupReplicationInfo(ctx, db)
		if grRole := queryGroupReplicationRole(ctx, db); grRole != "" {
			role = grRole
		} else {
			// 角色查询失败/无本机记录时，**不能**沿用主从回退（master）：
			// 节点刚入组、组复制重启、查询抖动等时刻会被当成"主库"，平台集群判定
			// 会因此瞬时误报「多主（疑似脑裂）」。未知就报未知（空角色）。
			role = ""
		}
		replicaOf = "" // GR 由前端 group 视图呈现，不依赖 replicaOf
	} else if grRole := queryGroupReplicationRole(ctx, db); grRole != "" {
		// 非 cluster 拓扑但实际在 GR 组里（配置未及时更新）：仍以真实角色为准。
		role = grRole
		replicaOf = ""
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
	// 平均语句响应时间（ms）：基于 performance_schema 中各语句类型的累计等待时间/次数加权威得出，
	// 反映实例处理 SQL 的真实时延，用于巡检报告「响应时间」维度。
	if lat, ok := queryMySQLStmtLatencyMs(ctx, db); ok {
		out = append(out, mk("mysql_query_latency_ms", round2(lat)))
	}

	// Group Replication 健康判定所需的附加信息。
	// 刻意不把 role/version 放进这些指标的标签里：存活指标上「仅采集成功时才存在」的标签会
	// 让 up=0/up=1 落到不同序列（已踩过坑），这里用固定的标签集 + member 维度表达组视图。
	if gr.SinglePrimaryMode != nil || gr.View != nil {
		grBase := map[string]string{
			"node":     c.node,
			"instance": realAddr,
			"topology": cfg.Topology,
			"group":    labelName,
			"name":     labelName,
			"version":  vars["version"],
		}
		grmk := func(name string, val float64, extra map[string]string) model.Metric {
			ls := make(map[string]string, len(grBase)+len(extra))
			for k, v := range grBase {
				ls[k] = v
			}
			for k, v := range extra {
				ls[k] = v
			}
			return model.Metric{Node: c.node, Name: name, Labels: ls, Value: val, Timestamp: now}
		}
		if gr.SinglePrimaryMode != nil {
			v := 0.0
			if *gr.SinglePrimaryMode {
				v = 1
			}
			out = append(out, grmk("mysql_gr_single_primary_mode", v, nil))
		}
		if gr.View != nil {
			out = append(out, grmk("mysql_gr_view_size", float64(len(gr.View)), nil))
			for host, state := range gr.View {
				out = append(out, grmk("mysql_gr_view_member", grMemberStateValue(state), map[string]string{"member": host}))
			}
		}
	}

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
	inst := normalizeInstanceAddr(cfg.Addr)
	labelName := cfg.Name
	if labelName == "" {
		labelName = inst
	}
	return model.MySQLInstance{
		Instance: inst, Name: labelName, Node: c.node,
		Role: role, Topology: cfg.Topology, Group: labelName, Up: false,
	}
}

// collectExporter 从 Prometheus exporter 拉取 /metrics。
func (c *MySQLCollector) collectExporter(ctx context.Context, cfg model.MySQLInstanceConfig, now int64) ([]model.Metric, model.MySQLInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	body, err := fetchMetrics(ctx, client, cfg.ExporterURL)
	if err != nil {
		slog.Warn("MySQL exporter 拉取失败", "target", safeExporterTarget(cfg.ExporterURL), "err", safeExporterError(err))
		return nil, c.downInstance(cfg, "unknown")
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, normalizeInstanceAddr(cfg.Addr), "mysql_", now)
	inst := normalizeInstanceAddr(cfg.Addr)
	labelName := cfg.Name
	if labelName == "" {
		labelName = inst
	}
	up := exporterHealth(string(body), len(metrics) > 0, "mysql_up", "mysql_instance_up")
	mi := model.MySQLInstance{
		Instance: inst, Name: labelName, Node: c.node,
		Role: "master", Topology: cfg.Topology, Group: labelName, Up: up,
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

// queryMySQLStmtLatencyMs 返回实例平均语句响应时间（毫秒）。
// 基于 performance_schema.events_statements_summary_by_digest 的累计等待时间/语句数加权得出，
// 即全部 SQL 类型的平均执行时延。需要 performance_schema 启用且当前用户可读该表；
// 否则（表不存在/无权限/未启用）返回 ok=false，由调用方决定是否上报该指标。
func queryMySQLStmtLatencyMs(ctx context.Context, db *sql.DB) (float64, bool) {
	var avgMs float64
	// SUM_TIMER_WAIT 以皮秒为单位，1ms = 1e9 ps。
	query := `SELECT COALESCE(SUM(SUM_TIMER_WAIT)/NULLIF(SUM(COUNT_STAR),0)/1000000000.0, 0)
		FROM performance_schema.events_statements_summary_by_digest`
	if err := db.QueryRowContext(ctx, query).Scan(&avgMs); err != nil {
		return 0, false
	}
	return avgMs, true
}

// queryGroupReplicationRole 查询本节点在 Group Replication 中的角色（PRIMARY/SECONDARY）。
// 非 GR 实例无本机记录或权限不足，返回空字符串。
func queryGroupReplicationRole(ctx context.Context, db *sql.DB) string {
	rows, err := db.QueryContext(ctx, `SELECT MEMBER_ROLE FROM performance_schema.replication_group_members WHERE MEMBER_ID = (SELECT @@server_uuid)`)
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

// grInfo 是本节点视角的 Group Replication 附加信息。
//
// 为什么需要它：仅凭「角色」无法判断集群是否健康——单主模式下出现多个 PRIMARY 是脑裂，
// 而多主模式（group_replication_single_primary_mode=OFF）下全部成员都是 PRIMARY 是**正常**形态，
// 必须结合模式才能下结论；另外「各节点看到的成员集合是否一致」是比数主库个数更直接的脑裂信号
// （全量重启时每个节点各自 bootstrap 成单成员组，角色看都是 PRIMARY，但视图各不相同）。
type grInfo struct {
	// SinglePrimaryMode 为单主模式时为 true，多主模式为 false，nil 表示未知（未启用 GR 或无权限）。
	SinglePrimaryMode *bool
	// View 是本节点在 replication_group_members 中看到的成员集合：MEMBER_HOST -> MEMBER_STATE。
	View map[string]string
}

// queryGroupReplicationInfo 采集 GR 模式与组视图。
// 非 GR 实例（插件未装/权限不足）各项为空，调用方不应上报相关指标。
func queryGroupReplicationInfo(ctx context.Context, db *sql.DB) grInfo {
	var info grInfo

	// 单主/多主模式：插件未加载时该变量不存在，查询报错即视为未知。
	var mode string
	if err := db.QueryRowContext(ctx, `SELECT @@group_replication_single_primary_mode`).Scan(&mode); err == nil {
		switch strings.ToUpper(strings.TrimSpace(mode)) {
		case "ON", "1":
			v := true
			info.SinglePrimaryMode = &v
		case "OFF", "0":
			v := false
			info.SinglePrimaryMode = &v
		}
	}

	// 组视图：本节点看到的有哪些成员、各自什么状态。
	rows, err := db.QueryContext(ctx, `SELECT MEMBER_HOST, MEMBER_STATE FROM performance_schema.replication_group_members`)
	if err != nil {
		return info
	}
	defer rows.Close()
	for rows.Next() {
		var host, state string
		if err := rows.Scan(&host, &state); err != nil {
			continue
		}
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if info.View == nil {
			info.View = map[string]string{}
		}
		info.View[host] = strings.ToUpper(strings.TrimSpace(state))
	}
	return info
}

// grMemberStateValue 把 GR 成员状态映射为数值，便于用时序指标表达「成员集合 + 状态」：
// ONLINE=1、RECOVERING=0.5、其余（OFFLINE/ERROR/UNREACHABLE 等）=0。
// 映射表与「指标目录」中的说明保持一致。
func grMemberStateValue(state string) float64 {
	switch state {
	case "ONLINE":
		return 1
	case "RECOVERING":
		return 0.5
	default:
		return 0
	}
}

// queryGlobalStatus 执行 SHOW GLOBAL STATUS，返回 Variable_name→Value 映射。
func queryGlobalStatus(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL STATUS")
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
func queryGlobalVariables(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW GLOBAL VARIABLES")
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
func querySlaveStatus(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SHOW SLAVE STATUS")
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
//
// 这样即使 Agent 用 127.0.0.1 连接本机 MySQL，监控面板也展示可识别的真实地址。
// normalizeInstanceAddr 规范化 MySQL 实例地址，默认端口 3306。
func normalizeInstanceAddr(addr string) string {
	return normalizeRemoteAddr(addr, "3306")
}
