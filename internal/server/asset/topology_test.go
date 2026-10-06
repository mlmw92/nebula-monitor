package asset

import (
	"errors"
	"testing"
)

// refOf 把资产转成引用（用例里频繁需要）。
func refOf(a Asset) Ref { return Ref{TypeKey: a.TypeKey, NaturalKey: a.NaturalKey} }

// seedInstance 建一个中间件实例资产（自然键 = <类型>:<地址>）。
func seedInstance(t *testing.T, svc *Service, typeKey, addr, node string) Ref {
	t.Helper()
	a, _, err := svc.Apply(Observation{
		TypeKey: TypeMiddlewareInst, NaturalKey: typeKey + ":" + addr, Name: typeKey + "-" + addr,
		Node: node, Source: SourceDiscovery,
	})
	if err != nil {
		t.Fatalf("准备实例 %s 失败: %v", addr, err)
	}
	return Ref{TypeKey: a.TypeKey, NaturalKey: a.NaturalKey}
}

// 邻域展开：中心 0 跳、直接邻居 1 跳、再往外 2 跳；跳数上限夹紧而不是报错。
func TestTopologyExpandsByDepth(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", map[string]string{"cpu": "4"}))
	if err != nil {
		t.Fatal(err)
	}
	redis := seedInstance(t, svc, "redis", "10.0.0.1:6379", "web-01")
	// 第二个实例挂在 redis 上（depends_on）→ 距中心 2 跳
	sidecar := seedInstance(t, svc, "nginx", "10.0.0.1:80", "web-01")
	if err := svc.LinkDiscovered(redis, refOf(host), LinkRunsOn); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkDiscovered(sidecar, redis, LinkDependsOn); err != nil {
		t.Fatal(err)
	}

	ref := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}

	one, err := svc.Topology(ref, 1, 0, nil)
	if err != nil {
		t.Fatalf("1 跳邻域失败: %v", err)
	}
	if len(one.Nodes) != 2 || len(one.Edges) != 1 {
		t.Fatalf("1 跳应为中心 + redis：nodes=%d edges=%d", len(one.Nodes), len(one.Edges))
	}
	if !one.Nodes[0].Root || one.Nodes[0].Depth != 0 {
		t.Fatalf("第一个节点应是中心且 0 跳：%+v", one.Nodes[0])
	}
	if one.Truncated {
		t.Fatal("未达上限不应标截断")
	}

	two, err := svc.Topology(ref, 2, 0, nil)
	if err != nil {
		t.Fatalf("2 跳邻域失败: %v", err)
	}
	if len(two.Nodes) != 3 || len(two.Edges) != 2 {
		t.Fatalf("2 跳应包含三个节点两条边：nodes=%d edges=%d", len(two.Nodes), len(two.Edges))
	}
	// 跳数必须真实反映距离（界面据此区分直接影响与间接影响）
	depth := map[string]int{}
	for _, n := range two.Nodes {
		depth[n.Asset.NaturalKey] = n.Depth
	}
	if depth["web-01"] != 0 || depth["redis:10.0.0.1:6379"] != 1 || depth["nginx:10.0.0.1:80"] != 2 {
		t.Fatalf("跳数不符：%v", depth)
	}

	// 超过上限夹紧而不是报错：传个奇怪的值不该让整张图打不开
	deep, err := svc.Topology(ref, 99, 99999, nil)
	if err != nil {
		t.Fatalf("越界跳数应被夹紧而不是报错: %v", err)
	}
	if deep.Depth != MaxTopologyDepth {
		t.Fatalf("跳数应夹紧到 %d，实际 %d", MaxTopologyDepth, deep.Depth)
	}
}

// 无关系的资产：只有一个中心节点、零条边——不是错误。
func TestTopologyIsolatedAsset(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Topology(Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}, 2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || len(res.Edges) != 0 || res.Truncated {
		t.Fatalf("孤立资产应只有中心节点：%+v", res)
	}
	if res.Root.NaturalKey != "web-01" {
		t.Fatalf("中心标识不符：%+v", res.Root)
	}
}

// 环路不能把遍历卡住，也不能把同一个节点算两次。
func TestTopologyHandlesCycles(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	redis := seedInstance(t, svc, "redis", "10.0.0.1:6379", "web-01")
	hostRef := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}
	// 互为依赖：真实的配置漂移里出现过这种环
	if err := svc.LinkDiscovered(redis, hostRef, LinkRunsOn); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkDiscovered(hostRef, redis, LinkDependsOn); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Topology(hostRef, MaxTopologyDepth, 0, nil)
	if err != nil {
		t.Fatalf("环路邻域失败: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("环路上只有两个节点，实际 %d：%+v", len(res.Nodes), res.Nodes)
	}
	if len(res.Edges) != 2 {
		t.Fatalf("两条边都应返回，实际 %d", len(res.Edges))
	}
}

// 节点上限必须真的生效并**显式标记截断**：静默截断会让人以为"关系就这么多"。
func TestTopologyTruncatesAtNodeLimit(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	hostRef := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}
	for i := 0; i < 8; i++ {
		inst := seedInstance(t, svc, "redis", "10.0.0."+string(rune('1'+i))+":6379", "web-01")
		if err := svc.LinkDiscovered(inst, hostRef, LinkRunsOn); err != nil {
			t.Fatal(err)
		}
	}

	res, err := svc.Topology(hostRef, 1, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatalf("达到节点上限必须标截断：%+v", res)
	}
	if len(res.Nodes) > 3 {
		t.Fatalf("节点数不得超过上限 3，实际 %d", len(res.Nodes))
	}
	// 被上限挡在门外的端点，其边也不得出现在图里（不能指向不存在的节点）
	keys := map[string]bool{}
	for _, n := range res.Nodes {
		keys[n.Asset.TypeKey+"|"+n.Asset.NaturalKey] = true
	}
	for _, e := range res.Edges {
		if !keys[e.From.TypeKey+"|"+e.From.NaturalKey] || !keys[e.To.TypeKey+"|"+e.To.NaturalKey] {
			t.Fatalf("边指向了图里不存在的节点：%+v", e)
		}
	}
}

// 资源范围：范围外的邻居与相关边一律不出现在图里；中心不在范围内按"看不到"处理。
func TestTopologyRespectsScope(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := svc.Apply(hostObservation("db-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	hostRef := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}
	// 跨范围的一条边：web-01 上的实例 depends_on db-01
	inst := seedInstance(t, svc, "redis", "10.0.0.1:6379", "web-01")
	if err := svc.LinkDiscovered(inst, Ref{TypeKey: other.TypeKey, NaturalKey: other.NaturalKey}, LinkDependsOn); err != nil {
		t.Fatal(err)
	}
	if err := svc.LinkDiscovered(inst, hostRef, LinkRunsOn); err != nil {
		t.Fatal(err)
	}

	// 受限用户只看得到 web-01：db-01 及其边都不出现
	res, err := svc.Topology(hostRef, 2, 0, []string{"web-01"})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range res.Nodes {
		if n.Asset.NaturalKey == "db-01" {
			t.Fatalf("范围外节点不得出现在图里：%+v", res.Nodes)
		}
	}
	for _, e := range res.Edges {
		if e.To.NaturalKey == "db-01" || e.From.NaturalKey == "db-01" {
			t.Fatalf("跨范围的边不得出现：%+v", e)
		}
	}
	if len(res.Nodes) != 2 || len(res.Edges) != 1 {
		t.Fatalf("范围内应剩中心 + 实例一条边：nodes=%d edges=%d", len(res.Nodes), len(res.Edges))
	}

	// 中心本身不在范围内 → 与"看不到"同语义
	if _, err := svc.Topology(Ref{TypeKey: other.TypeKey, NaturalKey: other.NaturalKey}, 1, 0, []string{"web-01"}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("范围外的中心应报 ErrOutOfScope，实际 %v", err)
	}

	// 空可见集合：同样按看不到处理（不能退化成"不过滤"）
	if _, err := svc.Topology(hostRef, 1, 0, []string{}); !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("空可见集合应报 ErrOutOfScope，实际 %v", err)
	}
}

// 归属节点为空的资产（例如未落在已知主机上的 Pod）对受限用户不可见：
// 与 nodeInScope 的取向一致——不知道属于哪台机器就不算在范围内。
func TestTopologyHidesUnattributedNodesFromRestrictedUsers(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	orphan, _, err := svc.Apply(Observation{
		TypeKey: TypePod, NaturalKey: PodNaturalKey("https://10.0.0.9:6443", "default", "web-1"),
		Name: "web-1", Node: "", Source: SourceDiscovery,
	})
	if err != nil {
		t.Fatal(err)
	}
	hostRef := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}
	if err := svc.LinkDiscovered(Ref{TypeKey: orphan.TypeKey, NaturalKey: orphan.NaturalKey}, hostRef, LinkRunsOn); err != nil {
		t.Fatal(err)
	}

	restricted, err := svc.Topology(hostRef, 2, 0, []string{"web-01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(restricted.Nodes) != 1 || len(restricted.Edges) != 0 {
		t.Fatalf("归属为空的节点对受限用户不可见：%+v", restricted)
	}

	global, err := svc.Topology(hostRef, 2, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(global.Nodes) != 2 || len(global.Edges) != 1 {
		t.Fatalf("全局范围应看到全部：nodes=%d edges=%d", len(global.Nodes), len(global.Edges))
	}
}

// 按实例地址找资产（告警 → 资产联动的入口）。
//
// 地址里出现 LIKE 通配符（下划线是主机名里的常见字符）时**不得**变成通配匹配：
// 那会把不相干的实例一起捞进来，而界面上看不出哪条才是告警说的那个。
func TestInstancesByAddr(t *testing.T) {
	svc, _ := newTestService(t)
	seedInstance(t, svc, "redis", "127.0.0.1:6379", "web-01")
	// 同一地址、不同类型：两条都是有效候选，必须全部返回（挑一条就是"看的是另一个实例"）
	seedInstance(t, svc, "k8s", "127.0.0.1:6379", "web-01")
	seedInstance(t, svc, "redis", "db_1:6379", "web-01")
	// 与 db_1 只差一个字符：不转义 `_` 时会被它一起命中
	seedInstance(t, svc, "redis", "dbX1:6379", "web-01")

	got, err := svc.InstancesByAddr("127.0.0.1:6379")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("同地址不同类型的实例应全部返回，实际 %d：%+v", len(got), got)
	}

	underscore, err := svc.InstancesByAddr("db_1:6379")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(underscore) != 1 || underscore[0].NaturalKey != "redis:db_1:6379" {
		t.Fatalf("下划线必须按字面匹配：%+v", underscore)
	}

	if got, err := svc.InstancesByAddr(""); err != nil || len(got) != 0 {
		t.Fatalf("空地址应返回空：%+v err=%v", got, err)
	}
	if got, err := svc.InstancesByAddr("9.9.9.9:1234"); err != nil || len(got) != 0 {
		t.Fatalf("不存在的地址应返回空：%+v err=%v", got, err)
	}
}

// 结果必须稳定：同一份数据两次查询顺序一致，否则前端力导向图每次打开都会重新洗牌。
func TestTopologyOrderIsStable(t *testing.T) {
	svc, _ := newTestService(t)
	host, _, err := svc.Apply(hostObservation("web-01", nil))
	if err != nil {
		t.Fatal(err)
	}
	hostRef := Ref{TypeKey: host.TypeKey, NaturalKey: host.NaturalKey}
	for _, addr := range []string{"10.0.0.9:6379", "10.0.0.1:6379", "10.0.0.5:6379"} {
		inst := seedInstance(t, svc, "redis", addr, "web-01")
		if err := svc.LinkDiscovered(inst, hostRef, LinkRunsOn); err != nil {
			t.Fatal(err)
		}
	}
	first, err := svc.Topology(hostRef, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Topology(hostRef, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Nodes) != len(second.Nodes) || len(first.Edges) != len(second.Edges) {
		t.Fatal("两次查询规模应一致")
	}
	for i := range first.Nodes {
		if first.Nodes[i].Asset.NaturalKey != second.Nodes[i].Asset.NaturalKey {
			t.Fatalf("节点顺序不稳定：%v vs %v", first.Nodes[i].Asset.NaturalKey, second.Nodes[i].Asset.NaturalKey)
		}
	}
	for i := range first.Edges {
		if first.Edges[i].To.NaturalKey != second.Edges[i].To.NaturalKey {
			t.Fatalf("边顺序不稳定：%+v vs %+v", first.Edges[i], second.Edges[i])
		}
	}
}
