package report

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 报告周期化调度（全景表 11-4）。
//
// 守三件事：
//  1. 到点才生成（改配置热生效、关闭时不生成）；
//  2. 生成失败**必须可见**（否则"报告怎么不出了"只能靠翻文件时间猜）；
//  3. 上次运行时间跨重启保留（否则频繁重启的机器每次启动都重新生成一份报告）。

// newTestScheduler 造一个"生成必然成功"的调度器（生成函数被替换成假的：
// 调度要守的是到点/失败可见/跨重启，与报告内容无关）。
func newTestScheduler(t *testing.T, initial ScheduleConfig) (*Scheduler, string) {
	t.Helper()
	dir := t.TempDir()
	gen := NewGenerator(nil, nil, nil, filepath.Join(dir, "reports"))
	path := filepath.Join(dir, "report_schedule.yaml")
	s, err := NewScheduler(path, initial, gen)
	if err != nil {
		t.Fatalf("创建调度器失败: %v", err)
	}
	s.generate = func(ReportType) (string, error) { return "rpt-test", nil }
	return s, path
}

// newTestGenerator 造一个只用于"重新构造调度器"的生成器（其 Generate 在这些用例里不会被调用）。
func newTestGenerator(t *testing.T) *Generator {
	t.Helper()
	return NewGenerator(nil, nil, nil, filepath.Join(t.TempDir(), "reports"))
}

// 非法取值被校正而不是拒绝：配置是人在 Web 上填的，一个越界的间隔不该让整个调度失效。
func TestSchedulerNormalizesConfig(t *testing.T) {
	s, _ := newTestScheduler(t, ScheduleConfig{Enabled: true, Type: "hourly", IntervalHours: 99999})
	cfg := s.Config()
	if cfg.Type != string(ReportWeekly) {
		t.Fatalf("未知类型应回落周报：%q", cfg.Type)
	}
	if cfg.IntervalHours != MaxScheduleIntervalHours {
		t.Fatalf("越界间隔应夹紧到 %d：%d", MaxScheduleIntervalHours, cfg.IntervalHours)
	}

	if err := s.Save(ScheduleConfig{Enabled: true, Type: "daily", IntervalHours: 0}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := s.Config().IntervalHours; got != DefaultScheduleIntervalHours {
		t.Fatalf("0 应回落默认间隔：%d", got)
	}
}

// 保存后重新构造调度器（模拟重启）应读到同一份配置。
func TestSchedulerPersistsAcrossRestart(t *testing.T) {
	s, path := newTestScheduler(t, ScheduleConfig{Enabled: false, Type: string(ReportDaily), IntervalHours: 6})
	if err := s.Save(ScheduleConfig{Enabled: true, Type: string(ReportMonthly), IntervalHours: 12}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	again, err := NewScheduler(path, ScheduleConfig{Enabled: false, Type: string(ReportWeekly), IntervalHours: 24}, newTestGenerator(t))
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	cfg := again.Config()
	if !cfg.Enabled || cfg.Type != string(ReportMonthly) || cfg.IntervalHours != 12 {
		t.Fatalf("重启后配置不符：%+v", cfg)
	}
}

// 生成失败要写进结果与状态（而不是只在日志里）。
func TestSchedulerRunNowRecordsFailure(t *testing.T) {
	s, _ := newTestScheduler(t, ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 1})
	s.generate = func(ReportType) (string, error) { return "", errors.New("磁盘只读") }

	run := s.RunNow(true)
	if run.Error == "" {
		t.Fatal("生成失败必须写进结果")
	}
	if !run.Manual {
		t.Fatal("应标记为手动触发")
	}
	st := s.Status()
	if st.Last == nil || st.Last.Error == "" {
		t.Fatalf("状态里应带上次失败原因：%+v", st)
	}
	if st.Config.LastError == "" {
		t.Fatal("失败原因应落盘（跨重启可见）")
	}
}

// 到点判断：未启用不生成；未到间隔不生成；到点生成。
func TestSchedulerTickRespectsInterval(t *testing.T) {
	s, _ := newTestScheduler(t, ScheduleConfig{Enabled: false, Type: string(ReportDaily), IntervalHours: 1})
	now := time.Now()
	s.now = func() time.Time { return now }

	// 未启用：不生成
	s.tick()
	if st := s.Status(); st.Last != nil {
		t.Fatalf("未启用时不应生成：%+v", st.Last)
	}

	// 启用 + 从未运行过：到点（立即生成一次）
	if err := s.Save(ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 1}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.tick()
	first := s.Status()
	if first.Last == nil || first.Last.At == 0 {
		t.Fatalf("启用且从未运行时应生成一次：%+v", first)
	}

	// 刚生成过：未到间隔，不再生成
	before := s.Config().LastRunAt
	s.tick()
	if s.Config().LastRunAt != before {
		t.Fatal("未到间隔不应重复生成")
	}

	// 时间推进超过间隔：再生成一次
	now = now.Add(2 * time.Hour)
	s.now = func() time.Time { return now }
	s.tick()
	if s.Config().LastRunAt == before {
		t.Fatal("超过间隔应再生成一次")
	}
}

// 下次生成时间只在启用时给出：关闭时显示"下次几点"会让人以为它还会跑。
func TestSchedulerStatusNextAt(t *testing.T) {
	s, _ := newTestScheduler(t, ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 6})
	s.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	run := s.RunNow(false)
	if st := s.Status(); st.NextAt != run.At+6*int64(time.Hour/time.Millisecond) {
		t.Fatalf("下次时间应为上次 + 间隔：%+v", st)
	}
	if err := s.Save(ScheduleConfig{Enabled: false, Type: string(ReportDaily), IntervalHours: 6}); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.NextAt != 0 {
		t.Fatalf("关闭时不应给出下次时间：%+v", st)
	}
	// 运行状态不被"保存配置"覆盖（否则界面上"上次运行"会凭空消失）
	if st := s.Status(); st.Last == nil || st.Last.At != run.At {
		t.Fatalf("保存配置不应清掉运行状态：%+v", st.Last)
	}
}

// 运行状态跨重启保留：否则频繁重启的机器每次启动都会重新生成一份报告。
func TestSchedulerRunStateSurvivesRestart(t *testing.T) {
	s, path := newTestScheduler(t, ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 24})
	s.now = func() time.Time { return time.UnixMilli(1_700_000_000_000) }
	run := s.RunNow(false)

	again, err := NewScheduler(path, ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 24}, newTestGenerator(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Config().LastRunAt; got != run.At {
		t.Fatalf("上次运行时间应跨重启保留：%d != %d", got, run.At)
	}
	// 重启后立刻 tick 不应重复生成（还在间隔内）
	again.now = func() time.Time { return time.UnixMilli(run.At).Add(time.Minute) }
	again.tick()
	if again.Config().LastRunAt != run.At {
		t.Fatal("重启后未到间隔不应重复生成")
	}
}

// Run 在 ctx 取消后应退出（后台循环不能变成退出时的泄漏）。
func TestSchedulerRunStopsOnContextCancel(t *testing.T) {
	s, _ := newTestScheduler(t, ScheduleConfig{Enabled: true, Type: string(ReportDaily), IntervalHours: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后调度循环应退出")
	}
}

// nil 安全：未注入调度器时接口层会拿到 nil，不能 panic。
func TestSchedulerNilSafe(t *testing.T) {
	var s *Scheduler
	if cfg := s.Config(); cfg.IntervalHours != 0 {
		t.Fatalf("nil 应返回零值配置：%+v", cfg)
	}
	if st := s.Status(); st.Last != nil {
		t.Fatalf("nil 应返回空状态：%+v", st)
	}
	if err := s.Save(ScheduleConfig{}); err == nil {
		t.Fatal("nil 保存应报错")
	}
	if run := s.RunNow(true); !strings.Contains(run.Error, "未启用") {
		t.Fatalf("nil 运行应给出明确原因：%+v", run)
	}
	s.Run(context.Background()) // 不应 panic
}
