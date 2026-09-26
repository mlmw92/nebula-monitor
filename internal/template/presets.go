package template

// 本文件是**开箱即用的模板预设**：为常见中间件预先写好取数与映射规则，
// 用户只需补上生效分组（groups）与真实地址，不必从零理解 exporter 的输出形态。
//
// 放在共享包而非 Server 侧：Server 用它提供「从预设创建」接口，
// Agent 侧的映射测试也直接引用同一份规则（避免预设与测试各写一份而漂移）。
//
// 关于规则的取舍原则（写这些预设时逐条核对过真实 exporter 的输出）：
//   - 一律用 keep 收窄到该中间件的指标族：exporter 普遍同时暴露 go_*/process_*/promhttp_* 等自身运行指标，
//     全量透传会把基数浪费在与被监控对象无关的序列上；
//   - 一律用 drop 去掉 `*_created`：新版 client 库为每个 counter/gauge 都带一条 created 时间戳序列，
//     对监控没有意义却会成倍放大基数；
//   - 直方图的 `_bucket` 默认丢弃、保留 `_sum`/`_count`：bucket 数量随分位配置膨胀，
//     而本项目目前没有分位数查询口径，保留只是在浪费基数；
//   - 预设**不预设聚合**：默认保留维度标签（如 RabbitMQ 的 queue），
//     想汇总时再显式加 rules.aggregate——否则预设会在用户不知情的情况下丢掉细节。

// Preset 是一个开箱即用的模板预设。
type Preset struct {
	// ID 同时是指标前缀与类型标识（Web 端建好后即在「中间件监控」里出现）。
	ID string
	// Title 是展示名。
	Title string
	// Desc 一句话说明这个中间件采集什么。
	Desc string
	// Note 记录前置条件与该预设的实测边界（前端直接展示给用户）。
	Note string
	// Config 是预填配置：Groups 留空（由用户在 Web 端选择生效分组），Targets 用本机默认端口。
	Config Config
}

func tpl(id, title string, groups []string, addr string, rules Rules) Config {
	return Config{
		ID:      id,
		Title:   title,
		Kind:    KindPrometheusExporter,
		Groups:  groups,
		Targets: []Target{{Addr: addr}},
		Rules:   rules,
	}
}

// Presets 返回全部预设（顺序即前端下拉顺序：按常见程度排列）。
func Presets() []Preset {
	return []Preset{
		{
			ID:    "rabbitmq",
			Title: "RabbitMQ",
			Desc:  "队列积压、消费者数、连接与内存（rabbitmq_prometheus 插件）",
			Note: "需启用 rabbitmq_prometheus 插件（默认端口 15692）。" +
				"队列多时按队列维度的序列数会迅速增长：超过单模板上限会被截断并告警，" +
				"如需只保留汇总值，请加 rules.aggregate（如 match: \"^rabbitmq_queue_\", op: sum）。",
			Config: tpl("rabbitmq", "RabbitMQ", nil, "http://127.0.0.1:15692/metrics", Rules{
				Keep: "^rabbitmq_",
				Drop: "_created$",
			}),
		},
		{
			ID:    "elasticsearch",
			Title: "Elasticsearch",
			Desc:  "集群健康、JVM 内存、索引文档数（_prometheus/metrics 端点）",
			Note: "需启用 Elasticsearch 的 prometheus 模块（7.16+，端点 /_prometheus/metrics）。" +
				"未启用时只有 _nodes/stats 这样的「按节点 id 分组的 JSON」，当前模板语法无法表达该结构。",
			Config: tpl("elasticsearch", "Elasticsearch", nil, "http://127.0.0.1:9200/_prometheus/metrics", Rules{
				Keep: "^elasticsearch_",
				Drop: "_created$",
			}),
		},
		{
			ID:    "etcd",
			Title: "Etcd",
			Desc:  "集群是否有主、DB 大小、WAL/磁盘同步延迟、提案数",
			Note: "etcd 自带 /metrics（默认端口 2379），无需额外 exporter。" +
				"直方图只保留 _sum/_count（丢弃 _bucket，本项目暂无分位数口径）。",
			Config: tpl("etcd", "Etcd", nil, "http://127.0.0.1:2379/metrics", Rules{
				Keep: "^etcd_",
				Drop: "_bucket$",
			}),
		},
		{
			ID:    "clickhouse",
			Title: "ClickHouse",
			Desc:  "查询/写入事件计数与运行时指标（内置 /metrics，默认 9363）",
			Note: "ClickHouse 的指标名是 ClickHouseProfileEvents_/ClickHouseMetrics_ 前缀（大写），" +
				"预设用 rename 改写成 clickhouse_events_/clickhouse_metrics_ 以便阅读与检索。",
			Config: tpl("clickhouse", "ClickHouse", nil, "http://127.0.0.1:9363/metrics", Rules{
				Keep: "^ClickHouse",
				Drop: "_created$",
				Rename: []RenameRule{
					{Match: "^ClickHouseProfileEvents_", To: "events_"},
					{Match: "^ClickHouseMetrics_", To: "metrics_"},
					{Match: "^ClickHouseAsyncMetrics_", To: "async_"},
				},
			}),
		},
		{
			ID:    "nacos",
			Title: "Nacos",
			Desc:  "配置与服务治理指标（配置数、读写统计、长轮询、注册实例数）",
			Note: "需开启 metrics：Nacos 2.x 内置 /nacos/actuator/prometheus（默认端口 8848），" +
				"1.x 的暴露路径随版本与插件不同，请以现场为准。" +
				"该 exporter 把多种含义塞进同一个指标族、用 name 标签区分（nacos_monitor{module=\"config\",name=\"longPolling\"}），" +
				"预设用 rules.promoteLabel 把它提升为独立指标名（nacos_monitor_longPolling）——" +
				"否则所有含义都挤在 nacos_monitor 一个名字下，既无法分别看趋势，也无法按含义配告警。",
			Config: tpl("nacos", "Nacos", nil, "http://127.0.0.1:8848/nacos/actuator/prometheus", Rules{
				Keep: "^nacos_",
				Drop: "_created$",
				// name 标签是该族「含义标识」，提升为指标名后同样从标签集中移除
				PromoteLabel: []PromoteLabelRule{{Match: "^nacos_monitor$", Label: "name"}},
			}),
		},
		{
			// id 取 zk 而不是 zookeeper：模板 id 会作为指标前缀加到响应中的名字上，
			// 而 exporter 暴露的指标族本身就是 zk_*，用 zookeeper 会得到 zookeeper_zk_xxx 这种叠词。
			ID:    "zk",
			Title: "ZooKeeper",
			Desc:  "节点角色、连接数、平均延迟、znode/watch 数（zookeeper-exporter）",
			Note: "ZooKeeper 自身不暴露 Prometheus 指标，需第三方 exporter（如 dabealu/zookeeper-exporter，端口 9141）。" +
				"请把地址改成该 exporter 的 /metrics。",
			Config: tpl("zk", "ZooKeeper", nil, "http://127.0.0.1:9141/metrics", Rules{
				Keep: "^zk_",
				Drop: "_created$",
			}),
		},
	}
}

// PresetByID 按 id 取预设。
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
