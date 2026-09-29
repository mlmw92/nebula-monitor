// Package mwreg 提供中间件类型的注册表（内置类型清单的唯一来源）。
//
// 为什么需要它：此前「有哪些中间件类型」「每类的存活指标」「卡片展示哪些指标」分散在四处硬编码
// （api 的 middlewareTypes 与 mwSummarySpecs、report 的 mwDefs、alert 的 serviceMetric 与
// KnownServices），新增一类中间件要改多处，漏改一处就是静默不一致。这份表把它们收敛成一处。
//
// 收敛时发现的两处真实漂移（分散维护的必然结果）：
//   - 报告侧把 Kubernetes 的类型键写成 "kubernetes"，而 api/告警侧用 "k8s"；
//   - 报告侧完全没有 FastDFS 条目，于是报告里看不到它。
//
// 另有一处**看似漂移、实为刻意**的差异：报告侧 Docker 用容器**总数**指标是否存在来判定存活，
// 而不是容器的 up 值——因为「0 个运行容器」不该被报告判成离线。这不是缺陷，而是同一类中间件
// 在「报告」与「告警」眼里本就可能是不同对象，因此注册表为它保留了 ReportUpMetric /
// ReportPresenceUp 两个显式字段（见 Type 的注释），而不是强行统一。
package mwreg

// Kind 区分内置类型与模板派生类型。
type Kind string

const (
	// KindBuiltin 内置类型（编译期固定）。
	KindBuiltin Kind = "builtin"
)

// SummarySpec 描述卡片/报告上要展示的一个摘要指标。
type SummarySpec struct {
	Metric    string
	Label     string
	Agg       string // max / avg / sum
	Unit      string
	WarnAbove float64
}

// Type 是一个中间件类型在展示层与告警层所需的全部信息。
type Type struct {
	// Key 类型标识，同时也是该类指标的命名前缀（redis / rabbitmq）。
	Key   string
	Label string
	Kind  Kind
	Emoji string // 报告用图标
	// UpMetric 存活指标名；空值表示该类没有存活指标（不应发生）。
	UpMetric string
	// UpLabels 存活指标上的附加过滤条件。内置类型为空。
	UpLabels map[string]string
	// ConnMetric 报告里「连接/负载」一栏取的指标（可为空）。
	ConnMetric string
	// KeyMetrics 报告明细表取的指标列表。
	KeyMetrics []string
	// Summary 卡片摘要指标。
	Summary []SummarySpec
	// ReportUpMetric 是**报告侧专用**存活指标（留空则用 UpMetric）。
	//
	// 存在的理由：同一类中间件在「告警」与「报告」眼里可能不是同一个对象。
	// Docker 即如此——告警关心每个容器（docker_container_up），
	// 而报告关心守护进程是否在采集（docker_containers_total 是否有数据），
	// 否则「0 个运行容器」会被误报成离线。
	ReportUpMetric string
	// ReportPresenceUp 表示报告侧以「该指标是否有数据」判定存活，而非看值是否 > 0。
	ReportPresenceUp bool
	// ReportThroughputMetric 是报告「吞吐/速率」一栏取的指标（可为空）。
	// 显式声明而不复用 Summary[0]：报告列与卡片摘要的取舍理由不同，
	// 依赖顺序会让「调整卡片摘要」意外改掉报告内容。
	ReportThroughputMetric string
}

// UpMetricForReport 返回报告侧应查询的存活指标。
func (t Type) UpMetricForReport() string {
	if t.ReportUpMetric != "" {
		return t.ReportUpMetric
	}
	return t.UpMetric
}

// Registry 是中间件类型注册表。
type Registry struct{}

// New 创建注册表。
func New() *Registry { return &Registry{} }

// builtinOnly 供未注入模板源的调用方复用（避免各处 new 出多个等价实例）。
var builtinOnly = &Registry{}

// BuiltinOnly 返回内置类型的注册表（与 New 等价，保留以兼容历史调用方）。
func BuiltinOnly() *Registry { return builtinOnly }

// Types 返回全部类型（顺序稳定：前端展示与测试断言都依赖顺序稳定）。
func (r *Registry) Types() []Type {
	out := make([]Type, len(builtinTypes))
	copy(out, builtinTypes)
	return out
}

// Get 按 key 查询类型。
func (r *Registry) Get(key string) (Type, bool) {
	for _, t := range r.Types() {
		if t.Key == key {
			return t, true
		}
	}
	return Type{}, false
}

// Has 判断 key 是否为已知类型。
func (r *Registry) Has(key string) bool {
	_, ok := r.Get(key)
	return ok
}

// Keys 返回全部类型 key。
func (r *Registry) Keys() []string {
	types := r.Types()
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, t.Key)
	}
	return out
}
