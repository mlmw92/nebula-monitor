package asset

import "testing"

// 「变更 ↔ 审计」关联在存储/服务层的用例。
//
// 关注点有三条，且都不在"正常路径能不能跑通"，而在边界上：
//   - 采集侧写的变更**不能**带关联 id：它没有对应的接口调用，编一个出来就是假证据；
//   - 按关联 id 查变更必须**与台账同一套范围**（节点 + 业务标签），少一个维度就是越权；
//   - 空关联 id 必须被拒绝：它对应采集侧的全部变更，放行等于把整张表当"这一次操作"。

func TestChangesCarryRequestIDOnlyForManualWrites(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }

	// 采集侧建档：无关联 id。
	if _, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpuCores": "8"})); err != nil {
		t.Fatalf("采集建档失败: %v", err)
	}
	ref := Ref{TypeKey: TypeHost, NaturalKey: "web-01"}
	for _, rec := range mustHistory(t, svc, ref) {
		if rec.RequestID != "" {
			t.Fatalf("采集侧变更不该带关联 id，field=%s 实际 %q", rec.Field, rec.RequestID)
		}
	}

	// 人工操作的四条写入路径都要带上：建档 / 属性 / 标签 / 删除人工值。
	const rid = "3f2a91c4d5e60718"
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: SourceManual, Actor: "alice", RequestID: rid,
		Attrs: map[string]string{"cpuCores": "16"},
	}); err != nil {
		t.Fatalf("人工修改属性失败: %v", err)
	}
	if _, err := svc.SetLabels(ref, map[string]string{"env": "prod"}, nil, "alice", rid); err != nil {
		t.Fatalf("人工打标签失败: %v", err)
	}
	if _, err := svc.ResetManual(ref, []string{"cpuCores"}, "alice", rid); err != nil {
		t.Fatalf("恢复采集值失败: %v", err)
	}

	manual := 0
	for _, rec := range mustHistory(t, svc, ref) {
		if rec.Source != SourceManual {
			continue
		}
		manual++
		if rec.RequestID != rid {
			t.Fatalf("人工变更 %s 应带关联 id %q，实际 %q", rec.Field, rid, rec.RequestID)
		}
	}
	if manual < 3 {
		t.Fatalf("应有建档/属性/标签/恢复中的多条人工变更，实际只见到 %d 条", manual)
	}

	// 反向入口：按关联 id 取到的就是刚才这几次人工改动。
	items, truncated, err := svc.ChangesByRequest(rid, ChangeScope{}, 0)
	if err != nil {
		t.Fatalf("按关联 id 查变更失败: %v", err)
	}
	if truncated {
		t.Fatal("这么少的记录不该被截断")
	}
	if len(items) != manual {
		t.Fatalf("反向查到 %d 条，人工变更共 %d 条", len(items), manual)
	}
	for _, it := range items {
		if it.NaturalKey != "web-01" || it.TypeKey != TypeHost {
			t.Fatalf("反向查询应带资产身份，实际 %s/%s", it.TypeKey, it.NaturalKey)
		}
	}
}

func TestChangesByRequestRejectsEmptyID(t *testing.T) {
	svc, _ := newTestService(t)
	// 空 id 对应的是采集侧写的全部变更：放行就等于把整张变更表当成"这一次操作"。
	for _, bad := range []string{"", "   "} {
		if _, _, err := svc.ChangesByRequest(bad, ChangeScope{}, 0); err == nil {
			t.Fatalf("关联 id %q 应被拒绝", bad)
		}
	}
}

func TestChangesByRequestRespectsAssetScope(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	const rid = "aabbccddeeff0011"

	for _, host := range []string{"web-01", "db-01"} {
		if _, _, err := svc.Apply(hostObservation(host, nil)); err != nil {
			t.Fatalf("准备 %s 失败: %v", host, err)
		}
	}
	if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: "web-01"},
		map[string]string{"biz": "pay"}, nil, "alice", rid); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}
	if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: "db-01"},
		map[string]string{"biz": "risk"}, nil, "alice", rid); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}

	// 不设范围：两条都要看得见（说明过滤确实是范围造成的，而不是查询本身丢了数据）。
	all, _, err := svc.ChangesByRequest(rid, ChangeScope{}, 0)
	if err != nil {
		t.Fatalf("查变更失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("不设范围应看到 2 条，实际 %d", len(all))
	}

	// 业务范围 biz=pay：只剩 web-01 那一条。
	// 这一条是安全边界——受限用户能从"这次操作改了什么"里看到范围外资产的字段值，
	// 等于绕开了资产详情那条路。
	scoped, _, err := svc.ChangesByRequest(rid, ChangeScope{
		LabelSelectors: []LabelSelector{{Key: "biz", Value: "pay"}},
	}, 0)
	if err != nil {
		t.Fatalf("按范围查变更失败: %v", err)
	}
	if len(scoped) != 1 || scoped[0].NaturalKey != "web-01" {
		t.Fatalf("业务范围应只放行 web-01，实际 %#v", scoped)
	}

	// 受限但没有任何选择器：恒不匹配（绝不退化成"不过滤"）。
	empty, _, err := svc.ChangesByRequest(rid, ChangeScope{LabelSelectors: []LabelSelector{}}, 0)
	if err != nil {
		t.Fatalf("查变更失败: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("空选择器应恒不匹配，实际返回 %d 条", len(empty))
	}

	// 节点维度同理：指定一个不相干的节点，一条都不该有。
	if nodes, _, err := svc.ChangesByRequest(rid, ChangeScope{Nodes: []string{"other-node"}}, 0); err != nil {
		t.Fatalf("查变更失败: %v", err)
	} else if len(nodes) != 0 {
		t.Fatalf("节点范围外应恒不匹配，实际 %d 条", len(nodes))
	}
}

// 条数上限：取满时必须**显式**回报已截断，否则界面会让人以为"这次就动了这些"。
func TestChangesByRequestReportsTruncation(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	const rid = "0011223344556677"

	if _, _, err := svc.Apply(hostObservation("web-01", nil)); err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: SourceManual, Actor: "alice", RequestID: rid,
		Attrs: map[string]string{"a": "1", "b": "2", "c": "3"},
	}); err != nil {
		t.Fatalf("人工改三个字段失败: %v", err)
	}

	items, truncated, err := svc.ChangesByRequest(rid, ChangeScope{}, 2)
	if err != nil {
		t.Fatalf("查变更失败: %v", err)
	}
	if len(items) != 2 || !truncated {
		t.Fatalf("limit=2 应返回 2 条且标记截断，实际 %d 条 truncated=%v", len(items), truncated)
	}
	full, truncated, err := svc.ChangesByRequest(rid, ChangeScope{}, 0)
	if err != nil {
		t.Fatalf("查变更失败: %v", err)
	}
	if len(full) != 3 || truncated {
		t.Fatalf("默认上限下应返回全部 3 条且不截断，实际 %d 条 truncated=%v", len(full), truncated)
	}
}

func mustHistory(t *testing.T, svc *Service, ref Ref) []ChangeRecord {
	t.Helper()
	recs, err := svc.History(ref, 0)
	if err != nil {
		t.Fatalf("读取变更历史失败: %v", err)
	}
	return recs
}
