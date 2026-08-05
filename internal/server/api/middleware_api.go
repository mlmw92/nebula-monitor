package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/report"
)

// ---- MySQL ----

func (a *API) handleMySQLInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("mysql_instance_up", nil)
	if err != nil {
		slog.Error("查询 MySQL 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type mysqlInstanceInfo struct {
		Node              string  `json:"node"`
		Instance          string  `json:"instance"`
		Name              string  `json:"name"`
		Role              string  `json:"role"`
		Topology          string  `json:"topology"`
		Version           string  `json:"version"`
		Up                bool    `json:"up"`
		Group             string  `json:"group"`
		ReplicaOf         string  `json:"replicaOf,omitempty"`
		ThreadsConnected  float64 `json:"threadsConnected"`
		ThreadsRunning    float64 `json:"threadsRunning"`
		MaxConnections    float64 `json:"maxConnections"`
		QueriesPerSec     float64 `json:"queriesPerSec"`
		SlowQueries       float64 `json:"slowQueries"`
		BufferPoolHitRate float64 `json:"bufferPoolHitRate"`
		RowLockWaits      float64 `json:"rowLockWaits"`
		Deadlocks         float64 `json:"deadlocks"`
		SecondsBehindMaster float64 `json:"secondsBehindMaster"`
		ComCommit         float64 `json:"comCommit"`
		ComRollback       float64 `json:"comRollback"`
		BytesReceived     float64 `json:"bytesReceived"`
		BytesSent         float64 `json:"bytesSent"`
		Uptime            float64 `json:"uptime"`
	}

	// 实时在线状态：以 node|instance 为键记录最新 up 值（>0 为在线）。
	// 注意即时查询对超过 lookback-delta 的旧样本视为 stale 返回空，
	// 因此离线的 Agent 不会出现在 liveUp 中——这正是需要注册表补充的部分。
	liveUp := map[string]bool{}
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		liveUp[node+"|"+instance] = s.Points[len(s.Points)-1].Value > 0
	}

	// 配置清单（含离线实例）：来自实例注册表，保证 Agent 宕机后仍可枚举，
	// 不会被误判为"尚未配置 MySQL 监控"。元数据取最后已知上报，Up 暂置 false。
	instances := map[string]*mysqlInstanceInfo{}
	var keys []string
	for _, m := range instancereg.Default.MySQLInstances() {
		key := m.Node + "|" + m.Instance
		if _, exists := instances[key]; exists {
			continue
		}
		instances[key] = &mysqlInstanceInfo{
			Node:      m.Node,
			Instance:  m.Instance,
			Name:      m.Name,
			Role:      m.Role,
			Topology:  m.Topology,
			Version:   m.Version,
			Group:     m.Group,
			ReplicaOf: m.ReplicaOf,
			Up:        false,
		}
		keys = append(keys, key)
	}
	// 用实时在线状态覆盖（仅对注册表中已有的实例生效）
	for key, up := range liveUp {
		if ri, ok := instances[key]; ok {
			ri.Up = up
		}
	}

	metricMap := map[string]func(ri *mysqlInstanceInfo, v float64){
		"mysql_threads_connected":          func(ri *mysqlInstanceInfo, v float64) { ri.ThreadsConnected = round2(v) },
		"mysql_threads_running":            func(ri *mysqlInstanceInfo, v float64) { ri.ThreadsRunning = round2(v) },
		"mysql_max_connections":            func(ri *mysqlInstanceInfo, v float64) { ri.MaxConnections = round2(v) },
		"mysql_queries_per_sec":            func(ri *mysqlInstanceInfo, v float64) { ri.QueriesPerSec = round2(v) },
		"mysql_slow_queries":               func(ri *mysqlInstanceInfo, v float64) { ri.SlowQueries = round2(v) },
		"mysql_innodb_buffer_pool_hit_rate": func(ri *mysqlInstanceInfo, v float64) { ri.BufferPoolHitRate = round2(v) },
		"mysql_innodb_row_lock_waits":      func(ri *mysqlInstanceInfo, v float64) { ri.RowLockWaits = round2(v) },
		"mysql_innodb_deadlocks":           func(ri *mysqlInstanceInfo, v float64) { ri.Deadlocks = round2(v) },
		"mysql_seconds_behind_master":      func(ri *mysqlInstanceInfo, v float64) { ri.SecondsBehindMaster = round2(v) },
		"mysql_com_commit":                 func(ri *mysqlInstanceInfo, v float64) { ri.ComCommit = round2(v) },
		"mysql_com_rollback":               func(ri *mysqlInstanceInfo, v float64) { ri.ComRollback = round2(v) },
		"mysql_bytes_received":             func(ri *mysqlInstanceInfo, v float64) { ri.BytesReceived = round2(v) },
		"mysql_bytes_sent":                 func(ri *mysqlInstanceInfo, v float64) { ri.BytesSent = round2(v) },
		"mysql_uptime":                     func(ri *mysqlInstanceInfo, v float64) { ri.Uptime = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 MySQL 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]mysqlInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// ---- PostgreSQL ----

func (a *API) handlePostgresInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("postgres_instance_up", nil)
	if err != nil {
		slog.Error("查询 PostgreSQL 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type postgresInstanceInfo struct {
		Node            string  `json:"node"`
		Instance        string  `json:"instance"`
		Name            string  `json:"name"`
		Role            string  `json:"role"`
		Topology        string  `json:"topology"`
		Version         string  `json:"version"`
		Database        string  `json:"database"`
		Up              bool    `json:"up"`
		Group           string  `json:"group"`
		Numbackends     float64 `json:"numbackends"`
		MaxConnections  float64 `json:"maxConnections"`
		XactCommit      float64 `json:"xactCommit"`
		XactRollback    float64 `json:"xactRollback"`
		CacheHitRatio   float64 `json:"cacheHitRatio"`
		Deadlocks       float64 `json:"deadlocks"`
		ReplicationLag  float64 `json:"replicationLag"`
		DatabaseSize    float64 `json:"databaseSize"`
		Uptime          float64 `json:"uptime"`
	}

	instances := map[string]*postgresInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if ri, exists := instances[key]; exists {
			ri.Role = s.Labels["role"]
			ri.Topology = s.Labels["topology"]
			ri.Version = s.Labels["version"]
			ri.Database = s.Labels["database"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &postgresInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Topology: s.Labels["topology"],
				Version:  s.Labels["version"],
				Database: s.Labels["database"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的实例，
	// 补列出来并标记为离线，避免误判为"尚未配置 PostgreSQL 监控"。
	for _, pi := range instancereg.Default.PostgresInstances() {
		key := pi.Node + "|" + pi.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &postgresInstanceInfo{
			Node:     pi.Node,
			Instance: pi.Instance,
			Name:     pi.Name,
			Role:     pi.Role,
			Topology: pi.Topology,
			Version:  pi.Version,
			Database: pi.Database,
			Group:    pi.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *postgresInstanceInfo, v float64){
		"postgres_numbackends":           func(ri *postgresInstanceInfo, v float64) { ri.Numbackends = round2(v) },
		"postgres_max_connections":       func(ri *postgresInstanceInfo, v float64) { ri.MaxConnections = round2(v) },
		"postgres_xact_commit":           func(ri *postgresInstanceInfo, v float64) { ri.XactCommit = round2(v) },
		"postgres_xact_rollback":         func(ri *postgresInstanceInfo, v float64) { ri.XactRollback = round2(v) },
		"postgres_cache_hit_ratio":       func(ri *postgresInstanceInfo, v float64) { ri.CacheHitRatio = round2(v) },
		"postgres_deadlocks":             func(ri *postgresInstanceInfo, v float64) { ri.Deadlocks = round2(v) },
		"postgres_replication_lag_bytes": func(ri *postgresInstanceInfo, v float64) { ri.ReplicationLag = round2(v) },
		"postgres_database_size_bytes":   func(ri *postgresInstanceInfo, v float64) { ri.DatabaseSize = round2(v) },
		"postgres_uptime_seconds":        func(ri *postgresInstanceInfo, v float64) { ri.Uptime = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 PostgreSQL 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]postgresInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// handleMongoDBInstances 返回 MongoDB 实例列表与运行摘要（来自实例注册表 + 最新指标）。
func (a *API) handleMongoDBInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("mongodb_up", nil)
	if err != nil {
		slog.Error("查询 MongoDB 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type mongoInstanceInfo struct {
		Node               string  `json:"node"`
		Instance           string  `json:"instance"`
		Name               string  `json:"name"`
		Role               string  `json:"role"`
		Topology           string  `json:"topology"`
		Version            string  `json:"version"`
		Group              string  `json:"group"`
		Up                 bool    `json:"up"`
		ConnectionsCurrent float64 `json:"connectionsCurrent"`
		ConnectionsAvail   float64 `json:"connectionsAvailable"`
		MemResidentMB      float64 `json:"memResidentMB"`
		MemVirtualMB       float64 `json:"memVirtualMB"`
		OpInsert           float64 `json:"opInsert"`
		OpQuery            float64 `json:"opQuery"`
		OpUpdate           float64 `json:"opUpdate"`
		OpDelete           float64 `json:"opDelete"`
		OpCommand          float64 `json:"opCommand"`
		DbDataSizeMB       float64 `json:"dbDataSizeMB"`
		DbStorageSizeMB    float64 `json:"dbStorageSizeMB"`
		DbObjects          float64 `json:"dbObjects"`
		DbIndexes          float64 `json:"dbIndexes"`
		DbIndexSizeMB      float64 `json:"dbIndexSizeMB"`
		ReplState          float64 `json:"replState"`
		ReplHealth         float64 `json:"replHealth"`
		ReplLag            float64 `json:"replLag"`
		Uptime             float64 `json:"uptime"`
	}

	instances := map[string]*mongoInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if ri, exists := instances[key]; exists {
			ri.Role = s.Labels["role"]
			ri.Topology = s.Labels["topology"]
			ri.Version = s.Labels["version"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &mongoInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Topology: s.Labels["topology"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	for _, mi := range instancereg.Default.MongoDBInstances() {
		key := mi.Node + "|" + mi.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &mongoInstanceInfo{
			Node:     mi.Node,
			Instance: mi.Instance,
			Name:     mi.Name,
			Role:     mi.Role,
			Topology: mi.Topology,
			Version:  mi.Version,
			Group:    mi.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *mongoInstanceInfo, v float64){
		"mongodb_uptime_seconds":         func(ri *mongoInstanceInfo, v float64) { ri.Uptime = round2(v) },
		"mongodb_connections_current":    func(ri *mongoInstanceInfo, v float64) { ri.ConnectionsCurrent = round2(v) },
		"mongodb_connections_available":  func(ri *mongoInstanceInfo, v float64) { ri.ConnectionsAvail = round2(v) },
		"mongodb_mem_resident_bytes":     func(ri *mongoInstanceInfo, v float64) { ri.MemResidentMB = round2(v / 1024 / 1024) },
		"mongodb_mem_virtual_bytes":      func(ri *mongoInstanceInfo, v float64) { ri.MemVirtualMB = round2(v / 1024 / 1024) },
		"mongodb_opcounters_insert":      func(ri *mongoInstanceInfo, v float64) { ri.OpInsert = round2(v) },
		"mongodb_opcounters_query":       func(ri *mongoInstanceInfo, v float64) { ri.OpQuery = round2(v) },
		"mongodb_opcounters_update":      func(ri *mongoInstanceInfo, v float64) { ri.OpUpdate = round2(v) },
		"mongodb_opcounters_delete":      func(ri *mongoInstanceInfo, v float64) { ri.OpDelete = round2(v) },
		"mongodb_opcounters_command":     func(ri *mongoInstanceInfo, v float64) { ri.OpCommand = round2(v) },
		"mongodb_db_dataSize_bytes":      func(ri *mongoInstanceInfo, v float64) { ri.DbDataSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_storageSize_bytes":   func(ri *mongoInstanceInfo, v float64) { ri.DbStorageSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_indexSize_bytes":     func(ri *mongoInstanceInfo, v float64) { ri.DbIndexSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_objects":             func(ri *mongoInstanceInfo, v float64) { ri.DbObjects = round2(v) },
		"mongodb_db_indexes":             func(ri *mongoInstanceInfo, v float64) { ri.DbIndexes = round2(v) },
		"mongodb_repl_state":             func(ri *mongoInstanceInfo, v float64) { ri.ReplState = round2(v) },
		"mongodb_repl_health":            func(ri *mongoInstanceInfo, v float64) { ri.ReplHealth = round2(v) },
		"mongodb_repl_lag":               func(ri *mongoInstanceInfo, v float64) { ri.ReplLag = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 MongoDB 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]mongoInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// handleFastDFSInstances 返回 FastDFS 实例列表与运行摘要（来自实例注册表 + 最新指标）。
func (a *API) handleFastDFSInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("fastdfs_up", nil)
	if err != nil {
		slog.Error("查询 FastDFS 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type fastdfsInstanceInfo struct {
		Node           string  `json:"node"`
		Instance       string  `json:"instance"`
		Name           string  `json:"name"`
		Role           string  `json:"role"`
		Group          string  `json:"group"`
		Up             bool    `json:"up"`
		GroupTotal     float64 `json:"groupTotal"`
		StorageTotal   float64 `json:"storageTotal"`
		StorageOnline  float64 `json:"storageOnline"`
		StorageOffline float64 `json:"storageOffline"`
		TotalSpaceMB   float64 `json:"totalSpaceMB"`
		FreeSpaceMB    float64 `json:"freeSpaceMB"`
		UsedSpaceMB    float64 `json:"usedSpaceMB"`
		TrunkFreeMB    float64 `json:"trunkFreeMB"`
		DiskReadMB     float64 `json:"diskReadMB"`
		DiskWriteMB    float64 `json:"diskWriteMB"`
		NetRecvMB      float64 `json:"netRecvMB"`
		NetSentMB      float64 `json:"netSentMB"`
	}

	instances := map[string]*fastdfsInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if ri, exists := instances[key]; exists {
			ri.Role = s.Labels["role"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &fastdfsInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	for _, fi := range instancereg.Default.FastDFSInstances() {
		key := fi.Node + "|" + fi.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &fastdfsInstanceInfo{
			Node:     fi.Node,
			Instance: fi.Instance,
			Name:     fi.Name,
			Role:     fi.Role,
			Group:    fi.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *fastdfsInstanceInfo, v float64){
		"fastdfs_group_count":          func(ri *fastdfsInstanceInfo, v float64) { ri.GroupTotal = round2(v) },
		"fastdfs_storage_count":        func(ri *fastdfsInstanceInfo, v float64) { ri.StorageTotal = round2(v) },
		"fastdfs_storage_online_count": func(ri *fastdfsInstanceInfo, v float64) { ri.StorageOnline = round2(v) },
		"fastdfs_storage_offline_count": func(ri *fastdfsInstanceInfo, v float64) { ri.StorageOffline = round2(v) },
		"fastdfs_total_space":          func(ri *fastdfsInstanceInfo, v float64) { ri.TotalSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_free_space":           func(ri *fastdfsInstanceInfo, v float64) { ri.FreeSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_used_space":           func(ri *fastdfsInstanceInfo, v float64) { ri.UsedSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_trunk_free_space":     func(ri *fastdfsInstanceInfo, v float64) { ri.TrunkFreeMB = round2(v / 1024 / 1024) },
		"fastdfs_disk_read_bytes":      func(ri *fastdfsInstanceInfo, v float64) { ri.DiskReadMB = round2(v / 1024 / 1024) },
		"fastdfs_disk_write_bytes":     func(ri *fastdfsInstanceInfo, v float64) { ri.DiskWriteMB = round2(v / 1024 / 1024) },
		"fastdfs_net_recv_bytes":       func(ri *fastdfsInstanceInfo, v float64) { ri.NetRecvMB = round2(v / 1024 / 1024) },
		"fastdfs_net_sent_bytes":       func(ri *fastdfsInstanceInfo, v float64) { ri.NetSentMB = round2(v / 1024 / 1024) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 FastDFS 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]fastdfsInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// ---- Nginx ----

func (a *API) handleNginxInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("nginx_instance_up", nil)
	if err != nil {
		slog.Error("查询 Nginx 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type nginxInstanceInfo struct {
		Node              string  `json:"node"`
		NodeIP            string  `json:"nodeIp"`
		Instance          string  `json:"instance"`
		Name              string  `json:"name"`
		Version           string  `json:"version"`
		Up                bool    `json:"up"`
		Group             string  `json:"group"`
		ActiveConnections float64 `json:"activeConnections"`
		Accepts           float64 `json:"accepts"`
		Handled           float64 `json:"handled"`
		Requests          float64 `json:"requests"`
		Reading           float64 `json:"reading"`
		Writing           float64 `json:"writing"`
		Waiting           float64 `json:"waiting"`
		ConnectionDropRate float64 `json:"connectionDropRate"`
	}

	instances := map[string]*nginxInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if ri, exists := instances[key]; exists {
			ri.Version = s.Labels["version"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &nginxInstanceInfo{
				Node:     node,
				NodeIP:   a.nodeIP(node),
				Instance: instance,
				Name:    s.Labels["name"],
				Version: s.Labels["version"],
				Group:   s.Labels["group"],
				Up:      s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的实例，
	// 补列出来并标记为离线，避免误判为"尚未配置 Nginx 监控"。
	for _, ni := range instancereg.Default.NginxInstances() {
		key := ni.Node + "|" + ni.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &nginxInstanceInfo{
			Node:     ni.Node,
			NodeIP:   a.nodeIP(ni.Node),
			Instance: ni.Instance,
			Name:     ni.Name,
			Version:  ni.Version,
			Group:    ni.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *nginxInstanceInfo, v float64){
		"nginx_active_connections":   func(ri *nginxInstanceInfo, v float64) { ri.ActiveConnections = round2(v) },
		"nginx_accepts":              func(ri *nginxInstanceInfo, v float64) { ri.Accepts = round2(v) },
		"nginx_handled":              func(ri *nginxInstanceInfo, v float64) { ri.Handled = round2(v) },
		"nginx_requests":             func(ri *nginxInstanceInfo, v float64) { ri.Requests = round2(v) },
		"nginx_reading":              func(ri *nginxInstanceInfo, v float64) { ri.Reading = round2(v) },
		"nginx_writing":              func(ri *nginxInstanceInfo, v float64) { ri.Writing = round2(v) },
		"nginx_waiting":              func(ri *nginxInstanceInfo, v float64) { ri.Waiting = round2(v) },
		"nginx_connection_drop_rate": func(ri *nginxInstanceInfo, v float64) { ri.ConnectionDropRate = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 Nginx 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]nginxInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// middlewareOverviewType 单类中间件健康度。
type middlewareOverviewType struct {
	Type       string           `json:"type"`       // redis/mysql/postgres/nginx/kafka/docker/rocketmq/k8s
	Label      string           `json:"label"`      // 中文名
	Total      int              `json:"total"`      // 实例总数
	Up         int              `json:"up"`         // 在线实例数
	Down       int              `json:"down"`       // 离线实例数
	AlertCount int              `json:"alertCount"` // 关联活跃告警数
	Summary    []mwSummaryItem  `json:"summary"`    // 核心指标摘要（卡片展示）
}

// mwSummaryItem 是某类中间件在总览卡片上展示的核心指标摘要。
type mwSummaryItem struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
	Warn  bool    `json:"warn"` // 是否超过预警阈值
}

// mwSummarySpec 描述某类中间件在卡片上要展示的核心指标及其聚合方式。
type mwSummarySpec struct {
	metric    string
	label     string
	agg       string // max / avg / sum
	unit      string
	warnAbove float64
}

// mwSummarySpecs 各中间件类型在总览卡片上的核心指标摘要定义。
var mwSummarySpecs = map[string][]mwSummarySpec{
	"redis": {
		{"redis_ops_per_sec", "QPS峰值", "max", "", 0},
		{"redis_used_memory_percent", "内存使用率", "avg", "%", 85},
		{"redis_hit_rate", "命中率", "avg", "%", 0},
	},
	"mysql": {
		{"mysql_queries_per_sec", "QPS", "sum", "", 0},
		{"mysql_threads_connected", "连接数", "max", "", 0},
		{"mysql_innodb_buffer_pool_hit_rate", "缓冲命中率", "avg", "%", 0},
	},
	"postgres": {
		{"postgres_numbackends", "连接数", "max", "", 0},
		{"postgres_cache_hit_ratio", "缓存命中率", "avg", "%", 0},
		{"postgres_replication_lag_bytes", "复制延迟", "max", "B", 0},
	},
	"nginx": {
		{"nginx_active_connections", "活动连接", "max", "", 0},
		{"nginx_requests", "请求量", "sum", "", 0},
		{"nginx_5xx_rate", "5xx率", "avg", "%", 1},
	},
	"kafka": {
		{"kafka_consumer_lag", "消费积压", "sum", "", 0},
		{"kafka_under_replicated_partitions", "欠副本分区", "sum", "", 0},
		{"kafka_offline_partitions", "离线分区", "sum", "", 0},
	},
	"docker": {
		{"docker_container_up", "运行容器", "sum", "", 0},
		{"docker_container_cpu_percent", "CPU使用率", "avg", "%", 0},
		{"docker_container_mem_percent", "内存使用率", "avg", "%", 0},
	},
	"rocketmq": {
		{"rocketmq_producer_tps", "生产TPS", "sum", "", 0},
		{"rocketmq_message_accumulation", "消息堆积", "sum", "", 0},
		{"rocketmq_consumer_lag", "消费积压", "sum", "", 0},
	},
	"k8s": {
		{"k8s_pods_running", "运行Pod", "sum", "", 0},
		{"k8s_pods_pending", "待调度Pod", "sum", "", 0},
		{"k8s_nodes_ready", "就绪节点", "sum", "", 0},
	},
	"mongodb": {
		{"mongodb_uptime_seconds", "运行时长", "avg", "s", 0},
		{"mongodb_connections_current", "当前连接数", "avg", "", 0},
		{"mongodb_mem_resident_bytes", "常驻内存", "avg", "MB", 0},
		{"mongodb_opcounters_command", "命令数", "sum", "", 0},
		{"mongodb_db_dataSize_bytes", "数据大小", "avg", "MB", 0},
	},
	"fastdfs": {
		{"fastdfs_storage_total", "Storage节点", "sum", "", 0},
		{"fastdfs_storage_online_count", "在线Storage", "sum", "", 0},
		{"fastdfs_total_space", "总空间", "sum", "MB", 0},
		{"fastdfs_free_space", "空闲空间", "sum", "MB", 0},
		{"fastdfs_used_space", "已用空间", "sum", "MB", 0},
	},
}

// mwAggregateLatest 对指定指标的「最新值」按 agg 方式跨所有序列聚合（sum/avg/max）。
func mwAggregateLatest(a *API, metric, agg string) (float64, bool) {
	series, err := a.store.QueryAllLatest(metric, nil)
	if err != nil {
		return 0, false
	}
	var sum, max, count float64
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		v := s.Points[len(s.Points)-1].Value
		sum += v
		count++
		if count == 1 || v > max {
			max = v
		}
	}
	if count == 0 {
		return 0, false
	}
	switch agg {
	case "max":
		return max, true
	case "sum":
		return sum, true
	default:
		return round2(sum / count), true
	}
}

// middlewareOverviewResp 是 /api/v1/middleware/overview 的响应体。
type middlewareOverviewResp struct {
	Total      int                      `json:"total"`
	Up         int                      `json:"up"`
	Down       int                      `json:"down"`
	AlertCount int                      `json:"alertCount"`
	Types      []middlewareOverviewType `json:"types"`
}

// middlewareTypes 10 类中间件的 up 指标与展示名。
var middlewareTypes = []struct{ typ, label, upMetric string }{
	{"redis", "Redis", "redis_instance_up"},
	{"mysql", "MySQL", "mysql_instance_up"},
	{"postgres", "PostgreSQL", "postgres_instance_up"},
	{"nginx", "Nginx", "nginx_instance_up"},
	{"kafka", "Kafka", "kafka_instance_up"},
	{"docker", "Docker", "docker_container_up"},
	{"rocketmq", "RocketMQ", "rocketmq_instance_up"},
	{"k8s", "Kubernetes", "k8s_cluster_up"},
	{"mongodb", "MongoDB", "mongodb_up"},
	{"fastdfs", "FastDFS", "fastdfs_up"},
}

// handleMiddlewareOverview 返回中间件健康度总览（各类型实例数/在线率/告警数），
// 供数据大屏中间件监控板块一次拉取，避免前端 8 个请求轮询。
func (a *API) handleMiddlewareOverview(w http.ResponseWriter, r *http.Request) {
	// 活跃告警按指标前缀归类
	alertCount := map[string]int{}
	for _, ev := range a.alerts.Active() {
		metric := strings.ToLower(ev.Metric)
		for _, t := range middlewareTypes {
			if strings.HasPrefix(metric, t.typ+"_") {
				alertCount[t.typ]++
				break
			}
		}
	}

	resp := middlewareOverviewResp{Types: make([]middlewareOverviewType, 0, len(middlewareTypes))}
	for _, t := range middlewareTypes {
		item := middlewareOverviewType{Type: t.typ, Label: t.label, AlertCount: alertCount[t.typ]}
		series, err := a.store.QueryAllLatest(t.upMetric, nil)
		if err != nil {
			slog.Warn("查询中间件 up 指标失败", "metric", t.upMetric, "err", err)
			resp.Types = append(resp.Types, item)
			continue
		}
		seen := map[string]bool{}
		for _, s := range series {
			key := s.Labels["node"] + "|" + s.Labels["instance"]
			if key == "|" || seen[key] {
				continue
			}
			seen[key] = true
			item.Total++
			if len(s.Points) > 0 && s.Points[len(s.Points)-1].Value > 0 {
				item.Up++
			} else {
				item.Down++
			}
		}
		// 核心指标摘要（卡片展示）
		if specs, ok := mwSummarySpecs[t.typ]; ok {
			for _, sp := range specs {
				if v, ok := mwAggregateLatest(a, sp.metric, sp.agg); ok {
					item.Summary = append(item.Summary, mwSummaryItem{
						Key:   sp.metric,
						Label: sp.label,
						Value: v,
						Unit:  sp.unit,
						Warn:  sp.warnAbove > 0 && v >= sp.warnAbove,
					})
				}
			}
		}
		resp.Total += item.Total
		resp.Up += item.Up
		resp.Down += item.Down
		resp.AlertCount += item.AlertCount
		resp.Types = append(resp.Types, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// nodeIP 返回指定节点上报的主机 IP（primaryIP，首个非回环 IPv4）；节点未在线或查不到时返回空串。
func (a *API) nodeIP(node string) string {
	if node == "" {
		return ""
	}
	n, ok := a.nodeMgr.GetNode(node)
	if !ok {
		return ""
	}
	return n.IP
}

// ---- Kafka ----

func (a *API) handleKafkaInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("kafka_instance_up", nil)
	if err != nil {
		slog.Error("查询 Kafka 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type kafkaInstanceInfo struct {
		Node                       string  `json:"node"`
		Instance                   string  `json:"instance"`
		Name                       string  `json:"name"`
		Role                       string  `json:"role"`
		Version                    string  `json:"version"`
		Up                         bool    `json:"up"`
		Group                      string  `json:"group"`
		BrokerCount                float64 `json:"brokerCount"`
		TopicCount                 float64 `json:"topicCount"`
		PartitionCount             float64 `json:"partitionCount"`
		UnderReplicatedPartitions  float64 `json:"underReplicatedPartitions"`
		OfflinePartitions          float64 `json:"offlinePartitions"`
		ConsumerGroupCount         float64 `json:"consumerGroupCount"`
		ConsumerLag                float64 `json:"consumerLag"`
		ConsumerLagMax             float64 `json:"consumerLagMax"`
		ActiveControllerCount      float64 `json:"activeControllerCount"`
	}

	instances := map[string]*kafkaInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		name := s.Labels["name"]
		if name == "" {
			name = s.Labels["group"]
		}
		if ri, exists := instances[key]; exists {
			ri.Role = s.Labels["role"]
			ri.Version = s.Labels["version"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &kafkaInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     name,
				Role:     s.Labels["role"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的实例，
	// 补列出来并标记为离线，避免误判为"尚未配置 Kafka 监控"。
	for _, ki := range instancereg.Default.KafkaInstances() {
		key := ki.Node + "|" + ki.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &kafkaInstanceInfo{
			Node:     ki.Node,
			Instance: ki.Instance,
			Name:     ki.Name,
			Role:     ki.Role,
			Version:  ki.Version,
			Group:    ki.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *kafkaInstanceInfo, v float64){
		"kafka_broker_count":                func(ri *kafkaInstanceInfo, v float64) { ri.BrokerCount = round2(v) },
		"kafka_topic_count":                 func(ri *kafkaInstanceInfo, v float64) { ri.TopicCount = round2(v) },
		"kafka_partition_count":             func(ri *kafkaInstanceInfo, v float64) { ri.PartitionCount = round2(v) },
		"kafka_under_replicated_partitions": func(ri *kafkaInstanceInfo, v float64) { ri.UnderReplicatedPartitions = round2(v) },
		"kafka_offline_partitions":          func(ri *kafkaInstanceInfo, v float64) { ri.OfflinePartitions = round2(v) },
		"kafka_consumer_group_count":        func(ri *kafkaInstanceInfo, v float64) { ri.ConsumerGroupCount = round2(v) },
		"kafka_consumer_lag":                func(ri *kafkaInstanceInfo, v float64) { ri.ConsumerLag = round2(v) },
		"kafka_consumer_lag_max":            func(ri *kafkaInstanceInfo, v float64) { ri.ConsumerLagMax = round2(v) },
		"kafka_active_controller_count":     func(ri *kafkaInstanceInfo, v float64) { ri.ActiveControllerCount = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 Kafka 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]kafkaInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// hostFromDaemon 从 daemon 地址（如 tcp://1.2.3.4:2375 或 unix:///var/run/docker.sock）提取 host 部分作为 IP 展示。
func hostFromDaemon(daemon string) string {
	if daemon == "" {
		return ""
	}
	if strings.HasPrefix(daemon, "unix://") {
		return "" // 本地 socket 无 IP
	}
	if i := strings.Index(daemon, "://"); i >= 0 {
		daemon = daemon[i+3:]
	}
	if i := strings.Index(daemon, "/"); i >= 0 {
		daemon = daemon[:i]
	}
	if i := strings.LastIndex(daemon, ":"); i >= 0 {
		if !strings.Contains(daemon, "[") {
			daemon = daemon[:i]
		}
	}
	return daemon
}

// ---- Docker ----

func (a *API) handleDockerContainers(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("docker_container_up", nil)
	if err != nil {
		slog.Error("查询 Docker 容器失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type dockerContainerInfo struct {
		Node            string  `json:"node"`
		Instance        string  `json:"instance"` // 容器短 ID
		Name            string  `json:"name"`     // 容器名
		Image           string  `json:"image"`
		Status          string  `json:"status"`
		Up              bool    `json:"up"`
		Group           string  `json:"group"`
		CPUPercent      float64 `json:"cpuPercent"`
		MemUsage        float64 `json:"memUsage"`
		MemLimit        float64 `json:"memLimit"`
		MemPercent      float64 `json:"memPercent"`
		NetRx           float64 `json:"netRx"`
		NetTx           float64 `json:"netTx"`
		DiskRead        float64 `json:"diskRead"`
		DiskWrite       float64 `json:"diskWrite"`
		PidsCurrent     float64 `json:"pidsCurrent"`
	}

	instances := map[string]*dockerContainerInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if _, exists := instances[key]; !exists {
			ri := &dockerContainerInfo{
				Node:    node,
				Instance: instance,
				Name:    s.Labels["container_name"],
				Image:   s.Labels["image"],
				Status:  s.Labels["status"],
				Group:   s.Labels["group"],
				Up:      s.Points[len(s.Points)-1].Value > 0,
			}
			instances[key] = ri
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的容器，
	// 补列出来并标记为离线，避免误判为"尚未接入 Docker 监控"。
	for _, di := range instancereg.Default.DockerInstances() {
		key := di.Node + "|" + di.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &dockerContainerInfo{
			Node:     di.Node,
			Instance: di.Instance,
			Name:     di.Name,
			Image:    di.Image,
			Status:   di.Status,
			Group:    di.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *dockerContainerInfo, v float64){
		"docker_container_cpu_percent":     func(ri *dockerContainerInfo, v float64) { ri.CPUPercent = round2(v) },
		"docker_container_mem_usage_bytes": func(ri *dockerContainerInfo, v float64) { ri.MemUsage = round2(v) },
		"docker_container_mem_limit_bytes": func(ri *dockerContainerInfo, v float64) { ri.MemLimit = round2(v) },
		"docker_container_mem_percent":     func(ri *dockerContainerInfo, v float64) { ri.MemPercent = round2(v) },
		"docker_container_net_rx_bytes":    func(ri *dockerContainerInfo, v float64) { ri.NetRx = round2(v) },
		"docker_container_net_tx_bytes":    func(ri *dockerContainerInfo, v float64) { ri.NetTx = round2(v) },
		"docker_container_disk_read_bytes": func(ri *dockerContainerInfo, v float64) { ri.DiskRead = round2(v) },
		"docker_container_disk_write_bytes": func(ri *dockerContainerInfo, v float64) { ri.DiskWrite = round2(v) },
		"docker_container_pids_current":    func(ri *dockerContainerInfo, v float64) { ri.PidsCurrent = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 Docker 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]dockerContainerInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	// 按节点名+容器名排序
	sort.Slice(out, func(i, j int) bool {
		if out[i].Node != out[j].Node {
			return out[i].Node < out[j].Node
		}
		return out[i].Name < out[j].Name
	})

	// Docker 主机（daemon）汇总：即便无容器也能展示接入状态与镜像/容器数
	type dockerHostInfo struct {
		Node              string  `json:"node"`
		Daemon            string  `json:"daemon"`
		IP                string  `json:"ip"`
		NodeIP            string  `json:"nodeIp"`
		Group             string  `json:"group"`
		Up                bool    `json:"up"`
		ContainersTotal   float64 `json:"containersTotal"`
		ContainersRunning float64 `json:"containersRunning"`
		ContainersStopped float64 `json:"containersStopped"`
		ImagesTotal       float64 `json:"imagesTotal"`
	}
	hosts := map[string]*dockerHostInfo{}
	var hostKeys []string
		if totalSeries, err := a.store.QueryAllLatest("docker_containers_total", nil); err == nil {
			for _, s := range totalSeries {
				node := s.Labels["node"]
				daemon := s.Labels["instance"]
				if node == "" || daemon == "" || len(s.Points) == 0 {
					continue
				}
				key := node + "|" + daemon
				if _, ok := hosts[key]; !ok {
					hosts[key] = &dockerHostInfo{
						Node:            node,
						Daemon:          daemon,
						IP:              hostFromDaemon(daemon),
						NodeIP:          a.nodeIP(node),
						Group:           s.Labels["group"],
						Up:              true,
						ContainersTotal: s.Points[len(s.Points)-1].Value,
					}
					hostKeys = append(hostKeys, key)
				}
			}
		}
	for metric, setter := range map[string]func(*dockerHostInfo, float64){
		"docker_containers_running":  func(h *dockerHostInfo, v float64) { h.ContainersRunning = v },
		"docker_containers_stopped":  func(h *dockerHostInfo, v float64) { h.ContainersStopped = v },
		"docker_images_total":         func(h *dockerHostInfo, v float64) { h.ImagesTotal = v },
	} {
		series, err := a.store.QueryAllLatest(metric, nil)
		if err != nil {
			slog.Warn("聚合 Docker daemon 指标查询失败", "metric", metric, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			daemon := s.Labels["instance"]
			if node == "" || daemon == "" || len(s.Points) == 0 {
				continue
			}
			if h, ok := hosts[node+"|"+daemon]; ok {
				setter(h, s.Points[len(s.Points)-1].Value)
			}
		}
	}
	hostOut := make([]dockerHostInfo, 0, len(hostKeys))
	for _, k := range hostKeys {
		hostOut = append(hostOut, *hosts[k])
	}
	sort.Slice(hostOut, func(i, j int) bool { return hostOut[i].Node < hostOut[j].Node })

	writeJSON(w, 200, map[string]interface{}{"containers": out, "hosts": hostOut})
}

// ---- RocketMQ ----

func (a *API) handleRocketMQInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("rocketmq_instance_up", nil)
	if err != nil {
		slog.Error("查询 RocketMQ 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type rocketmqInstanceInfo struct {
		Node                 string  `json:"node"`
		Instance             string  `json:"instance"`
		Name                 string  `json:"name"`
		Role                 string  `json:"role"`
		Version              string  `json:"version"`
		Up                   bool    `json:"up"`
		Group                string  `json:"group"`
		BrokerCount          float64 `json:"brokerCount"`
		TopicCount           float64 `json:"topicCount"`
		ConsumerGroupCount   float64 `json:"consumerGroupCount"`
		BrokerTPS            float64 `json:"brokerTps"`
		ProducerTPS          float64 `json:"producerTps"`
		ConsumerTPS          float64 `json:"consumerTps"`
		MessageAccumulation  float64 `json:"messageAccumulation"`
		ConsumerLag          float64 `json:"consumerLag"`
	}

	instances := map[string]*rocketmqInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if ri, exists := instances[key]; exists {
			ri.Role = s.Labels["role"]
			ri.Version = s.Labels["version"]
			ri.Group = s.Labels["group"]
			ri.Up = s.Points[len(s.Points)-1].Value > 0
		} else {
			instances[key] = &rocketmqInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的实例，
	// 补列出来并标记为离线，避免误判为"尚未配置 RocketMQ 监控"。
	for _, rmi := range instancereg.Default.RocketMQInstances() {
		key := rmi.Node + "|" + rmi.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &rocketmqInstanceInfo{
			Node:     rmi.Node,
			Instance: rmi.Instance,
			Name:     rmi.Name,
			Role:     rmi.Role,
			Version:  rmi.Version,
			Group:    rmi.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ri *rocketmqInstanceInfo, v float64){
		"rocketmq_broker_count":           func(ri *rocketmqInstanceInfo, v float64) { ri.BrokerCount = round2(v) },
		"rocketmq_topic_count":            func(ri *rocketmqInstanceInfo, v float64) { ri.TopicCount = round2(v) },
		"rocketmq_consumer_group_count":   func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerGroupCount = round2(v) },
		"rocketmq_broker_tps":             func(ri *rocketmqInstanceInfo, v float64) { ri.BrokerTPS = round2(v) },
		"rocketmq_producer_tps":           func(ri *rocketmqInstanceInfo, v float64) { ri.ProducerTPS = round2(v) },
		"rocketmq_consumer_tps":           func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerTPS = round2(v) },
		"rocketmq_message_accumulation":   func(ri *rocketmqInstanceInfo, v float64) { ri.MessageAccumulation = round2(v) },
		"rocketmq_consumer_lag":           func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerLag = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 RocketMQ 指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]rocketmqInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// ---- 维护窗口 ----

func (a *API) handleMaintenanceGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, a.maintenance.Get())
}

func (a *API) handleMaintenanceSet(w http.ResponseWriter, r *http.Request) {
	var mw model.MaintenanceWindow
	if err := json.NewDecoder(r.Body).Decode(&mw); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	a.maintenance.Set(mw)
	writeJSON(w, 200, mw)
}

// ---- 拨测任务 ----

func (a *API) handleDialtestList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"tasks": a.dialtest.List()})
}

func (a *API) handleDialtestCreate(w http.ResponseWriter, r *http.Request) {
	var t dialtest.Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	created := a.dialtest.Create(t)
	writeJSON(w, 200, created)
}

func (a *API) handleDialtestUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var t dialtest.Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	t.ID = id
	if err := a.dialtest.Update(t); err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, t)
}

func (a *API) handleDialtestDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.dialtest.Delete(id); err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func (a *API) handleDialtestLatest(w http.ResponseWriter, r *http.Request) {
	// 查询最近拨测结果
	upSeries, err := a.store.QueryAllLatest("dial_test_up", nil)
	if err != nil {
		slog.Warn("查询拨测结果失败", "err", err)
		writeJSON(w, 200, map[string]interface{}{"results": []interface{}{}})
		return
	}
	latencySeries, _ := a.store.QueryAllLatest("dial_test_latency", nil)
	certSeries, _ := a.store.QueryAllLatest("dial_test_cert_expiry", nil)

	type dialtestResult struct {
		Name       string  `json:"name"`
		Type       string  `json:"type"`
		Target     string  `json:"target"`
		Up         bool    `json:"up"`
		Latency    float64 `json:"latency"`
		CertExpiry float64 `json:"certExpiry,omitempty"`
		Error      string  `json:"error,omitempty"`
	}

	// 任务名 -> 最近异常原因（由 Scheduler 记录到 Store 的内存结果）
	errByTask := map[string]string{}
	if last := a.dialtest.LastResults(); len(last) > 0 {
		nameByID := map[string]string{}
		for _, t := range a.dialtest.List() {
			nameByID[t.ID] = t.Name
		}
		for id, r := range last {
			if !r.Up && r.Error != "" {
				if n, ok := nameByID[id]; ok {
					errByTask[n] = r.Error
				}
			}
		}
	}

	results := map[string]*dialtestResult{}
	for _, s := range upSeries {
		name := s.Labels["name"]
		if name == "" || len(s.Points) == 0 {
			continue
		}
		results[name] = &dialtestResult{
			Name:   name,
			Type:   s.Labels["type"],
			Target: s.Labels["target"],
			Up:     s.Points[len(s.Points)-1].Value > 0,
			Error:  errByTask[name],
		}
	}
	for _, s := range latencySeries {
		name := s.Labels["name"]
		if name == "" || len(s.Points) == 0 {
			continue
		}
		if r, ok := results[name]; ok {
			r.Latency = round2(s.Points[len(s.Points)-1].Value)
		}
	}
	for _, s := range certSeries {
		name := s.Labels["name"]
		if name == "" || len(s.Points) == 0 {
			continue
		}
		if r, ok := results[name]; ok {
			r.CertExpiry = round2(s.Points[len(s.Points)-1].Value)
		}
	}

	out := make([]dialtestResult, 0, len(results))
	for _, r := range results {
		out = append(out, *r)
	}
	writeJSON(w, 200, map[string]interface{}{"results": out})
}

// ---- 报告生成 ----

func (a *API) handleReportGenerate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Type string `json:"type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	id, err := a.report.Generate(report.ReportType(req.Type))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"id": id})
}

func (a *API) handleReportDownload(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	html, err := a.report.GetHTML(id)
	if err != nil {
		http.Error(w, "report not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func (a *API) handleReportHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"reports": a.report.History()})
}

// ---- Kubernetes ----

func (a *API) handleK8sInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("k8s_cluster_up", nil)
	if err != nil {
		slog.Error("查询 K8s 集群失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type k8sClusterInfo struct {
		Node                 string  `json:"node"`
		Instance             string  `json:"instance"`
		Name                 string  `json:"name"`
		Version              string  `json:"version"`
		Up                   bool    `json:"up"`
		Group                string  `json:"group"`
		NodesTotal           float64 `json:"nodesTotal"`
		NodesReady           float64 `json:"nodesReady"`
		PodsTotal            float64 `json:"podsTotal"`
		PodsRunning          float64 `json:"podsRunning"`
		PodsPending          float64 `json:"podsPending"`
		PodsFailed           float64 `json:"podsFailed"`
		DeploymentsTotal     float64 `json:"deploymentsTotal"`
		DeploymentsUnhealthy float64 `json:"deploymentsUnhealthy"`
		StatefulSetsTotal    float64 `json:"statefulSetsTotal"`
		StatefulSetsUnhealthy float64 `json:"statefulSetsUnhealthy"`
		DaemonSetsTotal      float64 `json:"daemonSetsTotal"`
		DaemonSetsUnhealthy  float64 `json:"daemonSetsUnhealthy"`
	}

	clusters := map[string]*k8sClusterInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if _, exists := clusters[key]; !exists {
			ci := &k8sClusterInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			clusters[key] = ci
			keys = append(keys, key)
		}
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的集群，
	// 补列出来并标记为离线，避免误判为"尚未配置 Kubernetes 监控"。
	for _, ki := range instancereg.Default.K8sInstances() {
		key := ki.Node + "|" + ki.Instance
		if _, ok := clusters[key]; ok {
			continue
		}
		clusters[key] = &k8sClusterInfo{
			Node:     ki.Node,
			Instance: ki.Instance,
			Name:     ki.Name,
			Version:  ki.Version,
			Group:    ki.Group,
			Up:       false,
		}
		keys = append(keys, key)
	}

	metricMap := map[string]func(ci *k8sClusterInfo, v float64){
		"k8s_nodes_total":            func(ci *k8sClusterInfo, v float64) { ci.NodesTotal = v },
		"k8s_nodes_ready":            func(ci *k8sClusterInfo, v float64) { ci.NodesReady = v },
		"k8s_pods_total":             func(ci *k8sClusterInfo, v float64) { ci.PodsTotal = v },
		"k8s_pods_running":           func(ci *k8sClusterInfo, v float64) { ci.PodsRunning = v },
		"k8s_pods_pending":           func(ci *k8sClusterInfo, v float64) { ci.PodsPending = v },
		"k8s_pods_failed":            func(ci *k8sClusterInfo, v float64) { ci.PodsFailed = v },
		"k8s_deployments_total":      func(ci *k8sClusterInfo, v float64) { ci.DeploymentsTotal = v },
		"k8s_deployments_unhealthy":  func(ci *k8sClusterInfo, v float64) { ci.DeploymentsUnhealthy = v },
		"k8s_statefulsets_total":     func(ci *k8sClusterInfo, v float64) { ci.StatefulSetsTotal = v },
		"k8s_statefulsets_unhealthy": func(ci *k8sClusterInfo, v float64) { ci.StatefulSetsUnhealthy = v },
		"k8s_daemonsets_total":       func(ci *k8sClusterInfo, v float64) { ci.DaemonSetsTotal = v },
		"k8s_daemonsets_unhealthy":   func(ci *k8sClusterInfo, v float64) { ci.DaemonSetsUnhealthy = v },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 K8s 集群指标查询失败", "metric", metricName, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			ci, ok := clusters[node+"|"+instance]
			if !ok {
				continue
			}
			setter(ci, s.Points[len(s.Points)-1].Value)
		}
	}

	clusterOut := make([]k8sClusterInfo, 0, len(keys))
	for _, k := range keys {
		clusterOut = append(clusterOut, *clusters[k])
	}
	sort.Slice(clusterOut, func(i, j int) bool { return clusterOut[i].Name < clusterOut[j].Name })

	// 节点明细
	type k8sNodeInfo struct {
		Cluster   string  `json:"cluster"`
		Instance  string  `json:"instance"`
		NodeName  string  `json:"nodeName"`
		Role      string  `json:"role"`
		IP        string  `json:"ip"`
		Ready     bool    `json:"ready"`
		CPUCores  float64 `json:"cpuCores"`
		MemBytes  float64 `json:"memBytes"`
	}
	nodes := map[string]*k8sNodeInfo{}
	var nodeKeys []string
	if readySeries, err := a.store.QueryAllLatest("k8s_node_ready", nil); err == nil {
		for _, s := range readySeries {
			instance := s.Labels["instance"]
			nodeName := s.Labels["node_name"]
			if instance == "" || nodeName == "" || len(s.Points) == 0 {
				continue
			}
			key := instance + "|" + nodeName
			if _, ok := nodes[key]; !ok {
			nodes[key] = &k8sNodeInfo{
				Cluster:  s.Labels["name"],
				Instance: instance,
				NodeName: nodeName,
				Role:     s.Labels["role"],
				IP:       s.Labels["internal_ip"],
				Ready:    s.Points[len(s.Points)-1].Value > 0,
			}
				nodeKeys = append(nodeKeys, key)
			}
		}
	}
	for metric, setter := range map[string]func(*k8sNodeInfo, float64){
		"k8s_node_cpu_usage_cores": func(n *k8sNodeInfo, v float64) { n.CPUCores = round2(v) },
		"k8s_node_mem_usage_bytes": func(n *k8sNodeInfo, v float64) { n.MemBytes = round2(v) },
	} {
		series, err := a.store.QueryAllLatest(metric, nil)
		if err != nil {
			continue
		}
		for _, s := range series {
			instance := s.Labels["instance"]
			nodeName := s.Labels["node_name"]
			if instance == "" || nodeName == "" || len(s.Points) == 0 {
				continue
			}
			if n, ok := nodes[instance+"|"+nodeName]; ok {
				setter(n, s.Points[len(s.Points)-1].Value)
			}
		}
	}
	nodeOut := make([]k8sNodeInfo, 0, len(nodeKeys))
	for _, k := range nodeKeys {
		nodeOut = append(nodeOut, *nodes[k])
	}
	sort.Slice(nodeOut, func(i, j int) bool { return nodeOut[i].NodeName < nodeOut[j].NodeName })

	// 异常 Pod 明细
	type k8sPodInfo struct {
		Cluster   string `json:"cluster"`
		Instance  string `json:"instance"`
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Phase     string `json:"phase"`
	}
	var podOut []k8sPodInfo
	if phaseSeries, err := a.store.QueryAllLatest("k8s_pod_phase", nil); err == nil {
		for _, s := range phaseSeries {
			instance := s.Labels["instance"]
			pod := s.Labels["pod"]
			if instance == "" || pod == "" || len(s.Points) == 0 || s.Points[len(s.Points)-1].Value == 0 {
				continue
			}
			podOut = append(podOut, k8sPodInfo{
				Cluster:   s.Labels["name"],
				Instance:  instance,
				Namespace: s.Labels["namespace"],
				Pod:       pod,
				Phase:     s.Labels["phase"],
			})
		}
	}
	sort.Slice(podOut, func(i, j int) bool { return podOut[i].Pod < podOut[j].Pod })

	writeJSON(w, 200, map[string]interface{}{"clusters": clusterOut, "nodes": nodeOut, "pods": podOut})
}
