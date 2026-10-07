package collector

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/nebula/monitor/internal/model"
)

// PostgresCollector 采集 PostgreSQL 实例指标，支持直连与 exporter 双模式。
type PostgresCollector struct {
	node      string
	instances []model.PostgresInstanceConfig
}

// NewPostgresCollector 创建 PostgresCollector。
func NewPostgresCollector(node string, instances []model.PostgresInstanceConfig) *PostgresCollector {
	return &PostgresCollector{node: node, instances: instances}
}

// Collect 采集所有 PostgreSQL 实例指标（等价于 CollectCtx(context.Background())）。
func (c *PostgresCollector) Collect() ([]model.Metric, []model.PostgresInstance) {
	return c.CollectCtx(context.Background())
}

// CollectCtx 采集所有 PostgreSQL 实例指标；ctx 取消或超时后停止采集剩余实例。
func (c *PostgresCollector) CollectCtx(ctx context.Context) ([]model.Metric, []model.PostgresInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.PostgresInstance

	for _, cfg := range c.instances {
		if err := ctx.Err(); err != nil {
			slog.Warn("PostgreSQL 采集被中断，跳过剩余实例", "err", err)
			break
		}
		if cfg.ExporterURL != "" {
			m, pi := c.collectExporter(ctx, cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, pi)
			continue
		}
		m, pi := c.collectDirect(ctx, cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, pi)
	}
	return metrics, instances
}

// collectDirect 直连 PostgreSQL 采集。
func (c *PostgresCollector) collectDirect(ctx context.Context, cfg model.PostgresInstanceConfig, now int64) ([]model.Metric, model.PostgresInstance) {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	host, port := splitHostPort(cfg.Addr, "5432")
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
		host, port, cfg.User, cfg.Password, cfg.Database, sslMode)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		slog.Warn("PostgreSQL 连接失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		slog.Warn("PostgreSQL ping 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}

	// 1. pg_stat_database（当前连接的库）
	dbStat, err := queryPGStatDatabase(ctx, db, cfg.Database)
	if err != nil {
		slog.Warn("pg_stat_database 查询失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	// 2. SHOW max_connections / server_version
	settings, _ := queryPGSettings(ctx, db)
	// 3. pg_stat_replication（复制延迟）
	replLag, replState, isStandby, _ := queryPGReplication(ctx, db)

	role := "master"
	if isStandby {
		role = "standby"
	}
	labels := map[string]string{
		"node":     c.node,
		"instance": normalizeRemoteAddr(cfg.Addr, ""),
		"role":     role,
		"topology": cfg.Topology,
		"group":    cfg.Name,
		"name":     cfg.Name,
		"version":  settings["server_version"],
		"database": cfg.Database,
	}
	// 4. standby 上取主库地址——主从关系是"依赖"这类关联里唯一有**直接证据**的来源，
	//    而 PostgresInstance.ReplicaOf 此前没有任何采集器填它，于是"副本 → 主库"这条边
	//    永远建不起来（redis/mysql 早就填了）。数据就在 pg_stat_wal_receiver.conninfo 里。
	//    取不到（不是 standby、权限不足、连接串缺 host/port）就留空：宁可不建边，不猜。
	replicaOf := ""
	if isStandby {
		if primary := queryPGPrimaryAddr(ctx, db); primary != "" {
			replicaOf = primary
			labels["replica_of"] = primary
		}
	}

	mk := func(name string, val float64) model.Metric {
		return model.Metric{Node: c.node, Name: name, Labels: labels, Value: val, Timestamp: now}
	}

	var out []model.Metric
	out = append(out, mk("postgres_instance_up", 1))
	out = append(out, mk("postgres_numbackends", parseFloat(dbStat["numbackends"])))
	out = append(out, mk("postgres_max_connections", parseFloat(settings["max_connections"])))
	out = append(out, mk("postgres_active_connections", parseFloat(dbStat["numbackends"])))
	out = append(out, mk("postgres_xact_commit", parseFloat(dbStat["xact_commit"])))
	out = append(out, mk("postgres_xact_rollback", parseFloat(dbStat["xact_rollback"])))
	out = append(out, mk("postgres_tup_returned", parseFloat(dbStat["tup_returned"])))
	out = append(out, mk("postgres_tup_fetched", parseFloat(dbStat["tup_fetched"])))
	out = append(out, mk("postgres_tup_inserted", parseFloat(dbStat["tup_inserted"])))
	out = append(out, mk("postgres_tup_updated", parseFloat(dbStat["tup_updated"])))
	out = append(out, mk("postgres_tup_deleted", parseFloat(dbStat["tup_deleted"])))
	blksHit := parseFloat(dbStat["blks_hit"])
	blksRead := parseFloat(dbStat["blks_read"])
	out = append(out, mk("postgres_blks_hit", blksHit))
	out = append(out, mk("postgres_blks_read", blksRead))
	if blksHit+blksRead > 0 {
		out = append(out, mk("postgres_cache_hit_ratio", round2(blksHit/(blksHit+blksRead)*100)))
	} else {
		out = append(out, mk("postgres_cache_hit_ratio", 0))
	}
	out = append(out, mk("postgres_deadlocks", parseFloat(dbStat["deadlocks"])))
	if replLag >= 0 {
		out = append(out, mk("postgres_replication_lag_bytes", replLag))
	}
	if replState != "" {
		labels["replication_state"] = replState
	}
	// 数据库大小
	if size, err := queryPGDatabaseSize(ctx, db, cfg.Database); err == nil {
		out = append(out, mk("postgres_database_size_bytes", float64(size)))
	}
	// 运行时长
	if uptime, err := queryPGUptime(ctx, db); err == nil {
		out = append(out, mk("postgres_uptime_seconds", uptime))
	}
	// 平均语句响应时间（ms）：基于 pg_stat_statements 的累计执行时间/调用次数加权得出，
	// 反映实例处理 SQL 的真实时延，用于巡检报告「响应时间」维度。
	if lat, ok := queryPGStmtLatencyMs(ctx, db, settings["server_version"]); ok {
		out = append(out, mk("postgres_query_latency_ms", round2(lat)))
	}

	pi := model.PostgresInstance{
		Instance:  normalizeRemoteAddr(cfg.Addr, ""),
		Name:      cfg.Name,
		Node:      c.node,
		Role:      role,
		Topology:  cfg.Topology,
		Group:     cfg.Name,
		ReplicaOf: replicaOf,
		Version:   settings["server_version"],
		Database:  cfg.Database,
		Up:        true,
	}
	return out, pi
}

func (c *PostgresCollector) downInstance(cfg model.PostgresInstanceConfig, role string) model.PostgresInstance {
	return model.PostgresInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node,
		Role: role, Topology: cfg.Topology, Group: cfg.Name, Database: cfg.Database, Up: false,
	}
}

func (c *PostgresCollector) collectExporter(ctx context.Context, cfg model.PostgresInstanceConfig, now int64) ([]model.Metric, model.PostgresInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	body, err := fetchMetrics(ctx, client, cfg.ExporterURL)
	if err != nil {
		slog.Warn("PostgreSQL exporter 拉取失败", "target", safeExporterTarget(cfg.ExporterURL), "err", safeExporterError(err))
		return nil, c.downInstance(cfg, "unknown")
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, normalizeRemoteAddr(cfg.Addr, ""), "postgres_", now)
	up := exporterHealth(string(body), len(metrics) > 0, "pg_up", "postgres_up", "postgres_instance_up")
	pi := model.PostgresInstance{
		Instance: normalizeRemoteAddr(cfg.Addr, ""), Name: cfg.Name, Node: c.node,
		Role: "master", Topology: cfg.Topology, Group: cfg.Name, Database: cfg.Database, Up: up,
	}
	for _, m := range metrics {
		if m.Name == "postgres_instance_up" && m.Labels != nil {
			if v, ok := m.Labels["version"]; ok {
				pi.Version = v
			}
			if r, ok := m.Labels["role"]; ok {
				pi.Role = r
			}
		}
	}
	return metrics, pi
}

// queryPGStatDatabase 查询 pg_stat_database 中指定库的统计。
func queryPGStatDatabase(ctx context.Context, db *sql.DB, dbName string) (map[string]string, error) {
	query := `SELECT numbackends, xact_commit, xact_rollback, tup_returned, tup_fetched,
		tup_inserted, tup_updated, tup_deleted, blks_hit, blks_read, deadlocks
		FROM pg_stat_database WHERE datname = $1`
	row := db.QueryRowContext(ctx, query, dbName)
	var numbackends, xactCommit, xactRollback, tupReturned, tupFetched,
		tupInserted, tupUpdated, tupDeleted, blksHit, blksRead, deadlocks int64
	err := row.Scan(&numbackends, &xactCommit, &xactRollback, &tupReturned, &tupFetched,
		&tupInserted, &tupUpdated, &tupDeleted, &blksHit, &blksRead, &deadlocks)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"numbackends":   fmt.Sprintf("%d", numbackends),
		"xact_commit":   fmt.Sprintf("%d", xactCommit),
		"xact_rollback": fmt.Sprintf("%d", xactRollback),
		"tup_returned":  fmt.Sprintf("%d", tupReturned),
		"tup_fetched":   fmt.Sprintf("%d", tupFetched),
		"tup_inserted":  fmt.Sprintf("%d", tupInserted),
		"tup_updated":   fmt.Sprintf("%d", tupUpdated),
		"tup_deleted":   fmt.Sprintf("%d", tupDeleted),
		"blks_hit":      fmt.Sprintf("%d", blksHit),
		"blks_read":     fmt.Sprintf("%d", blksRead),
		"deadlocks":     fmt.Sprintf("%d", deadlocks),
	}, nil
}

// queryPGSettings 查询关键 SHOW 设置。
func queryPGSettings(ctx context.Context, db *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	settings := []string{"max_connections", "server_version"}
	for _, s := range settings {
		var val string
		// PostgreSQL 中 SHOW 不支持参数化，直接拼接（仅固定字符串，无注入风险）
		query := fmt.Sprintf("SHOW %s", s)
		if err := db.QueryRowContext(ctx, query).Scan(&val); err == nil {
			out[s] = val
		}
	}
	return out, nil
}

// queryPGReplication 查询复制延迟。返回 (lagBytes, state, isStandby, error)。
// 在 standby 节点上 pg_stat_replication 为空，isStandby 通过 pg_is_in_recovery() 判断。
func queryPGReplication(ctx context.Context, db *sql.DB) (float64, string, bool, error) {
	var isRecovery bool
	err := db.QueryRowContext(ctx, "SELECT pg_is_in_recovery()").Scan(&isRecovery)
	if err != nil {
		return -1, "", false, err
	}
	if !isRecovery {
		// master 节点，查询 slave 连接
		var lag float64
		var state string
		err := db.QueryRowContext(ctx, "SELECT COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0), state FROM pg_stat_replication LIMIT 1").Scan(&lag, &state)
		if err != nil {
			return -1, "", false, nil // 无 slave 连接不算错误
		}
		return lag, state, false, nil
	}
	// standby 节点
	var lag float64
	var state string
	err = db.QueryRowContext(ctx, "SELECT pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 'streaming'").Scan(&lag, &state)
	if err != nil {
		return -1, "", true, nil
	}
	return lag, state, true, nil
}

// queryPGPrimaryAddr 返回 standby 所连主库的 `host:port`；取不到返回空串。
//
// 数据来自 pg_stat_wal_receiver.conninfo（standby 上必有，形如
// `host=10.0.0.9 port=5432 user=repl application_name=...`）。**host 与 port 缺一不可**：
// 拼不出地址的连接串直接丢掉，别把它当成"主库在某个奇怪的地方"——宁可不建边，也不给错依赖。
func queryPGPrimaryAddr(ctx context.Context, db *sql.DB) string {
	var conninfo string
	if err := db.QueryRowContext(ctx,
		"SELECT conninfo FROM pg_stat_wal_receiver LIMIT 1").Scan(&conninfo); err != nil {
		return ""
	}
	return parsePGConninfo(conninfo)
}

// parsePGConninfo 从 libpq 连接串里取出 `host:port`；取不到返回空串。
//
// 单独抽出来是为了能直接测字符串：真实路径要先有一个 standby（dev-server 的 PG 是单机），
// 而这段解析恰恰是最容易出细节错的地方——值可能带引号（含空格时 libpq 会加），
// host 与 port 也必须**同时**存在，拼不出地址的连接串一律丢掉。
func parsePGConninfo(conninfo string) string {
	var host, port string
	for _, field := range strings.Fields(conninfo) {
		switch {
		case strings.HasPrefix(field, "host="):
			host = strings.Trim(strings.TrimPrefix(field, "host="), "'\"")
		case strings.HasPrefix(field, "port="):
			port = strings.Trim(strings.TrimPrefix(field, "port="), "'\"")
		}
	}
	if host == "" || port == "" {
		return ""
	}
	return normalizeRemoteAddr(host+":"+port, "")
}

// queryPGDatabaseSize 查询数据库大小（字节）。
func queryPGDatabaseSize(ctx context.Context, db *sql.DB, dbName string) (int64, error) {
	var size int64
	err := db.QueryRowContext(ctx, "SELECT pg_database_size($1)", dbName).Scan(&size)
	return size, err
}

// queryPGUptime 查询 PostgreSQL 运行时长（秒）。
func queryPGUptime(ctx context.Context, db *sql.DB) (float64, error) {
	var uptime float64
	err := db.QueryRowContext(ctx, "SELECT EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))").Scan(&uptime)
	return uptime, err
}

// queryPGStmtLatencyMs 返回实例平均语句响应时间（毫秒），基于 pg_stat_statements。
// PG 13+ 使用 total_exec_time 列，更早版本使用 total_time 列；扩展未安装/未加载时返回 ok=false。
func queryPGStmtLatencyMs(ctx context.Context, db *sql.DB, version string) (float64, bool) {
	var exists int
	if err := db.QueryRowContext(ctx, "SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements' LIMIT 1").Scan(&exists); err != nil {
		return 0, false // 扩展未安装
	}
	col := "total_exec_time"
	if major := pgMajorVersion(version); major > 0 && major < 13 {
		col = "total_time"
	}
	var avgMs float64
	query := fmt.Sprintf("SELECT COALESCE(SUM(%s)/NULLIF(SUM(calls),0), 0) FROM pg_stat_statements", col)
	if err := db.QueryRowContext(ctx, query).Scan(&avgMs); err != nil {
		return 0, false
	}
	return avgMs, true
}

// pgMajorVersion 从 server_version 字符串（如 "15.3 (Debian...)"）解析主版本号。
func pgMajorVersion(version string) int {
	v := strings.TrimSpace(version)
	if idx := strings.IndexAny(v, " "); idx >= 0 {
		v = v[:idx]
	}
	if idx := strings.IndexByte(v, '.'); idx >= 0 {
		v = v[:idx]
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// splitHostPort 拆分 host:port，port 为空时返回默认值。
func splitHostPort(addr, defaultPort string) (string, string) {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[:idx], addr[idx+1:]
	}
	return addr, defaultPort
}
