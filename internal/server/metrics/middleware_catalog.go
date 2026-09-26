package metrics

// init 注册中间件（Agent/直连采集）指标元数据。
//
// 不变量：此处登记的 Name 必须与「Agent 采集器 / Server receiver 实际写入时序库的名字」
// 逐字一致。名字写错的后果不是报错，而是静默失效——「指标浏览」按目录查询永远查不到数据，
// 且 `/metrics/active` 会把它标为未上线，排查时极易被误判为「采集没开」。
//
// 注意两种采集模式的名字来源不同：
//   - **直连采集**（默认）：名字由本仓库代码决定，见 internal/agent/collector/*.go 与
//     internal/server/receiver/receiver.go，此处登记的就是这些名字（存活指标为
//     `<mw>_instance_up`，MongoDB/FastDFS 为 `<mw>_up`，K8s 为 `k8s_cluster_up`）。
//   - **exporter 模式**：名字来自外部 exporter 的 /metrics 文本（按中间件前缀透传），
//     例如 mysqld_exporter 暴露的 mysql_up（不加反引号，避免被源码扫描类工具误判为指标字面量）。
//     这类名字随 exporter 实现而定，不在此处登记；需要时在「指标浏览」中直接搜索即可。
func init() {
	// —— Redis ——
	Register(MetricMeta{Name: "redis_instance_up", Title: "Redis 存活", Category: CatRedis, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "redis_connected_clients", Title: "已连接客户端", Category: CatRedis, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_used_memory", Title: "内存占用", Category: CatRedis, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_maxclients", Title: "连接数上限", Category: CatRedis, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "redis_hit_rate", Title: "命中率", Category: CatRedis, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "redis_ops_per_sec", Title: "QPS", Category: CatRedis, Unit: "次/s", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_uptime_in_seconds", Title: "运行时长", Category: CatRedis, Unit: "s", Chart: ChartLine})

	// —— MySQL ——
	Register(MetricMeta{Name: "mysql_instance_up", Title: "MySQL 存活", Category: CatMySQL, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "mysql_queries_per_sec", Title: "QPS", Category: CatMySQL, Unit: "次/s", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_threads_connected", Title: "活跃连接数", Category: CatMySQL, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_slow_queries", Title: "慢查询数", Category: CatMySQL, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_uptime", Title: "运行时长", Category: CatMySQL, Unit: "s", Chart: ChartLine})

	// —— PostgreSQL ——
	Register(MetricMeta{Name: "postgres_instance_up", Title: "PostgreSQL 存活", Category: CatPostgres, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "postgres_numbackends", Title: "连接数", Category: CatPostgres, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "postgres_uptime_seconds", Title: "运行时长", Category: CatPostgres, Unit: "s", Chart: ChartLine})

	// —— Nginx ——
	Register(MetricMeta{Name: "nginx_instance_up", Title: "Nginx 存活", Category: CatNginx, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "nginx_active_connections", Title: "活跃连接", Category: CatNginx, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "nginx_requests", Title: "累计请求数", Category: CatNginx, Unit: "个", Chart: ChartLine})

	// —— Kafka ——
	Register(MetricMeta{Name: "kafka_instance_up", Title: "Kafka 存活", Category: CatKafka, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "kafka_broker_count", Title: "Broker 数", Category: CatKafka, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "kafka_under_replicated_partitions", Title: "副本不足分区数", Category: CatKafka, Unit: "个", Chart: ChartBar})

	// —— Docker ——
	Register(MetricMeta{Name: "docker_container_up", Title: "容器存活", Category: CatDocker, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "docker_containers_total", Title: "容器数", Category: CatDocker, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "docker_container_net_rx_bytes", Title: "容器网络接收", Category: CatDocker, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "docker_container_net_tx_bytes", Title: "容器网络发送", Category: CatDocker, Unit: "B", Chart: ChartLine})

	// —— MongoDB ——
	Register(MetricMeta{Name: "mongodb_up", Title: "MongoDB 存活", Category: CatMongo, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "mongodb_uptime_seconds", Title: "运行时长", Category: CatMongo, Unit: "s", Chart: ChartLine})
	Register(MetricMeta{Name: "mongodb_connections_current", Title: "当前连接数", Category: CatMongo, Unit: "个", Chart: ChartLine})

	// —— RocketMQ ——
	Register(MetricMeta{Name: "rocketmq_instance_up", Title: "RocketMQ 存活", Category: CatRocketMQ, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "rocketmq_producer_tps", Title: "生产 TPS", Category: CatRocketMQ, Unit: "次/s", Chart: ChartLine})

	// —— Kubernetes ——
	Register(MetricMeta{Name: "k8s_cluster_up", Title: "Kubernetes 存活", Category: CatK8s, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "k8s_node_cpu_usage_cores", Title: "节点 CPU 用量", Category: CatK8s, Unit: "核", Chart: ChartLine})
	Register(MetricMeta{Name: "k8s_node_mem_usage_bytes", Title: "节点内存用量", Category: CatK8s, Unit: "B", Chart: ChartLine})

	// —— FastDFS ——
	// （此前 FastDFS 在指标目录中完全没有条目，连分类常量都缺失）
	Register(MetricMeta{Name: "fastdfs_up", Title: "FastDFS 存活", Category: CatFastDFS, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "fastdfs_storage_count", Title: "Storage 节点数", Category: CatFastDFS, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "fastdfs_storage_online_count", Title: "在线 Storage", Category: CatFastDFS, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "fastdfs_storage_offline_count", Title: "离线 Storage", Category: CatFastDFS, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "fastdfs_group_count", Title: "Group 数", Category: CatFastDFS, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "fastdfs_total_space", Title: "总空间", Category: CatFastDFS, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "fastdfs_free_space", Title: "空闲空间", Category: CatFastDFS, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "fastdfs_used_space", Title: "已用空间", Category: CatFastDFS, Unit: "B", Chart: ChartLine})
}
