// Package selfmon 采集 Server 自身的运行指标。
//
// 设计取舍：
//   - 指标写入现有 TSDB（`self_*` 前缀），而非另造 `/metrics` 端点：自监控指标因此
//     天然可查询、可画趋势，也能直接复用现有告警规则来监控 Server 自己；
//   - 计数经装饰器采集（见 wrappers.go），不改动既有调用点，也不容易漏埋点；
//   - 同一份快照同时供 `/api/v1/self/status` 与 `/healthz`、`/readyz` 使用，
//     避免三处各算一套口径。
package selfmon

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// MetricPrefix 是自监控指标名前缀。
const MetricPrefix = "self_"

// defaultNodeLabel 是取不到主机名时的节点标签兜底。
const defaultNodeLabel = "monitor-server"

// Monitor 收集 Server 自身运行指标。所有计数均为原子操作，可并发调用。
type Monitor struct {
	version   string
	startedAt time.Time
	node      string

	httpRequests atomic.Int64
	httpErrors   atomic.Int64

	writeTotal  atomic.Int64
	writeErrors atomic.Int64
	queryTotal  atomic.Int64
	queryErrors atomic.Int64

	wsStats func() int // WebSocket 连接数（拉取式，由 Hub 维护，避免两处计数不一致）

	evalTotal atomic.Int64
	evalLast  atomic.Int64 // 最后一次评估完成的 Unix 秒；0 表示从未评估

	notifyMu     sync.Mutex
	notifySent   map[string]int64
	notifyFailed map[string]int64

	// 域内统计由外部注入，避免 selfmon 反向依赖 alert / node 包。
	alertStats   func() (firing, suppressed int)
	nodeStats    func() (online, total int)
	evalInterval time.Duration
}

// New 创建自监控收集器。
func New(version string) *Monitor {
	node, err := os.Hostname()
	if err != nil || node == "" {
		node = defaultNodeLabel
	}
	return &Monitor{
		version:      version,
		startedAt:    time.Now(),
		node:         node,
		notifySent:   map[string]int64{},
		notifyFailed: map[string]int64{},
	}
}

// Version 返回本次启动的版本号。
func (m *Monitor) Version() string { return m.version }

// Node 返回自监控指标的节点标签（默认主机名）。
func (m *Monitor) Node() string { return m.node }

// StartedAt 返回进程启动时间。
func (m *Monitor) StartedAt() time.Time { return m.startedAt }

// SetAlertStats 注入告警统计：活跃 firing 数与其中被抑制的数量。
func (m *Monitor) SetAlertStats(f func() (firing, suppressed int)) { m.alertStats = f }

// SetNodeStats 注入节点统计：在线数与总数。
func (m *Monitor) SetNodeStats(f func() (online, total int)) { m.nodeStats = f }

// SetEvalInterval 注入告警评估周期，供 /readyz 判断评估循环是否停摆；0 表示未启用告警。
func (m *Monitor) SetEvalInterval(d time.Duration) { m.evalInterval = d }

// EvalInterval 返回告警评估周期。
func (m *Monitor) EvalInterval() time.Duration { return m.evalInterval }

// AddHTTPRequest 记录一次 HTTP 请求；status >= 400 计入错误数。
func (m *Monitor) AddHTTPRequest(status int) {
	m.httpRequests.Add(1)
	if status >= 400 {
		m.httpErrors.Add(1)
	}
}

// SetWSStats 注入 WebSocket 连接数读取函数（由 Hub 维护真实连接表）。
// 采用拉取式而非自增计数：Hub 已维护 clients 集合，两处各记一份容易不一致。
func (m *Monitor) SetWSStats(f func() int) { m.wsStats = f }

// AddWrite 记录一次时序库写入及其结果。
func (m *Monitor) AddWrite(err error) {
	m.writeTotal.Add(1)
	if err != nil {
		m.writeErrors.Add(1)
	}
}

// AddQuery 记录一次时序库查询及其结果。
func (m *Monitor) AddQuery(err error) {
	m.queryTotal.Add(1)
	if err != nil {
		m.queryErrors.Add(1)
	}
}

// AddNotify 记录一次通知发送，按渠道分别统计。
func (m *Monitor) AddNotify(channel string, err error) {
	if channel == "" {
		channel = "unknown"
	}
	m.notifyMu.Lock()
	defer m.notifyMu.Unlock()
	if err != nil {
		m.notifyFailed[channel]++
		return
	}
	m.notifySent[channel]++
}

// AddEval 记录一轮告警评估完成。
func (m *Monitor) AddEval() {
	m.evalTotal.Add(1)
	m.evalLast.Store(time.Now().Unix())
}

// HTTPStats 是 HTTP 请求统计。
type HTTPStats struct {
	Requests int64 `json:"requests"`
	Errors   int64 `json:"errors"`
}

// TSDBStats 是时序库读写统计。
type TSDBStats struct {
	WriteTotal  int64 `json:"writeTotal"`
	WriteErrors int64 `json:"writeErrors"`
	QueryTotal  int64 `json:"queryTotal"`
	QueryErrors int64 `json:"queryErrors"`
}

// ChannelStats 是单渠道通知统计。
type ChannelStats struct {
	Sent   int64 `json:"sent"`
	Failed int64 `json:"failed"`
}

// NotifyStats 是通知统计。
type NotifyStats struct {
	Sent      int64                   `json:"sent"`
	Failed    int64                   `json:"failed"`
	ByChannel map[string]ChannelStats `json:"byChannel"`
}

// AlertStats 是告警引擎统计。EvalAgeSeconds 为 -1 表示尚未完成首次评估。
type AlertStats struct {
	EvalTotal       int64 `json:"evalTotal"`
	EvalAgeSeconds  int64 `json:"evalAgeSeconds"`
	EvalIntervalSec int64 `json:"evalIntervalSeconds"`
	Firing          int   `json:"firing"`
	Suppressed      int   `json:"suppressed"`
}

// NodeStats 是节点在线统计。
type NodeStats struct {
	Online int `json:"online"`
	Total  int `json:"total"`
}

// Snapshot 是自监控快照。
type Snapshot struct {
	Version        string      `json:"version"`
	Node           string      `json:"node"`
	StartedAt      int64       `json:"startedAt"`
	UptimeSeconds  int64       `json:"uptimeSeconds"`
	CollectedAt    int64       `json:"collectedAt"`
	Goroutines     int         `json:"goroutines"`
	HeapAllocBytes uint64      `json:"heapAllocBytes"`
	GCCount        uint32      `json:"gcCount"`
	HTTP           HTTPStats   `json:"http"`
	TSDB           TSDBStats   `json:"tsdb"`
	Notify         NotifyStats `json:"notify"`
	WSConnections  int64       `json:"wsConnections"`
	Alert          AlertStats  `json:"alert"`
	Nodes          NodeStats   `json:"nodes"`
}

// Snapshot 返回当前自监控快照。nil 接收者返回零值快照，便于调用方免判空。
func (m *Monitor) Snapshot() Snapshot {
	now := time.Now()
	if m == nil {
		return Snapshot{CollectedAt: now.UnixMilli(), Alert: AlertStats{EvalAgeSeconds: -1}}
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	s := Snapshot{
		Version:        m.version,
		Node:           m.node,
		StartedAt:      m.startedAt.UnixMilli(),
		UptimeSeconds:  int64(now.Sub(m.startedAt).Seconds()),
		CollectedAt:    now.UnixMilli(),
		Goroutines:     runtime.NumGoroutine(),
		HeapAllocBytes: ms.HeapAlloc,
		GCCount:        ms.NumGC,
		HTTP: HTTPStats{
			Requests: m.httpRequests.Load(),
			Errors:   m.httpErrors.Load(),
		},
		TSDB: TSDBStats{
			WriteTotal:  m.writeTotal.Load(),
			WriteErrors: m.writeErrors.Load(),
			QueryTotal:  m.queryTotal.Load(),
			QueryErrors: m.queryErrors.Load(),
		},
	}
	if m.wsStats != nil {
		s.WSConnections = int64(m.wsStats())
	}
	s.Alert.EvalTotal = m.evalTotal.Load()
	s.Alert.EvalIntervalSec = int64(m.evalInterval / time.Second)
	s.Alert.EvalAgeSeconds = -1
	if last := m.evalLast.Load(); last > 0 {
		s.Alert.EvalAgeSeconds = now.Unix() - last
	}
	if m.alertStats != nil {
		s.Alert.Firing, s.Alert.Suppressed = m.alertStats()
	}
	if m.nodeStats != nil {
		s.Nodes.Online, s.Nodes.Total = m.nodeStats()
	}

	m.notifyMu.Lock()
	s.Notify.ByChannel = make(map[string]ChannelStats, len(m.notifySent)+len(m.notifyFailed))
	for ch, n := range m.notifySent {
		c := s.Notify.ByChannel[ch]
		c.Sent = n
		s.Notify.ByChannel[ch] = c
		s.Notify.Sent += n
	}
	for ch, n := range m.notifyFailed {
		c := s.Notify.ByChannel[ch]
		c.Failed = n
		s.Notify.ByChannel[ch] = c
		s.Notify.Failed += n
	}
	m.notifyMu.Unlock()

	return s
}

// Metrics 把快照转换为可写入时序库的指标集合（节点标签为主机名，附加 kind=server）。
func (m *Monitor) Metrics(now time.Time) []model.Metric {
	s := m.Snapshot()
	ts := now.UnixMilli()

	values := []struct {
		name  string
		value float64
	}{
		{"self_uptime_seconds", float64(s.UptimeSeconds)},
		{"self_goroutines", float64(s.Goroutines)},
		{"self_heap_alloc_bytes", float64(s.HeapAllocBytes)},
		{"self_gc_total", float64(s.GCCount)},
		{"self_http_requests_total", float64(s.HTTP.Requests)},
		{"self_http_errors_total", float64(s.HTTP.Errors)},
		{"self_tsdb_write_total", float64(s.TSDB.WriteTotal)},
		{"self_tsdb_write_errors_total", float64(s.TSDB.WriteErrors)},
		{"self_tsdb_query_total", float64(s.TSDB.QueryTotal)},
		{"self_tsdb_query_errors_total", float64(s.TSDB.QueryErrors)},
		{"self_notify_sent_total", float64(s.Notify.Sent)},
		{"self_notify_failed_total", float64(s.Notify.Failed)},
		{"self_ws_connections", float64(s.WSConnections)},
		{"self_alert_eval_total", float64(s.Alert.EvalTotal)},
		{"self_alert_eval_age_seconds", float64(s.Alert.EvalAgeSeconds)},
		{"self_alert_firing", float64(s.Alert.Firing)},
		{"self_alert_suppressed", float64(s.Alert.Suppressed)},
		{"self_nodes_online", float64(s.Nodes.Online)},
		{"self_nodes_total", float64(s.Nodes.Total)},
	}

	out := make([]model.Metric, 0, len(values)+2*len(s.Notify.ByChannel))
	for _, v := range values {
		out = append(out, model.Metric{
			Node:      s.Node,
			Name:      v.name,
			Labels:    map[string]string{"kind": "server"},
			Value:     v.value,
			Timestamp: ts,
		})
	}
	// 渠道维度（低基数：渠道种类有限），便于定位「只有某个渠道发不出去」。
	for ch, c := range s.Notify.ByChannel {
		labels := map[string]string{"kind": "server", "channel": ch}
		out = append(out,
			model.Metric{Node: s.Node, Name: "self_notify_sent_channel_total", Labels: labels, Value: float64(c.Sent), Timestamp: ts},
			model.Metric{Node: s.Node, Name: "self_notify_failed_channel_total", Labels: labels, Value: float64(c.Failed), Timestamp: ts},
		)
	}
	return out
}
