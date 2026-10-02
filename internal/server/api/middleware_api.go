package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/report"
)

// ---- MySQL ----

// mysqlInstanceInfo 是 MySQL 实例接口的响应行（包级定义：集群摘要也需要按实例聚合）。
type mysqlInstanceInfo struct {
	Node                string  `json:"node"`
	Instance            string  `json:"instance"`
	Name                string  `json:"name"`
	Role                string  `json:"role"`
	Topology            string  `json:"topology"`
	Version             string  `json:"version"`
	Up                  bool    `json:"up"`
	Group               string  `json:"group"`
	ReplicaOf           string  `json:"replicaOf,omitempty"`
	ThreadsConnected    float64 `json:"threadsConnected"`
	ThreadsRunning      float64 `json:"threadsRunning"`
	MaxConnections      float64 `json:"maxConnections"`
	QueriesPerSec       float64 `json:"queriesPerSec"`
	SlowQueries         float64 `json:"slowQueries"`
	BufferPoolHitRate   float64 `json:"bufferPoolHitRate"`
	RowLockWaits        float64 `json:"rowLockWaits"`
	Deadlocks           float64 `json:"deadlocks"`
	SecondsBehindMaster float64 `json:"secondsBehindMaster"`
	ComCommit           float64 `json:"comCommit"`
	ComRollback         float64 `json:"comRollback"`
	BytesReceived       float64 `json:"bytesReceived"`
	BytesSent           float64 `json:"bytesSent"`
	Uptime              float64 `json:"uptime"`
}

func (a *API) handleMySQLInstances(w http.ResponseWriter, r *http.Request) {
	upSeries, err := a.store.QueryAllLatest("mysql_instance_up", nil)
	if err != nil {
		slog.Error("查询 MySQL 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	// 实时在线状态：以 node|instance 为键记录最新 up 值（>0 为在线）。
	// 注意即时查询对超过 lookback-delta 的旧样本视为 stale 返回空，
	// 因此离线的 Agent 不会出现在 liveUp 中——这正是需要注册表补充的部分。
	liveUp := map[string]bool{}
	liveUpTs := map[string]int64{}
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		last := s.Points[len(s.Points)-1]
		if !newestSampleKept(liveUpTs, key, last.Timestamp) {
			continue
		}
		liveUp[key] = last.Value > 0
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
		"mysql_threads_connected":           func(ri *mysqlInstanceInfo, v float64) { ri.ThreadsConnected = round2(v) },
		"mysql_threads_running":             func(ri *mysqlInstanceInfo, v float64) { ri.ThreadsRunning = round2(v) },
		"mysql_max_connections":             func(ri *mysqlInstanceInfo, v float64) { ri.MaxConnections = round2(v) },
		"mysql_queries_per_sec":             func(ri *mysqlInstanceInfo, v float64) { ri.QueriesPerSec = round2(v) },
		"mysql_slow_queries":                func(ri *mysqlInstanceInfo, v float64) { ri.SlowQueries = round2(v) },
		"mysql_innodb_buffer_pool_hit_rate": func(ri *mysqlInstanceInfo, v float64) { ri.BufferPoolHitRate = round2(v) },
		"mysql_innodb_row_lock_waits":       func(ri *mysqlInstanceInfo, v float64) { ri.RowLockWaits = round2(v) },
		"mysql_innodb_deadlocks":            func(ri *mysqlInstanceInfo, v float64) { ri.Deadlocks = round2(v) },
		"mysql_seconds_behind_master":       func(ri *mysqlInstanceInfo, v float64) { ri.SecondsBehindMaster = round2(v) },
		"mysql_com_commit":                  func(ri *mysqlInstanceInfo, v float64) { ri.ComCommit = round2(v) },
		"mysql_com_rollback":                func(ri *mysqlInstanceInfo, v float64) { ri.ComRollback = round2(v) },
		"mysql_bytes_received":              func(ri *mysqlInstanceInfo, v float64) { ri.BytesReceived = round2(v) },
		"mysql_bytes_sent":                  func(ri *mysqlInstanceInfo, v float64) { ri.BytesSent = round2(v) },
		"mysql_uptime":                      func(ri *mysqlInstanceInfo, v float64) { ri.Uptime = round2(v) },
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
	out := make([]mysqlInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{
		"instances": out,
		"clusters":  a.buildMySQLClusters(out),
	})
}

// mysqlClusterInfo 是 MySQL 集群组（Group Replication / InnoDB Cluster）的健康摘要，
// 供页面状态徽标直接渲染。
//
// 判定复用告警引擎同一实现（alert.ClassifyClusterFault）：此前页面自己按「有无离线 + 主从延迟」
// 算健康、从不数主库个数，导致同一实例在页面显示「运行正常」、在告警里被判「多主/脑裂」。
type mysqlClusterInfo struct {
	Name    string `json:"name"`
	Mode    string `json:"mode"`    // single=单主模式，multi=多主模式，unknown=未知（需升级 Agent）
	Fault   string `json:"fault"`   // 空字符串表示健康；非空为「集群状态损坏」的具体原因
	Members int    `json:"members"` // 组内成员数
}

// buildMySQLClusters 汇总各集群组状态：仅对 cluster 拓扑（Group Replication / InnoDB Cluster）出结论。
// visible 为已按用户资源范围过滤后的实例列表，保证结论不越权。
func (a *API) buildMySQLClusters(visible []mysqlInstanceInfo) []mysqlClusterInfo {
	nodeSeen, nameSeen := map[string]bool{}, map[string]bool{}
	var nodes, names []string
	visibleCount := map[string]int{}
	for _, i := range visible {
		if !strings.EqualFold(i.Topology, "cluster") {
			continue
		}
		if !nodeSeen[i.Node] {
			nodeSeen[i.Node] = true
			nodes = append(nodes, i.Node)
		}
		grp := i.Group
		if grp == "" {
			grp = i.Name
		}
		if grp == "" {
			grp = i.Instance
		}
		if !nameSeen[grp] {
			nameSeen[grp] = true
			names = append(names, grp)
		}
		visibleCount[grp]++
	}
	if len(names) == 0 {
		return nil
	}

	byGroup := map[string][]alert.ClusterMember{}
	for _, m := range alert.LoadClusterMembers(a.store, "mysql", nodes) {
		if !strings.EqualFold(m.Topology, "cluster") {
			continue
		}
		grp := m.Group
		if grp == "" {
			grp = m.Instance
		}
		byGroup[grp] = append(byGroup[grp], m)
	}

	out := make([]mysqlClusterInfo, 0, len(names))
	for _, grp := range names {
		members := byGroup[grp]
		info := mysqlClusterInfo{
			Name:    grp,
			Mode:    "unknown",
			Fault:   alert.ClassifyClusterFault(members),
			Members: len(members),
		}
		if info.Members == 0 {
			info.Members = visibleCount[grp]
		}
		single, multi := 0, 0
		for _, m := range members {
			if m.SinglePrimary == nil {
				continue
			}
			if *m.SinglePrimary {
				single++
			} else {
				multi++
			}
		}
		switch {
		case single > 0 && multi == 0:
			info.Mode = "single"
		case multi > 0 && single == 0:
			info.Mode = "multi"
		}
		out = append(out, info)
	}
	return out
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
		Node           string  `json:"node"`
		Instance       string  `json:"instance"`
		Name           string  `json:"name"`
		Role           string  `json:"role"`
		Topology       string  `json:"topology"`
		Version        string  `json:"version"`
		Database       string  `json:"database"`
		Up             bool    `json:"up"`
		Group          string  `json:"group"`
		Numbackends    float64 `json:"numbackends"`
		MaxConnections float64 `json:"maxConnections"`
		XactCommit     float64 `json:"xactCommit"`
		XactRollback   float64 `json:"xactRollback"`
		CacheHitRatio  float64 `json:"cacheHitRatio"`
		Deadlocks      float64 `json:"deadlocks"`
		ReplicationLag float64 `json:"replicationLag"`
		DatabaseSize   float64 `json:"databaseSize"`
		Uptime         float64 `json:"uptime"`
	}

	instances := map[string]*postgresInstanceInfo{}
	latestTs := map[string]int64{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		last := s.Points[len(s.Points)-1]
		if !newestSampleKept(latestTs, key, last.Timestamp) {
			continue
		}
		if ri, exists := instances[key]; exists {
			ri.Up = last.Value > 0
			// 元信息只在采集成功时才存在，保留已有非空值
			if v := s.Labels["role"]; v != "" {
				ri.Role = v
			}
			if v := s.Labels["topology"]; v != "" {
				ri.Topology = v
			}
			if v := s.Labels["version"]; v != "" {
				ri.Version = v
			}
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
				Up:       last.Value > 0,
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
	latestTs := map[string]int64{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		last := s.Points[len(s.Points)-1]
		if !newestSampleKept(latestTs, key, last.Timestamp) {
			continue
		}
		if ri, exists := instances[key]; exists {
			ri.Up = last.Value > 0
			// 角色/版本只在采集成功时存在（副本集角色 PRIMARY/SECONDARY），保留非空值
			if v := s.Labels["role"]; v != "" {
				ri.Role = v
			}
			if v := s.Labels["topology"]; v != "" {
				ri.Topology = v
			}
			if v := s.Labels["version"]; v != "" {
				ri.Version = v
			}
		} else {
			instances[key] = &mongoInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Topology: s.Labels["topology"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       last.Value > 0,
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
		"mongodb_uptime_seconds":        func(ri *mongoInstanceInfo, v float64) { ri.Uptime = round2(v) },
		"mongodb_connections_current":   func(ri *mongoInstanceInfo, v float64) { ri.ConnectionsCurrent = round2(v) },
		"mongodb_connections_available": func(ri *mongoInstanceInfo, v float64) { ri.ConnectionsAvail = round2(v) },
		"mongodb_mem_resident_bytes":    func(ri *mongoInstanceInfo, v float64) { ri.MemResidentMB = round2(v / 1024 / 1024) },
		"mongodb_mem_virtual_bytes":     func(ri *mongoInstanceInfo, v float64) { ri.MemVirtualMB = round2(v / 1024 / 1024) },
		"mongodb_opcounters_insert":     func(ri *mongoInstanceInfo, v float64) { ri.OpInsert = round2(v) },
		"mongodb_opcounters_query":      func(ri *mongoInstanceInfo, v float64) { ri.OpQuery = round2(v) },
		"mongodb_opcounters_update":     func(ri *mongoInstanceInfo, v float64) { ri.OpUpdate = round2(v) },
		"mongodb_opcounters_delete":     func(ri *mongoInstanceInfo, v float64) { ri.OpDelete = round2(v) },
		"mongodb_opcounters_command":    func(ri *mongoInstanceInfo, v float64) { ri.OpCommand = round2(v) },
		"mongodb_db_dataSize_bytes":     func(ri *mongoInstanceInfo, v float64) { ri.DbDataSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_storageSize_bytes":  func(ri *mongoInstanceInfo, v float64) { ri.DbStorageSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_indexSize_bytes":    func(ri *mongoInstanceInfo, v float64) { ri.DbIndexSizeMB = round2(v / 1024 / 1024) },
		"mongodb_db_objects":            func(ri *mongoInstanceInfo, v float64) { ri.DbObjects = round2(v) },
		"mongodb_db_indexes":            func(ri *mongoInstanceInfo, v float64) { ri.DbIndexes = round2(v) },
		"mongodb_repl_state":            func(ri *mongoInstanceInfo, v float64) { ri.ReplState = round2(v) },
		"mongodb_repl_health":           func(ri *mongoInstanceInfo, v float64) { ri.ReplHealth = round2(v) },
		"mongodb_repl_lag":              func(ri *mongoInstanceInfo, v float64) { ri.ReplLag = round2(v) },
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
		"fastdfs_group_count":           func(ri *fastdfsInstanceInfo, v float64) { ri.GroupTotal = round2(v) },
		"fastdfs_storage_count":         func(ri *fastdfsInstanceInfo, v float64) { ri.StorageTotal = round2(v) },
		"fastdfs_storage_online_count":  func(ri *fastdfsInstanceInfo, v float64) { ri.StorageOnline = round2(v) },
		"fastdfs_storage_offline_count": func(ri *fastdfsInstanceInfo, v float64) { ri.StorageOffline = round2(v) },
		"fastdfs_total_space":           func(ri *fastdfsInstanceInfo, v float64) { ri.TotalSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_free_space":            func(ri *fastdfsInstanceInfo, v float64) { ri.FreeSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_used_space":            func(ri *fastdfsInstanceInfo, v float64) { ri.UsedSpaceMB = round2(v / 1024 / 1024) },
		"fastdfs_trunk_free_space":      func(ri *fastdfsInstanceInfo, v float64) { ri.TrunkFreeMB = round2(v / 1024 / 1024) },
		"fastdfs_disk_read_bytes":       func(ri *fastdfsInstanceInfo, v float64) { ri.DiskReadMB = round2(v / 1024 / 1024) },
		"fastdfs_disk_write_bytes":      func(ri *fastdfsInstanceInfo, v float64) { ri.DiskWriteMB = round2(v / 1024 / 1024) },
		"fastdfs_net_recv_bytes":        func(ri *fastdfsInstanceInfo, v float64) { ri.NetRecvMB = round2(v / 1024 / 1024) },
		"fastdfs_net_sent_bytes":        func(ri *fastdfsInstanceInfo, v float64) { ri.NetSentMB = round2(v / 1024 / 1024) },
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
		Node               string  `json:"node"`
		NodeIP             string  `json:"nodeIp"`
		Instance           string  `json:"instance"`
		Name               string  `json:"name"`
		Version            string  `json:"version"`
		Up                 bool    `json:"up"`
		Group              string  `json:"group"`
		ActiveConnections  float64 `json:"activeConnections"`
		Accepts            float64 `json:"accepts"`
		Handled            float64 `json:"handled"`
		Requests           float64 `json:"requests"`
		Reading            float64 `json:"reading"`
		Writing            float64 `json:"writing"`
		Waiting            float64 `json:"waiting"`
		ConnectionDropRate float64 `json:"connectionDropRate"`
	}

	instances := map[string]*nginxInstanceInfo{}
	latestTs := map[string]int64{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		last := s.Points[len(s.Points)-1]
		if !newestSampleKept(latestTs, key, last.Timestamp) {
			continue
		}
		if ri, exists := instances[key]; exists {
			ri.Up = last.Value > 0
			// version 来自 stub_status 响应头，仅采集成功时存在，保留非空值
			if v := s.Labels["version"]; v != "" {
				ri.Version = v
			}
		} else {
			instances[key] = &nginxInstanceInfo{
				Node:     node,
				NodeIP:   a.nodeIP(node),
				Instance: instance,
				Name:     s.Labels["name"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       last.Value > 0,
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
	out := make([]nginxInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}

// middlewareOverviewType 单类中间件健康度。
type middlewareOverviewType struct {
	Type       string          `json:"type"`       // redis/mysql/postgres/nginx/kafka/docker/rocketmq/k8s
	Label      string          `json:"label"`      // 中文名
	Kind       string          `json:"kind"`       // builtin（内置）| template（由采集项模板派生）
	Total      int             `json:"total"`      // 实例总数
	Up         int             `json:"up"`         // 在线实例数
	Down       int             `json:"down"`       // 离线实例数
	AlertCount int             `json:"alertCount"` // 关联活跃告警数
	Summary    []mwSummaryItem `json:"summary"`    // 核心指标摘要（卡片展示）
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
//
// 具体内容已收敛到 mwreg 的类型注册表（内置类型在 mwreg/builtin.go，
// 模板派生类型由模板的 rules.metrics 现算），此处只保留渲染用的结构体。

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

// handleMiddlewareOverview 返回中间件健康度总览（各类型实例数/在线率/告警数），
// 供数据大屏中间件监控板块一次拉取，避免前端逐个请求轮询。
//
// 类型清单来自 mwreg 注册表：内置 10 类 + 由采集项模板派生的类型，
// 因此「新增中间件只写模板」在这里自动生效（前端据此渲染卡片与 Tab）。
func (a *API) handleMiddlewareOverview(w http.ResponseWriter, r *http.Request) {
	p := Principal(r)
	types := a.middlewareRegistry().Types()

	// 活跃告警按指标前缀归类（仅统计当前用户可见节点，避免泄露范围外的告警量）
	alertCount := map[string]int{}
	for _, ev := range a.alerts.Active() {
		if !a.nodeInScope(p, ev.Node) {
			continue
		}
		metric := strings.ToLower(ev.Metric)
		for _, t := range types {
			// 模板指标名同样以模板 id 为前缀（且 id 之间、id 与内置前缀之间互不为前缀，已由校验器保证）
			if strings.HasPrefix(metric, t.Key+"_") {
				alertCount[t.Key]++
				break
			}
		}
	}

	resp := middlewareOverviewResp{Types: make([]middlewareOverviewType, 0, len(types))}
	for _, t := range types {
		item := middlewareOverviewType{Type: t.Key, Label: t.Label, Kind: string(t.Kind), AlertCount: alertCount[t.Key]}
		series, err := a.store.QueryAllLatest(t.UpMetric, t.UpLabels)
		if err != nil {
			slog.Warn("查询中间件 up 指标失败", "metric", t.UpMetric, "err", err)
			resp.Types = append(resp.Types, item)
			continue
		}
		seen := map[string]bool{}
		for _, s := range series {
			// 资源范围：范围外节点的实例不参与计数（总数/在线/离线均需在过滤后重算）
			if !a.nodeInScope(p, s.Labels["node"]) {
				continue
			}
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
		for _, sp := range t.Summary {
			if v, ok := mwAggregateLatest(a, sp.Metric, sp.Agg); ok {
				item.Summary = append(item.Summary, mwSummaryItem{
					Key:   sp.Metric,
					Label: sp.Label,
					Value: v,
					Unit:  sp.Unit,
					Warn:  sp.WarnAbove > 0 && v >= sp.WarnAbove,
				})
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

// nodeDisplayName 返回指定节点的自定义显示名（别名）；未设置别名时返回空串，调用方应回退到真实主机名。
func (a *API) nodeDisplayName(node string) string {
	if node == "" {
		return ""
	}
	n, ok := a.nodeMgr.GetNode(node)
	if !ok {
		return ""
	}
	return n.DisplayName
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
		Node                      string  `json:"node"`
		Instance                  string  `json:"instance"`
		Name                      string  `json:"name"`
		Role                      string  `json:"role"`
		Version                   string  `json:"version"`
		Up                        bool    `json:"up"`
		Group                     string  `json:"group"`
		BrokerCount               float64 `json:"brokerCount"`
		TopicCount                float64 `json:"topicCount"`
		PartitionCount            float64 `json:"partitionCount"`
		UnderReplicatedPartitions float64 `json:"underReplicatedPartitions"`
		OfflinePartitions         float64 `json:"offlinePartitions"`
		ConsumerGroupCount        float64 `json:"consumerGroupCount"`
		ConsumerLag               float64 `json:"consumerLag"`
		ConsumerLagMax            float64 `json:"consumerLagMax"`
		ActiveControllerCount     float64 `json:"activeControllerCount"`
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
		Node        string  `json:"node"`
		Instance    string  `json:"instance"` // 容器短 ID
		Name        string  `json:"name"`     // 容器名
		Image       string  `json:"image"`
		Status      string  `json:"status"`
		Up          bool    `json:"up"`
		Group       string  `json:"group"`
		CPUPercent  float64 `json:"cpuPercent"`
		MemUsage    float64 `json:"memUsage"`
		MemLimit    float64 `json:"memLimit"`
		MemPercent  float64 `json:"memPercent"`
		NetRx       float64 `json:"netRx"`
		NetTx       float64 `json:"netTx"`
		DiskRead    float64 `json:"diskRead"`
		DiskWrite   float64 `json:"diskWrite"`
		PidsCurrent float64 `json:"pidsCurrent"`
	}

	instances := map[string]*dockerContainerInfo{}
	latestTs := map[string]int64{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		last := s.Points[len(s.Points)-1]
		// status 标签随容器启停变化（running/exited），容器重启后同一容器 ID 会
		// 同时存在旧 status 的 up=0 与新 status 的 up=1 两条序列，只取最新者。
		if !newestSampleKept(latestTs, key, last.Timestamp) {
			continue
		}
		ri, exists := instances[key]
		if !exists {
			ri = &dockerContainerInfo{Node: node, Instance: instance}
			instances[key] = ri
			keys = append(keys, key)
		}
		if v := s.Labels["container_name"]; v != "" {
			ri.Name = v
		}
		if v := s.Labels["image"]; v != "" {
			ri.Image = v
		}
		if v := s.Labels["status"]; v != "" {
			ri.Status = v
		}
		if v := s.Labels["group"]; v != "" {
			ri.Group = v
		}
		ri.Up = last.Value > 0
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
		"docker_container_cpu_percent":      func(ri *dockerContainerInfo, v float64) { ri.CPUPercent = round2(v) },
		"docker_container_mem_usage_bytes":  func(ri *dockerContainerInfo, v float64) { ri.MemUsage = round2(v) },
		"docker_container_mem_limit_bytes":  func(ri *dockerContainerInfo, v float64) { ri.MemLimit = round2(v) },
		"docker_container_mem_percent":      func(ri *dockerContainerInfo, v float64) { ri.MemPercent = round2(v) },
		"docker_container_net_rx_bytes":     func(ri *dockerContainerInfo, v float64) { ri.NetRx = round2(v) },
		"docker_container_net_tx_bytes":     func(ri *dockerContainerInfo, v float64) { ri.NetTx = round2(v) },
		"docker_container_disk_read_bytes":  func(ri *dockerContainerInfo, v float64) { ri.DiskRead = round2(v) },
		"docker_container_disk_write_bytes": func(ri *dockerContainerInfo, v float64) { ri.DiskWrite = round2(v) },
		"docker_container_pids_current":     func(ri *dockerContainerInfo, v float64) { ri.PidsCurrent = round2(v) },
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

	// 资源范围：受限用户只能看到范围内节点上的容器与 Docker 主机。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
		"docker_containers_running": func(h *dockerHostInfo, v float64) { h.ContainersRunning = v },
		"docker_containers_stopped": func(h *dockerHostInfo, v float64) { h.ContainersStopped = v },
		"docker_images_total":       func(h *dockerHostInfo, v float64) { h.ImagesTotal = v },
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
	hostKeys = filterByNodeScope(a, Principal(r), hostKeys, nodeOfKey)
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
		Node                string  `json:"node"`
		Instance            string  `json:"instance"`
		Name                string  `json:"name"`
		Role                string  `json:"role"`
		Version             string  `json:"version"`
		Up                  bool    `json:"up"`
		Group               string  `json:"group"`
		BrokerCount         float64 `json:"brokerCount"`
		TopicCount          float64 `json:"topicCount"`
		ConsumerGroupCount  float64 `json:"consumerGroupCount"`
		BrokerTPS           float64 `json:"brokerTps"`
		ProducerTPS         float64 `json:"producerTps"`
		ConsumerTPS         float64 `json:"consumerTps"`
		MessageAccumulation float64 `json:"messageAccumulation"`
		ConsumerLag         float64 `json:"consumerLag"`
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
		"rocketmq_broker_count":         func(ri *rocketmqInstanceInfo, v float64) { ri.BrokerCount = round2(v) },
		"rocketmq_topic_count":          func(ri *rocketmqInstanceInfo, v float64) { ri.TopicCount = round2(v) },
		"rocketmq_consumer_group_count": func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerGroupCount = round2(v) },
		"rocketmq_broker_tps":           func(ri *rocketmqInstanceInfo, v float64) { ri.BrokerTPS = round2(v) },
		"rocketmq_producer_tps":         func(ri *rocketmqInstanceInfo, v float64) { ri.ProducerTPS = round2(v) },
		"rocketmq_consumer_tps":         func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerTPS = round2(v) },
		"rocketmq_message_accumulation": func(ri *rocketmqInstanceInfo, v float64) { ri.MessageAccumulation = round2(v) },
		"rocketmq_consumer_lag":         func(ri *rocketmqInstanceInfo, v float64) { ri.ConsumerLag = round2(v) },
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

	// 资源范围：受限用户只能看到范围内节点上的实例。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
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
	if mw.Enabled && (mw.Start <= 0 || mw.End <= mw.Start) {
		http.Error(w, "enabled maintenance window requires end > start", http.StatusBadRequest)
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
		Node        string  `json:"node"`
		Instance    string  `json:"instance"`
		Name        string  `json:"name"`
		Version     string  `json:"version"`
		Up          bool    `json:"up"`
		Group       string  `json:"group"`
		NodesTotal  float64 `json:"nodesTotal"`
		NodesReady  float64 `json:"nodesReady"`
		PodsTotal   float64 `json:"podsTotal"`
		PodsRunning float64 `json:"podsRunning"`
		PodsPending float64 `json:"podsPending"`
		PodsFailed  float64 `json:"podsFailed"`
		// PodsAbnormal 是"有效状态不正常"的 Pod 数：含 phase=Running 但容器在
		// CrashLoopBackOff / RunContainerError 的那一类。与上面几个 phase 桶
		// **不是一个口径**（那些是生命周期阶段，这个是健康度），别互相推导。
		PodsAbnormal          float64 `json:"podsAbnormal"`
		DeploymentsTotal      float64 `json:"deploymentsTotal"`
		DeploymentsUnhealthy  float64 `json:"deploymentsUnhealthy"`
		StatefulSetsTotal     float64 `json:"statefulSetsTotal"`
		StatefulSetsUnhealthy float64 `json:"statefulSetsUnhealthy"`
		DaemonSetsTotal       float64 `json:"daemonSetsTotal"`
		DaemonSetsUnhealthy   float64 `json:"daemonSetsUnhealthy"`
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

	// 异常 Pod 计数：单独查一次而不是塞进上面的 metricMap，因为要区分
	// "指标值为 0" 与 "指标根本不存在"——后者是旧 Agent（不产出 k8s_pods_abnormal），
	// 此时必须退回旧口径 pending+failed，否则升级服务端后界面上的"异常 Pod"
	// 会直接变成 0，把已经存在的异常悄悄藏起来。
	abnormalSeen := map[string]bool{}
	if series, err := a.store.QueryAllLatest("k8s_pods_abnormal", nil); err == nil {
		for _, s := range series {
			node, instance := s.Labels["node"], s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			key := node + "|" + instance
			abnormalSeen[key] = true
			if ci, ok := clusters[key]; ok {
				ci.PodsAbnormal = s.Points[len(s.Points)-1].Value
			}
		}
	}
	for key, ci := range clusters {
		if !abnormalSeen[key] {
			ci.PodsAbnormal = ci.PodsPending + ci.PodsFailed
		}
	}

	// 资源范围：受限用户只能看到范围内节点上的集群。
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
	clusterOut := make([]k8sClusterInfo, 0, len(keys))
	for _, k := range keys {
		clusterOut = append(clusterOut, *clusters[k])
	}
	sort.Slice(clusterOut, func(i, j int) bool { return clusterOut[i].Name < clusterOut[j].Name })

	// 节点明细
	type k8sNodeInfo struct {
		Cluster  string  `json:"cluster"`
		Instance string  `json:"instance"`
		NodeName string  `json:"nodeName"`
		Role     string  `json:"role"`
		IP       string  `json:"ip"`
		Ready    bool    `json:"ready"`
		CPUCores float64 `json:"cpuCores"`
		MemBytes float64 `json:"memBytes"`
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
		// Phase 是生命周期阶段（Pending / Running ...），Status 是**有效状态**
		// （ImagePullBackOff / CrashLoopBackOff / RunContainerError ...）。
		// 两个都留：界面展示 Status（运维认得的那个），Phase 用于判断"是不是还在调度"。
		Phase  string `json:"phase"`
		Status string `json:"status"`
	}
	// 异常 Pod 明细。这里**不能**用 QueryAllLatest（即时查询）：
	//
	// VictoriaMetrics 的即时查询把返回的时间戳设成**求值时刻**而不是样本时间
	// （见 storage/querier.go 的 QueryInstantWithLookback 注释与 alert/engine.go 的
	// freshSampleWindow），于是同一 Pod 的历史序列（ContainerCreating / ErrImagePull /
	// ImagePullBackOff ...）时间戳完全相同，"按时间戳取最新"彻底失效；而且指标序列
	// 只要写过就不会消失，即时查询会一直把它们返回。实机验证看到的现象就是：
	// 一个 Pod 在列表里出现多行，而且已经跑起来的健康 Pod 也永久留在"异常"里。
	//
	// QueryInstantWithLookback 走 range-vector（expr[窗口]）：窗口内没有采样的陈旧
	// 序列直接不返回，且返回的是**真实样本时间戳**。窗口沿用告警引擎 freshSampleWindow
	// 的口径（90s ≈ 6 个采集周期，足以容忍抖动），再按 node|instance|namespace|pod
	// 取时间戳最新的一条，解决同一 Pod 在窗口内发生状态迁移时的重复行。
	const detailWindow = 90 * time.Second
	latestTS := map[string]int64{}
	bestPod := map[string]k8sPodInfo{}
	if phaseSeries, err := a.store.QueryInstantWithLookback("", "k8s_pod_phase", nil, detailWindow); err == nil {
		for _, s := range phaseSeries {
			node, instance := s.Labels["node"], s.Labels["instance"]
			pod := s.Labels["pod"]
			if instance == "" || pod == "" || len(s.Points) == 0 {
				continue
			}
			last := s.Points[len(s.Points)-1]
			if last.Value == 0 {
				continue
			}
			key := node + "|" + instance + "|" + s.Labels["namespace"] + "|" + pod
			// 同一 Pod 只采纳数据点时间戳最新的一条（约定见 mw_newest.go）。
			if !newestSampleKept(latestTS, key, last.Timestamp) {
				continue
			}
			// 旧 Agent 不产出 status 标签，退回 phase——显示得糙一点，好过显示空白。
			status := s.Labels["status"]
			if status == "" {
				status = s.Labels["phase"]
			}
			bestPod[key] = k8sPodInfo{
				Cluster:   s.Labels["name"],
				Instance:  instance,
				Namespace: s.Labels["namespace"],
				Pod:       pod,
				Phase:     s.Labels["phase"],
				Status:    status,
			}
		}
	}
	podOut := make([]k8sPodInfo, 0, len(bestPod))
	for _, p := range bestPod {
		podOut = append(podOut, p)
	}
	sort.Slice(podOut, func(i, j int) bool { return podOut[i].Pod < podOut[j].Pod })

	// 资源范围：K8s 工作节点与 Pod 数据自身不带 Agent 节点标签，
	// 按「可见集群」的 instance 归属过滤（clusterOut 已按范围过滤）。
	if p := Principal(r); p != nil && !p.Scope.IsGlobal() {
		allowed := map[string]bool{}
		for _, c := range clusterOut {
			allowed[c.Instance] = true
		}
		keptNodes := nodeOut[:0]
		for _, nd := range nodeOut {
			if allowed[nd.Instance] {
				keptNodes = append(keptNodes, nd)
			}
		}
		nodeOut = keptNodes
		keptPods := podOut[:0]
		for _, pd := range podOut {
			if allowed[pd.Instance] {
				keptPods = append(keptPods, pd)
			}
		}
		podOut = keptPods
	}

	writeJSON(w, 200, map[string]interface{}{"clusters": clusterOut, "nodes": nodeOut, "pods": podOut})
}
