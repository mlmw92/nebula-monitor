package mwreg

import (
	"testing"
)

// TestBuiltinOnlyHasFifteenTypes 内置 15 类。
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

// TestBuiltinSummaryForAllTypes 每个内置类型都应配置卡片摘要（缺失会让该类卡片无内容可展示）。
func TestBuiltinSummaryForAllTypes(t *testing.T) {
	for _, ty := range BuiltinOnly().Types() {
		if len(ty.Summary) == 0 {
			t.Errorf("内置类型 %s 缺少卡片摘要指标", ty.Key)
		}
	}
}

// TestNewWithoutArgs 空值安全：New() 与 nil 注册表都不 panic。
// nil 指针调用值方法不触碰接收者字段，因此与内置注册表等价。
func TestNewWithoutArgs(t *testing.T) {
	if got := New().Types(); len(got) != 15 {
		t.Fatalf("New() 应返回内置类型，got %d", len(got))
	}
	var r *Registry
	if got := r.Keys(); len(got) != 15 {
		t.Fatalf("nil 注册表应退化为内置类型，got %d", len(got))
	}
}
