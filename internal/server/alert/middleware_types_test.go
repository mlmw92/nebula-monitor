package alert

import (
	"testing"

	"github.com/nebula/monitor/internal/server/mwreg"
)

// TestBuiltinTypesKeepNoLabelFilter 内置类型不应被附加标签过滤（回归保护：
// 若误把历史模板的过滤逻辑套到内置类型上，所有内置告警都会查不到数据）。
func TestBuiltinTypesKeepNoLabelFilter(t *testing.T) {
	SetMiddlewareRegistry(mwreg.New())
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
