package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// producerDirs 指标产出方所在目录（相对本包）：只有这些位置会「写」指标名。
//   - Agent 采集器：直连采集、端口探测与日志采集的产出点；
//   - Server receiver：为 Redis / K8s 等合成的存活指标与进程快照；
//   - Server dialtest：拨测与证书到期的产出点（定时任务，不经过 Agent）。
var producerDirs = []string{
	filepath.Join("..", "..", "agent", "collector"),
	filepath.Join("..", "receiver"),
	filepath.Join("..", "dialtest"),
}

// catalogAllowlist 是「登记在目录里、但产出方不在本仓库」的指标名白名单。
//
// 目前为空：exporter 模式透传的外部指标名（如 mysqld_exporter 的 mysql_up）
// **刻意不登记**在目录里——它们的名字随对方实现而定，登记下来只会立刻漂移。
// 保留这个机制是为了将来确有「外部产出但需要出现在选择器里」的指标时有地方可写。
var catalogAllowlist = map[string]bool{}

// metricLiteral 匹配 Go 源码中「形如指标名」的字符串字面量，两种形态：
//  1. 含下划线的常规名（含 camelCase 段，如 mongodb_db_dataSize_bytes）；
//  2. 字母 + 数字结尾的负载类名（load1/load5/load15）——采集器里它们就是这样写的，
//     若坚持「必须含下划线」，这三个名字永远无法被校验（事实上有过这种漏网）。
//
// 第二种会顺带匹配到 utf8/sha256 这类无关字面量，使校验略偏宽松；
// 但守卫要抓的是「整个仓库都没人产出这个名字」，宽松不影响这一点。
var metricLiteral = regexp.MustCompile(`"([a-zA-Z][a-zA-Z0-9_]*_[a-zA-Z0-9_]+|[a-z]+[0-9]+)"`)

// TestCatalogNamesHaveProducers 指标目录登记的**每一条**指标，都必须在产出方源码
// （Agent collector / Server receiver / Server dialtest）里以字符串字面量出现。
//
// 为什么值得单独立一条守卫：这类错误的症状不是报错，而是静默失效——「指标浏览」按目录
// 查询永远查不到数据、按目录配的告警规则恒不触发，`/metrics/active` 还会把它标为未上线，
// 排查时极易误判为「采集没开」。本仓库此前就有 17 个名字与实现不符（如目录写 `mysql_up`、
// 代码产出 `mysql_instance_up`），其中一部分还被巡检报告当作取值来源，导致报告字段恒为空。
//
// 覆盖范围（2026-09-30 扩展）：此前只校验**中间件前缀**的条目，于是主机侧 4 处名字漂移
// （`proc_count`/`disk_read_bytes`/`disk_write_bytes`/`tcp_retrans_rate` 与采集器实际产出的
// `process_total`/`disk_read_rate`/`disk_write_rate`/`tcp_retransmit_rate` 不符）长期无人发现。
// 现在改为全量校验，只放过两类：`Dynamic`（名字由运行时拼出的指标族，见 MetricMeta.Dynamic）
// 与白名单（见 catalogAllowlist）。
func TestCatalogNamesHaveProducers(t *testing.T) {
	produced := collectProducedNames(t)

	for _, meta := range List() {
		if meta.Dynamic || catalogAllowlist[meta.Name] {
			continue
		}
		if !produced[meta.Name] {
			t.Errorf("指标目录登记的 %s 在产出方找不到：目录名与实现不符会让「指标浏览」查不到数据、告警规则恒不触发（都不报错）", meta.Name)
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

// TestCategoryTitlesCoverAllRegisteredCategories 每个出现在目录里的分类都必须有中文名，
// 否则告警表单与指标浏览会直接显示英文 key（`rabbitmq`、`probe` 这种）。
func TestCategoryTitlesCoverAllRegisteredCategories(t *testing.T) {
	for _, key := range SortedCategories() {
		if CategoryTitle(Category(key)) == key {
			t.Errorf("分类 %s 缺少中文名：请在 categoryTitles 中补上（界面会直接显示它）", key)
		}
	}
}

// TestAlertCatalogKeepsUnusualMetricsLast 不可告警的指标（累计计数器、容量信息、运行时长）
// 必须排在组内最后：隐藏它们会让人以为「这个指标不采集」，排前面则会诱导误选。
func TestAlertCatalogKeepsUnusualMetricsLast(t *testing.T) {
	for _, group := range AlertCatalog() {
		seenNoAlert := false
		for _, m := range group.Metrics {
			if m.NoAlert {
				seenNoAlert = true
				continue
			}
			if seenNoAlert {
				t.Errorf("分类 %s 中可告警指标 %s 排在了不可告警指标之后", group.Key, m.Name)
				break
			}
		}
	}
}

// collectProducedNames 扫描产出方源码，收集其中出现的「形如指标名」的字符串字面量。
//
// 刻意不做前缀白名单过滤：过滤会让「主机侧 / 拨测 / 日志」这些不带中间件前缀的名字
// 无法参与校验——而主机侧恰恰是漂移过的地方。代价是收集结果里混入一些非指标字面量
// （配置键、来源标识），它们只会让校验更宽松，不会误报。
func collectProducedNames(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	files := 0
	for _, dir := range producerDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("读取产出方目录失败 %s: %v（目录被移动时本守卫会失效，必须显式失败）", dir, err)
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
			files++
			for _, m := range metricLiteral.FindAllStringSubmatch(string(data), -1) {
				candidate := m[1]
				// 以 "_" 结尾的是动态拼名的前缀（如 "mongodb_opcounters_" + op），
				// 本身不是完整指标名，跳过。
				if strings.HasSuffix(candidate, "_") {
					continue
				}
				out[candidate] = true
			}
		}
	}
	if files < 20 {
		t.Fatalf("产出方源码只扫到 %d 个文件，检查 producerDirs 是否有效", files)
	}
	if len(out) < 150 {
		t.Fatalf("产出方指标名扫描结果异常偏少（%d 个），检查 producerDirs 是否有效", len(out))
	}
	return out
}
