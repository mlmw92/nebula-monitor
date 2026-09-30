// Package metrics 维护统一指标目录（Metric Catalog）。
//
// 指标目录是「可观测性增强」三大能力的共同底座：
//   - 指标自动发现：按分类暴露已被采集的指标全集；
//   - 自定义仪表盘：从中挑选指标组合面板；
//   - 历史数据导出：对目录中的指标做范围查询后落盘。
//
// 各采集模块在 Server 启动时调用 Register 注册自身指标，纯追加、不改采集逻辑。
package metrics

import (
	"sort"
	"sync"
)

// Category 指标分类。
type Category string

const (
	// CatHost 主机总览分类。
	CatHost Category = "host"
	// CatCPU CPU 分类。
	CatCPU Category = "cpu"
	// CatMemory 内存分类。
	CatMemory Category = "memory"
	// CatDisk 磁盘分类。
	CatDisk Category = "disk"
	// CatNetwork 网络分类。
	CatNetwork Category = "network"
	// CatLoad 系统负载分类。
	CatLoad Category = "load"
	// CatProcess 进程分类。
	CatProcess Category = "process"
	// CatRedis 中间件：Redis。
	CatRedis Category = "redis"
	// CatMySQL 中间件：MySQL。
	CatMySQL Category = "mysql"
	// CatPostgres 中间件：PostgreSQL。
	CatPostgres Category = "postgres"
	// CatNginx 中间件：Nginx。
	CatNginx Category = "nginx"
	// CatKafka 中间件：Kafka。
	CatKafka Category = "kafka"
	// CatDocker 中间件：Docker。
	CatDocker Category = "docker"
	// CatMongo 中间件：MongoDB。
	CatMongo Category = "mongodb"
	// CatRocketMQ 中间件：RocketMQ。
	CatRocketMQ Category = "rocketmq"
	// CatK8s 中间件：Kubernetes。
	CatK8s Category = "kubernetes"
	// CatFastDFS 中间件：FastDFS。
	CatFastDFS Category = "fastdfs"
	// CatElasticsearch 中间件：Elasticsearch。
	CatElasticsearch Category = "elasticsearch"
	// CatClickHouse 中间件：ClickHouse。
	CatClickHouse Category = "clickhouse"
	// CatNacos 中间件：Nacos。
	CatNacos Category = "nacos"
	// CatZooKeeper 中间件：ZooKeeper。
	CatZooKeeper Category = "zookeeper"
	// CatRabbitMQ 中间件：RabbitMQ。
	CatRabbitMQ Category = "rabbitmq"
	// CatProbe 主动探测：拨测（HTTP/TCP/ICMP）与端口探活、证书到期。
	// 与其它分类的区别：它不是"从被监控对象身上读出来的"，而是平台主动发起的探测结果。
	CatProbe Category = "probe"
	// CatSecurity 主机安全基线（root 登录、空口令、防火墙…），取值多为 0/1 状态位。
	CatSecurity Category = "security"
	// CatLog 集中日志采集自身的指标（采集是否正常、每轮行数、匹配计数、丢弃计数）。
	// 它们不是"从被监控对象读到"的指标，而是采集管道的自述，故单独成类。
	CatLog Category = "log"
)

// 指标「变差方向」取值（见 MetricMeta.WorseWhen）。
const (
	// WorseHigh 越大越糟（使用率、延迟、积压）。
	WorseHigh = "high"
	// WorseLow 越小越糟（可用内存、剩余天数、命中率）。
	WorseLow = "low"
)

// ChartType 推荐图表类型。
type ChartType string

const (
	// ChartLine 折线图。
	ChartLine ChartType = "line"
	// ChartArea 面积图。
	ChartArea ChartType = "area"
	// ChartBar 柱状图。
	ChartBar ChartType = "bar"
	// ChartGauge 仪表盘（单值）。
	ChartGauge ChartType = "gauge"
	// ChartPie 饼图。
	ChartPie ChartType = "pie"
)

// MetricMeta 单条指标的元数据。
type MetricMeta struct {
	Name     string    `json:"name"`     // Prometheus 指标名，如 cpu_usage
	Title    string    `json:"title"`    // 中文显示名
	Category Category  `json:"category"` // 分类
	Unit     string    `json:"unit"`     // 单位，如 %/MB/次
	Chart    ChartType `json:"chart"`    // 推荐图表类型
	Desc     string    `json:"desc,omitempty"`
	// WorseWhen 是「变差方向」：high（越大越糟，如使用率）/ low（越小越糟，如剩余天数、命中率）/ ""（不适用）。
	//
	// 告警表单据此在选中指标后自动选好比较运算符——「证书剩余天数」被设成 `>` 15 是这类表单最常见的错误，
	// 而它的症状是规则永不触发（静默失效）。
	WorseWhen string `json:"worseWhen,omitempty"`
	// NoAlert 标记该指标**不适合直接设阈值告警**：累计计数器（`*_total`、`*_requests`）、
	// 运行时长（`*_uptime*`）、以及纯信息类指标。它们不是"不能比较"，而是比较结果没有运维含义
	// （给 uptime 设阈值不会告诉你任何事），因此选择器里会把它排到后面并给出提示。
	//
	// 否定式命名是刻意的：绝大多数指标都可告警，逐个写 `Alertable: true` 只会淹没真正需要标注的那几条。
	// 对外同样用否定式（`noAlert`）：这样"缺少该字段"就等于"可告警"，前端不必处理三态。
	NoAlert bool `json:"noAlert,omitempty"`
	// Dynamic 标记「名字由运行时拼出」的指标族（如 nginx_access_requests_by_status{status="5xx"}、
	// 日志模式指标 `applog_log_<模式名>_total`）。这类名字无法在目录里逐一登记，
	// 但告警确实可用，故以"族"的形式登记一个代表并在界面上说明。
	Dynamic bool `json:"dynamic,omitempty"`
}

// catalog 全局指标目录，Register 在 init/启动时调用。
var (
	// mu 保护 entries 与 order 的并发读写。
	mu sync.RWMutex
	// entries 指标元数据映射（name -> meta）。
	entries = map[string]MetricMeta{}
	// order 指标注册顺序，保证目录展示稳定。
	order []string
)

// Register 注册（或覆盖）一条指标元数据。可安全重复调用。
func Register(m MetricMeta) {
	if m.Name == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := entries[m.Name]; !ok {
		order = append(order, m.Name)
	}
	entries[m.Name] = m
}

// List 返回全部指标（按注册顺序）。
func List() []MetricMeta {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]MetricMeta, 0, len(order))
	for _, n := range order {
		out = append(out, entries[n])
	}
	return out
}

// Meta 返回单个指标的元数据。用于需要「标题 / 单位」的展示侧（如容量预测文案），
// 避免各处再写一份中文名与单位。
func Meta(name string) (MetricMeta, bool) {
	mu.RLock()
	defer mu.RUnlock()
	m, ok := entries[name]
	return m, ok
}

// ListByCategory 返回按分类分组的指标（分类内保持注册顺序，分类间按名称排序）。
func ListByCategory() map[string][]MetricMeta {
	out := map[string][]MetricMeta{}
	for _, m := range List() {
		c := string(m.Category)
		out[c] = append(out[c], m)
	}
	return out
}

// SortedCategories 返回去重后的分类名列表（稳定排序）。
func SortedCategories() []string {
	set := map[string]struct{}{}
	for _, m := range List() {
		set[string(m.Category)] = struct{}{}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// categoryTitles 是分类的中文名。
//
// 放在服务端而不是前端：告警表单、指标浏览、模板说明都要显示它，
// 前端再抄一份的下场就是「新增一类中间件时分类名漏改、界面上出现英文 key」。
var categoryTitles = map[Category]string{
	CatHost:          "主机总览",
	CatCPU:           "CPU",
	CatMemory:        "内存",
	CatDisk:          "磁盘",
	CatNetwork:       "网络",
	CatLoad:          "系统负载",
	CatProcess:       "进程",
	CatRedis:         "Redis",
	CatMySQL:         "MySQL",
	CatPostgres:      "PostgreSQL",
	CatNginx:         "Nginx",
	CatKafka:         "Kafka",
	CatDocker:        "Docker",
	CatMongo:         "MongoDB",
	CatRocketMQ:      "RocketMQ",
	CatK8s:           "Kubernetes",
	CatFastDFS:       "FastDFS",
	CatElasticsearch: "Elasticsearch",
	CatClickHouse:    "ClickHouse",
	CatNacos:         "Nacos",
	CatZooKeeper:     "ZooKeeper",
	CatRabbitMQ:      "RabbitMQ",
	CatProbe:         "拨测与证书",
	CatSecurity:      "安全基线",
	CatLog:           "集中日志",
}

// CategoryTitle 返回分类的中文名；未登记的分类原样返回 key（界面不会出现空白，也便于发现遗漏）。
func CategoryTitle(c Category) string {
	if t, ok := categoryTitles[c]; ok {
		return t
	}
	return string(c)
}

// CategoryView 是「按分类分组」的对外形态（带中文分类名，供选择器直接渲染）。
type CategoryView struct {
	Key     string       `json:"key"`
	Title   string       `json:"title"`
	Metrics []MetricMeta `json:"metrics"`
}

// AlertCatalog 返回供「选择告警指标」使用的分组列表。
//
// 与 ListByCategory 的差别：带上分类中文名、按注册顺序稳定输出、且**不可告警的指标排在组内最后**
// （而不是隐藏）——隐藏会让人以为"这个指标不采集"，排后面既能减少误选，又不误导。
func AlertCatalog() []CategoryView {
	grouped := ListByCategory()
	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]CategoryView, 0, len(keys))
	for _, k := range keys {
		items := grouped[k]
		sort.SliceStable(items, func(i, j int) bool { return !items[i].NoAlert && items[j].NoAlert })
		out = append(out, CategoryView{Key: k, Title: CategoryTitle(Category(k)), Metrics: items})
	}
	return out
}
