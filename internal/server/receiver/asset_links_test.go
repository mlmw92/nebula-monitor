package receiver

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/node"
)

// 自动关系的用例（设计件 2026-10-07-asset-relation-auto-source-design.md，批次 21）。
//
// 两组边分别盯不同的事：
//   - depends_on（副本 → 主库）：顺序（副本可能先报）、主库不在台账时不造占位、
//     哨兵串不是地址、副本被提升/改指别处时旧边必须清掉；
//   - runs_on（实例 → 主机）：只有地址主机确实是这台机器时才建，且旧版本建下的错边要清掉，
//     但**人工认领过的边一律不动**。

func newLinkReceiver(t *testing.T) (*Receiver, *asset.Service) {
	t.Helper()
	dir := t.TempDir()
	store, err := asset.Open(filepath.Join(dir, "assets.db"))
	if err != nil {
		t.Fatalf("打开资产库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := asset.NewService(store)
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	rec := New(&assetTestStorage{}, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)
	rec.SetAssetService(svc)
	return rec, svc
}

func redisRef(addr string) asset.Ref {
	return asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:" + addr}
}

func hostRefOf(name string) asset.Ref {
	return asset.Ref{TypeKey: asset.TypeHost, NaturalKey: name}
}

// outLinks 返回某资产的出边（按 kind 过滤，空串表示全部）。
func outLinks(t *testing.T, svc *asset.Service, ref asset.Ref, kind asset.LinkKind) []asset.Link {
	t.Helper()
	links, err := svc.Links(ref)
	if err != nil {
		t.Fatalf("查询关联失败: %v", err)
	}
	out := make([]asset.Link, 0, len(links))
	for _, l := range links {
		if l.From != ref {
			continue
		}
		if kind != "" && l.Kind != kind {
			continue
		}
		out = append(out, l)
	}
	return out
}

func reportWithRedis(node, ip string, instances ...model.RedisInstance) model.ReportPayload {
	return model.ReportPayload{Node: node, Group: "g1", IP: ip, OS: "Ubuntu 24.04", Version: "1.30.40",
		RedisInstances: instances}
}

// 副本先于主库出现在同一份清单里，也必须能建出边——这是"先落全部实例、再统一建边"的理由。
func TestReplicaDependsOnMasterRegardlessOfOrder(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	const master = "10.0.0.9:6379"
	const replica = "10.0.0.10:6379"
	// 故意把副本排在主库前面
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: replica, Name: "r1", Role: "slave", Up: true, ReplicaOf: master},
		model.RedisInstance{Instance: master, Name: "m1", Role: "master", Up: true},
	))

	links := outLinks(t, svc, redisRef(replica), asset.LinkDependsOn)
	if len(links) != 1 {
		t.Fatalf("副本应有 1 条 depends_on 出边，实际 %d 条", len(links))
	}
	if links[0].To != redisRef(master) {
		t.Fatalf("依赖方向应是 副本 → 主库，实际指向 %s", links[0].To.NaturalKey)
	}
	if links[0].Source != asset.SourceDiscovery {
		t.Fatalf("采集建的边来源应为 discovery，实际 %q", links[0].Source)
	}
	// 主库自己不该反向依赖谁
	if got := outLinks(t, svc, redisRef(master), asset.LinkDependsOn); len(got) != 0 {
		t.Fatalf("主库不该有 depends_on 出边，实际 %d 条", len(got))
	}
	// 主从关系同时落成属性：关系图说"有这条边"，台账要能回答"为什么"
	if v, _ := mustAsset(t, svc, redisRef(replica)).Value("replicaOf"); v != master {
		t.Fatalf("副本的 replicaOf 属性应为 %q，实际 %q", master, v)
	}
}

// 主库由**另一台机器**上报：跨上报也要能建边（台账里查得到就建）。
func TestReplicaDependsOnMasterReportedByAnotherNode(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	const master = "10.0.0.9:6379"
	const replica = "10.0.0.10:6379"
	postReport(t, rec, reportWithRedis("db-01", "10.0.0.9",
		model.RedisInstance{Instance: master, Name: "m1", Role: "master", Up: true}))
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: replica, Name: "r1", Role: "slave", Up: true, ReplicaOf: master}))

	if got := outLinks(t, svc, redisRef(replica), asset.LinkDependsOn); len(got) != 1 {
		t.Fatalf("跨节点上报也应建出依赖边，实际 %d 条", len(got))
	}
}

// 主库不在台账：不建边、**不造占位资产**（宁可没有边，也不编一个我们不认识的库）。
func TestReplicaWithoutKnownMasterCreatesNoPlaceholder(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	const master = "10.0.0.99:6379"
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: "10.0.0.10:6379", Name: "r1", Role: "slave", Up: true, ReplicaOf: master},
	))

	if got := outLinks(t, svc, redisRef("10.0.0.10:6379"), asset.LinkDependsOn); len(got) != 0 {
		t.Fatalf("主库未知时不该建边，实际 %d 条", len(got))
	}
	if _, ok, err := svc.Get(redisRef(master)); err != nil || ok {
		t.Fatalf("不该为主库造占位资产（ok=%v err=%v）", ok, err)
	}
}

// `sentinel:<名字>` 是给界面看的标签、不是地址：拿它拼自然键会去查一个不存在的资产。
func TestReplicaOfNonAddressIsIgnored(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: "10.0.0.10:6379", Name: "r1", Role: "slave", Up: true,
			ReplicaOf: "sentinel:my-master"},
	))
	if got := outLinks(t, svc, redisRef("10.0.0.10:6379"), asset.LinkDependsOn); len(got) != 0 {
		t.Fatalf("非地址形态的 replicaOf 不该建边，实际 %d 条", len(got))
	}
}

// 副本被提升（replicaOf 变空）或改指别处：旧边必须清掉，否则影响面会一直显示一条已不存在的依赖。
func TestStaleDependencyIsCleanedUp(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	const oldMaster = "10.0.0.9:6379"
	const newMaster = "10.0.0.11:6379"
	const replica = "10.0.0.10:6379"
	base := []model.RedisInstance{
		{Instance: oldMaster, Name: "m1", Role: "master", Up: true},
		{Instance: newMaster, Name: "m2", Role: "master", Up: true},
	}
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5", append(base,
		model.RedisInstance{Instance: replica, Name: "r1", Role: "slave", Up: true, ReplicaOf: oldMaster})...))
	if got := outLinks(t, svc, redisRef(replica), asset.LinkDependsOn); len(got) != 1 {
		t.Fatalf("首次上报应建出 1 条依赖，实际 %d 条", len(got))
	}

	// 改指新主库：旧边清掉、新边建上
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5", append(base,
		model.RedisInstance{Instance: replica, Name: "r1", Role: "slave", Up: true, ReplicaOf: newMaster})...))
	got := outLinks(t, svc, redisRef(replica), asset.LinkDependsOn)
	if len(got) != 1 || got[0].To != redisRef(newMaster) {
		t.Fatalf("改指主库后应只剩指向新主库的那条，实际 %#v", got)
	}

	// 被提升为主库（replicaOf 变空）：依赖全部清掉
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5", append(base,
		model.RedisInstance{Instance: replica, Name: "r1", Role: "master", Up: true})...))
	if got := outLinks(t, svc, redisRef(replica), asset.LinkDependsOn); len(got) != 0 {
		t.Fatalf("被提升后不该再有任何依赖边，实际 %#v", got)
	}
}

// 宿主判定：地址主机确实是这台机器（回环 / 上报 IP / 主机名 / FQDN 短名）才建 runs_on。
func TestInstanceHostMatchBuildsRunsOn(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"回环", "127.0.0.1:6379"},
		{"回环 IPv6", "[::1]:6379"},
		{"与本机 IP 相同", "10.0.0.5:6379"},
		{"与主机名相同", "web-01:6379"},
		{"FQDN 短名相同", "web-01.internal:6379"},
		{"不是地址形态（docker 容器 ID）", "abc123def456"},
		{"带 scheme 的本机 URL", "https://127.0.0.1:6443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, svc := newLinkReceiver(t)
			postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
				model.RedisInstance{Instance: tc.addr, Name: "x", Role: "master", Up: true}))
			if got := outLinks(t, svc, redisRef(tc.addr), asset.LinkRunsOn); len(got) != 1 {
				t.Fatalf("%s：应挂到本机，实际 %d 条 runs_on", tc.name, len(got))
			}
		})
	}
}

// 地址明确指向**别的机器**（exporter 模式 / 跨机采集）：不建边，并清掉旧版本建下的错边，
// 但人工认领过的边一律不动。属性里留 addrHost 作为线索（否则"为什么没有边"无从判断）。
func TestForeignAddrDoesNotBuildRunsOnAndCleansLegacyEdge(t *testing.T) {
	rec, svc := newLinkReceiver(t)
	const addr = "10.0.0.77:6379"
	inst := redisRef(addr)
	host := hostRefOf("web-01")

	// 第一轮：先把资产落下来（本轮不该建边）
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: addr, Name: "remote", Role: "master", Up: true}))
	// 再手工造出"旧版本建下的错边"（采集来源）——资产必须先存在（LinkDiscovered 的契约）
	if err := svc.LinkDiscovered(inst, host, asset.LinkRunsOn); err != nil {
		t.Fatalf("准备旧边失败: %v", err)
	}
	// 第二轮：采集侧应把这条错边撤销
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: addr, Name: "remote", Role: "master", Up: true}))

	if got := outLinks(t, svc, inst, asset.LinkRunsOn); len(got) != 0 {
		t.Fatalf("地址指向别的机器时不该有 runs_on 边，实际 %#v", got)
	}
	a := mustAsset(t, svc, inst)
	if v, _ := a.Value("addrHost"); v != "10.0.0.77" {
		t.Fatalf("应把地址主机写进 addrHost 作为线索，实际 %q", v)
	}

	// 人工认领过的边：不被清理
	if err := svc.LinkManual(inst, host, asset.LinkRunsOn); err != nil {
		t.Fatalf("人工建边失败: %v", err)
	}
	postReport(t, rec, reportWithRedis("web-01", "10.0.0.5",
		model.RedisInstance{Instance: addr, Name: "remote", Role: "master", Up: true}))
	got := outLinks(t, svc, inst, asset.LinkRunsOn)
	if len(got) != 1 || got[0].Source != asset.SourceManual {
		t.Fatalf("人工认领的边不该被采集侧清掉，实际 %#v", got)
	}
}

func mustAsset(t *testing.T, svc *asset.Service, ref asset.Ref) asset.Asset {
	t.Helper()
	a, ok, err := svc.Get(ref)
	if err != nil || !ok {
		t.Fatalf("查询资产 %s 失败（ok=%v err=%v）", ref.NaturalKey, ok, err)
	}
	return a
}
