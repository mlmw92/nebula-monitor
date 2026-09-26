package analysis

import (
	"math"
	"strconv"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/metrics"
)

const (
	// minForecastSamples 建立趋势所需的最少有效采样点。
	minForecastSamples = 12
	// projectionDays 无上限指标（速率类）的预测跨度。
	projectionDays = 7
	// risingGrowthRatio 速率类指标判定「持续增长」的阈值：
	// 预测跨度内的增幅超过当前值的 20% 才提示，避免把日常波动当成增长。
	risingGrowthRatio = 0.2
	// minForecastRSquared 趋势拟合度下限：低于该值认为是波动而非趋势。
	minForecastRSquared = 0.45
	// minRisingRSquared 速率类指标的拟合度要求更高：无上限指标缺少「打满」这一硬信号，
	// 只能靠趋势本身，因此对噪声更保守。
	minRisingRSquared = 0.6
)

// forecastCeilings 定义百分比类指标的预测上限：达到上限即「耗尽 / 打满」。
// 未列出的指标视为无绝对上限（如网络速率），只预测增长趋势。
var forecastCeilings = map[string]float64{
	"disk_used_percent": 100,
	"mem_used_percent":  100,
	"cpu_usage":         100,
}

// analyzedMetrics 一次取数、同时用于动态基线与容量预测的指标，
// 避免同一指标为了「基线」和「预测」重复查询两次。
var analyzedMetrics = []string{"cpu_usage", "mem_used_percent", "disk_used_percent"}

// rateMetrics 只做容量预测的速率类指标：它们没有「打满」语义，
// 也不适合用持续偏离基线来判异常（业务流量本身就有周期）。
var rateMetrics = []string{"network_recv_rate", "network_sent_rate"}

// ForecastCapacity 使用线性回归预测分区容量达到 100% 的时间。保留既有入口。
func ForecastCapacity(labels map[string]string, points []model.Point) Forecast {
	return forecastMetric("disk_used_percent", labels, points)
}

// forecastMetric 对单个指标做线性回归并给出容量结论：
//   - 百分比类（有上限）：估算达到上限所需天数（DaysRemaining）与预计时间（ExhaustedAt）；
//   - 速率类（无上限）：只给预测值（ProjectedValue）与增长状态 rising，不报「耗尽」。
//
// 所有分支都以 Summary 给出一句可直接展示的结论，前端无需再拼中文与单位。
func forecastMetric(metric string, labels map[string]string, points []model.Point) Forecast {
	out := Forecast{
		Metric:      metric,
		MetricTitle: metricTitle(metric),
		Labels:      labels,
		Points:      points,
		Status:      "unknown",
		Ceiling:     forecastCeilings[metric],
	}
	if meta, ok := metrics.Meta(metric); ok {
		out.Unit = meta.Unit
	}

	if len(points) < minForecastSamples {
		out.Reason = "历史样本不足，至少需要 " + itoa(minForecastSamples) + " 个有效采样点"
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
	out.ProjectedValue = out.Latest + slope*projectionDays

	if out.Ceiling <= 0 {
		// 无上限指标：没有「打满」这一硬信号，只报告显著增长
		if out.RSquared < minRisingRSquared {
			out.Reason = "趋势波动较大，拟合可信度不足"
			return out
		}
		if out.Latest <= 0 || (out.ProjectedValue-out.Latest)/out.Latest < risingGrowthRatio {
			out.Reason = "增长幅度有限，暂无需干预"
			return out
		}
		out.Status = "rising"
		out.Summary = "按当前趋势，" + itoa(projectionDays) + " 天后约达 " + formatValue(out.ProjectedValue, out.Unit) +
			"（当前 " + formatValue(out.Latest, out.Unit) + "）"
		return out
	}

	if out.RSquared < minForecastRSquared {
		out.Reason = "趋势波动较大，拟合可信度不足"
		return out
	}
	out.DaysRemaining = math.Max(0, (out.Ceiling-out.Latest)/slope)
	out.ExhaustedAt = valid[len(valid)-1].Timestamp + int64(out.DaysRemaining*float64(24*time.Hour/time.Millisecond))
	out.Status = "normal"
	if out.DaysRemaining <= 7 {
		out.Status = "urgent"
	} else if out.DaysRemaining <= 30 {
		out.Status = "warning"
	}
	out.Summary = "预计 " + formatDays(out.DaysRemaining) + " 后达到 " + formatValue(out.Ceiling, out.Unit) +
		"（当前 " + formatValue(out.Latest, out.Unit) + "，约 " + formatNumber(out.RatePerDay) + out.Unit + "/天）"
	return out
}

// forecastTitle 生成容量结论标题。按指标语义区分「耗尽」「打满」与「增长」，
// 避免把 CPU 饱和说成「容量耗尽」。
func forecastTitle(f Forecast) string {
	title := f.MetricTitle
	if title == "" {
		title = f.Metric
	}
	switch {
	case f.Status == "rising":
		return title + "持续增长，预计 " + itoa(projectionDays) + " 天后达 " + formatValue(f.ProjectedValue, f.Unit)
	case f.Metric == "cpu_usage":
		return title + "可能在 " + formatDays(f.DaysRemaining) + " 后持续打满"
	case f.Metric == "mem_used_percent":
		return title + "可能在 " + formatDays(f.DaysRemaining) + " 后耗尽"
	default:
		return title + "可能在 " + formatDays(f.DaysRemaining) + " 后耗尽"
	}
}

// forecastSuggestion 给出按指标语义区分的处置建议。
func forecastSuggestion(f Forecast) string {
	switch f.Metric {
	case "mem_used_percent":
		return "核查是否存在内存泄漏与缓存膨胀，必要时扩容内存或调整应用缓存上限。"
	case "cpu_usage":
		return "核查热点进程与慢查询，必要时扩容 CPU、限流或优化业务逻辑。"
	case "network_recv_rate", "network_sent_rate":
		return "核查流量增长来源与带宽上限，评估扩容带宽、限速或增加缓存层。"
	default:
		return "核查日志、备份、临时文件与业务数据增长；安排清理、扩容或调整保留策略。"
	}
}

func forecastEvidence(f Forecast) Evidence {
	level, score := SeverityWarning, 30
	switch f.Status {
	case "urgent":
		level, score = SeverityCritical, 65
	case "rising":
		level, score = SeverityWarning, 25
	}
	return Evidence{Type: "capacity", Metric: f.Metric, Labels: f.Labels, Severity: level,
		Title:      forecastTitle(f),
		Detail:     "当前 " + formatValue(f.Latest, f.Unit) + "，近 7 天增长约 " + formatNumber(f.RatePerDay) + f.Unit + "/天，趋势拟合度 " + formatNumber(f.RSquared) + "。",
		Suggestion: forecastSuggestion(f), Score: score}
}

// metricTitle 返回指标中文名（优先取指标目录，保证与「指标浏览」等页面一致）。
func metricTitle(metric string) string {
	if meta, ok := metrics.Meta(metric); ok && meta.Title != "" {
		return meta.Title
	}
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

// formatValue 按单位格式化数值：百分比保留一位小数，字节/秒换算为可读单位。
func formatValue(value float64, unit string) string {
	switch unit {
	case "B/s":
		return formatBytes(value) + "/s"
	case "B":
		return formatBytes(value)
	case "%":
		return formatNumber(value) + "%"
	default:
		if unit == "" {
			return formatNumber(value)
		}
		return formatNumber(value) + unit
	}
}

// formatBytes 把字节数换算为 B / KB / MB / GB。
func formatBytes(value float64) string {
	switch {
	case math.Abs(value) >= 1024*1024*1024:
		return strconv.FormatFloat(value/1024/1024/1024, 'f', 2, 64) + " GB"
	case math.Abs(value) >= 1024*1024:
		return strconv.FormatFloat(value/1024/1024, 'f', 2, 64) + " MB"
	case math.Abs(value) >= 1024:
		return strconv.FormatFloat(value/1024, 'f', 1, 64) + " KB"
	default:
		return strconv.FormatFloat(value, 'f', 0, 64) + " B"
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
