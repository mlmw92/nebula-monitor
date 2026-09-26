package analysis

import (
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/metrics"
)

// ramp 生成「每小时递增 step」的采样序列（points 以小时为间隔）。
func ramp(start, step float64, n int) []float64 {
	values := make([]float64, n)
	for i := range values {
		values[i] = start + float64(i)*step
	}
	return values
}

// TestForecastMetric_MemoryCeiling 内存使用率为百分比类指标：预测何时打满 100%。
func TestForecastMetric_MemoryCeiling(t *testing.T) {
	out := forecastMetric("mem_used_percent", nil, points(ramp(70, 0.1, 30)))
	if out.Ceiling != 100 {
		t.Fatalf("内存使用率的上限应为 100，got %v", out.Ceiling)
	}
	if out.MetricTitle != "内存使用率" || out.Unit != "%" {
		t.Fatalf("应复用指标目录的标题与单位：%+v", out)
	}
	if out.Status != "warning" {
		t.Fatalf("预计 11 天打满应为 warning，实际 %q（%+v）", out.Status, out)
	}
	if out.DaysRemaining <= 0 || out.ExhaustedAt == 0 {
		t.Fatalf("应给出耗尽时间：%+v", out)
	}
	if !strings.Contains(out.Summary, "达到 100.0%") || !strings.Contains(out.Summary, "预计") {
		t.Fatalf("结论文案应可直接展示：%q", out.Summary)
	}
}

// TestForecastMetric_CPUUsesSaturationWording CPU 是「打满」而非「容量耗尽」。
func TestForecastMetric_CPUUsesSaturationWording(t *testing.T) {
	out := forecastMetric("cpu_usage", nil, points(ramp(80, 0.2, 30)))
	if out.Status != "urgent" {
		t.Fatalf("快速上升应判紧急，实际 %q（%+v）", out.Status, out)
	}
	ev := forecastEvidence(out)
	if !strings.Contains(ev.Title, "打满") {
		t.Fatalf("CPU 应使用「打满」措辞：%q", ev.Title)
	}
	if strings.Contains(ev.Title, "耗尽") {
		t.Fatalf("CPU 不应说「耗尽」：%q", ev.Title)
	}
	if !strings.Contains(ev.Suggestion, "CPU") {
		t.Fatalf("建议应针对 CPU：%q", ev.Suggestion)
	}
	if !strings.Contains(ev.Detail, "当前 85.8%") || !strings.Contains(ev.Detail, "拟合度") {
		t.Fatalf("明细应含当前值与拟合度：%q", ev.Detail)
	}
}

// TestForecastMetric_MemoryEvidence 内存建议应指向泄漏/扩容，而不是磁盘的清理话术。
func TestForecastMetric_MemoryEvidence(t *testing.T) {
	out := forecastMetric("mem_used_percent", nil, points(ramp(70, 0.1, 30)))
	ev := forecastEvidence(out)
	if !strings.Contains(ev.Title, "内存") || !strings.Contains(ev.Title, "耗尽") {
		t.Fatalf("内存标题不符：%q", ev.Title)
	}
	if !strings.Contains(ev.Suggestion, "内存") {
		t.Fatalf("内存建议不符：%q", ev.Suggestion)
	}
}

// TestForecastMetric_RateWithoutCeiling 速率类指标无绝对上限：
// 只给增长预测，不报「耗尽」，也不产生 DaysRemaining。
func TestForecastMetric_RateWithoutCeiling(t *testing.T) {
	out := forecastMetric("network_recv_rate", map[string]string{"iface": "eth0"}, points(ramp(1e6, 1e5, 30)))
	if out.Ceiling != 0 {
		t.Fatalf("网络速率不应有上限，got %v", out.Ceiling)
	}
	if out.Status != "rising" {
		t.Fatalf("显著增长应判 rising，实际 %q（%+v）", out.Status, out)
	}
	if out.DaysRemaining != 0 || out.ExhaustedAt != 0 {
		t.Fatalf("无上限指标不应给出耗尽时间：%+v", out)
	}
	if out.ProjectedValue <= out.Latest {
		t.Fatalf("应给出预测值：%+v", out)
	}
	if !strings.Contains(out.Summary, "7 天后约达") || !strings.Contains(out.Summary, "MB/s") {
		t.Fatalf("结论文案应含预测值与可读单位：%q", out.Summary)
	}

	ev := forecastEvidence(out)
	if !strings.Contains(ev.Title, "网络接收速率") || !strings.Contains(ev.Title, "持续增长") {
		t.Fatalf("标题不符：%q", ev.Title)
	}
	if !strings.Contains(ev.Suggestion, "带宽") {
		t.Fatalf("网络建议不符：%q", ev.Suggestion)
	}
}

// TestForecastMetric_RateGrowthBelowThreshold 增长幅度有限时不产生风险证据（避免日常波动刷屏）。
func TestForecastMetric_RateGrowthBelowThreshold(t *testing.T) {
	out := forecastMetric("network_sent_rate", nil, points(ramp(1e6, 1e3, 30)))
	if out.Status != "unknown" || out.Reason == "" {
		t.Fatalf("小幅增长应判为不可预测并给出原因：%+v", out)
	}
	if !strings.Contains(out.Reason, "增长幅度有限") {
		t.Fatalf("原因应说明幅度不足：%q", out.Reason)
	}
}

// TestForecastMetric_InsufficientSamples 样本不足时保持既有的「不可预测 + 原因」契约。
func TestForecastMetric_InsufficientSamples(t *testing.T) {
	out := forecastMetric("mem_used_percent", nil, points(ramp(70, 1, 5)))
	if out.Status != "unknown" || !strings.Contains(out.Reason, "12") {
		t.Fatalf("样本不足应说明所需样本数：%+v", out)
	}
	// 标题/单位即便不可预测也应可用，便于前端标明是哪个指标测不出来
	if out.MetricTitle != "内存使用率" {
		t.Fatalf("不可预测时仍应给出指标标题：%+v", out)
	}
}

// TestForecastMetricsHaveCatalogMetadata 指标清单的不变量：
// 参与预测的指标都必须在指标目录中登记（否则标题/单位会退化成英文指标名）。
func TestForecastMetricsHaveCatalogMetadata(t *testing.T) {
	for _, metric := range append(append([]string{}, analyzedMetrics...), rateMetrics...) {
		meta, ok := metrics.Meta(metric)
		if !ok || meta.Title == "" || meta.Unit == "" {
			t.Errorf("指标 %s 缺少目录元数据（标题/单位）：%+v ok=%v", metric, meta, ok)
		}
	}
	for _, metric := range rateMetrics {
		if ceiling := forecastCeilings[metric]; ceiling != 0 {
			t.Errorf("速率类指标 %s 不应配置上限，got %v", metric, ceiling)
		}
	}
	for _, metric := range analyzedMetrics {
		if forecastCeilings[metric] != 100 {
			t.Errorf("百分比类指标 %s 的上限应为 100，got %v", metric, forecastCeilings[metric])
		}
	}
}

// TestFormatValue 单位格式化：百分比、字节速率与空单位。
func TestFormatValue(t *testing.T) {
	cases := []struct {
		value float64
		unit  string
		want  string
	}{
		{92.4, "%", "92.4%"},
		{2048, "B", "2.0 KB"},
		{5 << 20, "B/s", "5.00 MB/s"},
		{3 << 30, "B/s", "3.00 GB/s"},
		{12, "", "12.0"},
		{7, "个", "7.0个"},
	}
	for _, tc := range cases {
		if got := formatValue(tc.value, tc.unit); got != tc.want {
			t.Errorf("formatValue(%v, %q) = %q, want %q", tc.value, tc.unit, got, tc.want)
		}
	}
}

// TestForecastCapacityStillDiskOnly 既有入口的语义不变：仍按磁盘 100% 上限预测。
func TestForecastCapacityStillDiskOnly(t *testing.T) {
	out := ForecastCapacity(map[string]string{"mount": "/data"}, points(ramp(50, 0.8, 30)))
	if out.Metric != "disk_used_percent" || out.Ceiling != 100 {
		t.Fatalf("既有入口应仍为磁盘预测：%+v", out)
	}
	if out.Status != "urgent" {
		t.Fatalf("该序列应判紧急，实际 %q", out.Status)
	}
	_ = model.Point{} // 保持 model 依赖（points 构造在其他用例内使用）
}
