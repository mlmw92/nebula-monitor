package analysis

import (
	"fmt"
	"sort"

	"github.com/nebula/monitor/internal/model"
)

func availabilityEvidence() Evidence {
	return Evidence{Type: "availability", Severity: SeverityWarning, Title: "主机当前离线", Detail: "离线主机的历史趋势仅供参考。", Suggestion: "确认网络连通性、Agent 服务和主机运行状态。", Score: 35}
}
func alertEvidence(item model.AlertEvent) Evidence {
	level := SeverityWarning
	score := 30
	if item.Severity == model.SeverityCritical {
		level = SeverityCritical
		score = 60
	}
	return Evidence{Type: "alert", Severity: level, Title: "存在活跃告警：" + item.RuleName, Detail: item.Message, Suggestion: "先处理活跃告警，并核对其与趋势异常是否指向同一资源。", Score: score}
}
func securityEvidence(item model.SecurityBaseline) Evidence {
	level := SeverityWarning
	score := 25
	if item.Score < 60 {
		level = SeverityCritical
		score = 50
	}
	return Evidence{Type: "security", Severity: level, Title: "安全基线评分偏低", Detail: fmt.Sprintf("当前安全合规评分 %.0f/100。", item.Score), Suggestion: "在安全中心核查未通过项与近期安全事件，优先处理高风险基线项。", Score: score}
}
func coverageOf(baselines []Baseline) Coverage {
	out := Coverage{RequiredSamples: minBaselineSamples}
	missing := map[string]bool{"cpu_usage": true, "mem_used_percent": true, "disk_used_percent": true}
	for _, item := range baselines {
		if item.SampleCount > out.ValidSamples {
			out.ValidSamples = item.SampleCount
		}
		if item.Status == "ok" {
			delete(missing, item.Metric)
		}
	}
	for metric := range missing {
		out.MissingMetrics = append(out.MissingMetrics, metric)
	}
	sort.Strings(out.MissingMetrics)
	out.Ready = len(out.MissingMetrics) == 0
	if out.Ready {
		out.Message = "关键指标历史样本充足，分析结论可用"
	} else if out.ValidSamples == 0 {
		out.Message = "尚未获得关键指标历史数据，请确认 Agent 上报与时序库写入"
	} else {
		out.Message = fmt.Sprintf("仍需至少 %d 个小时级有效样本后才可形成完整基线", out.RequiredSamples-out.ValidSamples)
		if out.ValidSamples >= out.RequiredSamples {
			out.Message = "部分关键指标缺失，当前结论仅覆盖已采集指标"
		}
	}
	return out
}
func riskTypes(evidence []Evidence) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(evidence))
	for _, item := range evidence {
		if !seen[item.Type] {
			seen[item.Type] = true
			out = append(out, item.Type)
		}
	}
	sort.Strings(out)
	return out
}

// ScoreRisk 按证据严重度生成可解释分数；同类证据只累加最严重的两条，避免单指标放大风险。
func ScoreRisk(evidence []Evidence) (int, Severity) {
	if len(evidence) == 0 {
		return 0, SeverityInfo
	}
	score, critical, warning := 0, false, false
	byType := make(map[string][]Evidence)
	for _, item := range evidence {
		byType[item.Type] = append(byType[item.Type], item)
	}
	for _, items := range byType {
		best, second := 0, 0
		for _, item := range items {
			if item.Score > best {
				second, best = best, item.Score
			} else if item.Score > second {
				second = item.Score
			}
			if item.Severity == SeverityCritical {
				critical = true
			}
			if item.Severity == SeverityWarning {
				warning = true
			}
		}
		score += best + second/3
	}
	if score > 100 {
		score = 100
	}
	if critical || score >= 60 {
		return score, SeverityCritical
	}
	if warning || score >= 25 {
		return score, SeverityWarning
	}
	return score, SeverityInfo
}
