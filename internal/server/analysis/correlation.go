package analysis

import (
	"fmt"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// Correlation 是由可观测信号推导出的只读故障链路，不代表确定因果关系。
type Correlation struct {
	Kind           string     `json:"kind"`
	Title          string     `json:"title"`
	Confidence     string     `json:"confidence"`
	Summary        string     `json:"summary"`
	Impact         []Impact   `json:"impact,omitempty"`
	Evidence       []Evidence `json:"evidence,omitempty"`
	Recommendation string     `json:"recommendation"`
}

// Impact 描述已注册资源或拨测任务的明确受影响范围。
type Impact struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// MiddlewareRef 为根因关联提供最小、脱敏的实例拓扑投影。
type MiddlewareRef struct {
	Kind     string
	Name     string
	Node     string
	Group    string
	Up       bool
	Topology string
}

// ProbeRef 为根因关联提供最近一次拨测状态；仅在目标明确匹配节点地址时关联。
type ProbeRef struct {
	Name   string
	Target string
	Up     bool
	Error  string
}

func correlateHost(h HostResult, middleware []MiddlewareRef, probes []ProbeRef, securityEvents []model.SecurityEvent) []Correlation {
	out := make([]Correlation, 0, 3)
	impacts := middlewareOnNode(h.Node, middleware)
	if !h.Online && len(impacts) > 0 {
		out = append(out, Correlation{
			Kind: "host_unavailable", Title: "主机离线可能影响已注册服务", Confidence: "high",
			Summary: fmt.Sprintf("主机 %s 当前离线，其上注册的 %d 个中间件实例可能不可用。", h.Node, len(impacts)),
			Impact:  impacts, Evidence: filterEvidence(h.Evidence, "availability"),
			Recommendation: "先恢复主机网络、系统与 Agent 连通性，再逐项确认受影响实例是否恢复。",
		})
	}
	if hasType(h.Evidence, "anomaly") || hasType(h.Evidence, "alert") {
		affected := append([]Impact{}, impacts...)
		affected = append(affected, probesMatchingHost(h, probes)...)
		if len(affected) > 0 {
			out = append(out, Correlation{
				Kind: "host_signal_impact", Title: "主机异常与服务可用性信号相关", Confidence: confidence(h.Evidence),
				Summary: "同一主机存在资源异常或活跃告警，并检测到明确归属的服务/拨测对象；请按时间线核对是否由主机负载、网络或进程变化引起。",
				Impact:  affected, Evidence: filterEvidence(h.Evidence, "anomaly", "alert"),
				Recommendation: "先检查异常起始时刻的 CPU、内存、磁盘、网络与进程，再验证关联服务和拨测目标。",
			})
		}
	}
	if events := recentSecurityEvents(h.Node, securityEvents); len(events) > 0 && (hasType(h.Evidence, "anomaly") || hasType(h.Evidence, "alert")) {
		out = append(out, Correlation{
			Kind: "security_runtime", Title: "安全事件与运行风险同主机出现", Confidence: "medium",
			Summary:        fmt.Sprintf("该主机近期出现 %d 条中高风险安全事件，且同时存在运行异常或活跃告警；这是关联线索，不等同于已确认根因。", len(events)),
			Evidence:       append(filterEvidence(h.Evidence, "anomaly", "alert", "security"), securityEventEvidence(events[0])),
			Recommendation: "在安全中心确认事件时间、账户和来源，并与运行指标异常开始时间比对后再处置。",
		})
	}
	return out
}

func middlewareOnNode(node string, all []MiddlewareRef) []Impact {
	out := make([]Impact, 0)
	for _, item := range all {
		if item.Node != node {
			continue
		}
		status := "正常"
		if !item.Up {
			status = "不可达"
		}
		out = append(out, Impact{Kind: item.Kind, Name: item.Name, Status: status})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func probesMatchingHost(h HostResult, probes []ProbeRef) []Impact {
	out := make([]Impact, 0)
	for _, item := range probes {
		if !item.Up && strings.Contains(item.Target, h.Node) {
			out = append(out, Impact{Kind: "拨测", Name: item.Name, Status: "失败：" + item.Error})
		}
	}
	return out
}

func recentSecurityEvents(node string, events []model.SecurityEvent) []model.SecurityEvent {
	out := make([]model.SecurityEvent, 0)
	for _, event := range events {
		if event.Node == node && (event.Severity == model.SeverityCritical || event.Severity == model.SeverityWarning) {
			out = append(out, event)
		}
	}
	return out
}

func securityEventEvidence(item model.SecurityEvent) Evidence {
	level := SeverityWarning
	if item.Severity == model.SeverityCritical {
		level = SeverityCritical
	}
	return Evidence{Type: "security_event", Severity: level, Title: "近期安全事件：" + item.Category, Detail: item.Message, Suggestion: "核对安全事件与运行异常的发生时间和关联账户。"}
}

func hasType(items []Evidence, kind string) bool {
	for _, item := range items {
		if item.Type == kind {
			return true
		}
	}
	return false
}

func filterEvidence(items []Evidence, kinds ...string) []Evidence {
	allowed := map[string]bool{}
	for _, kind := range kinds {
		allowed[kind] = true
	}
	out := make([]Evidence, 0)
	for _, item := range items {
		if allowed[item.Type] {
			out = append(out, item)
		}
	}
	return out
}

func confidence(items []Evidence) string {
	if hasType(items, "anomaly") && hasType(items, "alert") {
		return "high"
	}
	return "medium"
}
