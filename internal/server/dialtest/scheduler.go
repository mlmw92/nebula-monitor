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
	sink    AlertSink        // 告警联动回调（可选，由 alert.Engine 实现）
	upState map[string]bool  // 记录上一轮各任务的 up 状态，用于检测跃迁
}

// NewScheduler 创建调度器。
func NewScheduler(store *Store, st storage.Storage) *Scheduler {
	return &Scheduler{
		store:   store,
		storage: st,
		dialer:  NewDialer(),
		stop:    make(chan struct{}),
		upState: map[string]bool{},
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

		// 告警联动：仅在状态跃迁（正常↔故障）时回调，避免重复刷屏。
		s.mu.Lock()
		prevUp, seen := s.upState[task.ID]
		s.upState[task.ID] = result.Up
		sink := s.sink
		s.mu.Unlock()
		if sink != nil && (!seen || prevUp != result.Up) {
			sink.EmitDialtestAlert(task, result, result.Up)
		}
	}
	if len(allMetrics) > 0 {
		if err := s.storage.Write(allMetrics); err != nil {
			slog.Warn("拨测结果写入失败", "err", err)
		}
	}
}

// AlertSink 接收拨测状态变化（故障/恢复）并联动产生告警事件的回调接口。
// 由上层告警引擎实现，避免 dialtest 包直接依赖 alert 包形成循环引用。
type AlertSink interface {
	// EmitDialtestAlert 在拨测状态发生跃迁时调用：up=false 表示故障触发，up=true 表示恢复。
	EmitDialtestAlert(task Task, result Result, up bool)
}
