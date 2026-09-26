package alert

import (
	"testing"

	"github.com/nebula/monitor/internal/server/metrics"
)

// TestServiceMetricNamesAreRegistered 「服务离线」规则用 serviceMetric 取存活指标，
// 该指标名必须在指标目录中登记。
//
// 为什么值得单独立一条用例：这类错误的症状不是报错，而是「监控错了对象」——
// 曾出现 mongodb / fastdfs 落到 default、用 redis 的存活指标去判断它们是否在线，
// 结果是这两个服务的离线告警永不触发（或随 redis 状态误触发）。
// 本用例把「服务映射 ↔ 指标目录」固定下来：新增服务类型时若忘了登记指标，测试直接失败。
func TestServiceMetricNamesAreRegistered(t *testing.T) {
	for _, svc := range KnownServices {
		name := serviceMetric(svc)
		meta, ok := metrics.Meta(name)
		if !ok {
			t.Errorf("服务 %s 的存活指标 %s 未登记在指标目录（internal/server/metrics/middleware_catalog.go）", svc, name)
			continue
		}
		if meta.Title == "" {
			t.Errorf("指标 %s 缺少标题（指标浏览会显示成裸指标名）", name)
		}
	}
}

// TestServiceMetricUnknownFallsBack 未知服务回退到 redis 存活指标（前端已限制可选值，这里兜底）。
func TestServiceMetricUnknownFallsBack(t *testing.T) {
	if got := serviceMetric("no-such-service"); got != "redis_instance_up" {
		t.Fatalf("未知服务应回退到 redis 存活指标，got %q", got)
	}
}

// TestKnownServicesMatchValidator 白名单与已知清单保持一致（避免两处各自维护）。
func TestKnownServicesMatchValidator(t *testing.T) {
	for _, svc := range KnownServices {
		if !validService(svc) {
			t.Errorf("KnownServices 中的 %s 应通过 validService 校验", svc)
		}
	}
	if validService("no-such-service") {
		t.Error("未知服务不应通过 validService 校验")
	}
	if len(KnownServices) < 10 {
		t.Errorf("服务清单疑似缺项，当前 %d 项：%v", len(KnownServices), KnownServices)
	}
}
