package analysis

import (
	"math"
	"strconv"
	"time"

	"github.com/nebula/monitor/internal/model"
)

const minForecastSamples = 12

// ForecastCapacity 使用线性回归预测百分比容量达到 100 的时间。
func ForecastCapacity(labels map[string]string, points []model.Point) Forecast {
	out := Forecast{Metric: "disk_used_percent", Labels: labels, Points: points, Status: "unknown"}
	if len(points) < minForecastSamples {
		out.Reason = "历史样本不足，至少需要 12 个有效采样点"
		return out
	}
	valid := make([]model.Point, 0, len(points))
	for _, p := range points {
		if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
			valid = append(valid, p)
		}
	}
	if len(valid) < minForecastSamples {
		out.Reason = "有效样本不足，无法排除数据间断影响"
		return out
	}
	out.Latest = valid[len(valid)-1].Value
	base := float64(valid[0].Timestamp)
	var sumX, sumY, sumXX, sumXY float64
	for _, p := range valid {
		x := (float64(p.Timestamp) - base) / float64(24*time.Hour/time.Millisecond)
		sumX += x
		sumY += p.Value
		sumXX += x * x
		sumXY += x * p.Value
	}
	n := float64(len(valid))
	denominator := n*sumXX - sumX*sumX
	if denominator == 0 {
		out.Reason = "采样时间缺少变化，无法建立趋势"
		return out
	}
	slope := (n*sumXY - sumX*sumY) / denominator
	intercept := (sumY - slope*sumX) / n
	out.RatePerDay = slope
	var total, residual float64
	mean := sumY / n
	for _, p := range valid {
		x := (float64(p.Timestamp) - base) / float64(24*time.Hour/time.Millisecond)
		fitted := intercept + slope*x
		total += (p.Value - mean) * (p.Value - mean)
		residual += (p.Value - fitted) * (p.Value - fitted)
	}
	if total > 0 {
		out.RSquared = math.Max(0, 1-residual/total)
	}
	if slope <= 0 {
		out.Reason = "容量使用率未呈持续增长趋势"
		return out
	}
	if out.RSquared < 0.45 {
		out.Reason = "趋势波动较大，拟合可信度不足"
		return out
	}
	out.DaysRemaining = math.Max(0, (100-out.Latest)/slope)
	out.ExhaustedAt = valid[len(valid)-1].Timestamp + int64(out.DaysRemaining*float64(24*time.Hour/time.Millisecond))
	out.Status = "normal"
	if out.DaysRemaining <= 7 {
		out.Status = "urgent"
	} else if out.DaysRemaining <= 30 {
		out.Status = "warning"
	}
	return out
}

func forecastEvidence(f Forecast) Evidence {
	level, score := SeverityWarning, 30
	if f.Status == "urgent" {
		level, score = SeverityCritical, 65
	}
	return Evidence{Type: "capacity", Metric: f.Metric, Labels: f.Labels, Severity: level,
		Title:      "分区容量可能在 " + formatDays(f.DaysRemaining) + " 后耗尽",
		Detail:     "当前使用率 " + formatNumber(f.Latest) + "% ，近 7 天增长约 " + formatNumber(f.RatePerDay) + "%/天，趋势拟合度 " + formatNumber(f.RSquared) + "。",
		Suggestion: "核查日志、备份、临时文件与业务数据增长；安排清理、扩容或调整保留策略。", Score: score}
}

func metricTitle(metric string) string {
	switch metric {
	case "cpu_usage":
		return "CPU 使用率"
	case "mem_used_percent":
		return "内存使用率"
	case "disk_used_percent":
		return "磁盘使用率"
	default:
		return metric
	}
}
func formatNumber(value float64) string { return strconv.FormatFloat(value, 'f', 1, 64) }
func formatDays(value float64) string {
	if value < 1 {
		return "不足 1 天"
	}
	return strconv.FormatFloat(math.Ceil(value), 'f', 0, 64) + " 天"
}
func itoa(value int) string { return strconv.Itoa(value) }
