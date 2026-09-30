package metrics

// init 注册主机侧（Agent 采集）指标元数据。
//
// 不变量：此处登记的 Name 必须与 `internal/agent/collector` 实际写入时序库的名字**逐字一致**，
// 由 TestCatalogNamesHaveProducers 守卫。历史上这里曾把 `disk_read_bytes`/`disk_write_bytes`/
// `tcp_retrans_rate`/`proc_count` 写成了采集器从未产出的名字（采集器是 `disk_read_rate`/
// `disk_write_rate`/`tcp_retransmit_rate`/`process_total`）——症状是「指标浏览」按目录查永远查不到
// 数据、按目录配的告警规则恒不触发，而**都不会报错**。
//
// 关于 CatHost（主机总览）：早期把 cpu_usage / mem_used_percent / disk_used_percent / load1
// 在这里再登记一遍到 host 分类，但 Register 是「同名覆盖」，后一次登记会改写分类、位置仍留在最前，
// 结果是 host 分类**恒为空**。首页概览卡片已有这几项，这里不再重复登记。
func init() {
	// —— CPU ——
	Register(MetricMeta{Name: "cpu_usage", Title: "CPU 使用率", Category: CatCPU, Unit: "%", Chart: ChartArea,
		WorseWhen: WorseHigh, Desc: "采样周期内的 CPU 总使用率"})
	Register(MetricMeta{Name: "cpu_cores", Title: "CPU 核数", Category: CatCPU, Unit: "核", Chart: ChartGauge,
		NoAlert: true, Desc: "容量信息，随扩容变化，不适合设阈值"})

	// —— 内存 ——
	Register(MetricMeta{Name: "mem_used_percent", Title: "内存使用率", Category: CatMemory, Unit: "%", Chart: ChartArea,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "mem_used_bytes", Title: "已用内存", Category: CatMemory, Unit: "B", Chart: ChartLine,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "mem_total_bytes", Title: "总内存", Category: CatMemory, Unit: "B", Chart: ChartLine,
		NoAlert: true, Desc: "容量信息"})
	Register(MetricMeta{Name: "mem_available_bytes", Title: "可用内存", Category: CatMemory, Unit: "B", Chart: ChartLine,
		WorseWhen: WorseLow, Desc: "包含可回收的缓存，比「已用」更接近真实余量"})
	Register(MetricMeta{Name: "swap_used_percent", Title: "Swap 使用率", Category: CatMemory, Unit: "%", Chart: ChartArea,
		WorseWhen: WorseHigh, Desc: "Swap 被大量使用通常意味着物理内存不足"})
	Register(MetricMeta{Name: "swap_used_bytes", Title: "已用 Swap", Category: CatMemory, Unit: "B", Chart: ChartLine,
		WorseWhen: WorseHigh})

	// —— 磁盘 ——
	Register(MetricMeta{Name: "disk_used_percent", Title: "磁盘使用率", Category: CatDisk, Unit: "%", Chart: ChartArea,
		WorseWhen: WorseHigh, Desc: "规则引擎对该指标做了真实磁盘汇总（非文件系统逐项取最大）"})
	Register(MetricMeta{Name: "disk_used", Title: "已用磁盘", Category: CatDisk, Unit: "B", Chart: ChartLine,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "disk_total", Title: "磁盘总量", Category: CatDisk, Unit: "B", Chart: ChartLine,
		NoAlert: true, Desc: "容量信息"})
	Register(MetricMeta{Name: "disk_read_rate", Title: "磁盘读速率", Category: CatDisk, Unit: "B/s", Chart: ChartLine,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "disk_write_rate", Title: "磁盘写速率", Category: CatDisk, Unit: "B/s", Chart: ChartLine,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "disk_read_iops", Title: "磁盘读 IOPS", Category: CatDisk, Unit: "次/s", Chart: ChartLine,
		WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "disk_write_iops", Title: "磁盘写 IOPS", Category: CatDisk, Unit: "次/s", Chart: ChartLine,
		WorseWhen: WorseHigh})

	// —— 网络 ——
	Register(MetricMeta{Name: "network_recv_rate", Title: "网络接收速率", Category: CatNetwork, Unit: "B/s", Chart: ChartLine,
		Desc: "速率高低取决于业务，没有通用阈值；建议按带宽上限的百分比自行设定"})
	Register(MetricMeta{Name: "network_sent_rate", Title: "网络发送速率", Category: CatNetwork, Unit: "B/s", Chart: ChartLine,
		Desc: "速率高低取决于业务，没有通用阈值"})
	Register(MetricMeta{Name: "network_recv_total", Title: "网络累计接收", Category: CatNetwork, Unit: "B", Chart: ChartLine,
		NoAlert: true, Desc: "累计计数器，只能看增长速率"})
	Register(MetricMeta{Name: "network_sent_total", Title: "网络累计发送", Category: CatNetwork, Unit: "B", Chart: ChartLine,
		NoAlert: true, Desc: "累计计数器"})
	Register(MetricMeta{Name: "network_drop_rate", Title: "网络丢包速率", Category: CatNetwork, Unit: "个/s", Chart: ChartLine,
		WorseWhen: WorseHigh, Desc: "持续丢包是网络或网卡问题的直接证据"})
	Register(MetricMeta{Name: "tcp_retransmit_rate", Title: "TCP 重传速率", Category: CatNetwork, Unit: "个/s", Chart: ChartLine,
		WorseWhen: WorseHigh})

	// —— 负载 ——
	Register(MetricMeta{Name: "load1", Title: "负载(1m)", Category: CatLoad, Unit: "", Chart: ChartLine,
		WorseWhen: WorseHigh, Desc: "阈值应参照 CPU 核数（例如核数的 2 倍）"})
	Register(MetricMeta{Name: "load5", Title: "负载(5m)", Category: CatLoad, Unit: "", Chart: ChartLine, WorseWhen: WorseHigh})
	Register(MetricMeta{Name: "load15", Title: "负载(15m)", Category: CatLoad, Unit: "", Chart: ChartLine, WorseWhen: WorseHigh})

	// —— 进程 ——
	Register(MetricMeta{Name: "process_total", Title: "进程数", Category: CatProcess, Unit: "个", Chart: ChartLine,
		Desc: "进程数突增可能是 fork 风暴；正常水位因机器而异"})
	Register(MetricMeta{Name: "proc_cpu", Title: "进程 CPU 占用", Category: CatProcess, Unit: "%", Chart: ChartBar,
		WorseWhen: WorseHigh, Desc: "带进程标识维度（由服务端从上报的进程快照写入）"})
	Register(MetricMeta{Name: "proc_mem", Title: "进程内存占用", Category: CatProcess, Unit: "B", Chart: ChartBar,
		WorseWhen: WorseHigh, Desc: "带进程标识维度"})
}
