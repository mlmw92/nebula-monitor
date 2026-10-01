// Package ops 实现「统一下行操作通道」：平台下发一条白名单内的动作 → 指定节点执行 → 回执与审计。
//
// 它与既有 defense（fail2ban 防护指令）的关系是**泛化而非替代**：那条链路证明了
// 「响应体搭车 + 能力协商 + 回执 + 过期」这一形态可行，本包把这套骨架抽出来复用，
// 让新增一个运维动作只需要在 catalog.go 里加一行 + 在 Agent 侧实现同名动作。
//
// 四道护栏（缺一不可，对应设计件 §下行操作通道）：
//  1. **本机护栏**：Agent `agent.yaml` 的 `guards.ops` 开关，默认只读、写动作需显式开启并列出允许的单元。
//     动机：Agent 以 root 运行，Web 上的一个写权限 ≈ 一批机器的 root，因此"机器自身的同意优先于中心的授权"。
//  2. **能力协商**：只把动作下发给**声明支持该动作**的 Agent（`Capabilities.Ops`），旧 Agent 自动不受影响。
//  3. **中心授权 + 参数校验 + 审计**：创建任务需 `ops:exec`（高风险权限），参数按本文件的目录规格校验，
//     每次下发写审计。
//  4. **默认只读**：首批只放只读动作；写动作（重启服务）需同时满足本机护栏与允许清单。
package ops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// 权限点：看与做分开。执行属高风险——即使是只读查询，它也是"在成百上千台机器上执行东西"的能力。
const (
	// PermRead 查看动作目录与任务状态。
	PermRead = "ops:read"
	// PermExec 创建（下发）操作任务，属高风险权限。
	PermExec = "ops:exec"
)

// 内置动作标识。字面量定义在 internal/model（Agent 侧分派用的是同一批，不能各写一份）。
const (
	// KindNodeDiagnostics 采集节点诊断包（只读）。
	KindNodeDiagnostics = model.OpsKindNodeDiagnostics
	// KindSvcStatus 查询 systemd 单元状态（只读）。
	KindSvcStatus = model.OpsKindSvcStatus
	// KindSvcRestart 重启 systemd 单元（**写**，默认被本机护栏挡下）。
	KindSvcRestart = model.OpsKindSvcRestart
)

// unitPattern 是 systemd 单元名的白名单字符集（与 Agent 侧共用同一个正则）。
var unitPattern = model.OpsUnitPattern

// Param 是一个动作参数的规格。
type Param struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Required bool   `json:"required"`
	// Pattern 是完整匹配（锚定两端）的正则；空表示不限制。
	Pattern string `json:"pattern,omitempty"`
	Example string `json:"example,omitempty"`
	Desc    string `json:"desc,omitempty"`

	re *regexp.Regexp
}

// Action 是一个受支持的下行动作。
type Action struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Group 是界面上的归类（节点 / 服务 / …）。
	Group string `json:"group"`
	Desc  string `json:"desc"`
	// ReadOnly 为 true 表示该动作不改变目标机器状态；为 false 的动作需要 Agent 本机护栏显式放行。
	ReadOnly bool    `json:"readOnly"`
	Params   []Param `json:"params,omitempty"`
	// NeedAgent 是 Agent 侧需要实现的动作版本（仅用于界面提示，不参与校验）。
	NeedAgent string `json:"needAgent,omitempty"`
}

// catalog 是动作白名单。**新增动作只改这里 + Agent 侧实现**，服务端校验/界面/审计自动跟随。
var catalog = []Action{
	{
		Kind: KindNodeDiagnostics, Title: "节点诊断包", Group: "节点", ReadOnly: true,
		Desc: "一次性拉取该节点的只读诊断信息（负载、内存、磁盘、失败的服务单元、监听端口），用于排障时避免反复让运维登机器",
	},
	{
		Kind: KindSvcStatus, Title: "查询服务状态", Group: "服务", ReadOnly: true,
		Desc: "查询某个 systemd 单元的 active/enabled 状态与最近日志（只读）",
		Params: []Param{
			{Name: "unit", Title: "服务单元", Required: true, Pattern: unitPattern.String(),
				Example: "nginx.service", Desc: "systemd 单元名，需以 .service 结尾"},
		},
	},
	{
		Kind: KindSvcRestart, Title: "重启服务（写操作）", Group: "服务", ReadOnly: false,
		Desc: "重启某个 systemd 单元。默认不可用：需要目标机器在 agent.yaml 的 guards.ops 里显式开启写操作并列出该单元",
		Params: []Param{
			{Name: "unit", Title: "服务单元", Required: true, Pattern: unitPattern.String(),
				Example: "nginx.service", Desc: "systemd 单元名，需以 .service 结尾，且必须在目标机器的允许清单里"},
		},
	},
}

func init() {
	// 正则编译一次：校验路径在每次下发时都会走，重复编译没有意义；
	// 编译失败必须在启动时炸掉（写错的 Pattern 会让整类动作静默不可用）。
	for i := range catalog {
		for j := range catalog[i].Params {
			p := &catalog[i].Params[j]
			if p.Pattern == "" {
				continue
			}
			re, err := regexp.Compile("^(?:" + p.Pattern + ")$")
			if err != nil {
				panic(fmt.Sprintf("ops: 动作 %s 的参数 %s 正则编译失败: %v", catalog[i].Kind, p.Name, err))
			}
			p.re = re
		}
	}
}

// groupOrder 是界面上分组的固定顺序。
//
// 不按字符串排序：中文按字节序排出来的顺序是随机的（"服务"恰好排在"节点"前面纯属字节巧合），
// 用户看到的分组顺序会因为新增一个分组而整体变化。这里显式声明，未列出的分组排在最后。
var groupOrder = []string{"节点", "服务"}

func groupRank(g string) int {
	for i, name := range groupOrder {
		if name == g {
			return i
		}
	}
	return len(groupOrder)
}

// Catalog 返回动作目录（稳定排序：先按声明的分组顺序、组内只读优先、最后按 Kind）。
func Catalog() []Action {
	out := make([]Action, len(catalog))
	copy(out, catalog)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := groupRank(out[i].Group), groupRank(out[j].Group)
		if ri != rj {
			return ri < rj
		}
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		if out[i].ReadOnly != out[j].ReadOnly {
			return out[i].ReadOnly
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Lookup 按 Kind 查动作。
func Lookup(kind string) (Action, bool) {
	for _, a := range catalog {
		if a.Kind == kind {
			return a, true
		}
	}
	return Action{}, false
}

// Kinds 返回全部动作标识（供界面与测试使用）。
func Kinds() []string {
	out := make([]string, 0, len(catalog))
	for _, a := range catalog {
		out = append(out, a.Kind)
	}
	sort.Strings(out)
	return out
}

// Validate 校验动作与参数，返回归一化后的参数（第三道护栏的实体部分）。
//
// 三种都是"早失败"：未知动作、未知参数、参数不合法。放任它们过去，症状是 Agent 侧
// 静默不执行或执行出意外的东西，而创建者看到的是"任务已创建，等回执"。
func Validate(kind string, params map[string]string) (Action, map[string]string, error) {
	action, ok := Lookup(strings.TrimSpace(kind))
	if !ok {
		return Action{}, nil, fmt.Errorf("未知动作 %q（可选：%s）", kind, strings.Join(Kinds(), " / "))
	}
	// 未知参数一律拒绝：参数名写错时若被忽略，用户以为传了、Agent 却按默认行为执行。
	for name := range params {
		if !action.hasParam(name) {
			return Action{}, nil, fmt.Errorf("动作 %s 不支持参数 %q", action.Kind, name)
		}
	}
	out := map[string]string{}
	for _, spec := range action.Params {
		value := strings.TrimSpace(params[spec.Name])
		if value == "" {
			if spec.Required {
				title := spec.Title
				if title == "" {
					title = spec.Name
				}
				return Action{}, nil, fmt.Errorf("动作 %s 缺少必填参数「%s」", action.Kind, title)
			}
			continue
		}
		if spec.re != nil && !spec.re.MatchString(value) {
			return Action{}, nil, fmt.Errorf("参数「%s」取值 %q 不合法（示例：%s）", spec.Title, value, spec.Example)
		}
		out[spec.Name] = value
	}
	return action, out, nil
}

func (a Action) hasParam(name string) bool {
	for _, p := range a.Params {
		if p.Name == name {
			return true
		}
	}
	return false
}
