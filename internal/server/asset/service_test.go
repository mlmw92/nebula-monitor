package asset

import (
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
