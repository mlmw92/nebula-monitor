package dialtest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

const defaultIntervalSeconds = 60

// Scheduler 定时拨测调度器。
type Scheduler struct {
	store    *Store
	storage  storage.Storage
	dialer   *Dialer
	run      func(Task) Result // 可替换的执行器，便于调度测试
	mu       sync.Mutex
	stop     chan struct{}
	stopOnce sync.Once
	sink     AlertSink       // 告警联动回调（可选，由 alert.Engine 实现）
	upState  map[string]bool // 记录上一轮各任务的 up 状态（兼容保留）

	// 每个任务独立维护下次执行时间；ticker 只负责唤醒调度器，不决定拨测频率。
	nextRun  map[string]time.Time
	interval map[string]time.Duration
	known    map[string]Task

	// 故障确认防抖：连续失败达到阈值才触发故障告警，避免单次网络抖动产生
	// “故障→恢复”邮件对。firedDown 标记已发出故障，需在恢复时清除。
	failCount map[string]int
	firedDown map[string]bool
}

// NewScheduler 创建调度器。
func NewScheduler(store *Store, st storage.Storage) *Scheduler {
	dialer := NewDialer()
	return &Scheduler{
		store:     store,
		storage:   st,
		dialer:    dialer,
		run:       dialer.Run,
		stop:      make(chan struct{}),
		upState:   map[string]bool{},
		nextRun:   map[string]time.Time{},
		interval:  map[string]time.Duration{},
		known:     map[string]Task{},
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
		// 一秒只作为唤醒粒度；实际执行时间由每个任务的 interval 决定。
		ticker := time.NewTicker(time.Second)
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
	s.stopOnce.Do(func() { close(s.stop) })
}

// runOnce 执行到期任务。
func (s *Scheduler) runOnce() {
	s.runDue(time.Now())
}

// runDue 执行指定时刻到期的任务。时间参数独立出来，便于验证 interval 调度行为。
func (s *Scheduler) runDue(now time.Time) {
	tasks := s.store.List()
	s.reconcileTasks(tasks)

	s.mu.Lock()
	current := make(map[string]bool, len(tasks))
	due := make([]Task, 0, len(tasks))
	for _, task := range tasks {
		current[task.ID] = true
		if !task.Enabled {
			s.clearTaskStateLocked(task.ID)
			s.known[task.ID] = task
			continue
		}
		interval := taskInterval(task)
		oldInterval, hadInterval := s.interval[task.ID]
		next, hadNext := s.nextRun[task.ID]
		// 新任务或 interval 被修改时立即执行一次，随后按新 interval 调度。
		if !hadInterval || oldInterval != interval || !hadNext || !now.Before(next) {
			due = append(due, task)
			s.nextRun[task.ID] = now.Add(interval)
		}
		s.interval[task.ID] = interval
		s.known[task.ID] = task
	}
	for id := range s.known {
		if current[id] {
			continue
		}
		s.clearTaskStateLocked(id)
		delete(s.known, id)
	}
	s.mu.Unlock()

	if len(due) == 0 {
		return
	}
	nowMillis := now.UnixMilli()
	var allMetrics []model.Metric
	for _, task := range due {
		result := s.run(task)
		metrics := ResultToMetrics(result, task, nowMillis)
		allMetrics = append(allMetrics, metrics...)
		s.store.RecordResult(result)
		slog.Debug("拨测完成", "task", task.Name, "up", result.Up, "latency", result.Latency, "err", result.Error)

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
	if len(allMetrics) > 0 && s.storage != nil {
		if err := s.storage.Write(allMetrics); err != nil {
			slog.Warn("拨测结果写入失败", "err", err)
		}
	}
}

// reconcileTasks 将任务生命周期同步给告警引擎，关闭删除/禁用任务遗留的活跃告警。
func (s *Scheduler) reconcileTasks(tasks []Task) {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	if reconciler, ok := sink.(TaskReconciler); ok {
		reconciler.ReconcileDialtestTasks(tasks)
	}
}

func (s *Scheduler) clearTaskStateLocked(id string) {
	delete(s.nextRun, id)
	delete(s.interval, id)
	delete(s.upState, id)
	delete(s.failCount, id)
	delete(s.firedDown, id)
}

func taskInterval(task Task) time.Duration {
	seconds := task.Interval
	if seconds <= 0 {
		seconds = defaultIntervalSeconds
	}
	return time.Duration(seconds) * time.Second
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
	} else if reconciler, ok := sink.(DialtestSuccessReconciler); ok {
		// 进程重启后 Scheduler 的内存状态为空，但 Engine 可能从 VM 恢复了旧 firing。
		// 让一次成功探测幂等地清理这类旧告警，避免永久活跃。
		reconciler.ReconcileDialtestSuccess(task, result)
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

// TaskReconciler 是可选的告警生命周期同步接口。
type TaskReconciler interface {
	ReconcileDialtestTasks(tasks []Task)
}

// DialtestSuccessReconciler 是可选的成功结果对账接口。
type DialtestSuccessReconciler interface {
	ReconcileDialtestSuccess(task Task, result Result)
}
