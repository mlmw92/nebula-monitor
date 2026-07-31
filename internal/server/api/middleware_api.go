package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/dialtest"
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

	instances := map[string]*mysqlInstanceInfo{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		if _, exists := instances[key]; !exists {
			ri := &mysqlInstanceInfo{
				Node:      node,
				Instance:  instance,
				Name:      s.Labels["name"],
				Role:      s.Labels["role"],
				Topology:  s.Labels["topology"],
				Version:   s.Labels["version"],
				Group:     s.Labels["group"],
				ReplicaOf: s.Labels["replica_of"],
				Up:        s.Points[len(s.Points)-1].Value > 0,
			}
			instances[key] = ri
			keys = append(keys, key)
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
		if _, exists := instances[key]; !exists {
			ri := &postgresInstanceInfo{
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
			instances[key] = ri
			keys = append(keys, key)
		}
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
		if _, exists := instances[key]; !exists {
			ri := &nginxInstanceInfo{
				Node:    node,
				Instance: instance,
				Name:    s.Labels["name"],
				Version: s.Labels["version"],
				Group:   s.Labels["group"],
				Up:      s.Points[len(s.Points)-1].Value > 0,
			}
			instances[key] = ri
			keys = append(keys, key)
		}
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
		if _, exists := instances[key]; !exists {
			ri := &kafkaInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			instances[key] = ri
			keys = append(keys, key)
		}
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
		Group             string  `json:"group"`
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
					Group:           s.Labels["group"],
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
		if _, exists := instances[key]; !exists {
			ri := &rocketmqInstanceInfo{
				Node:     node,
				Instance: instance,
				Name:     s.Labels["name"],
				Role:     s.Labels["role"],
				Version:  s.Labels["version"],
				Group:    s.Labels["group"],
				Up:       s.Points[len(s.Points)-1].Value > 0,
			}
			instances[key] = ri
			keys = append(keys, key)
		}
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
