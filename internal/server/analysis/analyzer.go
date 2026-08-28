// Package analysis 将历史指标转化为可解释的异常、容量预测和风险排序结论。
package analysis

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/storage"
)

const (
	analysisWindow = 7 * 24 * time.Hour
	analysisStep   = time.Hour
	cacheTTL       = 5 * time.Minute
	maxNodes       = 200
)

// Severity 表示分析结论的风险等级。
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Evidence 是可展示、可追溯的单项风险依据。
type Evidence struct {
	Type       string            `json:"type"`
	Metric     string            `json:"metric"`
	Labels     map[string]string `json:"labels,omitempty"`
	Severity   Severity          `json:"severity"`
	Title      string            `json:"title"`
	Detail     string            `json:"detail"`
	Suggestion string            `json:"suggestion"`
	Score      int               `json:"score"`
}

// Baseline 表示稳健统计得到的动态正常区间与异常证据。
type Baseline struct {
	Metric         string            `json:"metric"`
	Labels         map[string]string `json:"labels,omitempty"`
	Median         float64           `json:"median"`
	Lower          float64           `json:"lower"`
	Upper          float64           `json:"upper"`
	Latest         float64           `json:"latest"`
	SampleCount    int               `json:"sampleCount"`
	Consecutive    int               `json:"consecutive"`
	IsAnomalous    bool              `json:"isAnomalous"`
	DeviationScore float64           `json:"deviationScore"`
	Status         string            `json:"status"`
	Points         []model.Point     `json:"points,omitempty"`
}

// Forecast 表示分区容量耗尽预测。不可预测时 Reason 必须非空。
type Forecast struct {
	Metric        string            `json:"metric"`
	Labels        map[string]string `json:"labels,omitempty"`
	Latest        float64           `json:"latest"`
	RatePerDay    float64           `json:"ratePerDay"`
	RSquared      float64           `json:"rSquared"`
	ExhaustedAt   int64             `json:"exhaustedAt,omitempty"`
	DaysRemaining float64           `json:"daysRemaining,omitempty"`
	Status        string            `json:"status"`
	Reason        string            `json:"reason,omitempty"`
	Points        []model.Point     `json:"points,omitempty"`
}

// HostResult 聚合单台主机的分析结论。
type HostResult struct {
	Node      string     `json:"node"`
	Group     string     `json:"group,omitempty"`
	Online    bool       `json:"online"`
	Score     int        `json:"score"`
	Severity  Severity   `json:"severity"`
	Evidence  []Evidence `json:"evidence"`
	Baselines []Baseline `json:"baselines"`
	Forecasts []Forecast `json:"forecasts"`
	Status    string     `json:"status"`
}

// Summary 是智能分析摘要和按风险排序的主机列表。
type Summary struct {
	GeneratedAt         int64        `json:"generatedAt"`
	Cached              bool         `json:"cached"`
	Status              string       `json:"status"`
	NodeCount           int          `json:"nodeCount"`
	RiskCount           int          `json:"riskCount"`
	AnomalyCount        int          `json:"anomalyCount"`
	UrgentCapacityCount int          `json:"urgentCapacityCount"`
	Hosts               []HostResult `json:"hosts"`
}

// Analyzer 只读查询现有时序库，不写入指标也不修改告警规则。
type Analyzer struct {
	store    storage.Storage
	nodes    *node.Manager
	mu       sync.Mutex
	cached   Summary
	cachedAt time.Time
}

func New(store storage.Storage, nodes *node.Manager) *Analyzer {
	return &Analyzer{store: store, nodes: nodes}
}

// Summary 返回有限时缓存的全局分析，避免概览刷新放大 PromQL 查询压力。
func (a *Analyzer) Summary(refresh bool) Summary {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !refresh && !a.cachedAt.IsZero() && time.Since(a.cachedAt) < cacheTTL {
		out := a.cached
		out.Cached = true
		return out
	}
	out := a.analyzeLocked()
	a.cached, a.cachedAt = out, time.Now()
	return out
}

// Host 返回指定节点的最新分析；节点不存在时返回 false。
func (a *Analyzer) Host(name string, refresh bool) (HostResult, bool) {
	summary := a.Summary(refresh)
	for _, h := range summary.Hosts {
		if h.Node == name {
			return h, true
		}
	}
	return HostResult{}, false
}

func (a *Analyzer) analyzeLocked() Summary {
	nodes := a.nodes.ListHostNodes()
	if len(nodes) > maxNodes {
		nodes = nodes[:maxNodes]
	}
	out := Summary{GeneratedAt: time.Now().UnixMilli(), Status: "ok", NodeCount: len(nodes), Hosts: make([]HostResult, 0, len(nodes))}
	end := time.Now()
	start := end.Add(-analysisWindow)
	for _, n := range nodes {
		h := a.analyzeHost(n, start, end)
		out.Hosts = append(out.Hosts, h)
		if h.Score > 0 {
			out.RiskCount++
		}
		for _, b := range h.Baselines {
			if b.IsAnomalous {
				out.AnomalyCount++
			}
		}
		for _, f := range h.Forecasts {
			if f.Status == "urgent" {
				out.UrgentCapacityCount++
			}
		}
	}
	sort.SliceStable(out.Hosts, func(i, j int) bool { return out.Hosts[i].Score > out.Hosts[j].Score })
	return out
}

func (a *Analyzer) analyzeHost(n model.Node, start, end time.Time) HostResult {
	h := HostResult{Node: n.Hostname, Group: n.Group, Online: n.Status == "online", Severity: SeverityInfo, Status: "ok"}
	if !h.Online {
		h.Evidence = append(h.Evidence, Evidence{Type: "availability", Severity: SeverityWarning, Title: "主机当前离线", Detail: "离线主机的历史趋势仅供参考。", Suggestion: "确认网络连通性、Agent 服务和主机运行状态。", Score: 35})
	}
	for _, metric := range []string{"cpu_usage", "mem_used_percent"} {
		series, err := a.store.QueryRange(n.Hostname, metric, nil, start.UnixMilli(), end.UnixMilli(), analysisStep.Milliseconds())
		if err != nil {
			slog.Warn("智能分析查询指标失败", "node", n.Hostname, "metric", metric, "err", err)
			continue
		}
		for _, s := range series {
			b := CalculateBaseline(metric, s.Labels, s.Points)
			h.Baselines = append(h.Baselines, b)
			if b.IsAnomalous {
				h.Evidence = append(h.Evidence, anomalyEvidence(b))
			}
		}
	}
	series, err := a.store.QueryRange(n.Hostname, "disk_used_percent", nil, start.UnixMilli(), end.UnixMilli(), analysisStep.Milliseconds())
	if err != nil {
		slog.Warn("智能分析查询磁盘趋势失败", "node", n.Hostname, "err", err)
	} else {
		for _, s := range series {
			b := CalculateBaseline("disk_used_percent", s.Labels, s.Points)
			h.Baselines = append(h.Baselines, b)
			if b.IsAnomalous {
				h.Evidence = append(h.Evidence, anomalyEvidence(b))
			}
			f := ForecastCapacity(s.Labels, s.Points)
			h.Forecasts = append(h.Forecasts, f)
			if f.Status == "urgent" || f.Status == "warning" {
				h.Evidence = append(h.Evidence, forecastEvidence(f))
			}
		}
	}
	h.Score, h.Severity = ScoreRisk(h.Evidence)
	if len(h.Baselines) == 0 && len(h.Forecasts) == 0 {
		h.Status = "no_data"
	}
	return h
}
