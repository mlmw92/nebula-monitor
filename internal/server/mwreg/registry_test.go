package mwreg

import (
	"testing"

	"github.com/nebula/monitor/internal/template"
)

// fakeSource 是 TemplateSource 的测试替身（内容可动态变化，用于验证「不缓存」）。
type fakeSource struct {
	list []template.Config
	rev  uint64
}

func (f *fakeSource) Snapshot() ([]template.Config, uint64) { return f.list, f.rev }

func twin(id, title string) template.Config {
	return template.Config{
		ID:     id,
		Title:  title,
		Kind:   template.KindPrometheusExporter,
		Groups: []string{"default"},
		Targets: []template.Target{
			{Addr: "http://127.0.0.1:15692/metrics"},
		},
		Rules: template.Rules{Metrics: []template.MetricRule{
			{Name: "queue_depth", Label: "队列深度", Unit: "个"},
			{Name: "consumers"},
		}},
	}
}

// TestBuiltinOnlyHasFifteenTypes 未注入模板源时保持内置 15 类。
func TestBuiltinOnlyHasFifteenTypes(t *testing.T) {
	reg := BuiltinOnly()
	if got := len(reg.Types()); got != 15 {
		t.Fatalf("内置类型数 = %d，want 15", got)
	}
	for _, key := range []string{"redis", "mysql", "postgres", "nginx", "kafka", "docker", "rocketmq", "k8s", "mongodb", "fastdfs",
		"rabbitmq", "elasticsearch", "clickhouse", "nacos", "zookeeper"} {
		if !reg.Has(key) {
			t.Errorf("内置类型缺少 %s", key)
		}
	}
	// 顺序即前端卡片顺序，固定下来避免无意变动
	if got := reg.Keys()[0]; got != "redis" {
		t.Fatalf("首个类型 = %q，want redis（顺序影响界面展示）", got)
	}
	if got := reg.Keys()[14]; got != "zookeeper" {
		t.Fatalf("末个类型 = %q，want zookeeper", got)
	}
}

// TestBuiltinUpMetrics 存活指标必须与专用采集器/接收端实际产出的名字一致
// （写错的后果是「服务离线」告警判断错误对象，且不会有任何报错）。
func TestBuiltinUpMetrics(t *testing.T) {
	want := map[string]string{
		"redis": "redis_instance_up", "mysql": "mysql_instance_up",
		"postgres": "postgres_instance_up", "nginx": "nginx_instance_up",
		"kafka": "kafka_instance_up", "docker": "docker_container_up",
		"rocketmq": "rocketmq_instance_up", "k8s": "k8s_cluster_up",
		"mongodb": "mongodb_up", "fastdfs": "fastdfs_up",
	}
	reg := BuiltinOnly()
	for key, metric := range want {
		t2, ok := reg.Get(key)
		if !ok {
			t.Errorf("缺少类型 %s", key)
			continue
		}
		if t2.UpMetric != metric {
			t.Errorf("%s 的存活指标 = %q，want %q", key, t2.UpMetric, metric)
		}
		if len(t2.UpLabels) != 0 {
			t.Errorf("%s 为内置类型，不应有附加标签过滤：%v", key, t2.UpLabels)
		}
	}
}

// TestBuiltinSummaryAndReportFields 卡片摘要与报告字段都从同一份表读出（收敛前的三处定义）。
func TestBuiltinSummaryAndReportFields(t *testing.T) {
	reg := BuiltinOnly()

	redis, _ := reg.Get("redis")
	if len(redis.Summary) == 0 || redis.Summary[0].Metric != "redis_ops_per_sec" {
		t.Fatalf("Redis 摘要不符：%+v", redis.Summary)
	}
	if redis.ConnMetric != "redis_connected_clients" || redis.Emoji == "" {
		t.Fatalf("Redis 报告字段缺失：conn=%q emoji=%q", redis.ConnMetric, redis.Emoji)
	}
	// 收敛时修掉的两处真实漂移，逐条固定下来
	if k8s, ok := reg.Get("k8s"); !ok || k8s.UpMetric != "k8s_cluster_up" {
		t.Fatal("k8s 类型键应为 k8s（原报告侧写成 kubernetes）")
	}
	if fd, ok := reg.Get("fastdfs"); !ok || fd.UpMetric != "fastdfs_up" {
		t.Fatal("FastDFS 应存在于类型表（原报告侧完全没有它）")
	}

	// Docker 是「看似漂移、实为刻意」的一处：告警按容器判定，报告按守护进程是否在采集判定
	//（0 个运行容器不该被报告判成离线）。两个语义都要被保留，故注册表为报告侧留了显式字段。
	docker, _ := reg.Get("docker")
	if docker.UpMetric != "docker_container_up" {
		t.Fatalf("Docker 告警侧存活指标 = %q，want docker_container_up", docker.UpMetric)
	}
	if docker.ReportUpMetric != "docker_containers_total" || !docker.ReportPresenceUp {
		t.Fatalf("Docker 报告侧应保留「以容器总数指标是否存在判定存活」的原语义：%+v", docker)
	}
	if docker.UpMetricForReport() != "docker_containers_total" {
		t.Fatalf("报告侧应查询 %q，got %q", "docker_containers_total", docker.UpMetricForReport())
	}
	// 其余类型的报告存活指标就是 UpMetric（未声明专用指标）
	if redis, _ := reg.Get("redis"); redis.UpMetricForReport() != redis.UpMetric {
		t.Fatal("未声明 ReportUpMetric 的类型，报告侧应回退为 UpMetric")
	}
}

// TestTemplateDerivedTypes 模板派生出独立类型：存活指标统一为 template_target_up + template 标签过滤。
func TestTemplateDerivedTypes(t *testing.T) {
	reg := New(&fakeSource{list: []template.Config{twin("testmw", "TestMW")}, rev: 1})
	types := reg.Types()
	if len(types) != 16 {
		t.Fatalf("类型数 = %d，want 16（15 内置 + 1 模板）", len(types))
	}
	t2, ok := reg.Get("testmw")
	if !ok {
		t.Fatal("缺少模板派生类型 testmw")
	}
	if t2.Kind != KindTemplate {
		t.Fatalf("Kind = %q，want template", t2.Kind)
	}
	if t2.Label != "TestMW" {
		t.Fatalf("Label 应取 title，got %q", t2.Label)
	}
	if t2.UpMetric != template.UpMetricName {
		t.Fatalf("存活指标 = %q，want %q", t2.UpMetric, template.UpMetricName)
	}
	if t2.UpLabels["template"] != "testmw" {
		t.Fatalf("模板类型必须按 template 标签过滤（否则所有模板共用同一指标名会互相误触发）：%v", t2.UpLabels)
	}
	if len(t2.Summary) != 2 {
		t.Fatalf("摘要应来自 rules.metrics，got %+v", t2.Summary)
	}
	// label 留空时回退为指标名；指标名按模板 id 加前缀
	if t2.Summary[0].Metric != "testmw_queue_depth" || t2.Summary[0].Label != "队列深度" || t2.Summary[0].Unit != "个" {
		t.Fatalf("摘要项不符：%+v", t2.Summary[0])
	}
	if t2.Summary[1].Label != "consumers" {
		t.Fatalf("label 留空应回退为指标名，got %q", t2.Summary[1].Label)
	}
}

// TestTemplateDerivedTypesCannotShadowBuiltin 模板 id 与内置类型同名时，
// Get 必须仍返回内置类型（内置优先），且 Types() 不产生重复 key。
func TestTemplateDerivedTypesCannotShadowBuiltin(t *testing.T) {
	reg := New(&fakeSource{list: []template.Config{twin("rabbitmq", "RabbitMQ")}, rev: 1})
	t2, ok := reg.Get("rabbitmq")
	if !ok {
		t.Fatal("缺少 rabbitmq")
	}
	if t2.Kind != KindBuiltin {
		t.Fatalf("同名模板不得遮蔽内置类型：Kind = %q", t2.Kind)
	}
	seen := map[string]int{}
	for _, t := range reg.Types() {
		seen[t.Key]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Fatalf("类型 key %s 在 Types() 中重复出现 %d 次", k, n)
		}
	}
}

// TestTemplateTypesNotCached 类型表不缓存：模板增删后下一次读取即生效（无需重启）。
func TestTemplateTypesNotCached(t *testing.T) {
	src := &fakeSource{}
	reg := New(src)
	if len(reg.Types()) != 15 {
		t.Fatal("初始应只有内置类型")
	}
	src.list = []template.Config{twin("testmw", "TestMW")}
	if len(reg.Types()) != 16 {
		t.Fatal("新增模板后类型表应立即反映（不缓存）")
	}
	src.list = nil
	if len(reg.Types()) != 15 {
		t.Fatal("删除模板后类型表应立即反映")
	}
}

// TestNilStoreAndNilRegistry 空值安全：不注入模板源、或调用方拿不到注册表时都不 panic。
func TestNilStoreAndNilRegistry(t *testing.T) {
	if got := New(nil).Types(); len(got) != 15 {
		t.Fatalf("store 为 nil 时应退化为内置类型，got %d", len(got))
	}
	var r *Registry
	if got := r.TemplateTypes(); got != nil {
		t.Fatalf("nil 注册表应返回空，got %+v", got)
	}
}
