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
//
// 历史说明：RabbitMQ / Elasticsearch / ClickHouse / Nacos / ZooKeeper 已升级为
// **专用内置采集**（internal/agent/collector + mwreg 内置类型表），不再提供模板预设——
// 内置采集的体验（专用 Tab / 摘要指标 / 服务离线告警）优于通用模板。Etcd 暂保留模板方式。

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
