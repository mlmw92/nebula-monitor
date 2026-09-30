// Package ops 是下行操作通道在 **Agent 侧**的执行器。
//
// 与 Server 侧的 internal/server/ops 是一对：那边定义"平台能下发什么"，这边决定
// "这台机器愿意执行什么"。两边的校验都是必要的——Server 校验保证下发的东西是干净的，
// Agent 校验保证即使中心说错了话（版本不匹配、被绕过），本机也不会执行意外的东西。
//
// 本机护栏（guards.ops，见 agent.yaml）是这套设计最关键的一道：
//   - 只读动作默认放行（readOnly 可显式关掉）；
//   - 写动作必须 **同时** 满足 write=true 且该单元在 units 清单里——两个条件缺一不放行，
//     避免"开了一个总开关就放开了所有服务"。
package ops

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// defaultTimeout 是单个动作的执行超时。
//
// 必须有超时：诊断类命令（df/ss/systemctl status）在磁盘卡死或 D-Bus 无响应时会一直挂着，
// 而 Agent 的采集循环不能被一条指令拖住——它是整台机器唯一的采集通道。
const defaultTimeout = 30 * time.Second

// maxSectionBytes 是单个输出分节的大小上限。
//
// 回执要搭在下一轮上报的请求体里送回，而 Server 对 /report 有请求体上限；
// 一条 `ps` 在全负载机器上能有几兆输出，不截断会把整轮上报撑爆（表现为"这台机器突然不上报了"）。
const maxSectionBytes = 16 << 10

// maxTotalBytes 是所有分节的合计上限。
const maxTotalBytes = 64 << 10

// Executor 串行执行下发的动作，并对同一任务 ID 幂等。
type Executor struct {
	node    string
	guards  config.OpsGuards
	timeout time.Duration
	// statePath 是执行记录的落盘路径（任务 ID → 结果）：Agent 重启或同一条指令被重复投递时，
	// 不重复执行——"重启服务"这种动作重复执行的后果不是多一次，而是把刚起来的服务再打一次。
	statePath string

	mu       sync.Mutex
	executed map[string]model.OpsResult
}

// New 创建执行器。statePath 为空时不落盘（仅内存幂等，用于测试或极简部署）。
func New(node string, guards config.OpsGuards, statePath string) *Executor {
	e := &Executor{
		node:      node,
		guards:    guards,
		timeout:   defaultTimeout,
		statePath: statePath,
		executed:  map[string]model.OpsResult{},
	}
	e.loadState()
	return e
}

// SetTimeout 覆盖执行超时（测试用）。
func (e *Executor) SetTimeout(d time.Duration) {
	if d > 0 {
		e.timeout = d
	}
}

// Guards 返回当前护栏（供状态展示）。
func (e *Executor) Guards() config.OpsGuards { return e.guards }

// Supported 返回本机放行的动作清单，用于上报 Capabilities.Ops。
//
// 它同时表达两件事：这个 Agent 版本认识哪些动作，以及**这台机器是否同意**执行它们。
// 因此写动作只有在护栏真正放行时才出现在清单里——Server 也就不会下发它。
func (e *Executor) Supported() []string {
	out := []string{}
	if e.guards.OpsReadOnlyEnabled() {
		out = append(out, model.OpsKindNodeDiagnostics, model.OpsKindSvcStatus)
	}
	if e.opWriteAllowed("") {
		// 只有存在允许清单时才声明写动作能力（units 为空时 opWriteAllowed 为假）
		out = append(out, model.OpsKindSvcRestart)
	}
	return out
}

// Execute 执行一条指令并返回回执。**永不返回未捕获的 panic/错误**：
// 执行失败必须以 failed 回执的形式回到 Server，否则任务会在界面上一直停在 running。
func (e *Executor) Execute(cmd model.OpsCommand) model.OpsResult {
	e.mu.Lock()
	if prev, ok := e.executed[cmd.ID]; ok {
		e.mu.Unlock()
		slog.Info("该操作任务已执行过，跳过重复执行", "id", cmd.ID, "kind", cmd.Kind)
		return prev
	}
	e.mu.Unlock()

	start := time.Now()
	res := e.run(cmd)
	res.CommandID = cmd.ID
	res.At = time.Now().UnixMilli()
	res.DurationMs = time.Since(start).Milliseconds()

	e.mu.Lock()
	e.executed[cmd.ID] = res
	e.mu.Unlock()
	e.saveState()

	slog.Info("操作任务执行完成", "id", cmd.ID, "kind", cmd.Kind, "state", res.State, "ms", res.DurationMs)
	return res
}

// run 按 kind 分派并套上护栏判定。
func (e *Executor) run(cmd model.OpsCommand) model.OpsResult {
	fail := func(msg string) model.OpsResult {
		return model.OpsResult{State: model.OpsStateFailed, Message: msg}
	}

	switch cmd.Kind {
	case model.OpsKindNodeDiagnostics:
		if !e.guards.OpsReadOnlyEnabled() {
			return fail("本机护栏未放行只读操作（agent.yaml 的 guards.ops.readOnly=false）")
		}
		return e.nodeDiagnostics()
	case model.OpsKindSvcStatus:
		if !e.guards.OpsReadOnlyEnabled() {
			return fail("本机护栏未放行只读操作（agent.yaml 的 guards.ops.readOnly=false）")
		}
		unit := cmd.Params["unit"]
		if !model.OpsUnitPattern.MatchString(unit) {
			return fail("单元名不合法：" + unit)
		}
		return e.svcStatus(unit)
	case model.OpsKindSvcRestart:
		unit := cmd.Params["unit"]
		if !model.OpsUnitPattern.MatchString(unit) {
			return fail("单元名不合法：" + unit)
		}
		if !e.guards.Write {
			return fail("本机护栏未放行写操作（需在 agent.yaml 的 guards.ops 中设置 write: true）")
		}
		if !e.guards.OpsAllowedUnits()[unit] {
			return fail("本机护栏未把 " + unit + " 列入 guards.ops.units，拒绝执行重启")
		}
		return e.svcRestart(unit)
	default:
		// 旧 Server 下发了本 Agent 不认识的动作：明确回绝，不要静默忽略。
		return fail("本机不支持该动作：" + cmd.Kind)
	}
}

// opWriteAllowed 判断是否声明/放行写动作（unit 为空表示"是否有可能放行任何写动作"）。
func (e *Executor) opWriteAllowed(unit string) bool {
	if !e.guards.Write {
		return false
	}
	allowed := e.guards.OpsAllowedUnits()
	if len(allowed) == 0 {
		return false
	}
	if unit == "" {
		return true
	}
	return allowed[unit]
}

// ---- 执行记录的落盘（幂等）----

func (e *Executor) loadState() {
	if e.statePath == "" {
		return
	}
	data, err := os.ReadFile(e.statePath)
	if err != nil {
		return
	}
	var snapshot map[string]model.OpsResult
	if err := json.Unmarshal(data, &snapshot); err != nil {
		slog.Warn("操作执行记录解析失败，忽略旧数据", "path", e.statePath, "err", err)
		return
	}
	e.mu.Lock()
	e.executed = snapshot
	e.mu.Unlock()
}

func (e *Executor) saveState() {
	if e.statePath == "" {
		return
	}
	e.mu.Lock()
	snapshot := make(map[string]model.OpsResult, len(e.executed))
	for id, res := range e.executed {
		snapshot[id] = res
	}
	e.mu.Unlock()

	// 只保留最近 200 条：执行记录是"防止重复执行"的手段，不是审计（审计在 Server 侧），
	// 无上限增长会让这个文件变成一个新的运维债。
	if len(snapshot) > 200 {
		snapshot = trimOldest(snapshot, 200)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	tmp := e.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("操作执行记录写入失败", "err", err)
		return
	}
	if err := os.Rename(tmp, e.statePath); err != nil {
		slog.Warn("操作执行记录落盘失败", "err", err)
	}
}

func trimOldest(in map[string]model.OpsResult, keep int) map[string]model.OpsResult {
	type item struct {
		id string
		at int64
	}
	items := make([]item, 0, len(in))
	for id, res := range in {
		items = append(items, item{id: id, at: res.At})
	}
	// 简单选择排序：条数上限只有几百，不值得引入排序依赖到执行热路径
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].at < items[i].at {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	out := make(map[string]model.OpsResult, keep)
	for _, it := range items[len(items)-keep:] {
		out[it.id] = in[it.id]
	}
	return out
}

// runCmd 执行一条命令并返回合并输出，附带超时。
//
// 统一封装的理由：此前 defense 包里散落着十几处 exec.Command（无 ctx、无超时），
// 一条卡住的 systemctl 就能把 Agent 的整个执行流程挂住。
func (e *Executor) runCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

// truncate 截断分节输出并显式标注——静默截断会让人以为"输出就这么多"。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "\n…（输出超过 " + itoa(limit) + " 字节，已截断）"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
