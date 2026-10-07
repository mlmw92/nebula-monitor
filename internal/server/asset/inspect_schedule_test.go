package asset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 周期化巡检的用例。
//
// 重点都不在"正常路径能不能跑通"，而在几条**静默出错**的边界上：
//   - 范围快照的三态（all / limited+空 / limited+非空）不能在 YAML 往返里被抹平成"不限"；
//   - 交集只准朝收窄方向变（降权要立即生效，且不能反过来放大）；
//   - 解析不到身份、范围收窄为空时**不得跑**（跑出一份"0 个资产、0 条差异"的记录会被读成"巡检过了，没问题"）；
//   - 失败也要推进 LastRunAt（否则每个 tick 重试并重复记错），但**跨重启不能重复执行**。

func newTestInspectScheduler(t *testing.T, svc *Service,
	resolve func(string) (ScopeSnapshot, bool)) (*InspectScheduler, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inspect_schedule.yaml")
	sched, err := NewInspectScheduler(path, InspectScheduleConfig{}, svc, resolve)
	if err != nil {
		t.Fatalf("创建周期化巡检调度器失败: %v", err)
	}
	return sched, path
}

// allScopeResolver 对任何身份都返回"两个维度都不限"。
func allScopeResolver(string) (ScopeSnapshot, bool) {
	return normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll}), true
}

func applyTestHost(t *testing.T, svc *Service, name string) {
	t.Helper()
	if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpuCores": "8"})); err != nil {
		t.Fatalf("建档 %s 失败: %v", name, err)
	}
}

func mustInspectRuns(t *testing.T, svc *Service) []InspectRun {
	t.Helper()
	runs, err := svc.InspectRuns(0)
	if err != nil {
		t.Fatalf("读巡检记录失败: %v", err)
	}
	return runs
}

// 范围快照的规范化与折算：三态必须原样保留下来。
func TestInspectScopeSnapshotThreeStates(t *testing.T) {
	// all ⇒ 该维度不生效（折算出 nil，而不是空切片）
	all := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll})
	f := all.Apply(ListFilter{})
	if f.Nodes != nil || f.LabelSelectors != nil {
		t.Fatalf("all 应折算成 nil（不生效），实际 nodes=%#v labels=%#v", f.Nodes, f.LabelSelectors)
	}
	if all.IsEmpty() {
		t.Fatal("两个维度都不限时不该被判成恒空")
	}

	// limited + 空清单 ⇒ 恒不匹配：必须折算出**非 nil 空切片**（nil 会被读成"不限"，那是越权）
	limited := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeLimited, AssetMode: ScopeModeLimited})
	f = limited.Apply(ListFilter{})
	if f.Nodes == nil || len(f.Nodes) != 0 {
		t.Fatalf("limited+空 应折算成非 nil 空切片，实际 %#v", f.Nodes)
	}
	if f.LabelSelectors == nil || len(f.LabelSelectors) != 0 {
		t.Fatalf("limited+空 的业务标签维度同理，实际 %#v", f.LabelSelectors)
	}
	if !limited.IsEmpty() {
		t.Fatal("受限且两维皆空必须是恒空（fail-closed）")
	}

	// 模式缺省 = 受限（不朝"不限"兜底），这是缺省即失败的取向
	blank := normalizeScopeSnapshot(ScopeSnapshot{})
	if blank.NodeMode != ScopeModeLimited || blank.AssetMode != ScopeModeLimited {
		t.Fatalf("模式缺省应按受限处理，实际 %#v", blank)
	}
	if !blank.IsEmpty() {
		t.Fatal("缺省（受限且空）必须是恒空")
	}

	// 非法取值同样按受限
	bogus := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: "whatever", AssetMode: ScopeModeAll})
	if bogus.NodeMode != ScopeModeLimited {
		t.Fatalf("未知模式应按受限处理，实际 %q", bogus.NodeMode)
	}
}

// 交集只准收窄。
func TestInspectScopeIntersectNarrowsOnly(t *testing.T) {
	wide := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll})
	narrow := normalizeScopeSnapshot(ScopeSnapshot{
		NodeMode: ScopeModeLimited, Nodes: []string{"web-01"}, AssetMode: ScopeModeAll,
	})

	// 存的宽、当前窄 → 取窄（降权立即生效）
	got := wide.Intersect(narrow)
	if got.NodeMode != ScopeModeLimited || len(got.Nodes) != 1 || got.Nodes[0] != "web-01" {
		t.Fatalf("宽∩窄 应得窄，实际 %#v", got)
	}
	// 存的窄、当前宽 → 仍是窄（不能放大）：两次交集结果一致
	back := narrow.Intersect(wide)
	if back.NodeMode != ScopeModeLimited || len(back.Nodes) != 1 || back.Nodes[0] != "web-01" {
		t.Fatalf("窄∩宽 不该放大，实际 %#v", back)
	}

	// 两个都有限定 → 取清单交集
	a := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeLimited, Nodes: []string{"web-01", "web-02"}, AssetMode: ScopeModeAll})
	b := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeLimited, Nodes: []string{"web-02", "web-03"}, AssetMode: ScopeModeAll})
	got = a.Intersect(b)
	if len(got.Nodes) != 1 || got.Nodes[0] != "web-02" {
		t.Fatalf("节点清单应取交集，实际 %#v", got.Nodes)
	}

	// 业务标签维度同理；交集为空即恒空
	p1 := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeLimited,
		Labels: []ScopeLabel{{Key: "biz", Value: "pay"}}})
	p2 := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeLimited,
		Labels: []ScopeLabel{{Key: "biz", Value: "risk"}}})
	if got = p1.Intersect(p2); !got.IsEmpty() {
		t.Fatalf("业务标签无交集应为恒空，实际 %#v", got)
	}
	if got = p1.Intersect(p1); len(got.Labels) != 1 || got.Labels[0].Value != "pay" {
		t.Fatalf("同标签相交应保留，实际 %#v", got.Labels)
	}
}

// 客户端的请求体抹不掉范围，越界间隔被校正（不是报错）。
func TestInspectScheduleSaveKeepsScopeAndNormalizes(t *testing.T) {
	svc, _ := newTestService(t)
	sched, _ := newTestInspectScheduler(t, svc, allScopeResolver)

	wide := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll})
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 0, Scope: wide}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	cfg := sched.Config()
	if cfg.IntervalHours != DefaultInspectScheduleIntervalHours {
		t.Fatalf("间隔 0 应被校正为默认 %d，实际 %d", DefaultInspectScheduleIntervalHours, cfg.IntervalHours)
	}
	if cfg.LastChangedBy != "admin" || cfg.LastChangedAt == 0 {
		t.Fatalf("应记录配置变更者与时间，实际 %q %d", cfg.LastChangedBy, cfg.LastChangedAt)
	}

	// 客户端提交零值范围（试图把范围抹成空）：必须沿用已保存的范围
	if err := sched.Save(InspectScheduleConfig{Enabled: true, Scope: ScopeSnapshot{}}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := sched.Config().Scope; got.NodeMode != ScopeModeAll || got.AssetMode != ScopeModeAll {
		t.Fatalf("零值范围不该覆盖已保存的范围，实际 %#v", got)
	}

	// 越界间隔矫正到上限
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 99999, Scope: wide}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := sched.Config().IntervalHours; got != MaxInspectScheduleIntervalHours {
		t.Fatalf("越界间隔应收敛到 %d，实际 %d", MaxInspectScheduleIntervalHours, got)
	}
}

// 到点才跑；跑了之后到点前不再跑；**跨重启不重复执行**（LastRunAt 跨重启保留）。
func TestInspectScheduleTickAndRestartDedupe(t *testing.T) {
	svc, _ := newTestService(t)
	base := time.UnixMilli(1_800_000_000_000)
	svc.now = func() int64 { return base.UnixMilli() }
	applyTestHost(t, svc, "web-01")

	sched, path := newTestInspectScheduler(t, svc, allScopeResolver)
	now := base
	sched.now = func() time.Time { return now }

	// 未启用：tick 不跑
	sched.tick()
	if runs := mustInspectRuns(t, svc); len(runs) != 0 {
		t.Fatalf("未启用不该执行，实际 %d 条记录", len(runs))
	}

	wide := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll})
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24, Scope: wide}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	sched.tick()
	runs := mustInspectRuns(t, svc)
	if len(runs) != 1 {
		t.Fatalf("启用后到点应执行一次，实际 %d 条", len(runs))
	}
	if runs[0].Actor != InspectScheduleActor {
		t.Fatalf("定时执行应留痕 %q，实际 %q", InspectScheduleActor, runs[0].Actor)
	}
	if st := sched.Status(); st.NextAt != base.UnixMilli()+int64(24*time.Hour/time.Millisecond) {
		t.Fatalf("下次预计时间不符：%d", st.NextAt)
	}

	// 同一个时刻再 tick：不到点，不跑
	sched.tick()
	if runs = mustInspectRuns(t, svc); len(runs) != 1 {
		t.Fatalf("不到点不该重复执行，实际 %d 条", len(runs))
	}

	// 跨重启：新建一个调度器指向同一份配置 → 仍不到点，不重复跑
	restarted, err := NewInspectScheduler(path, InspectScheduleConfig{}, svc, allScopeResolver)
	if err != nil {
		t.Fatalf("重启构造失败: %v", err)
	}
	restarted.now = func() time.Time { return now }
	restarted.tick()
	if runs = mustInspectRuns(t, svc); len(runs) != 1 {
		t.Fatalf("重启后不该重复执行（LastRunAt 应跨重启保留），实际 %d 条", len(runs))
	}

	// 到点：跑第二次
	now = base.Add(25 * time.Hour)
	restarted.tick()
	if runs = mustInspectRuns(t, svc); len(runs) != 2 {
		t.Fatalf("到点应执行，实际 %d 条", len(runs))
	}
}

// 身份解析不到 / 交集为空：**不执行**，但把原因写进状态，并且推进 LastRunAt（避免每个 tick 重试刷错）。
func TestInspectScheduleRefusesWithoutUsableScope(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_800_000_000_000 }
	applyTestHost(t, svc, "web-01")
	wide := normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll})

	// ① 保存者已不存在（账号被删/被禁）
	gone, _ := newTestInspectScheduler(t, svc, func(string) (ScopeSnapshot, bool) { return ScopeSnapshot{}, false })
	if err := gone.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24, Scope: wide}, "alice"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	run := gone.RunNow("", false)
	if run.Error == "" || !strings.Contains(run.Error, "已不存在") {
		t.Fatalf("身份解析不到应明确报出原因，实际 %q", run.Error)
	}
	if runs := mustInspectRuns(t, svc); len(runs) != 0 {
		t.Fatalf("身份解析不到时不得执行（fail-closed），实际跑了 %d 条", len(runs))
	}
	cfg := gone.Config()
	if cfg.LastRunAt == 0 {
		t.Fatal("失败也要推进 LastRunAt，否则每个 tick 都会重试并重复记同一条错")
	}
	if cfg.LastError == "" {
		t.Fatal("失败原因必须写进状态（界面要显示）")
	}

	// ② 当前范围收窄到没有任何节点 → 交集恒空
	narrowed, _ := newTestInspectScheduler(t, svc, func(string) (ScopeSnapshot, bool) {
		return normalizeScopeSnapshot(ScopeSnapshot{NodeMode: ScopeModeLimited, AssetMode: ScopeModeAll}), true
	})
	if err := narrowed.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24, Scope: wide}, "alice"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	run = narrowed.RunNow("", false)
	if run.Error == "" || !strings.Contains(run.Error, "收窄为空") {
		t.Fatalf("交集为空应明确报出原因，实际 %q", run.Error)
	}
	if runs := mustInspectRuns(t, svc); len(runs) != 0 {
		t.Fatalf("交集为空时不得执行（会产出误导性的 0 资产记录），实际跑了 %d 条", len(runs))
	}
}

// 手动触发要**再按点击者范围收窄**，并且留痕是登录名而不是 schedule。
func TestInspectScheduleManualRunNarrowsToClicker(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_800_000_000_000 }
	applyTestHost(t, svc, "web-01")
	applyTestHost(t, svc, "web-02")

	byUser := map[string]ScopeSnapshot{
		"admin":  {NodeMode: ScopeModeAll, AssetMode: ScopeModeAll},
		"narrow": {NodeMode: ScopeModeLimited, Nodes: []string{"web-01"}, AssetMode: ScopeModeAll},
	}
	resolve := func(user string) (ScopeSnapshot, bool) {
		s, ok := byUser[user]
		if !ok {
			return ScopeSnapshot{}, false
		}
		return normalizeScopeSnapshot(s), true
	}
	sched, _ := newTestInspectScheduler(t, svc, resolve)
	wide := normalizeScopeSnapshot(scopeAllSnapshot())
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24, Scope: wide}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 范围很窄的人点"立即执行"：只能看到自己范围内的那台
	run := sched.RunNow("narrow", true)
	if run.Error != "" {
		t.Fatalf("手动执行不该失败：%q", run.Error)
	}
	if run.Actor != "narrow" {
		t.Fatalf("手动执行应留痕登录名，实际 %q", run.Actor)
	}
	if run.Assets != 1 {
		t.Fatalf("手动执行必须再按点击者范围收窄（期望 1 台），实际 %d 台", run.Assets)
	}

	// 定时跑用保存者（admin）的范围：两台都覆盖
	if run = sched.RunNow("", false); run.Error != "" {
		t.Fatalf("定时执行不该失败：%q", run.Error)
	}
	if run.Actor != InspectScheduleActor {
		t.Fatalf("定时执行应留痕 %q，实际 %q", InspectScheduleActor, run.Actor)
	}
	if run.Assets != 2 {
		t.Fatalf("定时执行按保存者范围应覆盖 2 台，实际 %d 台", run.Assets)
	}

	// 两种触发都进了记录表（这是"谁跑的"的最终留痕处）
	var sawManual, sawScheduled bool
	for _, r := range mustInspectRuns(t, svc) {
		switch r.Actor {
		case "narrow":
			sawManual = true
		case InspectScheduleActor:
			sawScheduled = true
		}
	}
	if !sawManual || !sawScheduled {
		t.Fatalf("两种触发都应留下记录，实际 manual=%v scheduled=%v", sawManual, sawScheduled)
	}
}

// scopeAllSnapshot 取"两个维度都不限"的规范快照（用例里反复要用）。
func scopeAllSnapshot() ScopeSnapshot {
	return ScopeSnapshot{NodeMode: ScopeModeAll, AssetMode: ScopeModeAll}
}

// 上一轮还没跑完：跳过本轮并记录原因，不排队、也不产出记录。
func TestInspectScheduleSkipsWhenBusy(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_800_000_000_000 }
	applyTestHost(t, svc, "web-01")
	sched, _ := newTestInspectScheduler(t, svc, allScopeResolver)
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24,
		Scope: normalizeScopeSnapshot(scopeAllSnapshot())}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	sched.running = true // 白盒：模拟上一轮仍在执行
	run := sched.RunNow("", false)
	if !strings.Contains(run.Error, "尚未完成") {
		t.Fatalf("占用中应报「上一次尚未完成」，实际 %q", run.Error)
	}
	if runs := mustInspectRuns(t, svc); len(runs) != 0 {
		t.Fatalf("占用中不得执行，实际跑了 %d 条", len(runs))
	}
	sched.running = false
}

// 执行失败要被看见（不 panic、不静默）。关掉库来制造真实失败。
func TestInspectScheduleSurfacesFailure(t *testing.T) {
	svc, store := newTestService(t)
	svc.now = func() int64 { return 1_800_000_000_000 }
	applyTestHost(t, svc, "web-01")
	sched, _ := newTestInspectScheduler(t, svc, allScopeResolver)
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24,
		Scope: normalizeScopeSnapshot(scopeAllSnapshot())}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关库失败: %v", err)
	}

	run := sched.RunNow("", false)
	if run.Error == "" {
		t.Fatal("执行失败必须回传错误")
	}
	if cfg := sched.Config(); cfg.LastError == "" {
		t.Fatal("执行失败必须写进状态，界面才能显示")
	}
}

// 手动与自动共用同一入口：手动跑也要更新"上次运行"，否则界面会出现
// "我明明刚跑过，调度却说到点该跑了"。
func TestInspectScheduleManualRunUpdatesLastRun(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_800_000_000_000 }
	applyTestHost(t, svc, "web-01")
	sched, _ := newTestInspectScheduler(t, svc, allScopeResolver)
	if err := sched.Save(InspectScheduleConfig{Enabled: true, IntervalHours: 24,
		Scope: normalizeScopeSnapshot(scopeAllSnapshot())}, "admin"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	run := sched.RunNow("admin", true)
	if run.Error != "" {
		t.Fatalf("手动执行失败：%q", run.Error)
	}
	cfg := sched.Config()
	if cfg.LastRunAt != run.At || cfg.LastRunID == 0 {
		t.Fatalf("手动执行应更新上次运行（at=%d id=%d），实际 at=%d id=%d",
			run.At, run.RunID, cfg.LastRunAt, cfg.LastRunID)
	}
	if !cfg.LastManual {
		t.Fatal("上次运行应标记为手动（否则界面上那句话是错的）")
	}
	if st := sched.Status(); st.Last == nil || !st.Last.Manual {
		t.Fatalf("状态里的上次运行应带手动标记，实际 %#v", st.Last)
	}
}

// 配置文件不存在时用初始配置落盘（与保留策略一致）；坏配置要报错而不是静默用默认值。
func TestInspectScheduleConfigFileHandling(t *testing.T) {
	svc, _ := newTestService(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "inspect_schedule.yaml")
	if _, err := NewInspectScheduler(path, InspectScheduleConfig{IntervalHours: 6}, svc, allScopeResolver); err != nil {
		t.Fatalf("首建配置失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("首建应把配置落盘")
	}
	if err := os.WriteFile(path, []byte("enabled: ["), 0o600); err != nil {
		t.Fatalf("写坏配置失败: %v", err)
	}
	if _, err := NewInspectScheduler(path, InspectScheduleConfig{}, svc, allScopeResolver); err == nil {
		t.Fatal("坏配置应报错（静默用默认值会让「配置没生效」变成一个谜）")
	}
}
