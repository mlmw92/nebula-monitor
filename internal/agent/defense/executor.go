package defense

import (
	"log/slog"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// Executor 串行执行防护命令，并负责幂等回传。
// 每个 Agent 同时仅执行一个防护任务，避免 apt/dnf/yum 锁冲突与 jail 配置竞争。
type Executor struct {
	mgr *Manager
	mu  sync.Mutex
}

// NewExecutor 创建防护命令执行器。
func NewExecutor() *Executor {
	return &Executor{mgr: NewManager()}
}

// Execute 执行一条防护命令并返回结果回执。
// 对 enable/disable 按命令 ID 幂等：已执行的命令直接复用既有结果，不重复执行。
func (e *Executor) Execute(cmd model.DefenseCommand) model.DefenseCommandResult {
	res := model.DefenseCommandResult{
		CommandID: cmd.ID,
		UpdatedAt: time.Now().UnixMilli(),
	}

	// status 命令每次实时执行，不缓存
	if cmd.Type == model.DefenseActionStatus {
		msg, err := e.mgr.run(cmd)
		if err != nil {
			res.State = model.DefenseStateFailed
			res.Message = err.Error()
		} else {
			res.State = model.DefenseStateSucceeded
			res.Message = msg
		}
		return res
	}

	// enable/disable 幂等：已执行直接返回
	if msg, ok := e.mgr.isExecuted(cmd.ID); ok {
		slog.Info("防护命令已执行过，复用结果", "id", cmd.ID, "type", cmd.Type)
		res.State = model.DefenseStateSucceeded
		res.Message = msg
		return res
	}

	// 串行执行，避免并发包管理/配置竞争
	e.mu.Lock()
	msg, err := e.mgr.run(cmd)
	e.mu.Unlock()

	if err != nil {
		res.State = model.DefenseStateFailed
		res.Message = err.Error()
		slog.Warn("防护命令执行失败", "id", cmd.ID, "type", cmd.Type, "err", err)
	} else {
		res.State = model.DefenseStateSucceeded
		res.Message = msg
		slog.Info("防护命令执行成功", "id", cmd.ID, "type", cmd.Type, "msg", msg)
	}
	// 无论成败均记录，保证幂等（失败命令不会因重复投递而反复执行）
	e.mgr.markExecuted(cmd.ID, res.Message)
	return res
}

// Status 采集当前防护状态（供周期上报）。
func (e *Executor) Status() *model.DefenseStatus {
	return e.mgr.Status()
}
