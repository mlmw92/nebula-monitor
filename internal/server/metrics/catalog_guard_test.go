package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// middlewarePrefixes 中间件指标名前缀（与指标目录登记的分类一致）。
var middlewarePrefixes = []string{
	"redis_", "mysql_", "postgres_", "nginx_", "nginx_access_", "kafka_",
	"docker_", "rocketmq_", "k8s_", "mongodb_", "fastdfs_",
}

// producerDirs 指标产出方所在目录（相对本包）：只有这些位置会「写」指标名。
//   - Agent 采集器：直连采集与 exporter 模式的产出点；
//   - Server receiver：为 Redis / K8s 等合成的存活指标。
var producerDirs = []string{
	filepath.Join("..", "..", "agent", "collector"),
	filepath.Join("..", "receiver"),
}

// metricLiteral 匹配 Go 源码中的字符串字面量（含 camelCase 段，如 mongodb_db_dataSize_bytes）。
var metricLiteral = regexp.MustCompile(`"([a-zA-Z][a-zA-Z0-9_]*_[a-zA-Z0-9_]+)"`)

// TestCatalogNamesHaveProducers 指标目录登记的每个中间件指标名，都必须在采集侧
// （Agent collector / Server receiver）以字符串字面量出现。
//
// 为什么值得单独立一条守卫：这类错误的症状不是报错，而是静默失效——「指标浏览」按目录
// 查询永远查不到数据，`/metrics/active` 还会把它标为未上线，排查时极易误判为「采集没开」。
// 本仓库此前就有 17 个名字与实现不符（如目录写 `mysql_up`、代码产出 `mysql_instance_up`），
// 其中一部分还被巡检报告当作取值来源，导致报告字段恒为空。
//
// 不在校验范围：exporter 模式下的指标名来自外部 exporter（如 mysqld_exporter 的 `mysql_up`），
// 随对方实现而定，不要求出现在本仓库代码里。
func TestCatalogNamesHaveProducers(t *testing.T) {
	produced := collectProducedNames(t)

	for _, meta := range List() {
		if !isMiddlewareMetric(meta.Name) {
			continue // 主机侧指标不在本测试范围
		}
		if !produced[meta.Name] {
			t.Errorf("指标目录登记的 %s 在采集侧找不到产出方：目录名与实现不符会让「指标浏览」查不到数据、/metrics/active 标为未上线", meta.Name)
		}
	}
}

// TestServiceUpMetricsAreRegistered 每个中间件的存活指标都必须在目录中登记：
// 「服务离线」告警与中间件总览卡片都按存活指标判断在线状态，漏登记意味着该类型无法被判断。
func TestServiceUpMetricsAreRegistered(t *testing.T) {
	upMetrics := map[string]string{
		"redis":    "redis_instance_up",
		"mysql":    "mysql_instance_up",
		"postgres": "postgres_instance_up",
		"nginx":    "nginx_instance_up",
		"kafka":    "kafka_instance_up",
		"rocketmq": "rocketmq_instance_up",
		"docker":   "docker_container_up",
		"k8s":      "k8s_cluster_up",
		"mongodb":  "mongodb_up",
		"fastdfs":  "fastdfs_up",
	}
	for svc, name := range upMetrics {
		if _, ok := Meta(name); !ok {
			t.Errorf("%s 的存活指标 %s 未登记在指标目录", svc, name)
		}
	}
}

func isMiddlewareMetric(name string) bool {
	for _, p := range middlewarePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// collectProducedNames 扫描产出方源码，收集实际会写入时序库的中间件指标名。
func collectProducedNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, dir := range producerDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读取产出方目录失败 %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("读取 %s 失败: %v", name, err)
			}
			for _, m := range metricLiteral.FindAllStringSubmatch(string(data), -1) {
				candidate := m[1]
				if !isMiddlewareMetric(candidate) {
					continue
				}
				// 以 "_" 结尾的是动态拼名的前缀（如 "mongodb_opcounters_" + op），
				// 本身不是完整指标名，跳过。
				if strings.HasSuffix(candidate, "_") {
					continue
				}
				out[candidate] = true
			}
		}
	}
	if len(out) < 100 {
		t.Fatalf("产出方指标名扫描结果异常偏少（%d 个），检查 producerDirs 是否有效", len(out))
	}
	return out
}
