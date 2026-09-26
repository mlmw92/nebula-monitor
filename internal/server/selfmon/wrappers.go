package selfmon

import (
	"context"
	"log/slog"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/storage"
)

// Storage 装饰 storage.Storage，统计读写次数与失败次数。
//
// 用装饰器而不是在各调用点埋点：Server 侧读写时序库的路径较多
// （Agent 上报写入、各查询接口、分析器、报告生成），逐个改容易漏。
// 自监控自身的上报走**未包装**的原始存储，避免把自己也算进去（见 Reporter）。
type Storage struct {
	inner storage.Storage
	mon   *Monitor
}

// NewStorage 包装底层存储。mon 为 nil 时退化为直通。
func NewStorage(inner storage.Storage, mon *Monitor) *Storage {
	return &Storage{inner: inner, mon: mon}
}

// Write 写入样本并计数。
func (s *Storage) Write(metrics []model.Metric) error {
	err := s.inner.Write(metrics)
	if s.mon != nil {
		s.mon.AddWrite(err)
	}
	return err
}

// QueryRange 范围查询并计数。
func (s *Storage) QueryRange(node, name string, labels map[string]string, start, end, step int64) ([]model.Series, error) {
	out, err := s.inner.QueryRange(node, name, labels, start, end, step)
	if s.mon != nil {
		s.mon.AddQuery(err)
	}
	return out, err
}

// QueryLatest 最新点查询并计数。
func (s *Storage) QueryLatest(node, name string, labels map[string]string) (*model.Point, error) {
	out, err := s.inner.QueryLatest(node, name, labels)
	if s.mon != nil {
		s.mon.AddQuery(err)
	}
	return out, err
}

// QueryInstant 即时查询并计数。
func (s *Storage) QueryInstant(node, name string, labels map[string]string) ([]model.Series, error) {
	out, err := s.inner.QueryInstant(node, name, labels)
	if s.mon != nil {
		s.mon.AddQuery(err)
	}
	return out, err
}

// QueryInstantWithLookback 带回溯的即时查询并计数。
func (s *Storage) QueryInstantWithLookback(node, name string, labels map[string]string, lookback time.Duration) ([]model.Series, error) {
	out, err := s.inner.QueryInstantWithLookback(node, name, labels, lookback)
	if s.mon != nil {
		s.mon.AddQuery(err)
	}
	return out, err
}

// QueryAllLatest 跨节点即时查询并计数。
func (s *Storage) QueryAllLatest(name string, labels map[string]string) ([]model.Series, error) {
	out, err := s.inner.QueryAllLatest(name, labels)
	if s.mon != nil {
		s.mon.AddQuery(err)
	}
	return out, err
}

// Close 释放底层存储。
func (s *Storage) Close() error { return s.inner.Close() }

// Backend 返回底层后端名。
func (s *Storage) Backend() string { return s.inner.Backend() }

// Notifier 装饰 alert.Notifier，按渠道统计通知成功与失败。
type Notifier struct {
	inner alert.Notifier
	mon   *Monitor
}

// WrapNotifiers 包装全部通知器；mon 为 nil 时原样返回。
func WrapNotifiers(notifiers []alert.Notifier, mon *Monitor) []alert.Notifier {
	if mon == nil {
		return notifiers
	}
	out := make([]alert.Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		out = append(out, &Notifier{inner: n, mon: mon})
	}
	return out
}

// Notify 单条通知并计数。
func (n *Notifier) Notify(ev model.AlertEvent) error {
	err := n.inner.Notify(ev)
	if n.mon != nil {
		n.mon.AddNotify(n.inner.Channel(), err)
	}
	return err
}

// NotifyGroup 分组通知并计数。
func (n *Notifier) NotifyGroup(events []model.AlertEvent) error {
	err := n.inner.NotifyGroup(events)
	if n.mon != nil {
		n.mon.AddNotify(n.inner.Channel(), err)
	}
	return err
}

// Channel 返回被包装通知器的渠道名。
func (n *Notifier) Channel() string { return n.inner.Channel() }

// DefaultReportInterval 是自监控指标写入时序库的默认周期。
const DefaultReportInterval = 30 * time.Second

// Reporter 周期把自监控快照写入时序库，使 `self_*` 指标可查询、可告警。
type Reporter struct {
	store    storage.Storage
	mon      *Monitor
	interval time.Duration
}

// NewReporter 创建上报器。store 应传**未包装**的原始存储，避免自监控写入自身计数。
func NewReporter(store storage.Storage, mon *Monitor, interval time.Duration) *Reporter {
	if interval <= 0 {
		interval = DefaultReportInterval
	}
	return &Reporter{store: store, mon: mon, interval: interval}
}

// Report 立即上报一次快照。
func (r *Reporter) Report() error {
	if r == nil || r.mon == nil || r.store == nil {
		return nil
	}
	return r.store.Write(r.mon.Metrics(time.Now()))
}

// Run 阻塞运行上报循环，直到 ctx 取消。启动时立即上报一次，便于刚启动即可观测。
func (r *Reporter) Run(ctx context.Context) {
	if r == nil || r.mon == nil || r.store == nil {
		return
	}
	if err := r.Report(); err != nil {
		slog.Warn("自监控首次上报失败", "err", err)
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.Report(); err != nil {
				slog.Warn("自监控上报失败", "err", err)
			}
		}
	}
}
