package mwreg

// builtinTypes 是内置的 10 类中间件。
//
// 这份表是「类型清单」的唯一来源，由原先散落四处的定义合并而来：
// api 的 middlewareTypes（Key/Label/UpMetric）、api 的 mwSummarySpecs（Summary）、
// report 的 mwDefs（Emoji/ConnMetric/KeyMetrics）。
//
// 顺序即前端卡片与报告分节的顺序：沿用原 middlewareTypes 的顺序（redis → fastdfs），
// 以免本次收敛造成界面顺序变化。
var builtinTypes = []Type{
	{
		Key: "redis", Label: "Redis", Kind: KindBuiltin, Emoji: "🗄️",
		UpMetric: "redis_instance_up", ConnMetric: "redis_connected_clients",
		ReportThroughputMetric: "redis_ops_per_sec",
		KeyMetrics: []string{
			"redis_connected_clients", "redis_maxclients", "redis_used_memory_percent",
			"redis_used_memory", "redis_hit_rate", "redis_cmd_latency_ms", "redis_ops_per_sec",
			"redis_replication_lag", "redis_evicted_keys", "redis_rejected_connections",
		},
		Summary: []SummarySpec{
			{Metric: "redis_ops_per_sec", Label: "QPS峰值", Agg: "max"},
			{Metric: "redis_used_memory_percent", Label: "内存使用率", Agg: "avg", Unit: "%", WarnAbove: 85},
			{Metric: "redis_hit_rate", Label: "命中率", Agg: "avg", Unit: "%"},
		},
	},
	{
		Key: "mysql", Label: "MySQL", Kind: KindBuiltin, Emoji: "🛢️",
		UpMetric: "mysql_instance_up", ConnMetric: "mysql_threads_connected",
		ReportThroughputMetric: "mysql_queries_per_sec",
		KeyMetrics: []string{
			"mysql_threads_connected", "mysql_max_connections", "mysql_innodb_buffer_pool_hit_rate",
			"mysql_queries_per_sec", "mysql_seconds_behind_master", "mysql_slow_queries",
			"mysql_query_latency_ms",
		},
		Summary: []SummarySpec{
			{Metric: "mysql_queries_per_sec", Label: "QPS", Agg: "sum"},
			{Metric: "mysql_threads_connected", Label: "连接数", Agg: "max"},
			{Metric: "mysql_innodb_buffer_pool_hit_rate", Label: "缓冲命中率", Agg: "avg", Unit: "%"},
		},
	},
	{
		Key: "postgres", Label: "PostgreSQL", Kind: KindBuiltin, Emoji: "🐘",
		UpMetric: "postgres_instance_up", ConnMetric: "postgres_numbackends",
		KeyMetrics: []string{
			"postgres_numbackends", "postgres_max_connections", "postgres_cache_hit_ratio",
			"postgres_replication_lag_bytes", "postgres_query_latency_ms",
		},
		Summary: []SummarySpec{
			{Metric: "postgres_numbackends", Label: "连接数", Agg: "max"},
			{Metric: "postgres_cache_hit_ratio", Label: "缓存命中率", Agg: "avg", Unit: "%"},
			{Metric: "postgres_replication_lag_bytes", Label: "复制延迟", Agg: "max", Unit: "B"},
		},
	},
	{
		Key: "nginx", Label: "Nginx", Kind: KindBuiltin, Emoji: "🌐",
		UpMetric: "nginx_instance_up", ConnMetric: "nginx_active_connections",
		// 5xx 只能按 status 标签从访问日志指标取，此处改用同样来自访问日志的请求速率
		KeyMetrics: []string{"nginx_active_connections", "nginx_access_requests_rate"},
		Summary: []SummarySpec{
			{Metric: "nginx_active_connections", Label: "活动连接", Agg: "max"},
			{Metric: "nginx_requests", Label: "请求量", Agg: "sum"},
			{Metric: "nginx_access_requests_rate", Label: "请求速率", Agg: "avg", Unit: "次/s"},
		},
	},
	{
		Key: "kafka", Label: "Kafka", Kind: KindBuiltin, Emoji: "📨",
		UpMetric: "kafka_instance_up",
		KeyMetrics: []string{
			"kafka_broker_count", "kafka_offline_partitions", "kafka_under_replicated_partitions",
			"kafka_consumer_lag", "kafka_active_controller_count", "kafka_topic_count",
		},
		Summary: []SummarySpec{
			{Metric: "kafka_consumer_lag", Label: "消费积压", Agg: "sum"},
			{Metric: "kafka_under_replicated_partitions", Label: "欠副本分区", Agg: "sum"},
			{Metric: "kafka_offline_partitions", Label: "离线分区", Agg: "sum"},
		},
	},
	{
		// 告警侧按容器判定：docker_container_up 每个容器一条。
		// 报告侧刻意不同——它关注「Docker 守护进程是否在采集」，用容器总数指标是否有数据判定，
		// 否则「0 个运行容器」会被报告误报成离线（保留改造前的行为，只是从硬编码搬到这里）。
		Key: "docker", Label: "Docker", Kind: KindBuiltin, Emoji: "🐳",
		UpMetric:       "docker_container_up",
		ReportUpMetric: "docker_containers_total", ReportPresenceUp: true,
		KeyMetrics: []string{
			"docker_containers_total", "docker_containers_running", "docker_containers_stopped",
			"docker_images_total",
		},
		Summary: []SummarySpec{
			{Metric: "docker_container_up", Label: "运行容器", Agg: "sum"},
			{Metric: "docker_container_cpu_percent", Label: "CPU使用率", Agg: "avg", Unit: "%"},
			{Metric: "docker_container_mem_percent", Label: "内存使用率", Agg: "avg", Unit: "%"},
		},
	},
	{
		Key: "rocketmq", Label: "RocketMQ", Kind: KindBuiltin, Emoji: "🚀",
		UpMetric: "rocketmq_instance_up",
		KeyMetrics: []string{
			"rocketmq_broker_count", "rocketmq_message_accumulation", "rocketmq_consumer_lag",
			"rocketmq_topic_count",
		},
		Summary: []SummarySpec{
			{Metric: "rocketmq_producer_tps", Label: "生产TPS", Agg: "sum"},
			{Metric: "rocketmq_message_accumulation", Label: "消息堆积", Agg: "sum"},
			{Metric: "rocketmq_consumer_lag", Label: "消费积压", Agg: "sum"},
		},
	},
	{
		// 类型键统一为 k8s（原报告侧写成 kubernetes，与 api/告警侧不一致）
		Key: "k8s", Label: "Kubernetes", Kind: KindBuiltin, Emoji: "☸️",
		UpMetric: "k8s_cluster_up",
		KeyMetrics: []string{
			"k8s_nodes_total", "k8s_nodes_ready", "k8s_pods_running",
			"k8s_pods_pending", "k8s_pods_failed", "k8s_deployments_unhealthy",
		},
		Summary: []SummarySpec{
			{Metric: "k8s_pods_running", Label: "运行Pod", Agg: "sum"},
			{Metric: "k8s_pods_pending", Label: "待调度Pod", Agg: "sum"},
			{Metric: "k8s_nodes_ready", Label: "就绪节点", Agg: "sum"},
		},
	},
	{
		Key: "mongodb", Label: "MongoDB", Kind: KindBuiltin, Emoji: "🍃",
		UpMetric: "mongodb_up",
		KeyMetrics: []string{
			"mongodb_connections_current", "mongodb_connections_available",
			"mongodb_repl_lag", "mongodb_repl_health", "mongodb_mem_resident_bytes",
		},
		Summary: []SummarySpec{
			{Metric: "mongodb_uptime_seconds", Label: "运行时长", Agg: "avg", Unit: "s"},
			{Metric: "mongodb_connections_current", Label: "当前连接数", Agg: "avg"},
			{Metric: "mongodb_mem_resident_bytes", Label: "常驻内存", Agg: "avg", Unit: "MB"},
			{Metric: "mongodb_opcounters_command", Label: "命令数", Agg: "sum"},
			{Metric: "mongodb_db_dataSize_bytes", Label: "数据大小", Agg: "avg", Unit: "MB"},
		},
	},
	{
		// FastDFS 原报告侧完全没有条目（报告里看不到它），此处补齐
		Key: "fastdfs", Label: "FastDFS", Kind: KindBuiltin, Emoji: "🗂️",
		UpMetric: "fastdfs_up",
		KeyMetrics: []string{
			"fastdfs_storage_count", "fastdfs_storage_online_count",
			"fastdfs_total_space", "fastdfs_free_space", "fastdfs_used_space",
		},
		Summary: []SummarySpec{
			{Metric: "fastdfs_storage_count", Label: "Storage节点", Agg: "sum"},
			{Metric: "fastdfs_storage_online_count", Label: "在线Storage", Agg: "sum"},
			// 空间类指标实际单位为字节（此前卡片标成 MB，数值却是字节）
			{Metric: "fastdfs_total_space", Label: "总空间", Agg: "sum", Unit: "B"},
			{Metric: "fastdfs_free_space", Label: "空闲空间", Agg: "sum", Unit: "B"},
			{Metric: "fastdfs_used_space", Label: "已用空间", Agg: "sum", Unit: "B"},
		},
	},
	{
		Key: "rabbitmq", Label: "RabbitMQ", Kind: KindBuiltin, Emoji: "🐇",
		UpMetric: "rabbitmq_instance_up", ConnMetric: "rabbitmq_connections",
		KeyMetrics: []string{
			"rabbitmq_connections", "rabbitmq_queues", "rabbitmq_queue_messages",
			"rabbitmq_consumers", "rabbitmq_publishers", "rabbitmq_process_memory_bytes",
			"rabbitmq_fd_used",
		},
		Summary: []SummarySpec{
			{Metric: "rabbitmq_connections", Label: "连接数", Agg: "max"},
			{Metric: "rabbitmq_queues", Label: "队列数", Agg: "max"},
			{Metric: "rabbitmq_queue_messages", Label: "消息总数", Agg: "sum"},
			{Metric: "rabbitmq_consumers", Label: "消费者数", Agg: "max"},
		},
	},
	{
		Key: "elasticsearch", Label: "Elasticsearch", Kind: KindBuiltin, Emoji: "🔍",
		UpMetric: "es_instance_up", ConnMetric: "es_nodes",
		KeyMetrics: []string{
			"es_cluster_status", "es_nodes", "es_data_nodes", "es_active_shards",
			"es_primary_shards", "es_unassigned_shards",
		},
		Summary: []SummarySpec{
			{Metric: "es_cluster_status", Label: "集群状态(0绿/1黄/2红)", Agg: "max", WarnAbove: 0},
			{Metric: "es_nodes", Label: "节点数", Agg: "max"},
			{Metric: "es_unassigned_shards", Label: "未分配分片", Agg: "sum"},
		},
	},
	{
		Key: "clickhouse", Label: "ClickHouse", Kind: KindBuiltin, Emoji: "🏢",
		UpMetric: "clickhouse_instance_up", ConnMetric: "clickhouse_tcp_connections",
		KeyMetrics: []string{
			"clickhouse_tcp_connections", "clickhouse_http_connections",
			"clickhouse_queries_running", "clickhouse_merges_running",
			"clickhouse_uptime_seconds",
		},
		Summary: []SummarySpec{
			{Metric: "clickhouse_tcp_connections", Label: "TCP连接", Agg: "max"},
			{Metric: "clickhouse_queries_running", Label: "运行中查询", Agg: "max"},
			{Metric: "clickhouse_merges_running", Label: "合并数", Agg: "max"},
		},
	},
	{
		Key: "nacos", Label: "Nacos", Kind: KindBuiltin, Emoji: "☁️",
		UpMetric: "nacos_instance_up",
		KeyMetrics: []string{"nacos_instance_up"},
		Summary: []SummarySpec{
			{Metric: "nacos_instance_up", Label: "在线实例", Agg: "sum"},
		},
	},
	{
		Key: "zookeeper", Label: "ZooKeeper", Kind: KindBuiltin, Emoji: "🦁",
		UpMetric: "zookeeper_instance_up",
		KeyMetrics: []string{
			"zookeeper_avg_latency", "zookeeper_outstanding_requests",
			"zookeeper_alive_connections", "zookeeper_znode_count",
			"zookeeper_followers", "zookeeper_synced_followers",
		},
		Summary: []SummarySpec{
			{Metric: "zookeeper_avg_latency", Label: "平均延迟", Agg: "avg", Unit: "ms"},
			{Metric: "zookeeper_outstanding_requests", Label: "未处理请求", Agg: "max"},
			{Metric: "zookeeper_alive_connections", Label: "活跃连接", Agg: "max"},
		},
	},
}
