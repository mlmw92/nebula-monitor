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
