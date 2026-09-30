package ops

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "ops_tasks.json"))
}

// 任务状态机：创建 → 领取（能力协商生效）→ running → succeeded。
func TestStore_LifecycleAndCapabilityGating(t *testing.T) {
	s := newTestStore(t)
	now := int64(1_700_000_000_000)
	s.SetNow(func() int64 { return now })

	task := s.Create(model.OpsCommand{Node: "web-01", Kind: KindSvcStatus, Params: map[string]string{"unit": "nginx.service"}}, "alice", "10.0.0.9", "排障")
	if task.State != model.OpsStateQueued || task.ID == "" {
		t.Fatalf("新建任务应为 queued 且有 ID：%+v", task)
	}
	if task.ExpireAt <= now {
		t.Fatalf("应自动设置过期时刻：%+v", task)
	}

	// 节点未声明支持该动作 → 不下发（否则任务会停在 delivered，最难排查）
	if cmd := s.Take("web-01", []string{KindNodeDiagnostics}); cmd != nil {
		t.Fatalf("未声明支持的动作不应下发，实际 %+v", cmd)
	}
	if got, _ := s.Get(task.ID); got.State != model.OpsStateQueued {
		t.Fatalf("未领取的任务状态不应变化，实际 %s", got.State)
	}

	cmd := s.Take("web-01", []string{KindSvcStatus, KindNodeDiagnostics})
	if cmd == nil || cmd.ID != task.ID {
		t.Fatalf("应领取到该任务，实际 %+v", cmd)
	}
	if got, _ := s.Get(task.ID); got.State != model.OpsStateDelivered || got.DeliveredAt == 0 {
		t.Fatalf("领取后应为 delivered 并记录时间：%+v", got)
	}

	// 同一节点同时只下发一条
	if again := s.Take("web-01", []string{KindSvcStatus}); again != nil {
		t.Fatalf("同一节点不应并发下发第二条，实际 %+v", again)
	}

	s.ApplyResult(model.OpsResult{CommandID: task.ID, State: model.OpsStateRunning})
	if got, _ := s.Get(task.ID); got.State != model.OpsStateRunning {
		t.Fatalf("running 回执应生效，实际 %s", got.State)
	}
	s.ApplyResult(model.OpsResult{CommandID: task.ID, State: model.OpsStateSucceeded, Message: "已查询", Data: map[string]string{"状态": "active"}, DurationMs: 12})
	got, _ := s.Get(task.ID)
	if got.State != model.OpsStateSucceeded || got.DoneAt == 0 || got.Data["状态"] != "active" || got.DurationMs != 12 {
		t.Fatalf("成功回执应完整落库：%+v", got)
	}
}

// 终态不可被后续（乱序/重放）回执改写：把 succeeded 打回 running 会让界面显示"还在执行"。
func TestStore_TerminalStateImmutable(t *testing.T) {
	s := newTestStore(t)
	task := s.Create(model.OpsCommand{Node: "web-01", Kind: KindNodeDiagnostics}, "alice", "", "")
	s.Take("web-01", []string{KindNodeDiagnostics})
	s.ApplyResult(model.OpsResult{CommandID: task.ID, State: model.OpsStateFailed, Message: "boom"})
	s.ApplyResult(model.OpsResult{CommandID: task.ID, State: model.OpsStateRunning})
	if got, _ := s.Get(task.ID); got.State != model.OpsStateFailed {
		t.Fatalf("终态不应被改写，实际 %s", got.State)
	}
	// 未知任务 ID 的回执应被忽略而不是 panic
	s.ApplyResult(model.OpsResult{CommandID: "ops-999", State: model.OpsStateSucceeded})
}

// 超时回收：任务不能"永远在排队"，否则操作者会一直等一个已经没人管的指令。
func TestStore_ExpireOverdue(t *testing.T) {
	s := newTestStore(t)
	now := int64(1_700_000_000_000)
	s.SetNow(func() int64 { return now })
	task := s.Create(model.OpsCommand{Node: "web-01", Kind: KindNodeDiagnostics}, "alice", "", "")

	now += taskTTL.Milliseconds() + 1
	s.ExpireOverdue()
	got, _ := s.Get(task.ID)
	if got.State != model.OpsStateExpired || got.Message == "" {
		t.Fatalf("超时任务应被回收为 expired 并给出原因：%+v", got)
	}
	// 已过期的不再下发
	if cmd := s.Take("web-01", []string{KindNodeDiagnostics}); cmd != nil {
		t.Fatalf("已过期任务不应再下发，实际 %+v", cmd)
	}
}

// 能力（Capabilities.Ops）落盘后可读回，并且只有变化时才保存。
func TestStore_CapsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ops_tasks.json")
	s := NewStore(path)
	s.SaveCaps("web-01", []string{KindSvcStatus, KindNodeDiagnostics})
	if got := s.Caps("web-01"); len(got) != 2 || got[0] != KindNodeDiagnostics {
		t.Fatalf("能力应排序返回，实际 %v", got)
	}
	if len(s.Caps("db-01")) != 0 {
		t.Fatalf("未声明的节点应为空")
	}

	reopened := NewStore(path)
	if got := reopened.Caps("web-01"); len(got) != 2 {
		t.Fatalf("重新打开后能力应保留，实际 %v", got)
	}
}

// Service.Create 的三类拒绝：参数不合法 / 节点没声明能力 / 写动作未被本机放行。
func TestService_CreateValidationAndGating(t *testing.T) {
	s := newTestStore(t)
	svc := NewService(s)

	// 1) 参数不合法 → 普通错误（改参数即可）
	if _, err := svc.Create("web-01", KindSvcStatus, map[string]string{"unit": "--now"}, "alice", "", ""); err == nil {
		t.Fatal("非法参数应被拒绝")
	} else if errors.Is(err, ErrUnsupported) {
		t.Fatalf("参数错误不该报成「节点不支持」：%v", err)
	}

	// 2) 节点没有任何能力声明 → ErrUnsupported（要升级 Agent 或开护栏）
	if _, err := svc.Create("web-01", KindSvcStatus, map[string]string{"unit": "nginx.service"}, "alice", "", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("未声明能力应为 ErrUnsupported，实际 %v", err)
	}

	// 3) 声明了只读、但没放行写 → ErrUnsupported，且提示要改的是**机器上**的配置
	s.SaveCaps("web-01", []string{KindNodeDiagnostics, KindSvcStatus})
	_, err := svc.Create("web-01", KindSvcRestart, map[string]string{"unit": "nginx.service"}, "alice", "", "")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("未放行的写动作应为 ErrUnsupported，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "guards.ops") {
		t.Fatalf("提示应指明去 agent.yaml 的 guards.ops 改，实际：%v", err)
	}

	// 4) 声明了写动作 → 创建成功，且操作者与原因都记录在案
	s.SaveCaps("web-01", []string{KindNodeDiagnostics, KindSvcStatus, KindSvcRestart})
	task, err := svc.Create("web-01", KindSvcRestart, map[string]string{"unit": "nginx.service"}, "alice", "10.0.0.9", "发版后重启")
	if err != nil {
		t.Fatalf("放行的写动作应可创建：%v", err)
	}
	if task.Kind != KindSvcRestart || task.Operator != "alice" || task.OperatorIP != "10.0.0.9" || task.Reason != "发版后重启" {
		t.Fatalf("任务应记录操作者、来源 IP 与原因：%+v", task)
	}
	if task.State != model.OpsStateQueued {
		t.Fatalf("新任务应为 queued，实际 %s", task.State)
	}
}

// 缺节点名必须报错（否则会被创建成一条永远没人领取的任务）。
func TestService_CreateRequiresNode(t *testing.T) {
	svc := NewService(newTestStore(t))
	if _, err := svc.Create("  ", KindNodeDiagnostics, nil, "alice", "", ""); err == nil {
		t.Fatal("缺目标节点应被拒绝")
	}
}
