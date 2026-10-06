package asset

import "testing"

// Pod 日志 → 资产联动：按「节点 + 命名空间 + Pod 名」反查。
//
// 为什么不用自然键：Pod 的自然键里含**集群**（PodNaturalKey(cluster, ns, name)），
// 而容器日志路径里没有集群标识——采集侧只知道"这个文件属于哪个 Pod"。
// 三个约束必须同时成立，否则会把日志标到错误的 Pod 上：
//   - **类型必须是 pod**：名字恰好相同的主机/实例资产不能被当成 Pod；
//   - **命名空间参与匹配**：同名 Pod 在别的命名空间里很常见；
//   - **节点参与匹配**：同名 Pod 在多台机器上都可能有，只按名字找会混成一条。

func declarePod(t *testing.T, svc *Service, cluster, ns, name, node string) {
	t.Helper()
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypePod, NaturalKey: PodNaturalKey(cluster, ns, name), Name: name, Node: node,
		Source: SourceDiscovery, Attrs: map[string]string{"namespace": ns},
	}); err != nil {
		t.Fatalf("落容器资产失败: %v", err)
	}
}

func TestAssetsByPod(t *testing.T) {
	svc, _ := newTestService(t)
	// 同一个 (命名空间, Pod 名) 出现在**两个集群**上：Pod 的自然键里含集群，
	// 因此这是两条不同的资产（同集群同名 Pod 只可能在同一个节点上，不会出现两条）。
	// 这正是"不指定节点时可能返回多条候选"的真实来源。
	declarePod(t, svc, "https://10.0.0.9:6443", "nebula-demo", "web-1", "worker-01")
	declarePod(t, svc, "https://10.0.0.20:6443", "nebula-demo", "web-1", "worker-02")
	declarePod(t, svc, "https://10.0.0.9:6443", "kube-system", "coredns", "worker-01")

	got, err := svc.AssetsByPod("worker-01", "nebula-demo", "web-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 1 || got[0].Node != "worker-01" || got[0].Name != "web-1" {
		t.Fatalf("应按节点 + 命名空间 + 名字精确命中一条：%+v", got)
	}

	// 不知道在哪台机器上：返回全部同名候选（调用方必须原样呈现，不能自己挑一条）
	all, err := svc.AssetsByPod("", "nebula-demo", "web-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("不指定节点应返回全部候选：%+v", all)
	}

	// 节点不匹配 → 不返回：把别的机器上的同名 Pod 当成这台机器的，就是"看的是另一个 Pod"
	if mismatched, err := svc.AssetsByPod("worker-03", "nebula-demo", "web-1"); err != nil || len(mismatched) != 0 {
		t.Fatalf("节点不匹配不应命中：%+v err=%v", mismatched, err)
	}
	// 命名空间不匹配 → 不返回
	if other, err := svc.AssetsByPod("worker-01", "default", "web-1"); err != nil || len(other) != 0 {
		t.Fatalf("命名空间不匹配不应命中：%+v err=%v", other, err)
	}
	// 名字不匹配 → 不返回
	if none, err := svc.AssetsByPod("worker-01", "nebula-demo", "web-9"); err != nil || len(none) != 0 {
		t.Fatalf("未知 Pod 不应命中：%+v err=%v", none, err)
	}
	// 命名空间或名字为空 → 空结果（不做"忽略这一维"的兜底：那会把范围放大到别的命名空间）
	if empty, err := svc.AssetsByPod("worker-01", "", "web-1"); err != nil || len(empty) != 0 {
		t.Fatalf("空命名空间应返回空：%+v err=%v", empty, err)
	}
	if empty, err := svc.AssetsByPod("worker-01", "nebula-demo", ""); err != nil || len(empty) != 0 {
		t.Fatalf("空名字应返回空：%+v err=%v", empty, err)
	}
}

// 名字恰好相同的主机资产不能被当成 Pod：类型不匹配就返回空，
// 否则日志会挂到一台"同名但根本不是这个 Pod"的机器上。
func TestAssetsByPodRequiresPodType(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-1", Name: "web-1", Node: "worker-01",
		Source: SourceDiscovery, Attrs: map[string]string{"namespace": "nebula-demo"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.AssetsByPod("worker-01", "nebula-demo", "web-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("只应命中 pod 类型：%+v", got)
	}
}
