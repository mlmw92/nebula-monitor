package collector

import (
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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

// Collect 采集所有 PostgreSQL 实例指标。
func (c *PostgresCollector) Collect() ([]model.Metric, []model.PostgresInstance) {
	if len(c.instances) == 0 {
		return nil, nil
	}
	now := model.NowMillis()
	var metrics []model.Metric
	var instances []model.PostgresInstance

	for _, cfg := range c.instances {
		if cfg.ExporterURL != "" {
			m, pi := c.collectExporter(cfg, now)
			metrics = append(metrics, m...)
			instances = append(instances, pi)
			continue
		}
		m, pi := c.collectDirect(cfg, now)
		metrics = append(metrics, m...)
		instances = append(instances, pi)
	}
	return metrics, instances
}

// collectDirect 直连 PostgreSQL 采集。
func (c *PostgresCollector) collectDirect(cfg model.PostgresInstanceConfig, now int64) ([]model.Metric, model.PostgresInstance) {
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
	if err := db.Ping(); err != nil {
		slog.Warn("PostgreSQL ping 失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}

	// 1. pg_stat_database（当前连接的库）
	dbStat, err := queryPGStatDatabase(db, cfg.Database)
	if err != nil {
		slog.Warn("pg_stat_database 查询失败", "addr", cfg.Addr, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	// 2. SHOW max_connections / server_version
	settings, _ := queryPGSettings(db)
	// 3. pg_stat_replication（复制延迟）
	replLag, replState, isStandby, _ := queryPGReplication(db)

	role := "master"
	if isStandby {
		role = "standby"
	}
	labels := map[string]string{
		"node":     c.node,
		"instance": cfg.Addr,
		"role":     role,
		"topology": cfg.Topology,
		"group":    cfg.Name,
		"version":  settings["server_version"],
		"database": cfg.Database,
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
	if size, err := queryPGDatabaseSize(db, cfg.Database); err == nil {
		out = append(out, mk("postgres_database_size_bytes", float64(size)))
	}
	// 运行时长
	if uptime, err := queryPGUptime(db); err == nil {
		out = append(out, mk("postgres_uptime_seconds", uptime))
	}

	pi := model.PostgresInstance{
		Instance: cfg.Addr,
		Name:     cfg.Name,
		Node:     c.node,
		Role:     role,
		Topology: cfg.Topology,
		Group:    cfg.Name,
		Version:  settings["server_version"],
		Database: cfg.Database,
		Up:       true,
	}
	return out, pi
}

func (c *PostgresCollector) downInstance(cfg model.PostgresInstanceConfig, role string) model.PostgresInstance {
	return model.PostgresInstance{
		Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
		Role: role, Topology: cfg.Topology, Group: cfg.Name, Database: cfg.Database, Up: false,
	}
}

func (c *PostgresCollector) collectExporter(cfg model.PostgresInstanceConfig, now int64) ([]model.Metric, model.PostgresInstance) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(cfg.ExporterURL)
	if err != nil {
		slog.Warn("PostgreSQL exporter 拉取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Warn("PostgreSQL exporter 读取失败", "url", cfg.ExporterURL, "err", err)
		return nil, c.downInstance(cfg, "unknown")
	}
	metrics := parsePrometheusTextWithPrefix(string(body), c.node, cfg.Addr, "postgres_", now)
	pi := model.PostgresInstance{
		Instance: cfg.Addr, Name: cfg.Name, Node: c.node,
		Role: "master", Topology: cfg.Topology, Group: cfg.Name, Database: cfg.Database, Up: true,
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
func queryPGStatDatabase(db *sql.DB, dbName string) (map[string]string, error) {
	query := `SELECT numbackends, xact_commit, xact_rollback, tup_returned, tup_fetched,
		tup_inserted, tup_updated, tup_deleted, blks_hit, blks_read, deadlocks
		FROM pg_stat_database WHERE datname = $1`
	row := db.QueryRow(query, dbName)
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
func queryPGSettings(db *sql.DB) (map[string]string, error) {
	out := map[string]string{}
	settings := []string{"max_connections", "server_version"}
	for _, s := range settings {
		var val string
		// PostgreSQL 中 SHOW 不支持参数化，直接拼接（仅固定字符串，无注入风险）
		query := fmt.Sprintf("SHOW %s", s)
		if err := db.QueryRow(query).Scan(&val); err == nil {
			out[s] = val
		}
	}
	return out, nil
}

// queryPGReplication 查询复制延迟。返回 (lagBytes, state, isStandby, error)。
// 在 standby 节点上 pg_stat_replication 为空，isStandby 通过 pg_is_in_recovery() 判断。
func queryPGReplication(db *sql.DB) (float64, string, bool, error) {
	var isRecovery bool
	err := db.QueryRow("SELECT pg_is_in_recovery()").Scan(&isRecovery)
	if err != nil {
		return -1, "", false, err
	}
	if !isRecovery {
		// master 节点，查询 slave 连接
		var lag float64
		var state string
		err := db.QueryRow("SELECT COALESCE(pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn), 0), state FROM pg_stat_replication LIMIT 1").Scan(&lag, &state)
		if err != nil {
			return -1, "", false, nil // 无 slave 连接不算错误
		}
		return lag, state, false, nil
	}
	// standby 节点
	var lag float64
	var state string
	err = db.QueryRow("SELECT pg_wal_lsn_diff(pg_last_wal_receive_lsn(), pg_last_wal_replay_lsn()), 'streaming'").Scan(&lag, &state)
	if err != nil {
		return -1, "", true, nil
	}
	return lag, state, true, nil
}

// queryPGDatabaseSize 查询数据库大小（字节）。
func queryPGDatabaseSize(db *sql.DB, dbName string) (int64, error) {
	var size int64
	err := db.QueryRow("SELECT pg_database_size($1)", dbName).Scan(&size)
	return size, err
}

// queryPGUptime 查询 PostgreSQL 运行时长（秒）。
func queryPGUptime(db *sql.DB) (float64, error) {
	var uptime float64
	err := db.QueryRow("SELECT EXTRACT(EPOCH FROM (now() - pg_postmaster_start_time()))").Scan(&uptime)
	return uptime, err
}

// splitHostPort 拆分 host:port，port 为空时返回默认值。
func splitHostPort(addr, defaultPort string) (string, string) {
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return addr[:idx], addr[idx+1:]
	}
	return addr, defaultPort
}
