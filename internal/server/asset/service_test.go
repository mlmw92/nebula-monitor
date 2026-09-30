package asset

import (
	"fmt"
	"path/filepath"
	"testing"
)

// newTestService 建一个临时库上的资产服务。
// 用真实 SQLite 而非假实现：本包的复杂度恰恰在 SQL 语义（联合主键、幂等 upsert、
// 事务写快照）上，用假实现会把「夹具的错误前提」当成通过。
func newTestService(t *testing.T) (*Service, *Store) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "asset.db"))
	if err != nil {
		t.Fatalf("打开资产库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store), store
}

func hostObservation(hostname string, attrs map[string]string) Observation {
	return Observation{
		TypeKey: TypeHost, NaturalKey: hostname, Name: hostname, Node: hostname,
		Attrs: attrs,
	}
}

// 重复提交同一观测只应有一个资产，且建档记录只有一条。
func TestApplyIsIdempotentAndRecordsInitialOnce(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }

	first, created, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "8"}))
	if err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	if !created {
		t.Fatal("首次提交应报告新建")
	}
	second, created, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "8"}))
	if err != nil {
		t.Fatalf("重复提交失败: %v", err)
	}
	if created {
		t.Fatal("重复提交不应再次新建资产")
	}
	if first.ID != second.ID {
		t.Fatalf("重复提交应命中同一资产: %d vs %d", first.ID, second.ID)
	}

	all, err := svc.List(ListFilter{TypeKey: TypeHost})
	if err != nil {
		t.Fatalf("列出资产失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("资产数应为 1，实际 %d", len(all))
	}

	history, err := svc.History(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 1 || history[0].Kind != ChangeInitial {
		t.Fatalf("重复提交后应只有一条建档记录，实际 %+v", history)
	}
}

// 采集值与人工值必须并存：人工值只影响生效值，不覆盖采集值。
func TestApplyKeepsDiscoveryAndManualValuesApart(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 2000 }

	if _, _, err := svc.Apply(hostObservation("web-02", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("写入采集值失败: %v", err)
	}
	manual := hostObservation("web-02", map[string]string{"cpu": "16"})
	manual.Source = SourceManual
	manual.Actor = "alice"
	got, _, err := svc.Apply(manual)
	if err != nil {
		t.Fatalf("写入人工值失败: %v", err)
	}

	if v, ok := got.Value("cpu"); !ok || v != "16" {
		t.Fatalf("生效值应优先人工值，实际 %q（ok=%v）", v, ok)
	}
	if v, ok := got.ValueFrom("cpu", SourceDiscovery); !ok || v != "8" {
		t.Fatalf("采集值应被保留，实际 %q（ok=%v）", v, ok)
	}
	if len(got.Attrs) != 2 {
		t.Fatalf("同一 key 的采集值与人工值应各占一条，实际 %d 条", len(got.Attrs))
	}

	history, err := svc.History(Ref{TypeKey: TypeHost, NaturalKey: "web-02"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("应有一条建档 + 一条人工变更，实际 %d 条", len(history))
	}
	if history[0].Source != SourceManual || history[0].Actor != "alice" || history[0].New != "16" {
		t.Fatalf("最新变更应记录人工值与操作人，实际 %+v", history[0])
	}
}

// 值没真变就不该写变更记录：否则每轮采集都会刷屏。
func TestApplyWritesChangeOnlyOnRealValueChange(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 3000 }

	if _, _, err := svc.Apply(hostObservation("web-03", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("首次提交失败: %v", err)
	}
	// 仅首尾空白不同：视为未变化。
	if _, _, err := svc.Apply(hostObservation("web-03", map[string]string{"cpu": " 8 "})); err != nil {
		t.Fatalf("提交未变化的值失败: %v", err)
	}
	history, err := svc.History(Ref{TypeKey: TypeHost, NaturalKey: "web-03"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("未变化不应产生变更记录，实际 %d 条", len(history))
	}

	if _, _, err := svc.Apply(hostObservation("web-03", map[string]string{"cpu": "16"})); err != nil {
		t.Fatalf("提交变化的值失败: %v", err)
	}
	history, err = svc.History(Ref{TypeKey: TypeHost, NaturalKey: "web-03"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("值变化应新增一条记录，实际 %d 条", len(history))
	}
	if history[0].Kind != ChangeUpdate || history[0].Old != "8" || history[0].New != "16" {
		t.Fatalf("变更记录应含字段级 diff，实际 %+v", history[0])
	}
}

// 非法输入必须报错，而不是静默建出一条无法再定位的资产。
func TestApplyRejectsBadInput(t *testing.T) {
	svc, _ := newTestService(t)

	if _, _, err := svc.Apply(Observation{TypeKey: TypeHost, NaturalKey: "  "}); err == nil {
		t.Fatal("空自然键应报错")
	}
	if _, _, err := svc.Apply(Observation{TypeKey: "not-exist", NaturalKey: "x"}); err == nil {
		t.Fatal("未注册的资产类型应报错")
	}
	bad := hostObservation("web-04", nil)
	bad.Source = Source("guess")
	if _, _, err := svc.Apply(bad); err == nil {
		t.Fatal("未知来源应报错")
	}
}

// 关联建立与解除都必须幂等。
func TestLinkIsIdempotentAndResolvesMissingAssets(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 4000 }

	if _, _, err := svc.Apply(hostObservation("web-05", nil)); err != nil {
		t.Fatalf("建立主机资产失败: %v", err)
	}
	inst := Observation{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379", Name: "dev-redis"}
	if _, _, err := svc.Apply(inst); err != nil {
		t.Fatalf("建立实例资产失败: %v", err)
	}

	host := Ref{TypeKey: TypeHost, NaturalKey: "web-05"}
	redis := Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	if err := svc.Link(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("建立关联失败: %v", err)
	}
	if err := svc.Link(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("重复建立关联应幂等，实际报错: %v", err)
	}
	links, err := svc.Links(redis)
	if err != nil {
		t.Fatalf("查询关联失败: %v", err)
	}
	if len(links) != 1 || links[0].Kind != LinkRunsOn || links[0].To.NaturalKey != "web-05" {
		t.Fatalf("关联应有且仅有一条，实际 %+v", links)
	}

	if err := svc.Link(redis, redis, LinkDependsOn); err == nil {
		t.Fatal("自关联应报错")
	}
	if err := svc.Link(redis, Ref{TypeKey: TypeHost, NaturalKey: "missing"}, LinkRunsOn); err == nil {
		t.Fatal("关联到不存在的资产应报错")
	}
	if err := svc.Link(redis, host, LinkKind("unknown")); err == nil {
		t.Fatal("未知关联类型应报错")
	}

	if err := svc.Unlink(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("解除关联失败: %v", err)
	}
	if err := svc.Unlink(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("重复解除应幂等，实际报错: %v", err)
	}
	links, err = svc.Links(redis)
	if err != nil {
		t.Fatalf("查询关联失败: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("解除后不应有剩余关联，实际 %+v", links)
	}
}

// 列表的筛选、关键词与分页边界。
func TestListFiltersAndPagination(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 5000 }

	for _, name := range []string{"web-11", "web-12", "web-13"} {
		ob := hostObservation(name, map[string]string{"cpu": "4"})
		ob.Node = "group-a"
		if _, _, err := svc.Apply(ob); err != nil {
			t.Fatalf("提交资产 %s 失败: %v", name, err)
		}
	}
	other := Observation{TypeKey: TypeMiddlewareInst, NaturalKey: "mysql:127.0.0.1:3306", Node: "group-b"}
	if _, _, err := svc.Apply(other); err != nil {
		t.Fatalf("提交实例资产失败: %v", err)
	}

	byType, err := svc.List(ListFilter{TypeKey: TypeHost})
	if err != nil {
		t.Fatalf("按类型过滤失败: %v", err)
	}
	if len(byType) != 3 {
		t.Fatalf("按类型过滤应得 3 条，实际 %d", len(byType))
	}

	byNode, err := svc.List(ListFilter{Node: "group-b"})
	if err != nil {
		t.Fatalf("按节点过滤失败: %v", err)
	}
	if len(byNode) != 1 || byNode[0].NaturalKey != "mysql:127.0.0.1:3306" {
		t.Fatalf("按节点过滤结果不符，实际 %+v", byNode)
	}

	byKeyword, err := svc.List(ListFilter{Keyword: "web-1"})
	if err != nil {
		t.Fatalf("按关键词过滤失败: %v", err)
	}
	if len(byKeyword) != 3 {
		t.Fatalf("关键词过滤应得 3 条，实际 %d", len(byKeyword))
	}

	page1, err := svc.List(ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页应得 2 条，实际 %d", len(page1))
	}
	page2, err := svc.List(ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页应得 2 条，实际 %d", len(page2))
	}
	if page1[0].ID == page2[0].ID && page1[1].ID == page2[1].ID {
		t.Fatal("翻页应返回不同数据")
	}

	clamped, err := svc.List(ListFilter{Limit: 99999})
	if err != nil {
		t.Fatalf("超大页大小查询失败: %v", err)
	}
	if len(clamped) != 4 {
		t.Fatalf("超大页大小应被收敛到上限且返回全部 4 条，实际 %d", len(clamped))
	}
}

// 分页必须按**资产**计数，而不是 join 后的行数：一个资产有几条属性就有几行，
// 直接对 join 结果 LIMIT 会让属性多的资产挤占同页额度、OFFSET 也随之漂移。
// 依据：前端反馈「台账页没有分页」时排查发现——接口的 total 只等于当前页长度，
// 且 limit 作用在 join 行上；本用例按「一个 12 属性资产 + 三个单属性资产」构造。
func TestListPaginatesAssetsNotAttributeRows(t *testing.T) {
	svc, _ := newTestService(t)
	many := map[string]string{}
	for i := 0; i < 12; i++ {
		many[fmt.Sprintf("attr%02d", i)] = "v"
	}
	if _, _, err := svc.Apply(hostObservation("web-01", many)); err != nil {
		t.Fatalf("写入多属性资产失败: %v", err)
	}
	for _, name := range []string{"web-02", "web-03", "web-04"} {
		if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpu": "8"})); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}

	page, err := svc.List(ListFilter{Limit: 2})
	if err != nil {
		t.Fatalf("分页查询失败: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("一页 2 条应按资产返回 2 个，实际 %d", len(page))
	}
	if len(page[0].Attrs) != 12 {
		t.Fatalf("本页资产应带全部属性（12 条），实际 %d", len(page[0].Attrs))
	}

	page2, err := svc.List(ListFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("第二页查询失败: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页应得 2 个，实际 %d", len(page2))
	}
	for _, a := range page {
		for _, b := range page2 {
			if a.ID == b.ID {
				t.Fatalf("翻页出现重复资产 %d", a.ID)
			}
		}
	}
}

// Count 与 List 共用同一套条件（含资源范围下推），否则「共 N 条」与实际能翻到的条数对不上。
func TestCountMatchesListConditions(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Apply(hostObservation("web-01", nil)); err != nil {
		t.Fatalf("写入 web-01 失败: %v", err)
	}
	if _, _, err := svc.Apply(hostObservation("web-02", nil)); err != nil {
		t.Fatalf("写入 web-02 失败: %v", err)
	}
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeMiddlewareInst, NaturalKey: "mysql:127.0.0.1:3306", Name: "dev-mysql", Node: "db-01",
	}); err != nil {
		t.Fatalf("写入实例资产失败: %v", err)
	}

	cases := []struct {
		name   string
		filter ListFilter
		want   int
	}{
		{"无条件", ListFilter{}, 3},
		{"按类型", ListFilter{TypeKey: TypeHost}, 2},
		{"按关键词", ListFilter{Keyword: "web"}, 2},
		{"按资源范围（可见 2 个节点中的 1 个）", ListFilter{Nodes: []string{"web-01", "db-01"}}, 2},
		// 空集合表示「受限但无可见节点」，必须恒空——若当成「不限制」，资源范围就成了越权旁路。
		{"空节点集合不等于不限制", ListFilter{Nodes: []string{}}, 0},
	}
	for _, tc := range cases {
		got, err := svc.Count(tc.filter)
		if err != nil {
			t.Fatalf("%s：统计失败 %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s：应为 %d，实际 %d", tc.name, tc.want, got)
		}
	}

	// 分页参数不影响总数：前端据此渲染分页器。
	if got, err := svc.Count(ListFilter{Limit: 1, Offset: 2}); err != nil || got != 3 {
		t.Fatalf("总数不应受分页参数影响：got=%d err=%v", got, err)
	}
}

// seedLedger 造一份覆盖四种来源/状态形态的台账：
// auto-01（仅采集）/ manual-01（采集 + 管理属性）/ mixed-01（采集 + 同字段人工值）/ manual-only（纯人工）。
func seedLedger(t *testing.T, svc *Service) {
	t.Helper()
	apply := func(typeKey, key, node string, src Source, attrs map[string]string) {
		t.Helper()
		if _, _, err := svc.Apply(Observation{
			TypeKey: typeKey, NaturalKey: key, Name: key, Node: node, Source: src, Actor: "tester", Attrs: attrs,
		}); err != nil {
			t.Fatalf("写入 %s 失败: %v", key, err)
		}
	}
	apply(TypeHost, "auto-01", "auto-01", "", map[string]string{"cpu": "8"})
	apply(TypeHost, "manual-01", "manual-01", "", map[string]string{"cpu": "8"})
	apply(TypeHost, "manual-01", "manual-01", SourceManual, map[string]string{OwnerKey: "张三"})
	apply(TypeHost, "mixed-01", "mixed-01", "", map[string]string{"mem": "64"})
	apply(TypeHost, "mixed-01", "mixed-01", SourceManual, map[string]string{"mem": "32"})
	apply(TypeHost, "manual-only", "manual-only", SourceManual, map[string]string{OwnerKey: "李四"})
}

// 来源 / 状态 / 责任人 / 冲突四类筛选都由数据推导（不落库），且与摘要在同一口径上。
func TestListFiltersBySourceStatusOwnerAndConflict(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }
	seedLedger(t, svc)

	count := func(name string, f ListFilter, want int) {
		t.Helper()
		got, err := svc.Count(f)
		if err != nil {
			t.Fatalf("%s：统计失败 %v", name, err)
		}
		if got != want {
			t.Fatalf("%s：应为 %d，实际 %d", name, want, got)
		}
	}

	// 来源：auto=无人工值；manual=有人工值且无冲突；mixed=同字段两种来源并存
	count("来源 auto", ListFilter{Source: SourceFilterAuto}, 1)
	count("来源 manual", ListFilter{Source: SourceFilterManual}, 2) // manual-01 与 manual-only
	count("来源 mixed", ListFilter{Source: SourceFilterMixed}, 1)   // mixed-01
	count("来源 不过滤", ListFilter{}, 4)

	// 状态：阈值之上（更早）为失联，之下为在线；从无采集为归档
	count("全部在线", ListFilter{Status: StatusOnline, StaleBefore: now}, 3)
	count("全部失联", ListFilter{Status: StatusMissing, StaleBefore: now + 1}, 3)
	count("归档（纯人工）", ListFilter{Status: StatusArchived}, 1)
	// 归档的资产不能同时算作失联：把阈值推到未来，也只有"被采集过"的那 3 个算失联
	count("失联不含归档", ListFilter{Status: StatusMissing, StaleBefore: now + 1}, 3)

	// 责任人：manual-only 与 manual-01 已指派；auto-01 / mixed-01 未指派
	count("无责任人", ListFilter{OwnerMissing: true}, 2)
	// 冲突：只有 mixed-01 同字段两种来源并存
	count("存在冲突", ListFilter{HasConflict: true}, 1)

	// 关键词命中属性值（拿"业务名/资产编号/IP"找资产是台账最常见的用法）
	count("关键词命中属性值", ListFilter{Keyword: "张三"}, 1)
	count("关键词命中自然键", ListFilter{Keyword: "mixed"}, 1)
	count("关键词命中名称", ListFilter{Keyword: "manual-only"}, 1)

	// 组合条件：混合来源 + 无责任人 → 只有 mixed-01
	count("组合条件", ListFilter{Source: SourceFilterMixed, OwnerMissing: true}, 1)
}

// 摘要的五个数字必须与列表同源：能用同一组筛选条件把列表查出来，数量一致。
func TestStatsMatchListAndScope(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }
	seedLedger(t, svc)
	// 建档与人工写入都会产生变更记录，这里校验的是「窗口是否真的生效」。
	stats, err := svc.Stats(ListFilter{}, now-3600_000)
	if err != nil {
		t.Fatalf("统计摘要失败: %v", err)
	}
	if stats.Total != 4 || stats.NoOwner != 2 || stats.Conflict != 1 {
		t.Fatalf("摘要数字不符：%+v", stats)
	}
	if stats.Missing != 0 {
		t.Fatalf("刚写入的资产不应算失联，实际 %d", stats.Missing)
	}
	if stats.Changes == 0 {
		t.Fatal("窗口内的变更数应大于 0")
	}
	// 窗口起点晚于所有写入：变更数为 0（证明窗口参数真的生效）
	if later, err := svc.Stats(ListFilter{}, now+1); err != nil || later.Changes != 0 {
		t.Fatalf("窗口外的变更不应被计入：%+v err=%v", later, err)
	}

	// 资源范围下推后摘要同样只数范围内的资产（摘要不得成为范围旁路）
	scoped, err := svc.Stats(ListFilter{Nodes: []string{"auto-01"}}, now-3600_000)
	if err != nil {
		t.Fatalf("范围内统计失败: %v", err)
	}
	// 范围内只有 auto-01：它的建档记录算 1 条变更；其余资产的变更不得被统计进来。
	if scoped.Total != 1 || scoped.NoOwner != 1 || scoped.Conflict != 0 || scoped.Changes != 1 {
		t.Fatalf("范围内摘要不符：%+v", scoped)
	}
	if scoped, err = svc.Stats(ListFilter{Nodes: []string{}}, now-3600_000); err != nil || scoped.Total != 0 {
		t.Fatalf("空节点集合应恒空：%+v err=%v", scoped, err)
	}
}

// 恢复采集值 = 删除人工值（不是把采集值写回），并留下可追溯的变更记录。
func TestResetManualKeepsDiscoveryAndRecordsChange(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	seedLedger(t, svc)

	ref := Ref{TypeKey: TypeHost, NaturalKey: "mixed-01"}
	updated, err := svc.ResetManual(ref, []string{" mem ", "mem", ""}, "alice")
	if err != nil {
		t.Fatalf("恢复采集值失败: %v", err)
	}
	// 生效值回落为采集值，且人工值确实不存在了
	if v, _ := updated.Value("mem"); v != "64" {
		t.Fatalf("恢复后生效值应为采集值 64，实际 %q", v)
	}
	if _, ok := updated.ValueFrom("mem", SourceManual); ok {
		t.Fatal("人工值应已删除")
	}
	if v, ok := updated.ValueFrom("mem", SourceDiscovery); !ok || v != "64" {
		t.Fatalf("采集值不得被删除，实际 %q ok=%v", v, ok)
	}

	history, err := svc.History(ref, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	newest := history[0]
	if newest.Field != "mem" || newest.Old != "32" || newest.New != "" ||
		newest.Source != SourceManual || newest.Kind != ChangeUpdate || newest.Actor != "alice" {
		t.Fatalf("恢复应留下字段级变更记录，实际 %+v", newest)
	}

	// 没有人工值时给明确错误，而不是静默成功
	if _, err := svc.ResetManual(ref, []string{"mem"}, "alice"); err == nil {
		t.Fatal("重复恢复应报错")
	}
	if _, err := svc.ResetManual(ref, []string{"  "}, "alice"); err == nil {
		t.Fatal("空字段名应报错")
	}
}

// 快照保存的是「生效值」，且字段集合可按关注项裁剪。
func TestSnapshotCapturesEffectiveValues(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 6000 }

	ob := hostObservation("web-21", map[string]string{"cpu": "8", "mem": "16G", "disk": "500G"})
	if _, _, err := svc.Apply(ob); err != nil {
		t.Fatalf("提交资产失败: %v", err)
	}
	manual := hostObservation("web-21", map[string]string{"mem": "32G"})
	manual.Source = SourceManual
	if _, _, err := svc.Apply(manual); err != nil {
		t.Fatalf("提交人工值失败: %v", err)
	}

	snap, err := svc.Snapshot(Ref{TypeKey: TypeHost, NaturalKey: "web-21"}, []string{"cpu", "mem"})
	if err != nil {
		t.Fatalf("生成快照失败: %v", err)
	}
	if len(snap.Fields) != 2 {
		t.Fatalf("快照应只含关注字段，实际 %+v", snap.Fields)
	}
	if snap.Fields["mem"] != "32G" {
		t.Fatalf("快照应取生效值（人工优先），实际 %q", snap.Fields["mem"])
	}

	headers, err := svc.Snapshots(Ref{TypeKey: TypeHost, NaturalKey: "web-21"}, 0)
	if err != nil {
		t.Fatalf("列出快照失败: %v", err)
	}
	if len(headers) != 1 || headers[0].ID != snap.ID {
		t.Fatalf("快照头应有且仅有一条，实际 %+v", headers)
	}
	fields, err := svc.SnapshotFields(snap.ID)
	if err != nil {
		t.Fatalf("读取快照字段失败: %v", err)
	}
	if fields["cpu"] != "8" || fields["mem"] != "32G" {
		t.Fatalf("快照字段读回不符，实际 %+v", fields)
	}
}

// 迁移幂等：同一路径重复打开不应报错，也不应丢数据。
func TestOpenIsIdempotentOnSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "asset.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	svc := NewService(first)
	if _, _, err := svc.Apply(hostObservation("web-31", map[string]string{"cpu": "2"})); err != nil {
		t.Fatalf("提交资产失败: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("再次打开失败（迁移应幂等）: %v", err)
	}
	defer second.Close()
	reopened := NewService(second)
	got, ok, err := reopened.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-31"})
	if err != nil {
		t.Fatalf("重新打开后查询失败: %v", err)
	}
	if !ok || got.NaturalKey != "web-31" {
		t.Fatalf("重新打开后应读到既有资产，实际 ok=%v %+v", ok, got)
	}
}
