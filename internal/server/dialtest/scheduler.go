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
}

// NewScheduler 创建调度器。
func NewScheduler(store *Store, st storage.Storage) *Scheduler {
	return &Scheduler{
		store:   store,
		storage: st,
		dialer:  NewDialer(),
		stop:    make(chan struct{}),
	}
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
	}
	if len(allMetrics) > 0 {
		if err := s.storage.Write(allMetrics); err != nil {
			slog.Warn("拨测结果写入失败", "err", err)
		}
	}
}
