package ops

import (
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// 本机护栏是这套通道最关键的一道：Agent 以 root 运行，Web 上的一个写权限 ≈ 一批机器的 root。
// 这些用例把"默认只读、写操作必须显式放行"钉死。

func boolPtr(v bool) *bool { return &v }

func newTestExecutor(g config.OpsGuards) *Executor {
	// statePath 留空：测试不落盘（真实部署写在 /var/lib/nebula-monitor/ops/）
	return New("test-node", g, "")
}

// 默认配置（只读允许、写全禁）只能声明两个只读动作。
func TestSupported_DefaultIsReadOnly(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	got := e.Supported()
	if len(got) != 2 || got[0] != model.OpsKindNodeDiagnostics || got[1] != model.OpsKindSvcStatus {
		t.Fatalf("默认应只声明两个只读动作，实际 %v", got)
	}
}

// 写动作要同时满足 write=true 与 units 非空——少任何一个都不声明能力。
func TestSupported_WriteNeedsSwitchAndUnits(t *testing.T) {
	only := newTestExecutor(config.OpsGuards{Write: true})
	if len(only.Supported()) != 2 {
		t.Fatalf("只开 write 但没有 units 时不应声明写动作，实际 %v", only.Supported())
	}
	both := newTestExecutor(config.OpsGuards{Write: true, Units: []string{"nginx"}})
	if len(both.Supported()) != 3 || both.Supported()[2] != model.OpsKindSvcRestart {
		t.Fatalf("write + units 应声明写动作，实际 %v", both.Supported())
	}
}

// 关掉只读护栏后连只读动作也不声明、不执行。
func TestReadOnlyGuardCanBeDisabled(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{ReadOnly: boolPtr(false)})
	if len(e.Supported()) != 0 {
		t.Fatalf("readOnly=false 时不应声明任何动作，实际 %v", e.Supported())
	}
	res := e.Execute(model.OpsCommand{ID: "ops-1", Kind: model.OpsKindNodeDiagnostics})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "只读") {
		t.Fatalf("应明确回绝并说明护栏原因，实际 %+v", res)
	}
}

// 写动作在护栏未放行时必须被拒绝，且拒绝理由要指向具体的配置项。
func TestSvcRestart_BlockedByGuards(t *testing.T) {
	cases := []struct {
		name   string
		guards config.OpsGuards
		wantIn string
	}{
		{"write 未开启", config.OpsGuards{}, "write"},
		{"write 开启但 units 为空", config.OpsGuards{Write: true}, "units"},
		{"单元不在允许清单", config.OpsGuards{Write: true, Units: []string{"redis.service"}}, "units"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestExecutor(tc.guards)
			res := e.Execute(model.OpsCommand{ID: "ops-2", Kind: model.OpsKindSvcRestart,
				Params: map[string]string{"unit": "nginx.service"}})
			if res.State != model.OpsStateFailed {
				t.Fatalf("应被护栏拒绝，实际 %+v", res)
			}
			if !strings.Contains(res.Message, tc.wantIn) {
				t.Fatalf("拒绝理由应提到 %q，实际 %q", tc.wantIn, res.Message)
			}
		})
	}
}

// 单元名不合法的输入在 Agent 侧也要拦（不能假设"中心一定校验过"）。
func TestUnitNameValidatedLocally(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{Write: true, Units: []string{"nginx.service"}})
	for _, bad := range []string{"", "--now", "nginx.timer", "../../etc/passwd", "nginx.service;id"} {
		res := e.Execute(model.OpsCommand{ID: "ops-" + bad, Kind: model.OpsKindSvcRestart,
			Params: map[string]string{"unit": bad}})
		if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "单元名不合法") {
			t.Fatalf("非法单元名 %q 应在本地被拒，实际 %+v", bad, res)
		}
	}
}

// 不认识的动作必须明确回绝（旧 Server / 新动作的组合），而不是静默忽略。
func TestUnknownKindIsRefused(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	res := e.Execute(model.OpsCommand{ID: "ops-3", Kind: "svc.explode"})
	if res.State != model.OpsStateFailed || !strings.Contains(res.Message, "不支持该动作") {
		t.Fatalf("未知动作应被回绝，实际 %+v", res)
	}
}

// 诊断包必须为**每个**分节给出结果：命令不存在时如实说明，而不是留空——
// 否则缺失的分节会被读成"那项没问题"。
func TestNodeDiagnosticsReportsEverySection(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	res := e.Execute(model.OpsCommand{ID: "ops-4", Kind: model.OpsKindNodeDiagnostics})
	if len(res.Data) != len(diagnosticCommands) {
		t.Fatalf("应为 %d 个分节都给出结果，实际 %d：%v", len(diagnosticCommands), len(res.Data), res.Data)
	}
	for _, c := range diagnosticCommands {
		if strings.TrimSpace(res.Data[c.Section]) == "" {
			t.Fatalf("分节 %s 为空（应给出输出或「命令不可用」说明）", c.Section)
		}
	}
	// 命令是否存在取决于平台，因此不断言 succeeded/failed，但必须给出结论与说明
	if res.State != model.OpsStateSucceeded && res.State != model.OpsStateFailed {
		t.Fatalf("必须给出终态，实际 %q", res.State)
	}
	if res.Message == "" {
		t.Fatal("必须给出执行摘要")
	}
}

// 同一任务 ID 重复投递不得重复执行：对"重启服务"这类动作，重复执行不是多一次，而是把刚起来的服务再打一次。
func TestExecuteIsIdempotentPerTask(t *testing.T) {
	e := newTestExecutor(config.OpsGuards{})
	if e.Supported() == nil {
		t.Fatal("能力声明不应为 nil")
	}
	cmd := model.OpsCommand{ID: "ops-5", Kind: model.OpsKindSvcStatus, Params: map[string]string{"unit": "nginx.service"}}
	first := e.Execute(cmd)
	if first.CommandID != cmd.ID {
		t.Fatalf("回执必须带上任务 ID，实际 %+v", first)
	}
	second := e.Execute(cmd)
	if second.At != first.At || second.DurationMs != first.DurationMs {
		t.Fatalf("重复投递应返回既有结果而不是重新执行：first=%+v second=%+v", first, second)
	}
}

// 落盘幂等：Agent 重启后同一条指令仍不重复执行。
func TestExecuteIdempotentAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/executed.json"
	first := New("n", config.OpsGuards{}, path)
	cmd := model.OpsCommand{ID: "ops-6", Kind: model.OpsKindSvcStatus, Params: map[string]string{"unit": "nginx.service"}}
	res := first.Execute(cmd)

	reopened := New("n", config.OpsGuards{}, path)
	again := reopened.Execute(cmd)
	if again.At != res.At {
		t.Fatalf("重启后应复用既有执行结果：res=%+v again=%+v", res, again)
	}
}
