package collector

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"
)

// collectTask 是一轮采集中一个可独立调度、可独立超时的采集任务。
// name 仅用于日志定位；run 接收带超时的子 context，并应把 ctx 传递给其内部
// 所有可取消的外部 I/O（HTTP / DB / dial / exec），使超时后能真正中止在途调用。
type collectTask struct {
	name string
	run  func(ctx context.Context) error
}

// runTasks 并发执行采集任务清单，并为每个任务施加独立超时。
//
// 语义约定：
//   - 并发执行，任一任务的失败或超时只影响该任务自身：记录日志后按「降级为空结果」
//     处理，不返回错误，也不会级联取消其它任务（单个中间件不可用不应影响其它采集）；
//   - 父 ctx 取消（如进程退出）会传播到全部任务；
//   - 任务内 panic 被隔离并记录堆栈，不影响其它任务与主进程；
//   - timeout <= 0 表示不做任务级超时截断，任务仅受父 ctx 约束；
//   - 函数返回时保证全部任务已结束，不遗留 goroutine。
func runTasks(ctx context.Context, timeout time.Duration, tasks []collectTask) {
	var wg sync.WaitGroup
	for _, t := range tasks {
		wg.Add(1)
		go func(t collectTask) {
			defer wg.Done()
			// panic 隔离：必须是最外层 defer，保证 panic 不会终止 agent 进程。
			defer func() {
				if p := recover(); p != nil {
					slog.Error("采集任务 panic 已隔离", "task", t.name, "panic", p, "stack", string(debug.Stack()))
				}
			}()

			taskCtx := ctx
			if timeout > 0 {
				var cancel context.CancelFunc
				taskCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}

			if err := t.run(taskCtx); err != nil {
				slog.Warn("采集任务失败，已降级跳过", "task", t.name, "err", err)
			}
		}(t)
	}
	wg.Wait()
}
