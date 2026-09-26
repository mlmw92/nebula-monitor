package alert

import (
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// CorrelationProvider 提供「节点 → 人工可读的关联结论」，用于在收敛摘要里附注根因线索。
//
// 只能用接口反向注入：analysis 包依赖 alert（`analysis.SetAlertStore` 接收 `alert.VMAlertStore`），
// 因此 alert 不能反向 import analysis；由 API 层用 `analysis.Analyzer` 实现本接口。
type CorrelationProvider interface {
	CorrelationNotes(node string) []string
}

const (
	// convergeMaxNotes 单条通知最多附注的关联结论条数。
	convergeMaxNotes = 6
	// convergeMaxNoteNodes 最多为前 N 个涉事节点取结论，避免多节点风暴时结论刷屏。
	convergeMaxNoteNodes = 3
)

// SetCorrelationProvider 注入关联结论提供者（可选；未注入时摘要不附注结论）。
func (e *Engine) SetCorrelationProvider(p CorrelationProvider) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.correlator = p
}

// correlationNotes 为一批告警收集关联结论：按涉事节点去重、限量取前若干节点。
// 注意：需在未持有 e.mu 时调用（内部会短暂取锁读取 provider）。
func (e *Engine) correlationNotes(events []model.AlertEvent) []string {
	e.mu.Lock()
	p := e.correlator
	e.mu.Unlock()
	if p == nil {
		return nil
	}

	seen := map[string]bool{}
	notes := make([]string, 0, convergeMaxNotes)
	for _, ev := range events {
		if ev.Node == "" || seen[ev.Node] {
			continue
		}
		if len(seen) >= convergeMaxNoteNodes {
			break
		}
		seen[ev.Node] = true
		for _, n := range p.CorrelationNotes(ev.Node) {
			if len(notes) >= convergeMaxNotes {
				return notes
			}
			notes = append(notes, ev.Node+"："+n)
		}
	}
	return notes
}

// convergeEvents 把一批同组告警收敛为「头部告警 + 摘要」。
//
// 语义：
//   - 0/1 条：原样返回，与改造前完全一致；
//   - ≥2 条：头部取「级别最高，同级取最早触发」的一条；其余折叠为计数与 Top N 明细；
//     摘要以**追加**方式写入头部 Message，因此渠道消息模板渲染出的正文不会被覆盖
//     （调用方必须把它放在模板渲染之后）。
//
// 收敛发生时的返回值恒为长度 1 的切片。
func convergeEvents(events []model.AlertEvent, headCount int, notes []string) []model.AlertEvent {
	if len(events) <= 1 {
		return events
	}
	if headCount <= 0 {
		headCount = defaultHeadCount
	}
	if headCount > maxHeadCount {
		headCount = maxHeadCount
	}

	ordered := append([]model.AlertEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Severity != ordered[j].Severity {
			return sevMoreSevere(ordered[i].Severity, ordered[j].Severity)
		}
		if ordered[i].StartsAt != ordered[j].StartsAt {
			return ordered[i].StartsAt < ordered[j].StartsAt
		}
		return ordered[i].Node < ordered[j].Node
	})

	bySeverity := map[string]int{}
	nodes := map[string]bool{}
	rules := []string{}
	ruleSeen := map[string]bool{}
	for _, ev := range ordered {
		bySeverity[string(ev.Severity)]++
		if ev.Node != "" {
			nodes[ev.Node] = true
		}
		if ev.RuleName != "" && !ruleSeen[ev.RuleName] {
			ruleSeen[ev.RuleName] = true
			rules = append(rules, ev.RuleName)
		}
	}
	sort.Strings(rules)

	head := ordered[0]
	limit := headCount
	if limit > len(ordered) {
		limit = len(ordered)
	}

	var b strings.Builder
	b.WriteString(head.Message)
	b.WriteString("\n\n【告警收敛】共 ")
	b.WriteString(strconv.Itoa(len(ordered)))
	b.WriteString(" 条告警合并为 1 条")
	if len(rules) > 0 {
		b.WriteString("\n  规则：")
		b.WriteString(rulesBrief(rules))
		b.WriteString("（共 ")
		b.WriteString(strconv.Itoa(len(rules)))
		b.WriteString(" 类）")
	}
	b.WriteString("\n  范围：")
	b.WriteString(strconv.Itoa(len(nodes)))
	b.WriteString(" 台主机")
	b.WriteString("\n  级别：")
	b.WriteString(severityBrief(bySeverity))
	b.WriteString("\n  最高：")
	b.WriteString(string(head.Severity))
	b.WriteString("\nTop ")
	b.WriteString(strconv.Itoa(limit))
	b.WriteString("：")
	for i := 0; i < limit; i++ {
		b.WriteString("\n  ")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		b.WriteString(eventBrief(ordered[i]))
	}
	if rest := len(ordered) - limit; rest > 0 {
		b.WriteString("\n其余 ")
		b.WriteString(strconv.Itoa(rest))
		b.WriteString(" 条已折叠（可在告警中心查看完整列表）。")
	}
	if len(notes) > 0 {
		b.WriteString("\n关联结论：")
		for _, n := range notes {
			b.WriteString("\n  - ")
			b.WriteString(n)
		}
	}

	head.Message = b.String()
	return []model.AlertEvent{head}
}

// rulesBrief 生成规则名摘要：最多列出 3 个，其余折叠为「等」。
func rulesBrief(names []string) string {
	const maxNames = 3
	if len(names) <= maxNames {
		return strings.Join(names, "、")
	}
	return strings.Join(names[:maxNames], "、") + " 等"
}

// severityBrief 生成「级别 计数」摘要，已知级别按严重度降序，未知级别排在末尾。
func severityBrief(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, s := range []model.Severity{model.SeverityCritical, model.SeverityWarning, model.SeverityInfo} {
		if n := counts[string(s)]; n > 0 {
			parts = append(parts, string(s)+" "+strconv.Itoa(n))
			delete(counts, string(s))
		}
	}
	// 自定义级别（若有）按字典序补在末尾，保证统计不丢
	rest := make([]string, 0, len(counts))
	for k := range counts {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		parts = append(parts, k+" "+strconv.Itoa(counts[k]))
	}
	return strings.Join(parts, " / ")
}

// eventBrief 生成单条告警的一行明细，便于短信 / IM 阅读。
func eventBrief(ev model.AlertEvent) string {
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(string(ev.Severity))
	b.WriteString("] ")
	if ev.Node != "" {
		b.WriteString(ev.Node)
		b.WriteString(" ")
	}
	if ev.Instance != "" && ev.Instance != ev.Node {
		b.WriteString(ev.Instance)
		b.WriteString(" ")
	}
	if ev.Metric != "" {
		b.WriteString(ev.Metric)
	}
	if ev.Operator != "" {
		b.WriteString(" ")
		b.WriteString(fmtNum(ev.Value))
		b.WriteString(" ")
		b.WriteString(ev.Operator)
		b.WriteString(" ")
		b.WriteString(fmtNum(ev.Threshold))
	}
	return strings.TrimRight(b.String(), " ")
}

// fmtNum 以最短可读形式格式化数值（92.4 → "92.4"，90 → "90"）。
func fmtNum(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
