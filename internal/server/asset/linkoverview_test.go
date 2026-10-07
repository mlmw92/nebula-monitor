package asset

import "testing"

// 关系总览与筛选子图的用例（批次 22 的全库视图）。
//
// 最要紧的一条是**范围裁剪对边的两端都生效**：只判一端，受限用户就能从"计数"或"图上多出来的
// 一个节点"里看出范围外资产的存在与规模。因此这里的负例比正例重要。

// linkFixture 造一套够用的关系数据：
//
//	web-01(biz=pay) 上有 redis:10.0.0.1:6379(biz=pay)
//	db-01(biz=risk) 上有 redis:10.0.0.2:6379(biz=risk)
//	边：inst1 --runs_on--> web-01、inst2 --runs_on--> db-01、inst2 --depends_on--> inst1（跨机）
//	另有一台没有任何关系的 host:lonely-01(biz=pay)
func linkFixture(t *testing.T) (*Service, Ref, Ref) {
	t.Helper()
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }

	hosts := map[string]string{"web-01": "pay", "db-01": "risk", "lonely-01": "pay"}
	for name, biz := range hosts {
		if _, _, err := svc.Apply(hostObservation(name, nil)); err != nil {
			t.Fatalf("建档 %s 失败: %v", name, err)
		}
		if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: name},
			map[string]string{"biz": biz}, nil, "alice", ""); err != nil {
			t.Fatalf("打标签失败: %v", err)
		}
	}
	insts := []struct{ addr, host, biz string }{
		{"10.0.0.1:6379", "web-01", "pay"},
		{"10.0.0.2:6379", "db-01", "risk"},
	}
	for _, it := range insts {
		ref := Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:" + it.addr}
		if _, _, err := svc.Apply(Observation{
			TypeKey: TypeMiddlewareInst, NaturalKey: "redis:" + it.addr, Name: "redis",
			Node: it.host, Source: SourceDiscovery, Actor: "agent",
		}); err != nil {
			t.Fatalf("建档实例失败: %v", err)
		}
		if _, err := svc.SetLabels(ref, map[string]string{"biz": it.biz}, nil, "alice", ""); err != nil {
			t.Fatalf("给实例打标签失败: %v", err)
		}
		if err := svc.LinkDiscovered(ref, Ref{TypeKey: TypeHost, NaturalKey: it.host}, LinkRunsOn); err != nil {
			t.Fatalf("建宿主边失败: %v", err)
		}
	}
	inst1 := Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:10.0.0.1:6379"}
	inst2 := Ref{TypeKey: TypeMiddlewareInst, NaturalKey: "redis:10.0.0.2:6379"}
	if err := svc.LinkDiscovered(inst2, inst1, LinkDependsOn); err != nil {
		t.Fatalf("建依赖边失败: %v", err)
	}
	return svc, inst1, inst2
}

func TestLinkOverviewGroupsAndCounts(t *testing.T) {
	svc, _, _ := linkFixture(t)

	ov, err := svc.LinkOverview(ChangeScope{})
	if err != nil {
		t.Fatalf("取关系总览失败: %v", err)
	}
	if ov.TotalLinks != 3 {
		t.Fatalf("全局应有 3 条边，实际 %d", ov.TotalLinks)
	}
	// 分组按 (来源类型, 种类, 目标类型, 来源) 聚合：这里是 instance→host runs_on ×2 与
	// instance→instance depends_on ×1。
	byKey := map[string]LinkStat{}
	for _, g := range ov.Groups {
		byKey[g.FromType+"|"+string(g.Kind)+"|"+g.ToType] = g
	}
	run := byKey[TypeMiddlewareInst+"|"+string(LinkRunsOn)+"|"+TypeHost]
	if run.Count != 2 {
		t.Fatalf("runs_on 分组应有 2 条，实际 %d", run.Count)
	}
	if run.Source != SourceDiscovery {
		t.Fatalf("这组边的来源应为 discovery，实际 %q", run.Source)
	}
	// 样本边必须带来两端的自然键（"这行到底是什么"不点进去就能看懂）
	if run.Sample.FromKey == "" || run.Sample.ToKey == "" {
		t.Fatalf("分组应带样本边，实际 %#v", run.Sample)
	}
	dep := byKey[TypeMiddlewareInst+"|"+string(LinkDependsOn)+"|"+TypeMiddlewareInst]
	if dep.Count != 1 {
		t.Fatalf("depends_on 分组应有 1 条，实际 %d", dep.Count)
	}
	// 无边资产：lonely-01 一台
	if ov.AssetsWithoutLinks != 1 {
		t.Fatalf("无边资产应为 1（lonely-01），实际 %d", ov.AssetsWithoutLinks)
	}
}

func TestLinkOverviewScopesBothEnds(t *testing.T) {
	svc, _, _ := linkFixture(t)

	// ① 节点维度限定在 web-01：只有"两端都在 web-01"的边算数——跨机的 depends_on 一条都不该计入。
	ov, err := svc.LinkOverview(ChangeScope{Nodes: []string{"web-01"}})
	if err != nil {
		t.Fatalf("取关系总览失败: %v", err)
	}
	if ov.TotalLinks != 1 {
		t.Fatalf("web-01 范围内应只有 1 条边（实例→本机），实际 %d", ov.TotalLinks)
	}
	for _, g := range ov.Groups {
		if g.Kind == LinkDependsOn {
			t.Fatalf("跨机的依赖边不该出现在单节点范围里：%#v", g)
		}
	}
	// 无边资产：web-01 与它上面的实例都有边；lonely-01 不在范围内 → 计数里没有它
	if ov.AssetsWithoutLinks != 0 {
		t.Fatalf("web-01 范围内无边资产应为 0（lonely-01 不在范围内），实际 %d", ov.AssetsWithoutLinks)
	}

	// ② 业务标签维度限定 biz=pay：能看见的资产是 web-01 / inst1 / lonely-01
	//    → 边只应剩 inst1→web-01 那一条（inst2 与 db-01 都是 risk，两端都不可见）。
	ov, err = svc.LinkOverview(ChangeScope{LabelSelectors: []LabelSelector{{Key: "biz", Value: "pay"}}})
	if err != nil {
		t.Fatalf("取关系总览失败: %v", err)
	}
	if ov.TotalLinks != 1 {
		t.Fatalf("biz=pay 范围内应只有 1 条边，实际 %d", ov.TotalLinks)
	}
	if ov.AssetsWithoutLinks != 1 {
		t.Fatalf("biz=pay 范围内无边资产应为 1（lonely-01），实际 %d", ov.AssetsWithoutLinks)
	}

	// ③ 受限但没有任何选择器：恒不匹配（绝不退化成"不过滤"）
	ov, err = svc.LinkOverview(ChangeScope{LabelSelectors: []LabelSelector{}})
	if err != nil {
		t.Fatalf("取关系总览失败: %v", err)
	}
	if ov.TotalLinks != 0 || ov.AssetsWithoutLinks != 0 {
		t.Fatalf("空选择器应恒不匹配，实际 links=%d without=%d", ov.TotalLinks, ov.AssetsWithoutLinks)
	}
}

func TestTopologyByFilterDimensions(t *testing.T) {
	svc, inst1, inst2 := linkFixture(t)
	// 再来一条人工边（用于验 source 筛选）
	if err := svc.LinkManual(Ref{TypeKey: TypeHost, NaturalKey: "lonely-01"}, inst1, LinkDependsOn); err != nil {
		t.Fatalf("建人工边失败: %v", err)
	}

	cases := []struct {
		name    string
		flt     TopologyFilter
		wantEdg int
	}{
		{"不筛：4 条边", TopologyFilter{}, 4},
		{"按种类：只 depends_on", TopologyFilter{Kinds: []LinkKind{LinkDependsOn}}, 2},
		{"按来源：只人工边", TopologyFilter{Sources: []Source{SourceManual}}, 1},
		{"按类型：主机参与的边（两端任一）",
			TopologyFilter{Types: []string{TypeHost}}, 3},
		// web-01 参与的边：inst1→web-01、inst2→inst1（目标在 web-01）、lonely-01→inst1
		{"按节点：web-01 参与的边",
			TopologyFilter{Nodes: []string{"web-01"}}, 3},
		{"叠加：depends_on 且人工",
			TopologyFilter{Kinds: []LinkKind{LinkDependsOn}, Sources: []Source{SourceManual}}, 1},
		{"叠加：depends_on 且主机参与",
			TopologyFilter{Kinds: []LinkKind{LinkDependsOn}, Types: []string{TypeHost}}, 1},
		{"互斥叠加：runs_on 且人工 = 空",
			TopologyFilter{Kinds: []LinkKind{LinkRunsOn}, Sources: []Source{SourceManual}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flt := tc.flt
			g, err := svc.TopologyByFilter(flt)
			if err != nil {
				t.Fatalf("取子图失败: %v", err)
			}
			if len(g.Edges) != tc.wantEdg {
				t.Fatalf("边数 = %d，期望 %d（total=%d）", len(g.Edges), tc.wantEdg, g.Total)
			}
			if g.Total != tc.wantEdg {
				t.Fatalf("total 应等于符合筛选的边数 %d，实际 %d", tc.wantEdg, g.Total)
			}
			if g.Truncated {
				t.Fatal("这么小的图不该标记截断")
			}
			// 每个节点都必须真的被某条边用到（不能带出孤立节点）
			used := map[string]bool{}
			for _, e := range g.Edges {
				used[e.From.TypeKey+"|"+e.From.NaturalKey] = true
				used[e.To.TypeKey+"|"+e.To.NaturalKey] = true
			}
			for _, n := range g.Nodes {
				if !used[n.Asset.TypeKey+"|"+n.Asset.NaturalKey] {
					t.Fatalf("节点 %s 没有任何边用到", n.Asset.NaturalKey)
				}
			}
		})
	}
	// 依赖边的方向必须原样保留（副本 → 主库），筛选不该改动它
	g, err := svc.TopologyByFilter(TopologyFilter{Kinds: []LinkKind{LinkDependsOn}, Sources: []Source{SourceDiscovery}})
	if err != nil {
		t.Fatalf("取子图失败: %v", err)
	}
	if len(g.Edges) != 1 || g.Edges[0].From != inst2 || g.Edges[0].To != inst1 {
		t.Fatalf("依赖边方向应保持 inst2 → inst1，实际 %#v", g.Edges)
	}
}

func TestTopologyByFilterRespectsScopeOnBothEnds(t *testing.T) {
	svc, _, _ := linkFixture(t)

	// 节点限定 web-01：跨机的依赖边两端不同节点，应被整条排除
	g, err := svc.TopologyByFilter(TopologyFilter{Scope: ChangeScope{Nodes: []string{"web-01"}}})
	if err != nil {
		t.Fatalf("取子图失败: %v", err)
	}
	if len(g.Edges) != 1 || g.Total != 1 {
		t.Fatalf("web-01 范围内应只有 1 条边，实际 edges=%d total=%d", len(g.Edges), g.Total)
	}
	for _, n := range g.Nodes {
		if n.Asset.Node != "web-01" {
			t.Fatalf("范围内不该出现别的节点的资产：%s（node=%s）", n.Asset.NaturalKey, n.Asset.Node)
		}
	}

	// 空选择器：恒不匹配
	g, err = svc.TopologyByFilter(TopologyFilter{Scope: ChangeScope{LabelSelectors: []LabelSelector{}}})
	if err != nil {
		t.Fatalf("取子图失败: %v", err)
	}
	if len(g.Edges) != 0 || g.Total != 0 {
		t.Fatalf("空选择器应恒不匹配，实际 edges=%d total=%d", len(g.Edges), g.Total)
	}
}

// 上限：取满时必须显式回报截断，并且 total 要说清"一共有多少条"。
func TestTopologyByFilterLimits(t *testing.T) {
	svc, _, _ := linkFixture(t)

	g, err := svc.TopologyByFilter(TopologyFilter{MaxEdges: 2})
	if err != nil {
		t.Fatalf("取子图失败: %v", err)
	}
	if len(g.Edges) != 2 {
		t.Fatalf("MaxEdges=2 应返回 2 条，实际 %d", len(g.Edges))
	}
	if !g.Truncated {
		t.Fatal("被边数上限截断时必须标记 truncated")
	}
	if g.Total != 3 {
		t.Fatalf("total 应是符合筛选的边总数 3（不受上限影响），实际 %d", g.Total)
	}

	// 节点上限同样生效：3 条边涉及 4 个资产，限到 3 个节点时至少要标截断
	g, err = svc.TopologyByFilter(TopologyFilter{MaxNodes: 3})
	if err != nil {
		t.Fatalf("取子图失败: %v", err)
	}
	if len(g.Nodes) > 3 {
		t.Fatalf("节点数不该超过上限，实际 %d", len(g.Nodes))
	}
	if !g.Truncated {
		t.Fatal("节点数触到上限时必须标记 truncated")
	}
	for _, e := range g.Edges {
		for _, end := range []Ref{e.From, e.To} {
			found := false
			for _, n := range g.Nodes {
				if n.Asset.TypeKey == end.TypeKey && n.Asset.NaturalKey == end.NaturalKey {
					found = true
				}
			}
			if !found {
				t.Fatalf("边 %s 的端点被节点上限挡在外面，这条边不该留在结果里", end.NaturalKey)
			}
		}
	}
}
