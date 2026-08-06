package metrics

// init 注册中间件（Agent/直连采集）指标元数据。
// 指标名取自各 collector 上报的 Name；此处登记常用的 up 与摘要类指标，
// 供「指标自动发现」覆盖中间件，仪表盘与导出可据此挑选。
func init() {
	// —— Redis ——
	Register(MetricMeta{Name: "redis_up", Title: "Redis 存活", Category: CatRedis, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "redis_connected_clients", Title: "已连接客户端", Category: CatRedis, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_used_memory_bytes", Title: "内存占用", Category: CatRedis, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_hit_rate", Title: "命中率", Category: CatRedis, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "redis_qps", Title: "QPS", Category: CatRedis, Unit: "次/s", Chart: ChartLine})
	Register(MetricMeta{Name: "redis_uptime_in_seconds", Title: "运行时长", Category: CatRedis, Unit: "s", Chart: ChartGauge})

	// —— MySQL ——
	Register(MetricMeta{Name: "mysql_up", Title: "MySQL 存活", Category: CatMySQL, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "mysql_queries_per_sec", Title: "QPS", Category: CatMySQL, Unit: "次/s", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_threads_connected", Title: "活跃连接数", Category: CatMySQL, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_slow_queries", Title: "慢查询数", Category: CatMySQL, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "mysql_uptime", Title: "运行时长", Category: CatMySQL, Unit: "s", Chart: ChartGauge})

	// —— PostgreSQL ——
	Register(MetricMeta{Name: "postgres_up", Title: "PostgreSQL 存活", Category: CatPostgres, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "postgres_connections", Title: "连接数", Category: CatPostgres, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "postgres_uptime_seconds", Title: "运行时长", Category: CatPostgres, Unit: "s", Chart: ChartGauge})

	// —— Nginx ——
	Register(MetricMeta{Name: "nginx_up", Title: "Nginx 存活", Category: CatNginx, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "nginx_active", Title: "活跃连接", Category: CatNginx, Unit: "个", Chart: ChartLine})
	Register(MetricMeta{Name: "nginx_requests_per_sec", Title: "请求速率", Category: CatNginx, Unit: "次/s", Chart: ChartLine})

	// —— Kafka ——
	Register(MetricMeta{Name: "kafka_up", Title: "Kafka 存活", Category: CatKafka, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "kafka_brokers", Title: "Broker 数", Category: CatKafka, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "kafka_under_replicated", Title: "副本不足分区数", Category: CatKafka, Unit: "个", Chart: ChartBar})

	// —— Docker ——
	Register(MetricMeta{Name: "docker_container_count", Title: "容器数", Category: CatDocker, Unit: "个", Chart: ChartGauge})
	Register(MetricMeta{Name: "docker_container_net_rx_bytes", Title: "容器网络接收", Category: CatDocker, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "docker_container_net_tx_bytes", Title: "容器网络发送", Category: CatDocker, Unit: "B", Chart: ChartLine})

	// —— MongoDB ——
	Register(MetricMeta{Name: "mongodb_up", Title: "MongoDB 存活", Category: CatMongo, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "mongodb_uptime_seconds", Title: "运行时长", Category: CatMongo, Unit: "s", Chart: ChartGauge})
	Register(MetricMeta{Name: "mongodb_connections", Title: "连接数", Category: CatMongo, Unit: "个", Chart: ChartLine})

	// —— RocketMQ ——
	Register(MetricMeta{Name: "rocketmq_up", Title: "RocketMQ 存活", Category: CatRocketMQ, Unit: "", Chart: ChartGauge})
	Register(MetricMeta{Name: "rocketmq_produce_tps", Title: "生产 TPS", Category: CatRocketMQ, Unit: "次/s", Chart: ChartLine})

	// —— Kubernetes ——
	Register(MetricMeta{Name: "k8s_node_cpu_usage_cores", Title: "节点 CPU 用量", Category: CatK8s, Unit: "核", Chart: ChartLine})
	Register(MetricMeta{Name: "k8s_node_mem_used_bytes", Title: "节点内存用量", Category: CatK8s, Unit: "B", Chart: ChartLine})
}
