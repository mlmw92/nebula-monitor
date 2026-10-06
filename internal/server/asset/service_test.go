package asset

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("建立关联失败: %v", err)
	}
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("重复建立关联应幂等，实际报错: %v", err)
	}
	links, err := svc.Links(redis)
	if err != nil {
		t.Fatalf("查询关联失败: %v", err)
	}
	if len(links) != 1 || links[0].Kind != LinkRunsOn || links[0].To.NaturalKey != "web-05" {
		t.Fatalf("关联应有且仅有一条，实际 %+v", links)
	}

	if err := svc.LinkDiscovered(redis, redis, LinkDependsOn); err == nil {
		t.Fatal("自关联应报错")
	}
	if err := svc.LinkDiscovered(redis, Ref{TypeKey: TypeHost, NaturalKey: "missing"}, LinkRunsOn); err == nil {
		t.Fatal("关联到不存在的资产应报错")
	}
	if err := svc.LinkDiscovered(redis, host, LinkKind("unknown")); err == nil {
		t.Fatal("未知关联类型应报错")
	}

	if err := svc.UnlinkManual(redis, host, LinkRunsOn, "tester"); err != nil {
		t.Fatalf("解除关联失败: %v", err)
	}
	if err := svc.UnlinkManual(redis, host, LinkRunsOn, "tester"); err != nil {
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

// 人工维护的关系优先于采集自动发现，且人工删除是**逻辑删除**。
//
// 这几条规则缺任何一条都会让用户的操作失效，所以逐条断言：
//  1. 采集建立的边来源是 discovery；
//  2. 人工建立同一条边 → 来源升为 manual；
//  3. 人工认领之后采集继续上报 → 仍是 manual（不降级）；
//  4. 人工删除 → 边消失且落下抑制；采集再报不会把它建回来；
//  5. 取消抑制之后，采集才能重建。
func TestManualLinkWinsOverDiscovery(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 5000 }

	if _, _, err := svc.Apply(hostObservation("web-06", nil)); err != nil {
		t.Fatalf("建立主机资产失败: %v", err)
	}
	inst := Observation{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6380", Name: "dev-redis-b"}
	if _, _, err := svc.Apply(inst); err != nil {
		t.Fatalf("建立实例资产失败: %v", err)
	}
	host := Ref{TypeKey: TypeHost, NaturalKey: "web-06"}
	redis := Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6380"}

	// sourceOf 返回这条边的来源；没有边时返回空串。
	sourceOf := func() string {
		t.Helper()
		links, err := svc.Links(redis)
		if err != nil {
			t.Fatalf("查询关联失败: %v", err)
		}
		if len(links) == 0 {
			return ""
		}
		return string(links[0].Source)
	}

	// 1. 采集建立 → discovery
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("采集建立关联失败: %v", err)
	}
	if got := sourceOf(); got != string(SourceDiscovery) {
		t.Fatalf("采集建立的边来源应为 discovery，实际 %q", got)
	}

	// 2. 人工认领同一条边 → 升为 manual
	if err := svc.LinkManual(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("人工建立关联失败: %v", err)
	}
	if got := sourceOf(); got != string(SourceManual) {
		t.Fatalf("人工建立后来源应为 manual，实际 %q", got)
	}

	// 3. 采集继续上报 → 不得把人工的认领降级回 discovery
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("采集重复上报不应报错: %v", err)
	}
	if got := sourceOf(); got != string(SourceManual) {
		t.Fatalf("人工认领的边不应被采集降级，实际 %q", got)
	}

	// 4. 人工删除 = 逻辑删除
	if err := svc.UnlinkManual(redis, host, LinkRunsOn, "tester"); err != nil {
		t.Fatalf("人工解除关联失败: %v", err)
	}
	if got := sourceOf(); got != "" {
		t.Fatalf("人工删除后不应还有边，实际来源 %q", got)
	}
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("抑制期间的采集上报不应报错: %v", err)
	}
	if got := sourceOf(); got != "" {
		t.Fatalf("被人工抑制的边不应被采集重建，实际来源 %q", got)
	}

	// 5. 取消抑制后才允许采集重建
	if err := svc.RestoreDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("取消抑制失败: %v", err)
	}
	if err := svc.LinkDiscovered(redis, host, LinkRunsOn); err != nil {
		t.Fatalf("取消抑制后采集建立关联失败: %v", err)
	}
	if got := sourceOf(); got != string(SourceDiscovery) {
		t.Fatalf("重建的边来源应为 discovery，实际 %q", got)
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

// 巡检第一次运行只建立基线（数据不足 ≠ 不合规），第二次才产出差异；
// 且运行态字段（up 等）必须被排除——否则每次探活翻转都会变成一条"变更"，
// 真正的配置变更会被淹掉。
func TestRunInspectBaselineThenDiff(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }

	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{
		"os": "Ubuntu 24.04", "cpu": "8", "up": "true",
	})); err != nil {
		t.Fatalf("写入资产失败: %v", err)
	}

	first, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("首次巡检失败: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("巡检记录应有主键")
	}
	if first.Assets != 1 || first.Baselined != 1 || first.Findings != 0 {
		t.Fatalf("首次巡检应只建立基线：%+v", first)
	}
	if first.Scope != "all" || first.Actor != "alice" {
		t.Fatalf("巡检记录应带范围与操作人：%+v", first)
	}

	// 配置项变化（cpu 8→16）+ 运行态字段翻转（up true→false）
	now += 60_000
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{
		"os": "Ubuntu 24.04", "cpu": "16", "up": "false",
	})); err != nil {
		t.Fatalf("写入资产失败: %v", err)
	}
	second, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("二次巡检失败: %v", err)
	}
	if second.Baselined != 0 {
		t.Fatalf("已有基线不应再计入 baselined：%+v", second)
	}
	if second.Findings != 1 {
		t.Fatalf("应只有 cpu 一条变更（up 属运行态字段应被排除），实际 %d：%+v",
			second.Findings, mustFindings(t, svc, second.ID))
	}
	f := mustFindings(t, svc, second.ID)[0]
	if f.Kind != FindingChanged || f.Level != FindingWarning {
		t.Fatalf("值变化应为 changed/warning，实际 %s/%s", f.Kind, f.Level)
	}
	if f.Field != "cpu" || f.Expected != "8" || f.Actual != "16" {
		t.Fatalf("差异项内容不符：%+v", f)
	}
	// 冗余身份字段：资产后来被删除时，历史结论仍应可读
	if f.AssetKey != "web-01" || f.AssetType != TypeHost || f.Node != "web-01" {
		t.Fatalf("差异项应带资产身份：%+v", f)
	}
	// 巡检只给结论：不得改动资产本身
	got, _, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatalf("回读资产失败: %v", err)
	}
	if v, _ := got.ValueFrom("cpu", SourceDiscovery); v != "16" {
		t.Fatalf("巡检不应改动资产属性，实际 cpu=%q", v)
	}
}

// 新增人工属性 → added（info）；把该人工值恢复掉、字段彻底消失 → missing（critical）。
func TestRunInspectAddedAndMissingKinds(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_100_000)
	svc.now = func() int64 { return now }
	ref := Ref{TypeKey: TypeHost, NaturalKey: "web-02"}

	if _, _, err := svc.Apply(hostObservation("web-02", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("写入资产失败: %v", err)
	}
	if _, err := svc.RunInspect(InspectScope{}, "alice"); err != nil {
		t.Fatalf("建立基线失败: %v", err)
	}

	manual := hostObservation("web-02", map[string]string{"vendor": "Dell"})
	manual.Source = SourceManual
	if _, _, err := svc.Apply(manual); err != nil {
		t.Fatalf("写入人工属性失败: %v", err)
	}
	now += 60_000
	added, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if !hasFinding(mustFindings(t, svc, added.ID), "vendor", FindingAdded, FindingInfo) {
		t.Fatalf("应报出新增字段 vendor：%+v", mustFindings(t, svc, added.ID))
	}

	// 清掉人工值：采集侧也没有该字段 → 字段从关注集合里消失 → missing
	if _, err := svc.ResetManual(ref, []string{"vendor"}, "alice"); err != nil {
		t.Fatalf("恢复采集值失败: %v", err)
	}
	now += 60_000
	missing, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if !hasFinding(mustFindings(t, svc, missing.ID), "vendor", FindingMissing, FindingCritical) {
		t.Fatalf("应报出缺失字段 vendor：%+v", mustFindings(t, svc, missing.ID))
	}
}

// 期望值（标杆）来自同类资产：其它同类型资产与之不一致的字段产出 deviation；
// 标杆自身不与自己比；清除标杆后不再有偏差。L3 不依赖 L2 基线（首次巡检即可报偏差）。
func TestRunInspectDeviationAgainstBaseline(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_700_000_200_000 }

	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"os": "Ubuntu 24.04", "cpu": "8"})); err != nil {
		t.Fatalf("写入资产失败: %v", err)
	}
	if _, _, err := svc.Apply(hostObservation("web-02", map[string]string{"os": "CentOS 7.9", "cpu": "8"})); err != nil {
		t.Fatalf("写入资产失败: %v", err)
	}

	b, err := svc.SetBaseline(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, "alice")
	if err != nil {
		t.Fatalf("设置标杆失败: %v", err)
	}
	if b.TypeKey != TypeHost || b.AssetKey != "web-01" || b.SnapshotID == 0 {
		t.Fatalf("标杆内容不符：%+v", b)
	}
	all, err := svc.Baselines()
	if err != nil || len(all) != 1 || all[0].TypeKey != TypeHost {
		t.Fatalf("标杆列表不符：%+v err=%v", all, err)
	}

	run, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if run.Baselined != 1 {
		t.Fatalf("标杆资产已因设置标杆而有了快照，只有 web-02 该计入 baselined，实际 %d", run.Baselined)
	}
	findings := mustFindings(t, svc, run.ID)
	if len(findings) != 1 {
		t.Fatalf("应只有 web-02 的 os 偏差，实际 %+v", findings)
	}
	f := findings[0]
	if f.AssetKey != "web-02" || f.Field != "os" || f.Kind != FindingDeviation || f.Level != FindingWarning {
		t.Fatalf("偏差内容不符：%+v", f)
	}
	if f.Expected != "Ubuntu 24.04" || f.Actual != "CentOS 7.9" {
		t.Fatalf("偏差的期望值与实际值不符：%+v", f)
	}

	if err := svc.ClearBaseline(TypeHost); err != nil {
		t.Fatalf("清除标杆失败: %v", err)
	}
	after, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if fs := mustFindings(t, svc, after.ID); len(fs) != 0 {
		t.Fatalf("清除标杆后不应再有偏差，实际 %+v", fs)
	}
}

// 资源范围必须下推到巡检：受限用户只检自己范围内的资产，
// 否则会出现"巡检报了某个资产、列表里却找不到它"。
func TestRunInspectRespectsResourceScope(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_700_000_300_000 }
	for _, name := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(name, nil)); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}

	scoped, err := svc.RunInspect(InspectScope{Filter: ListFilter{Nodes: []string{"web-01"}}}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if scoped.Assets != 1 || scoped.Baselined != 1 {
		t.Fatalf("范围内应只有 1 个资产：%+v", scoped)
	}
	if scoped.Scope != "scope:mine" {
		t.Fatalf("受限巡检的范围描述应为 scope:mine，实际 %q", scoped.Scope)
	}

	// 空节点集合 = 受限但无可见节点：恒空（不能退化成"检全部"）
	empty, err := svc.RunInspect(InspectScope{Filter: ListFilter{Nodes: []string{}}}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	if empty.Assets != 0 || empty.Baselined != 0 {
		t.Fatalf("无可见节点时应检 0 个资产：%+v", empty)
	}
}

// 巡检记录与差异项必须能按记录检索：界面靠它回答"上次检了什么、差在哪"。
func TestInspectRunsAndFindingsAreQueryable(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_700_000_400_000 }
	if _, err := svc.RunInspect(InspectScope{Filter: ListFilter{TypeKey: TypeHost}}, "alice"); err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	runs, err := svc.InspectRuns(0)
	if err != nil {
		t.Fatalf("列出巡检记录失败: %v", err)
	}
	if len(runs) != 1 || runs[0].Scope != "type:"+TypeHost {
		t.Fatalf("巡检记录不符：%+v", runs)
	}
	// 非法记录 ID 必须报错，而不是静默返回空差异
	if _, err := svc.InspectFindings(0, 0); err == nil {
		t.Fatal("空记录 ID 应报错")
	}
}

func TestInspectScopedHistorySurvivesReopenAndUsesCurrentAssetNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assets.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store)
	for _, name := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpu": "4"})); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.RunInspect(InspectScope{}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpu": "8"})); err != nil {
			t.Fatal(err)
		}
	}
	second, err := svc.RunInspect(InspectScope{}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc = NewService(store)
	runs, err := svc.InspectRunsInNodes(10, []string{"web-01"})
	if err != nil || len(runs) != 2 || runs[0].ID != second.ID || runs[0].Assets != 1 || runs[0].Findings != 1 ||
		runs[1].ID != first.ID || runs[1].Baselined != 1 {
		t.Fatalf("reopened scoped runs: %+v, %v", runs, err)
	}
	item, found, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil || !found {
		t.Fatalf("asset: %v %v", found, err)
	}
	if _, err := store.db.Exec(`UPDATE assets SET node='db-01' WHERE id=?`, item.ID); err != nil {
		t.Fatal(err)
	}
	runs, err = svc.InspectRunsInNodes(10, []string{"web-01"})
	if err != nil || len(runs) != 0 {
		t.Fatalf("moved asset remained visible: %+v %v", runs, err)
	}
	runs, err = svc.InspectRunsInNodes(10, []string{"db-01"})
	if err != nil || len(runs) != 2 || runs[0].Assets != 2 || runs[0].Findings != 2 {
		t.Fatalf("new scope did not include moved asset: %+v %v", runs, err)
	}
}

func TestInspectLegacyRunAndZeroAssetManifestDiffer(t *testing.T) {
	svc, store := newTestService(t)
	if _, err := store.db.Exec(`INSERT INTO inspect_runs(scope,actor,started_at,assets,baselined,findings,truncated) VALUES('node:web-01','old',1,1,0,1,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO inspect_findings(run_id,asset_id,asset_type,asset_key,asset_name,node,field,kind,level,expected,actual,at)
		VALUES(1,1,'host','web-01','web-01','web-01','cpu','changed','warning','4','8',1)`); err != nil {
		t.Fatal(err)
	}
	zero, err := svc.RunInspect(InspectScope{Filter: ListFilter{Nodes: []string{}}}, "admin")
	if err != nil || zero.Assets != 0 {
		t.Fatalf("zero run: %+v %v", zero, err)
	}
	visible, err := svc.InspectRunsInNodes(10, []string{"web-01"})
	if err != nil || len(visible) != 0 {
		t.Fatalf("old/zero run leaked: %+v %v", visible, err)
	}
	if _, ok, err := svc.InspectFindingsInNodes(1, 10, []string{"web-01"}); err != nil || ok {
		t.Fatalf("old finding visible: %v %v", ok, err)
	}
	all, err := svc.InspectRuns(10)
	if err != nil || len(all) != 2 {
		t.Fatalf("global legacy history: %+v %v", all, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM inspect_run_manifest WHERE run_id=?`, zero.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new zero run missing manifest: %d %v", count, err)
	}
}

func TestBaselineConditionalWritesRejectMovedAsset(t *testing.T) {
	svc, store := newTestService(t)
	for _, name := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpu": "4"})); err != nil {
			t.Fatal(err)
		}
	}
	web, _, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE assets SET node='db-01' WHERE id=?`, web.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetBaselineIfCurrent(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, "ops1", 0, []string{"web-01"}); !errors.Is(err, ErrBaselineChanged) {
		t.Fatalf("moved candidate accepted: %v", err)
	}
	if _, err := svc.SetBaseline(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ClearBaselineIfCurrent(TypeHost, web.ID, []string{"web-01"}); !errors.Is(err, ErrBaselineChanged) {
		t.Fatalf("moved baseline deleted: %v", err)
	}
}

// TestBaselineConditionalWritesRejectEmptyScope 「受限但无任何可见节点」必须报范围错误，
// 不能退化成 ErrBaselineChanged：后者会让用户反复刷新重试一个永远不会成功的操作。
func TestBaselineConditionalWritesRejectEmptyScope(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "4"})); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetBaselineIfCurrent(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, "ops1", 0, []string{}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("empty scope should be reported as out of scope: %v", err)
	}
	if err := svc.ClearBaselineIfCurrent(TypeHost, 1, []string{}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("empty scope clear should be out of scope: %v", err)
	}
	// 全局（nil）与有可见节点两种情形不受影响
	if _, err := svc.SetBaselineIfCurrent(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}, "ops1", 0, nil); err != nil {
		t.Fatalf("global scope should succeed: %v", err)
	}
}

func mustFindings(t *testing.T, svc *Service, runID int64) []InspectFinding {
	t.Helper()
	fs, err := svc.InspectFindings(runID, 0)
	if err != nil {
		t.Fatalf("读取差异项失败: %v", err)
	}
	return fs
}

func hasFinding(fs []InspectFinding, field string, kind FindingKind, level FindingLevel) bool {
	for _, f := range fs {
		if f.Field == field && f.Kind == kind && f.Level == level {
			return true
		}
	}
	return false
}

// 忽略必须「只隐藏、不停止采集」：列表与摘要默认不计入，但恢复后能看到最新状态；
// 采集上报也不能把它"复活"（忽略是用户的决定，不该被下一轮上报冲掉）。
func TestIgnoreHidesButKeepsCollecting(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_500_000)
	svc.now = func() int64 { return now }
	for _, name := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(name, map[string]string{"cpu": "8"})); err != nil {
			t.Fatalf("写入 %s 失败: %v", name, err)
		}
	}

	ref := Ref{TypeKey: TypeHost, NaturalKey: "web-01"}
	ignored, err := svc.Ignore(ref, "alice", "已下线，采集配置未清理")
	if err != nil {
		t.Fatalf("忽略失败: %v", err)
	}
	if !ignored.Ignored || ignored.IgnoredBy != "alice" || ignored.IgnoreReason == "" {
		t.Fatalf("忽略标记与理由应落库：%+v", ignored)
	}

	// 默认列表隐藏；with 可见；only 只剩它
	def, err := svc.List(ListFilter{})
	if err != nil || len(def) != 1 || def[0].NaturalKey != "db-01" {
		t.Fatalf("默认列表应隐藏已忽略资产：%+v err=%v", def, err)
	}
	with, err := svc.List(ListFilter{Ignored: IgnoreFilterWith})
	if err != nil || len(with) != 2 {
		t.Fatalf("含已忽略应返回 2 条：%d err=%v", len(with), err)
	}
	only, err := svc.List(ListFilter{Ignored: IgnoreFilterOnly})
	if err != nil || len(only) != 1 || only[0].NaturalKey != "web-01" || !only[0].Ignored {
		t.Fatalf("只看已忽略应只剩 web-01：%+v err=%v", only, err)
	}

	stats, err := svc.Stats(ListFilter{}, now)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if stats.Total != 1 || stats.Ignored != 1 {
		t.Fatalf("摘要应 total=1 ignored=1（已忽略不计入总数）：%+v", stats)
	}

	// 采集继续：属性照常刷新，但忽略标记不被冲掉
	now += 60_000
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "16"})); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	got, ok, err := svc.Get(ref)
	if err != nil || !ok {
		t.Fatalf("读取失败: ok=%v err=%v", ok, err)
	}
	if !got.Ignored {
		t.Fatal("采集上报不得把已忽略资产复活（忽略是用户的决定）")
	}
	if v, _ := got.ValueFrom("cpu", SourceDiscovery); v != "16" {
		t.Fatalf("已忽略资产的属性仍应继续刷新，实际 cpu=%q", v)
	}

	// 恢复：回到默认列表，摘要的已忽略数归零
	restored, err := svc.Restore(ref, "alice")
	if err != nil || restored.Ignored {
		t.Fatalf("恢复失败: %+v err=%v", restored, err)
	}
	after, err := svc.List(ListFilter{})
	if err != nil || len(after) != 2 {
		t.Fatalf("恢复后应回到列表里：%d err=%v", len(after), err)
	}
	stats2, _ := svc.Stats(ListFilter{}, now)
	if stats2.Ignored != 0 {
		t.Fatalf("恢复后不应还有已忽略资产：%+v", stats2)
	}
	// 幂等：重复恢复不报错
	if _, err := svc.Restore(ref, "alice"); err != nil {
		t.Fatalf("重复恢复应幂等成功: %v", err)
	}
}

// 彻底删除只对「纯人工建档」资产开放：采集资产删了会被下一轮上报重建（表现为"删了又回来"）。
func TestPurgeOnlyManualAssets(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1_700_000_600_000 }

	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("写入采集资产失败: %v", err)
	}
	manual := Observation{
		TypeKey: TypeHost, NaturalKey: "manual-01", Name: "手工台账设备", Node: "web-01",
		Source: SourceManual, Attrs: map[string]string{"vendor": "Dell"},
	}
	if _, _, err := svc.Apply(manual); err != nil {
		t.Fatalf("写入人工资产失败: %v", err)
	}
	// 把它设为该类型标杆，验证删除时标杆会被一并清除（否则标杆指向不存在的资产）
	if _, err := svc.SetBaseline(Ref{TypeKey: TypeHost, NaturalKey: "manual-01"}, "alice"); err != nil {
		t.Fatalf("设置标杆失败: %v", err)
	}

	if _, err := svc.Purge(Ref{TypeKey: TypeHost, NaturalKey: "web-01"}); err == nil {
		t.Fatal("采集资产不应允许彻底删除（应提示改用忽略）")
	}
	cleared, err := svc.Purge(Ref{TypeKey: TypeHost, NaturalKey: "manual-01"})
	if err != nil {
		t.Fatalf("人工资产应可彻底删除: %v", err)
	}
	if !cleared {
		t.Fatal("该资产是标杆，删除时应报告已清除标杆")
	}
	if _, ok, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "manual-01"}); err != nil || ok {
		t.Fatalf("删除后应查不到: ok=%v err=%v", ok, err)
	}
	bl, err := svc.Baselines()
	if err != nil || len(bl) != 0 {
		t.Fatalf("标杆应随资产删除被清除：%+v err=%v", bl, err)
	}
}

// 标签：与属性分开管理，可筛选、进变更历史，且不参与巡检比对。
func TestLabelsSetFilterHistory(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_700_000)
	svc.now = func() int64 { return now }
	ref := Ref{TypeKey: TypeHost, NaturalKey: "web-01"}
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	got, err := svc.SetLabels(ref, map[string]string{"env": "prod", "team": "sre"}, nil, "alice")
	if err != nil {
		t.Fatalf("写标签失败: %v", err)
	}
	if got.Labels["env"] != "prod" || got.Labels["team"] != "sre" {
		t.Fatalf("标签未落库：%+v", got.Labels)
	}

	// 筛选：key:value 精确匹配；只给 key 表示"存在该标签"
	if n, err := svc.Count(ListFilter{Label: "env:prod"}); err != nil || n != 1 {
		t.Fatalf("按 env:prod 应筛出 1 条：n=%d err=%v", n, err)
	}
	if n, err := svc.Count(ListFilter{Label: "env:test"}); err != nil || n != 0 {
		t.Fatalf("按 env:test 应筛出 0 条：n=%d err=%v", n, err)
	}
	if n, err := svc.Count(ListFilter{Label: "team"}); err != nil || n != 1 {
		t.Fatalf("按 team 应筛出 1 条：n=%d err=%v", n, err)
	}

	// 变更历史：值与原先相同不写记录，改了才写（字段名带 label: 前缀）
	now += 1000
	if _, err := svc.SetLabels(ref, map[string]string{"env": "prod"}, nil, "alice"); err != nil {
		t.Fatalf("重复写同值失败: %v", err)
	}
	hist, err := svc.History(ref, 0)
	if err != nil {
		t.Fatalf("查询历史失败: %v", err)
	}
	countLabel := func(field string) int {
		n := 0
		for _, h := range hist {
			if h.Field == field {
				n++
			}
		}
		return n
	}
	if n := countLabel("label:env"); n != 1 {
		t.Fatalf("同值重复写入不应产生新记录，label:env 记录数=%d", n)
	}

	now += 1000
	if _, err := svc.SetLabels(ref, map[string]string{"env": "test"}, []string{"team"}, "alice"); err != nil {
		t.Fatalf("改标签失败: %v", err)
	}
	hist, _ = svc.History(ref, 0)
	// 历史按时间倒序（at DESC, id DESC），因此同一字段的**首次出现即最新一条**。
	newest := func(field string) *ChangeRecord {
		for i := range hist {
			if hist[i].Field == field {
				return &hist[i]
			}
		}
		return nil
	}
	envChange := newest("label:env")
	if envChange == nil || envChange.Old != "prod" || envChange.New != "test" || envChange.Actor != "alice" {
		t.Fatalf("标签变更应进历史（含旧值/新值与操作人）：%+v", envChange)
	}
	teamChange := newest("label:team")
	if teamChange == nil || teamChange.New != "" {
		t.Fatalf("删除标签应记一条新值为空的记录：%+v", teamChange)
	}
	got, _, _ = svc.Get(ref)
	if _, exists := got.Labels["team"]; exists {
		t.Fatal("删除的标签不应还在")
	}

	// 非法输入：空键名、同时写入与删除
	if _, err := svc.SetLabels(ref, map[string]string{"  ": "x"}, nil, "alice"); err == nil {
		t.Fatal("空标签键应报错")
	}
	if _, err := svc.SetLabels(ref, map[string]string{"env": "x"}, []string{"env"}, "alice"); err == nil {
		t.Fatal("同一键同时写入与删除应报错")
	}

	// 巡检不受标签影响：标签不是配置项，不该出现在差异项里
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "8"})); err != nil {
		t.Fatalf("上报失败: %v", err)
	}
	if _, err := svc.RunInspect(InspectScope{}, "alice"); err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	now += 1000
	if _, err := svc.SetLabels(ref, map[string]string{"env": "prod"}, nil, "alice"); err != nil {
		t.Fatalf("改标签失败: %v", err)
	}
	run, err := svc.RunInspect(InspectScope{}, "alice")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	for _, f := range mustFindings(t, svc, run.ID) {
		if strings.HasPrefix(f.Field, "label:") {
			t.Fatalf("标签不是配置项，不应产出差异项：%+v", f)
		}
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

// 值长期不变的资产也必须刷新「最近上报」。
//
// 依据：真实反馈「中间件资产全部显示失联」。中间件的 version / topology / up 等属性几乎不变，
// 而 Apply 只在值变化时才写属性行——若「最近上报」取自属性行的 updated_at，它就会永久冻结在
// 最后一次变更时刻，超阈值后整批资产被误判为失联（主机同样如此：os / cpuCores 也不变）。
func TestApplyRefreshesLastSeenEvenWhenValuesUnchanged(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }

	ob := Observation{
		TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379", Name: "dev-redis",
		Node: "web-01", Attrs: map[string]string{"version": "6.2.6", "topology": "standalone"},
	}
	first, created, err := svc.Apply(ob)
	if err != nil || !created {
		t.Fatalf("首次上报失败: created=%v err=%v", created, err)
	}
	if first.LastSeenAt() != now {
		t.Fatalf("首次上报后最近上报应为 %d，实际 %d", now, first.LastSeenAt())
	}

	// 一小时后再上报，值完全没变。
	now += 3600_000
	second, created, err := svc.Apply(ob)
	if err != nil || created {
		t.Fatalf("重复上报失败: created=%v err=%v", created, err)
	}
	if second.LastSeenAt() != now {
		t.Fatalf("值没变也必须刷新最近上报：期望 %d，实际 %d", now, second.LastSeenAt())
	}

	// 但不能因为刷新时间而制造变更记录噪声：仍然只有一条建档记录。
	hist, err := svc.History(Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(hist) != 1 || hist[0].Kind != ChangeInitial {
		t.Fatalf("值没变不应产生变更记录，实际 %+v", hist)
	}
}

// 失联判定必须基于「最近上报」，而不是属性的最后变更时刻。
func TestListStatusUsesLastSeenNotValueChange(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }
	attrs := map[string]string{"os": "Ubuntu 24.04"}
	if _, _, err := svc.Apply(hostObservation("web-01", attrs)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	// 40 分钟后再上报（值没变）；失联阈值为 30 分钟 → 仍应算在线。
	now += 40 * 60_000
	if _, _, err := svc.Apply(hostObservation("web-01", attrs)); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	staleBefore := now - 30*60_000

	if n, err := svc.Count(ListFilter{Status: StatusMissing, StaleBefore: staleBefore}); err != nil || n != 0 {
		t.Fatalf("值长期不变的资产不应被判失联：n=%d err=%v", n, err)
	}
	if n, err := svc.Count(ListFilter{Status: StatusOnline, StaleBefore: staleBefore}); err != nil || n != 1 {
		t.Fatalf("应为在线 1 条：n=%d err=%v", n, err)
	}
	// 摘要的「失联」必须与列表同源：这里是 0。
	stats, err := svc.Stats(ListFilter{StaleBefore: staleBefore}, 0)
	if err != nil {
		t.Fatalf("统计摘要失败: %v", err)
	}
	if stats.Missing != 0 || stats.Total != 1 {
		t.Fatalf("摘要应为 total=1 missing=0，实际 %+v", stats)
	}
}

// 主机名大小写不敏感：K8s 的节点名按 RFC 1123 一律小写，而 Agent 上报的 hostname 保留系统原样。
// 两者关联时必须折叠大小写，否则同一台机器被判成两台（Pod 的 runs_on 与资源范围归属一起落空）。
func TestGetHostByNameFoldsCase(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Apply(hostObservation("VM-0-10-ubuntu", nil)); err != nil {
		t.Fatalf("写入主机失败: %v", err)
	}

	got, ok, err := svc.GetHostByName("vm-0-10-ubuntu")
	if err != nil || !ok {
		t.Fatalf("大小写不同也应找到：ok=%v err=%v", ok, err)
	}
	if got.NaturalKey != "VM-0-10-ubuntu" {
		t.Fatalf("应返回台账里的规范键（调用方要拿它当归属节点），实际 %q", got.NaturalKey)
	}

	// 不能过度匹配：不同的主机名、空名都必须找不到
	for _, bad := range []string{"vm-0-10-ubuntu-2", "vm-0-10", ""} {
		if _, found, err := svc.GetHostByName(bad); err != nil || found {
			t.Fatalf("主机名 %q 不该命中：found=%v err=%v", bad, found, err)
		}
	}
}

// 短命类型（容器 / 工作负载）默认不进列表与摘要。
//
// 理由不是"少显示几行"，而是治理数字会被 churn 冲垮：被滚动更新替换掉的旧 Pod
// 停止上报后会被判失联，计入之后顶部会出现"失联 200"，把真实故障埋掉；
// 而"无责任人"对短命对象也没有意义——没人会为滚动更新掉的 Pod 指派责任人。
func TestExcludeTypesKeepsEphemeralOutOfListAndStats(t *testing.T) {
	svc, _ := newTestService(t)
	now := int64(1_700_000_000_000)
	svc.now = func() int64 { return now }

	mustApply := func(ob Observation) {
		t.Helper()
		if _, _, err := svc.Apply(ob); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	mustApply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: SourceDiscovery, Attrs: map[string]string{"owner": "张三"},
	})
	mustApply(Observation{
		TypeKey: TypePod, NaturalKey: PodNaturalKey("c1", "default", "web-1"), Name: "web-1",
		Node: "web-01", Source: SourceDiscovery, Attrs: map[string]string{"restarts": "0"},
	})
	mustApply(Observation{
		TypeKey: TypeWorkload, NaturalKey: WorkloadNaturalKey("c1", "default", "deployment", "web"),
		Name: "web", Node: "web-01", Source: SourceDiscovery,
	})

	// 默认视图（带排除）：只剩主机。列表、计数、摘要必须同时只剩主机——
	// 三者共用同一处 WHERE（assetWhere），否则会出现「摘要说 1、列表有 3 条」。
	exclude := ListFilter{ExcludeTypes: EphemeralTypes(), StaleBefore: now - 30*60_000}
	items, err := svc.List(exclude)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(items) != 1 || items[0].TypeKey != TypeHost {
		t.Fatalf("默认视图应只剩主机，实际 %+v", items)
	}
	if n, err := svc.Count(exclude); err != nil || n != 1 {
		t.Fatalf("计数应与列表同源（1），实际 n=%d err=%v", n, err)
	}
	stats, err := svc.Stats(exclude, 0)
	if err != nil {
		t.Fatalf("摘要失败: %v", err)
	}
	if stats.Total != 1 {
		t.Fatalf("摘要总数应排除短命类型（1），实际 %+v", stats)
	}

	// 显式按类型查（调用方不设排除）：仍然拿得到——用户明确要看某一类时，
	// 再把它排除掉是自相矛盾的。
	pods, err := svc.List(ListFilter{TypeKey: TypePod})
	if err != nil || len(pods) != 1 || pods[0].TypeKey != TypePod {
		t.Fatalf("显式按类型查容器应返回 1 条，实际 %d err=%v", len(pods), err)
	}
}
