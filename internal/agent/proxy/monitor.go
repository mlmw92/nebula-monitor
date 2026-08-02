// monitor.go 提供代理模式自监控指标收集。
//
// Edge/Hub 自身的运行指标（连接数、转发数、丢弃数、重连数）通过本结构维护，
// 周期性由 Agent 主循环读取并上报 Server，纳入现有告警体系。
package proxy

import "sync/atomic"

// Metrics 是代理模式的自监控指标。
type Metrics struct {
	ConnActive     atomic.Int64 // 当前活跃隧道连接数
	ForwardTotal   atomic.Int64 // 累计成功转发的请求数
	DroppedTotal   atomic.Int64 // 累计丢弃的请求数（缓冲满或超时）
	ReconnectTotal atomic.Int64 // 累计重连次数
	BufferDepth    atomic.Int64 // 当前缓冲区深度（Edge 断连期间）
}

// NewMetrics 创建指标计数器。
func NewMetrics() *Metrics {
	return &Metrics{}
}

// Snapshot 返回指标快照（用于上报）。
type MetricsSnapshot struct {
	ConnActive     int64 `json:"conn_active"`
	ForwardTotal   int64 `json:"forward_total"`
	DroppedTotal   int64 `json:"dropped_total"`
	ReconnectTotal int64 `json:"reconnect_total"`
	BufferDepth    int64 `json:"buffer_depth"`
}

// Snapshot 读取当前指标快照。
func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ConnActive:     m.ConnActive.Load(),
		ForwardTotal:   m.ForwardTotal.Load(),
		DroppedTotal:   m.DroppedTotal.Load(),
		ReconnectTotal: m.ReconnectTotal.Load(),
		BufferDepth:    m.BufferDepth.Load(),
	}
}
