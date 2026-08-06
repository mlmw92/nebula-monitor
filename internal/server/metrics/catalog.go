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
	CatHost     Category = "host"      // 主机总览
	CatCPU      Category = "cpu"       // CPU
	CatMemory   Category = "memory"    // 内存
	CatDisk     Category = "disk"      // 磁盘
	CatNetwork  Category = "network"   // 网络
	CatLoad     Category = "load"      // 系统负载
	CatProcess  Category = "process"   // 进程
	CatRedis    Category = "redis"     // 中间件：Redis
	CatMySQL    Category = "mysql"     // 中间件：MySQL
	CatPostgres Category = "postgres"  // 中间件：PostgreSQL
	CatNginx    Category = "nginx"     // 中间件：Nginx
	CatKafka    Category = "kafka"     // 中间件：Kafka
	CatDocker   Category = "docker"    // 中间件：Docker
	CatMongo    Category = "mongodb"   // 中间件：MongoDB
	CatRocketMQ Category = "rocketmq"  // 中间件：RocketMQ
	CatK8s      Category = "kubernetes" // 中间件：Kubernetes
)

// ChartType 推荐图表类型。
type ChartType string

const (
	ChartLine   ChartType = "line"
	ChartArea   ChartType = "area"
	ChartBar    ChartType = "bar"
	ChartGauge  ChartType = "gauge"
	ChartPie    ChartType = "pie"
)

// MetricMeta 单条指标的元数据。
type MetricMeta struct {
	Name     string    `json:"name"`     // Prometheus 指标名，如 cpu_usage
	Title    string    `json:"title"`    // 中文显示名
	Category Category   `json:"category"` // 分类
	Unit     string    `json:"unit"`     // 单位，如 %/MB/次
	Chart    ChartType `json:"chart"`    // 推荐图表类型
	Desc     string    `json:"desc,omitempty"`
}

// catalog 全局指标目录，Register 在 init/启动时调用。
var (
	mu      sync.RWMutex
	entries = map[string]MetricMeta{} // name -> meta
	order   []string                  // 保持注册顺序
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
