package metrics

// init 注册主机侧（Agent 采集）指标元数据。
// 指标名与 internal/agent/collector 各采集器上报的 Name 保持一致。
func init() {
	// —— 主机总览 ——
	Register(MetricMeta{Name: "cpu_usage", Title: "CPU 使用率", Category: CatHost, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "mem_used_percent", Title: "内存使用率", Category: CatHost, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "disk_used_percent", Title: "磁盘使用率", Category: CatHost, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "load1", Title: "系统负载(1m)", Category: CatHost, Unit: "", Chart: ChartLine})

	// —— CPU ——
	Register(MetricMeta{Name: "cpu_cores", Title: "CPU 核数", Category: CatCPU, Unit: "核", Chart: ChartGauge})
	Register(MetricMeta{Name: "cpu_usage", Title: "CPU 使用率", Category: CatCPU, Unit: "%", Chart: ChartArea})

	// —— 内存 ——
	Register(MetricMeta{Name: "mem_used_percent", Title: "内存使用率", Category: CatMemory, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "mem_used_bytes", Title: "已用内存", Category: CatMemory, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "mem_total_bytes", Title: "总内存", Category: CatMemory, Unit: "B", Chart: ChartGauge})
	Register(MetricMeta{Name: "swap_used_percent", Title: "Swap 使用率", Category: CatMemory, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "swap_used_bytes", Title: "已用 Swap", Category: CatMemory, Unit: "B", Chart: ChartLine})

	// —— 磁盘 ——
	Register(MetricMeta{Name: "disk_used_percent", Title: "磁盘使用率", Category: CatDisk, Unit: "%", Chart: ChartArea})
	Register(MetricMeta{Name: "disk_used", Title: "已用磁盘", Category: CatDisk, Unit: "B", Chart: ChartLine})
	Register(MetricMeta{Name: "disk_total", Title: "磁盘总量", Category: CatDisk, Unit: "B", Chart: ChartGauge})
	Register(MetricMeta{Name: "disk_read_bytes", Title: "磁盘读速率", Category: CatDisk, Unit: "B/s", Chart: ChartLine})
	Register(MetricMeta{Name: "disk_write_bytes", Title: "磁盘写速率", Category: CatDisk, Unit: "B/s", Chart: ChartLine})

	// —— 网络 ——
	Register(MetricMeta{Name: "network_recv_rate", Title: "网络接收速率", Category: CatNetwork, Unit: "B/s", Chart: ChartLine})
	Register(MetricMeta{Name: "network_sent_rate", Title: "网络发送速率", Category: CatNetwork, Unit: "B/s", Chart: ChartLine})
	Register(MetricMeta{Name: "network_drop_rate", Title: "网络丢包速率", Category: CatNetwork, Unit: "个/s", Chart: ChartLine})
	Register(MetricMeta{Name: "tcp_retrans_rate", Title: "TCP 重传速率", Category: CatNetwork, Unit: "个/s", Chart: ChartLine})

	// —— 负载 ——
	Register(MetricMeta{Name: "load1", Title: "负载(1m)", Category: CatLoad, Unit: "", Chart: ChartLine})
	Register(MetricMeta{Name: "load5", Title: "负载(5m)", Category: CatLoad, Unit: "", Chart: ChartLine})
	Register(MetricMeta{Name: "load15", Title: "负载(15m)", Category: CatLoad, Unit: "", Chart: ChartLine})

	// —— 进程 ——
	Register(MetricMeta{Name: "proc_count", Title: "进程数", Category: CatProcess, Unit: "个", Chart: ChartLine})
}
