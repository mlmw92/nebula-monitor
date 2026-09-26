package api

import (
	"strings"

	"github.com/nebula/monitor/internal/server/analysis"
)

// CorrelationNotes 实现 alert.CorrelationProvider：把智能分析的关联结论压成「一行一条」的文案，
// 供告警风暴收敛的摘要在通知里附注根因线索。
//
// 为何由 API 层实现：analysis 包依赖 alert（`analysis.SetAlertStore` 接收 `alert.VMAlertStore`），
// alert 侧不能反向 import analysis，只能在 alert 定义接口、由持有 Analyzer 的 API 层反向注入。
//
// 性能：Analyzer.Host 走其内部缓存（refresh=false），所以风暴期间不会反复重算分析。
func (a *API) CorrelationNotes(node string) []string {
	if a.analysis == nil || node == "" {
		return nil
	}
	res, ok := a.analysis.Host(node, analysis.NormalizeWindow(24), false)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(res.Correlations))
	for _, c := range res.Correlations {
		if text := correlateText(c); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// correlateText 生成单条关联结论的一行文案：标题 + 摘要，缺一即用另一方兜底。
func correlateText(c analysis.Correlation) string {
	title := strings.TrimSpace(c.Title)
	summary := strings.TrimSpace(c.Summary)
	switch {
	case title != "" && summary != "":
		return title + "：" + summary
	case summary != "":
		return summary
	default:
		return title
	}
}
