package collector

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// 本组测试覆盖「采集并发调度内核」的通用语义：并发执行、任务级超时、panic 隔离、
// 错误降级不级联、父 ctx 传播、无 goroutine 泄漏。
// 全部使用假任务，不依赖任何真实网络/中间件。

func TestRunTasks_SlowTaskDoesNotBlockOthers(t *testing.T) {
	var fastRan atomic.Bool
	slow := collectTask{name: "slow", run: func(ctx context.Context) error {
		<-ctx.Done() // 模拟卡在 I/O 上，只有 ctx 结束才返回
		return ctx.Err()
	}}
	fast := collectTask{name: "fast", run: func(ctx context.Context) error {
		fastRan.Store(true)
		return nil
	}}

	start := time.Now()
	runTasks(context.Background(), 120*time.Millisecond, []collectTask{slow, fast})
	elapsed := time.Since(start)

	if !fastRan.Load() {
		t.Fatal("快任务未被执行")
	}
	if elapsed < 120*time.Millisecond {
		t.Fatalf("整体耗时 %v 小于任务超时，说明未等待任务结束", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("慢任务未被超时截断，整体耗时 %v", elapsed)
	}
}

func TestRunTasks_TaskPanic_OthersUnaffected(t *testing.T) {
	var othersRan atomic.Int32
	panicTask := collectTask{name: "panic", run: func(ctx context.Context) error {
		panic("boom")
	}}
	var tasks []collectTask
	tasks = append(tasks, panicTask)
	for i := 0; i < 3; i++ {
		tasks = append(tasks, collectTask{name: "ok", run: func(ctx context.Context) error {
			othersRan.Add(1)
			return nil
		}})
	}

	runTasks(context.Background(), time.Second, tasks)

	if got := othersRan.Load(); got != 3 {
		t.Fatalf("panic 影响了其它任务：期望 3 个其它任务执行，实际 %d", got)
	}
}

func TestRunTasks_ErrorDoesNotCascadeCancel(t *testing.T) {
	var slowDone atomic.Bool
	tasks := []collectTask{
		{name: "fail-fast", run: func(ctx context.Context) error {
			return errors.New("采集失败")
		}},
		{name: "slow-but-ok", run: func(ctx context.Context) error {
			select {
			case <-time.After(80 * time.Millisecond):
				slowDone.Store(true)
			case <-ctx.Done():
				// 被级联取消：视为回归
			}
			return nil
		}},
	}

	runTasks(context.Background(), time.Second, tasks)

	if !slowDone.Load() {
		t.Fatal("单个任务失败导致了其它任务被级联取消（应各自降级、互不影响）")
	}
}

func TestRunTasks_TimeoutZeroMeansNoTaskTimeout(t *testing.T) {
	var deadlineSeen atomic.Bool
	task := collectTask{name: "no-timeout", run: func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); ok {
			deadlineSeen.Store(true)
		}
		time.Sleep(30 * time.Millisecond)
		return nil
	}}

	runTasks(context.Background(), 0, []collectTask{task})

	if deadlineSeen.Load() {
		t.Fatal("timeout=0 时不应为任务施加 deadline")
	}
}

func TestRunTasks_ParentCtxCancelStopsTasks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	task := collectTask{name: "waits", run: func(ctx context.Context) error {
		<-ctx.Done()
		canceled.Store(true)
		return ctx.Err()
	}}

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	runTasks(ctx, time.Minute, []collectTask{task})
	elapsed := time.Since(start)

	if !canceled.Load() {
		t.Fatal("父 ctx 取消未传播到任务")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("父 ctx 取消后未及时返回，耗时 %v", elapsed)
	}
}

func TestRunTasks_NoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	var tasks []collectTask
	for i := 0; i < 24; i++ {
		tasks = append(tasks, collectTask{name: "blocking", run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}})
	}
	runTasks(context.Background(), 20*time.Millisecond, tasks)

	// 给运行时的 goroutine 退出留一点调度时间
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseline+2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if after := runtime.NumGoroutine(); after > baseline+2 {
		t.Fatalf("疑似 goroutine 泄漏：基线 %d，结束后 %d", baseline, after)
	}
}

func TestRunTasks_EmptyTaskList(t *testing.T) {
	start := time.Now()
	runTasks(context.Background(), time.Second, nil)
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("空任务清单应立即返回，实际耗时 %v", elapsed)
	}
}
