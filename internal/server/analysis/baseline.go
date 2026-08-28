package analysis

import (
	"math"
	"sort"

	"github.com/nebula/monitor/internal/model"
)

const minBaselineSamples = 24

// CalculateBaseline 以中位数和 MAD 构造稳健正常区间，并要求末尾连续三个点越界才判定异常。
func CalculateBaseline(metric string, labels map[string]string, points []model.Point) Baseline {
	out := Baseline{Metric: metric, Labels: labels, SampleCount: len(points), RequiredSamples: minBaselineSamples, Points: points, Status: "ok"}
	if len(points) < minBaselineSamples {
		out.Status = "insufficient_data"
		return out
	}
	values := make([]float64, 0, len(points))
	for _, p := range points {
		if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
			values = append(values, p.Value)
		}
	}
	if len(values) < minBaselineSamples {
		out.Status = "insufficient_data"
		return out
	}
	out.SampleCount = len(values)
	out.Median = median(values)
	deviations := make([]float64, len(values))
	for i, value := range values {
		deviations[i] = math.Abs(value - out.Median)
	}
	mad := median(deviations)
	spread := math.Max(mad*1.4826, baselineFloor(metric))
	out.Lower = math.Max(0, out.Median-3*spread)
	out.Upper = out.Median + 3*spread
	out.Latest = values[len(values)-1]
	out.DeviationScore = math.Abs(out.Latest-out.Median) / spread
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] < out.Lower || values[i] > out.Upper {
			out.Consecutive++
			continue
		}
		break
	}
	out.IsAnomalous = out.Consecutive >= 3
	return out
}

func baselineFloor(metric string) float64 {
	switch metric {
	case "cpu_usage", "mem_used_percent", "disk_used_percent":
		return 1
	default:
		return 0.01
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	middle := len(copyValues) / 2
	if len(copyValues)%2 == 0 {
		return (copyValues[middle-1] + copyValues[middle]) / 2
	}
	return copyValues[middle]
}

func anomalyEvidence(b Baseline) Evidence {
	level, score := SeverityWarning, 35
	if b.DeviationScore >= 6 || b.Latest >= 95 {
		level, score = SeverityCritical, 60
	}
	return Evidence{Type: "anomaly", Metric: b.Metric, Labels: b.Labels, Severity: level,
		Title:      metricTitle(b.Metric) + "持续偏离动态基线",
		Detail:     "最新值 " + formatNumber(b.Latest) + "，正常区间 " + formatNumber(b.Lower) + "–" + formatNumber(b.Upper) + "，已连续 " + itoa(b.Consecutive) + " 个采样点越界。",
		Suggestion: "结合该主机的进程、中间件与近期变更记录核查负载来源；如为稳定业务增长，可人工调整告警阈值。", Score: score}
}
