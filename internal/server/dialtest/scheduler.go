package dialtest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

// Scheduler 定时拨测调度器。
type Scheduler struct {
	store   *Store
	storage storage.Storage
	dialer  *Dialer
	mu      sync.Mutex
	stop    chan struct{}
	sink    AlertSink       // 告警联动回调（可选，由 alert.Engine 实现）
	upState map[string]bool // 记录上一轮各任务的 up 状态（兼容保留）

	// 故障确认防抖：连续失败达到阈值才触发故障告警，避免单次网络抖动产生
	// “故障→恢复”邮件对。firedDown 标记已发出故障，需在恢复时清除。
	failCount map[string]int
	firedDown map[string]bool
}

// NewScheduler 创建调度器。
func NewScheduler(store *Store, st storage.Storage) *Scheduler {
	return &Scheduler{
		store:     store,
		storage:   st,
		dialer:    NewDialer(),
		stop:      make(chan struct{}),
		upState:   map[string]bool{},
		failCount: map[string]int{},
		firedDown: map[string]bool{},
	}
}

// SetSink 设置告警联动回调。拨测状态发生跃迁（正常↔故障）时通知上层产生告警事件。
func (s *Scheduler) SetSink(sink AlertSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// Start 启动调度循环。
func (s *Scheduler) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		s.runOnce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-ticker.C:
				s.runOnce()
			}
		}
	}()
}

// Stop 停止调度。
func (s *Scheduler) Stop() {
	close(s.stop)
}

// runOnce 执行一轮拨测。
func (s *Scheduler) runOnce() {
	tasks := s.store.List()
	if len(tasks) == 0 {
		return
	}
	now := model.NowMillis()
	var allMetrics []model.Metric
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		result := s.dialer.Run(task)
		metrics := ResultToMetrics(result, task, now)
		allMetrics = append(allMetrics, metrics...)
		s.store.RecordResult(result)
		slog.Debug("拨测完成", "task", task.Name, "up", result.Up, "latency", result.Latency, "err", result.Error)

		// 告警联动：基于连续失败次数做防抖，单次抖动不会触发“故障/恢复”邮件对。
		s.mu.Lock()
		s.upState[task.ID] = result.Up
		sink := s.sink
		s.mu.Unlock()
		if sink != nil {
			s.evaluateAlert(task, result, sink)
			// HTTPS 任务：SSL 证书过期检测（仅当成功解析到对端证书时）。
			if task.Type == TaskTypeHTTPS && result.CertNotAfter > 0 {
				sink.EmitCertAlert(task, result)
			}
		}
	}
	if len(allMetrics) > 0 {
		if err := s.storage.Write(allMetrics); err != nil {
			slog.Warn("拨测结果写入失败", "err", err)
		}
	}
}

// evaluateAlert 基于连续失败次数决定是否触发故障/恢复告警，抑制单次网络抖动产生的误报。
//
// 规则：
//   - 连续失败达到阈值（task.FailThreshold，≤0 时默认 3）才触发一次“故障”告警；
//   - 故障触发后，下一次成功探测即触发“恢复”告警，并清除计数（恢复应即时通知）；
//   - 未达到阈值的孤立失败/成功不触发任何通知，避免“故障→恢复”邮件刷屏。
func (s *Scheduler) evaluateAlert(task Task, result Result, sink AlertSink) {
	threshold := task.FailThreshold
	if threshold <= 0 {
		threshold = 3
	}

	if !result.Up {
		s.mu.Lock()
		s.failCount[task.ID]++
		fc := s.failCount[task.ID]
		fired := s.firedDown[task.ID]
		s.mu.Unlock()
		if fc >= threshold && !fired {
			s.mu.Lock()
			s.firedDown[task.ID] = true
			s.mu.Unlock()
			slog.Info("拨测连续失败达到阈值，触发故障告警", "task", task.Name, "failCount", fc, "threshold", threshold)
			sink.EmitDialtestAlert(task, result, false)
		}
		return
	}

	// 探测成功：重置连续失败计数；若此前已触发故障则发出恢复告警。
	s.mu.Lock()
	s.failCount[task.ID] = 0
	wasFired := s.firedDown[task.ID]
	s.firedDown[task.ID] = false
	s.mu.Unlock()
	if wasFired {
		sink.EmitDialtestAlert(task, result, true)
	}
}

// AlertSink 接收拨测状态变化（故障/恢复）并联动产生告警事件的回调接口。
// 由上层告警引擎实现，避免 dialtest 包直接依赖 alert 包形成循环引用。
type AlertSink interface {
	// EmitDialtestAlert 在拨测状态发生跃迁时调用：up=false 表示故障触发，up=true 表示恢复。
	EmitDialtestAlert(task Task, result Result, up bool)
	// EmitCertAlert 在检测到 HTTPS 任务 SSL 证书剩余天数低于阈值时调用（证书过期预警）。
	EmitCertAlert(task Task, result Result)
}
