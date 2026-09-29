package alert

import (
	"testing"

	"github.com/nebula/monitor/internal/server/mwreg"
	"github.com/nebula/monitor/internal/template"
)

// fakeTemplateSource 是模板快照的测试替身。
type fakeTemplateSource struct{ list []template.Config }

func (f *fakeTemplateSource) Snapshot() ([]template.Config, uint64) { return f.list, 1 }

// TestTemplateTypesEnterAlerting 注入注册表后，模板派生类型应能被「服务离线」规则监控
// ——否则「新增中间件只写模板」到告警这一步就断了。
func TestTemplateTypesEnterAlerting(t *testing.T) {
	reg := mwreg.New(&fakeTemplateSource{list: []template.Config{{
		ID:      "testmw",
		Title:   "TestMW",
		Groups:  []string{"default"},
		Targets: []template.Target{{Addr: "http://127.0.0.1:15692/metrics"}},
	}}})
	SetMiddlewareRegistry(reg)
	defer SetMiddlewareRegistry(mwreg.BuiltinOnly()) // 包级变量，测完还原

	if !validService("testmw") {
		t.Fatal("模板派生类型应通过服务类型校验")
	}
	if got := serviceMetric("testmw"); got != template.UpMetricName {
		t.Fatalf("模板类型的存活指标 = %q，want %q", got, template.UpMetricName)
	}
	// 关键：必须带 template 标签过滤。所有模板共用 template_target_up，
	// 不过滤会让「A 模板离线」被 B 模板的实例误触发。
	labels := upLabelsFor("testmw")
	if labels["template"] != "testmw" {
		t.Fatalf("模板类型的存活指标应带 template 过滤，got %v", labels)
	}
	// 与内置同名的模板 id 不得遮蔽内置类型的存活指标
	if got := serviceMetric("rabbitmq"); got != "rabbitmq_instance_up" {
		t.Fatalf("同名模板不应遮蔽内置类型的存活指标，got %q", got)
	}
}

// TestBuiltinTypesKeepNoLabelFilter 内置类型不应被附加标签过滤（回归保护：
// 若误把模板的过滤逻辑套到内置类型上，所有内置告警都会查不到数据）。
func TestBuiltinTypesKeepNoLabelFilter(t *testing.T) {
	SetMiddlewareRegistry(mwreg.New(&fakeTemplateSource{}))
	defer SetMiddlewareRegistry(mwreg.BuiltinOnly())

	for _, svc := range KnownServices {
		if labels := upLabelsFor(svc); len(labels) != 0 {
			t.Errorf("内置类型 %s 不应有标签过滤，got %v", svc, labels)
		}
		if serviceMetric(svc) == "" {
			t.Errorf("内置类型 %s 的存活指标不应为空", svc)
		}
	}
}

// TestUnknownServiceStillAccepted 语义保持：未注入注册表时行为与改造前一致
// （内置类型可用、未知类型回退），避免注册化引入行为漂移。
func TestUnknownServiceStillAccepted(t *testing.T) {
	SetMiddlewareRegistry(mwreg.BuiltinOnly())
	if validService("no-such-service") {
		t.Fatal("未知服务不应通过校验")
	}
	if got := serviceMetric("no-such-service"); got != "redis_instance_up" {
		t.Fatalf("未知服务的存活指标应回退，got %q", got)
	}
	if labels := upLabelsFor("no-such-service"); labels != nil {
		t.Fatalf("未知服务不应有标签过滤，got %v", labels)
	}
}
