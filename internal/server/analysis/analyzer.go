// Package analysis 将历史指标转化为可解释的异常、容量预测和风险排序结论。
package analysis

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/storage"
)

const (
	defaultWindow = 7 * 24 * time.Hour
	cacheTTL      = 5 * time.Minute
	maxNodes      = 200
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

// Coverage 明确数据是否足以得出分析结论，避免把无数据当作低风险。
type Coverage struct {
	RequiredSamples int      `json:"requiredSamples"`
	ValidSamples    int      `json:"validSamples"`
	Ready           bool     `json:"ready"`
	MissingMetrics  []string `json:"missingMetrics,omitempty"`
	Message         string   `json:"message"`
}

// Baseline 表示稳健统计得到的动态正常区间与异常证据。
type Baseline struct {
	Metric          string            `json:"metric"`
	Labels          map[string]string `json:"labels,omitempty"`
	Median          float64           `json:"median"`
	Lower           float64           `json:"lower"`
	Upper           float64           `json:"upper"`
	Latest          float64           `json:"latest"`
	SampleCount     int               `json:"sampleCount"`
	RequiredSamples int               `json:"requiredSamples"`
	Consecutive     int               `json:"consecutive"`
	IsAnomalous     bool              `json:"isAnomalous"`
	DeviationScore  float64           `json:"deviationScore"`
	Status          string            `json:"status"`
	Points          []model.Point     `json:"points,omitempty"`
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
	Node         string        `json:"node"`
	IP           string        `json:"ip,omitempty"`
	Group        string        `json:"group,omitempty"`
	Online       bool          `json:"online"`
	Score        int           `json:"score"`
	Severity     Severity      `json:"severity"`
	RiskTypes    []string      `json:"riskTypes"`
	Evidence     []Evidence    `json:"evidence"`
	Baselines    []Baseline    `json:"baselines"`
	Forecasts    []Forecast    `json:"forecasts"`
	Coverage     Coverage      `json:"coverage"`
	Status       string        `json:"status"`
	Correlations []Correlation `json:"correlations,omitempty"`
}

// Summary 是智能分析摘要和按风险排序的主机列表。
type Summary struct {
	GeneratedAt           int64        `json:"generatedAt"`
	Cached                bool         `json:"cached"`
	Status                string       `json:"status"`
	WindowHours           int          `json:"windowHours"`
	NodeCount             int          `json:"nodeCount"`
	ReadyNodeCount        int          `json:"readyNodeCount"`
	InsufficientNodeCount int          `json:"insufficientNodeCount"`
	RiskCount             int          `json:"riskCount"`
	AnomalyCount          int          `json:"anomalyCount"`
	UrgentCapacityCount   int          `json:"urgentCapacityCount"`
	Hosts                 []HostResult `json:"hosts"`
}

type cacheEntry struct {
	summary Summary
	at      time.Time
}

// Analyzer 只读查询现有时序库，不写入指标也不修改告警规则。
type Analyzer struct {
	store     storage.Storage
	nodes     *node.Manager
	alerts    *alert.VMAlertStore
	security  *security.Store
	instances *instancereg.Registry
	dialtests *dialtest.Store
	mu        sync.Mutex
	cached    map[time.Duration]cacheEntry
}

func New(store storage.Storage, nodes *node.Manager) *Analyzer {
	return &Analyzer{store: store, nodes: nodes, cached: map[time.Duration]cacheEntry{}}
}
func (a *Analyzer) SetAlertStore(store *alert.VMAlertStore)            { a.alerts = store }
func (a *Analyzer) SetSecurityStore(store *security.Store)             { a.security = store }
func (a *Analyzer) SetInstanceRegistry(registry *instancereg.Registry) { a.instances = registry }
func (a *Analyzer) SetDialtestStore(store *dialtest.Store)             { a.dialtests = store }

// NormalizeWindow 限制受支持窗口，非法值回退 7 天。
func NormalizeWindow(hours int) time.Duration {
	switch hours {
	case 24:
		return 24 * time.Hour
	case 168:
		return 7 * 24 * time.Hour
	case 720:
		return 30 * 24 * time.Hour
	default:
		return defaultWindow
	}
}
func windowStep(window time.Duration) time.Duration {
	if window >= 30*24*time.Hour {
		return 6 * time.Hour
	}
	return time.Hour
}

// Summary 返回指定窗口的有限时缓存分析。
func (a *Analyzer) Summary(window time.Duration, refresh bool) Summary {
	window = NormalizeWindow(int(window.Hours()))
	a.mu.Lock()
	defer a.mu.Unlock()
	if cached := a.cached[window]; !refresh && !cached.at.IsZero() && time.Since(cached.at) < cacheTTL {
		out := cached.summary
		out.Cached = true
		return out
	}
	out := a.analyzeLocked(window)
	a.cached[window] = cacheEntry{summary: out, at: time.Now()}
	return out
}
func (a *Analyzer) Host(name string, window time.Duration, refresh bool) (HostResult, bool) {
	for _, h := range a.Summary(window, refresh).Hosts {
		if h.Node == name {
			return h, true
		}
	}
	return HostResult{}, false
}
func (a *Analyzer) analyzeLocked(window time.Duration) Summary {
	nodes := a.nodes.ListHostNodes()
	if len(nodes) > maxNodes {
		nodes = nodes[:maxNodes]
	}
	active := map[string][]model.AlertEvent{}
	if a.alerts != nil {
		for _, item := range a.alerts.Active() {
			active[item.Node] = append(active[item.Node], item)
		}
	}
	baselines := map[string]model.SecurityBaseline{}
	securityEvents := []model.SecurityEvent(nil)
	if a.security != nil {
		for _, item := range a.security.Baselines() {
			baselines[item.Node] = item
		}
		securityEvents = a.security.Events(200, "", "")
	}
	middleware := a.middlewareRefs()
	probes := a.probeRefs()
	out := Summary{GeneratedAt: time.Now().UnixMilli(), Status: "ok", WindowHours: int(window.Hours()), NodeCount: len(nodes), Hosts: make([]HostResult, 0, len(nodes))}
	end := time.Now()
	start := end.Add(-window)
	for _, n := range nodes {
		h := a.analyzeHost(n, start, end, windowStep(window), active[n.Hostname], baselines[n.Hostname])
		h.Correlations = correlateHost(h, middleware, probes, securityEvents)
		out.Hosts = append(out.Hosts, h)
		if h.Coverage.Ready {
			out.ReadyNodeCount++
		} else {
			out.InsufficientNodeCount++
		}
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
	sort.SliceStable(out.Hosts, func(i, j int) bool {
		if out.Hosts[i].Score == out.Hosts[j].Score {
			return !out.Hosts[i].Coverage.Ready && out.Hosts[j].Coverage.Ready
		}
		return out.Hosts[i].Score > out.Hosts[j].Score
	})
	return out
}
func (a *Analyzer) analyzeHost(n model.Node, start, end time.Time, step time.Duration, active []model.AlertEvent, securityBaseline model.SecurityBaseline) HostResult {
	h := HostResult{Node: n.Hostname, IP: n.IP, Group: n.Group, Online: n.Status == "online", Severity: SeverityInfo, Status: "ok", Coverage: Coverage{RequiredSamples: minBaselineSamples}}
	if !h.Online {
		h.Evidence = append(h.Evidence, availabilityEvidence())
	}
	for _, metric := range []string{"cpu_usage", "mem_used_percent"} {
		a.collectBaseline(&h, n.Hostname, metric, start, end, step)
	}
	a.collectDisk(&h, n.Hostname, start, end, step)
	for _, item := range active {
		h.Evidence = append(h.Evidence, alertEvidence(item))
	}
	if securityBaseline.Node != "" && securityBaseline.Score < 80 {
		h.Evidence = append(h.Evidence, securityEvidence(securityBaseline))
	}
	h.Coverage = coverageOf(h.Baselines)
	h.Score, h.Severity = ScoreRisk(h.Evidence)
	h.RiskTypes = riskTypes(h.Evidence)
	if !h.Coverage.Ready {
		h.Status = "insufficient_data"
	} else if len(h.Baselines) == 0 && len(h.Forecasts) == 0 {
		h.Status = "no_data"
	}
	return h
}
func (a *Analyzer) middlewareRefs() []MiddlewareRef {
	if a.instances == nil {
		return nil
	}
	refs := make([]MiddlewareRef, 0)
	for _, item := range a.instances.RedisInstances() {
		refs = append(refs, MiddlewareRef{Kind: "Redis", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up, Topology: item.Topology})
	}
	for _, item := range a.instances.MySQLInstances() {
		refs = append(refs, MiddlewareRef{Kind: "MySQL", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up, Topology: item.Topology})
	}
	for _, item := range a.instances.PostgresInstances() {
		refs = append(refs, MiddlewareRef{Kind: "PostgreSQL", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up, Topology: item.Topology})
	}
	for _, item := range a.instances.MongoDBInstances() {
		refs = append(refs, MiddlewareRef{Kind: "MongoDB", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up, Topology: item.Topology})
	}
	for _, item := range a.instances.NginxInstances() {
		refs = append(refs, MiddlewareRef{Kind: "Nginx", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up})
	}
	for _, item := range a.instances.KafkaInstances() {
		refs = append(refs, MiddlewareRef{Kind: "Kafka", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up})
	}
	for _, item := range a.instances.RocketMQInstances() {
		refs = append(refs, MiddlewareRef{Kind: "RocketMQ", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up})
	}
	for _, item := range a.instances.K8sInstances() {
		refs = append(refs, MiddlewareRef{Kind: "Kubernetes", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up})
	}
	for _, item := range a.instances.FastDFSInstances() {
		refs = append(refs, MiddlewareRef{Kind: "FastDFS", Name: instanceName(item.Name, item.Instance), Node: item.Node, Group: item.Group, Up: item.Up})
	}
	return refs
}

func (a *Analyzer) probeRefs() []ProbeRef {
	if a.dialtests == nil {
		return nil
	}
	results := a.dialtests.LastResults()
	refs := make([]ProbeRef, 0, len(results))
	for _, task := range a.dialtests.List() {
		if result, ok := results[task.ID]; ok {
			refs = append(refs, ProbeRef{Name: task.Name, Target: task.Target, Up: result.Up, Error: result.Error})
		}
	}
	return refs
}
func instanceName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func (a *Analyzer) collectBaseline(h *HostResult, nodeName, metric string, start, end time.Time, step time.Duration) {
	series, err := a.store.QueryRange(nodeName, metric, nil, start.UnixMilli(), end.UnixMilli(), step.Milliseconds())
	if err != nil {
		slog.Warn("智能分析查询指标失败", "node", nodeName, "metric", metric, "err", err)
		return
	}
	if len(series) == 0 {
		return
	}
	for _, s := range series {
		b := CalculateBaseline(metric, s.Labels, s.Points)
		h.Baselines = append(h.Baselines, b)
		if b.IsAnomalous {
			h.Evidence = append(h.Evidence, anomalyEvidence(b))
		}
	}
}
func (a *Analyzer) collectDisk(h *HostResult, nodeName string, start, end time.Time, step time.Duration) {
	series, err := a.store.QueryRange(nodeName, "disk_used_percent", nil, start.UnixMilli(), end.UnixMilli(), step.Milliseconds())
	if err != nil {
		slog.Warn("智能分析查询磁盘趋势失败", "node", nodeName, "err", err)
		return
	}
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
