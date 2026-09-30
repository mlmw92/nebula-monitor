package ops

import (
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// 三个内置动作的实现。
//
// 共同约定：
//   - 只用**固定命令 + 固定参数**，绝不拼 shell（无 sh -c、无管道）——这样"参数注入"
//     在类型层面就不成立，剩下的只需校验单元名这类会被拼进参数的输入；
//   - 输出按分节返回（Data 的键是分节中文名），并逐节截断；
//   - 命令存在与否、是否允许（权限）都要能被区分：`ss` 在新系统上可能不存在，
//     这时应说明"命令不可用"而不是把它当成故障结论。

// diagnosticCommand 是一条诊断命令。
type diagnosticCommand struct {
	// Section 是结果里的分节标题（中文，直接展示给运维）。
	Section string
	Name    string
	Args    []string
	// Trim 为真时只保留前 maxDiagnosticLines 行（ps 这类输出可能非常长）。
	Trim bool
}

// maxDiagnosticLines 是 ps 类命令保留的行数。
const maxDiagnosticLines = 15

// diagnosticCommands 是节点诊断包采集的命令集合。
//
// 刻意覆盖"登机器第一眼看什么"：负载与内存、磁盘、失败的服务、CPU 占用最高的进程、监听端口。
// 全部只读，全部不依赖 shell 语法（管道改用命令自带参数 + Go 侧截断实现）。
var diagnosticCommands = []diagnosticCommand{
	{Section: "系统信息", Name: "uname", Args: []string{"-a"}},
	{Section: "运行时长与负载", Name: "uptime"},
	{Section: "内存(MB)", Name: "free", Args: []string{"-m"}},
	{Section: "磁盘", Name: "df", Args: []string{"-h"}},
	{Section: "CPU/内存占用 TOP", Name: "ps", Args: []string{"-eo", "pid,comm,%cpu,%mem", "--sort=-%cpu"}, Trim: true},
	{Section: "失败的服务单元", Name: "systemctl", Args: []string{"--failed", "--no-pager"}},
	{Section: "监听端口", Name: "ss", Args: []string{"-lntu"}},
}

// nodeDiagnostics 采集只读诊断包。
func (e *Executor) nodeDiagnostics() model.OpsResult {
	data := map[string]string{}
	total := 0
	okCount := 0
	var failed []string

	for _, c := range diagnosticCommands {
		out, err := e.runCmd(c.Name, c.Args...)
		body := out
		if c.Trim {
			body = firstLines(body, maxDiagnosticLines)
		}
		body = strings.TrimSpace(body)
		if err != nil && body == "" {
			// 命令不存在或无权限：如实说明，别让它看起来像"这项没有问题"。
			body = "（命令不可用：" + c.Name + " " + strings.Join(c.Args, " ") + " → " + err.Error() + "）"
			failed = append(failed, c.Section)
		} else {
			okCount++
		}
		if body == "" {
			body = "（无输出）"
		}
		body = truncate(body, maxSectionBytes)
		if total+len(body) > maxTotalBytes {
			data[c.Section] = "（已达回执体积上限，本节省略）"
			continue
		}
		total += len(body)
		data[c.Section] = body
	}

	if okCount == 0 {
		return model.OpsResult{
			State:   model.OpsStateFailed,
			Message: "所有诊断命令都执行失败（可能是 Agent 权限不足或系统缺少基础工具）",
			Data:    data,
		}
	}
	msg := "诊断包已生成，共 " + itoa(len(data)) + " 节"
	if len(failed) > 0 {
		// 部分失败也要说清楚：否则缺失的分节会被读成"那项没问题"。
		msg += "；其中 " + itoa(len(failed)) + " 节命令不可用：" + strings.Join(failed, "、")
	}
	return model.OpsResult{State: model.OpsStateSucceeded, Message: msg, Data: data}
}

// svcStatus 查询 systemd 单元状态（只读）。
func (e *Executor) svcStatus(unit string) model.OpsResult {
	data := map[string]string{}
	// is-active / is-enabled 的退出码就是答案（0=active/enabled，非 0=其它状态），
	// 因此"命令返回非 0"不等于失败——这三点必须分别取原始输出。
	for _, q := range []struct{ Section, Arg string }{
		{"运行状态(is-active)", "is-active"},
		{"开机自启(is-enabled)", "is-enabled"},
	} {
		out, err := e.runCmd("systemctl", q.Arg, unit)
		text := strings.TrimSpace(out)
		if text == "" && err != nil {
			text = "（查询失败：" + err.Error() + "）"
		}
		data[q.Section] = truncate(text, maxSectionBytes)
	}
	// status 对未运行的单元会返回非 0，但输出恰恰是要看的内容——因此不看 err，只看有没有输出。
	out, _ := e.runCmd("systemctl", "status", unit, "--no-pager", "-n", "10")
	if text := strings.TrimSpace(out); text != "" {
		data["最近状态与日志"] = truncate(text, maxSectionBytes)
	}
	return model.OpsResult{State: model.OpsStateSucceeded, Message: "已查询 " + unit, Data: data}
}

// svcRestart 重启 systemd 单元（写操作；调用前已通过本机护栏检查）。
func (e *Executor) svcRestart(unit string) model.OpsResult {
	out, err := e.runCmd("systemctl", "restart", unit)
	data := map[string]string{}
	if text := strings.TrimSpace(out); text != "" {
		data["输出"] = truncate(text, maxSectionBytes)
	}
	if err != nil {
		return model.OpsResult{
			State:   model.OpsStateFailed,
			Message: "重启 " + unit + " 失败：" + err.Error(),
			Data:    data,
		}
	}
	// 重启后立刻确认一次状态：命令成功只说明"提交成功"，不等于服务真的起来了。
	// 这一条是这个动作最有价值的部分——否则操作者还得再发一条查询指令。
	status, _ := e.runCmd("systemctl", "is-active", unit)
	state := strings.TrimSpace(status)
	data["重启后状态"] = state
	msg := "已重启 " + unit
	if state != "active" {
		return model.OpsResult{State: model.OpsStateFailed, Message: msg + "，但当前状态为 " + state, Data: data}
	}
	return model.OpsResult{State: model.OpsStateSucceeded, Message: msg + "，当前状态 active", Data: data}
}

// firstLines 取前 n 行（n<=0 表示全部）。
func firstLines(s string, n int) string {
	if n <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + "\n…（已省略其余 " + itoa(len(lines)-n) + " 行）"
}
