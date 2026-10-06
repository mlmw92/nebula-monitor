package asset

import (
	"testing"
)

// 日志 → 资产联动的地基：按「人工声明的日志来源 + 归属节点」找资产。
//
// 两个约束必须同时成立，否则会把日志标到错误的资产上：
//   - **只认人工值**：采集侧不会上报 logSource，出现采集值只可能是脏数据；
//   - **节点参与匹配**：同一个来源名（applog）在多台机器上都会配置，只按来源名找
//     会把所有机器混成一条，用户会看到一台机器的日志被标成另一台机器的资产。

func declareLogSource(t *testing.T, svc *Service, typeKey, key, node, source string) {
	t.Helper()
	if _, _, err := svc.Apply(Observation{
		TypeKey: typeKey, NaturalKey: key, Name: key, Node: node,
		Source: SourceManual, Attrs: map[string]string{LogSourceKey: source},
	}); err != nil {
		t.Fatalf("声明日志来源失败: %v", err)
	}
}

func TestAssetsByLogSource(t *testing.T) {
	svc, _ := newTestService(t)
	declareLogSource(t, svc, TypeHost, "web-01", "web-01", "applog")
	declareLogSource(t, svc, TypeHost, "web-02", "web-02", "applog")
	declareLogSource(t, svc, TypeHost, "db-01", "db-01", "dblog")

	got, err := svc.AssetsByLogSource("applog", "web-01")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 1 || got[0].NaturalKey != "web-01" {
		t.Fatalf("应按来源 + 节点精确命中一条：%+v", got)
	}

	// 同一个来源在多台机器上：不指定节点时全部返回（调用方必须原样呈现候选）
	all, err := svc.AssetsByLogSource("applog", "")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("同名来源应返回全部候选：%+v", all)
	}

	// 节点不匹配 → 不返回（不能把别的机器的资产当成这台机器的）
	if mismatched, err := svc.AssetsByLogSource("applog", "db-01"); err != nil || len(mismatched) != 0 {
		t.Fatalf("节点不匹配不应命中：%+v err=%v", mismatched, err)
	}
	if none, err := svc.AssetsByLogSource("no-such-source", "web-01"); err != nil || len(none) != 0 {
		t.Fatalf("未知来源不应命中：%+v err=%v", none, err)
	}
	if empty, err := svc.AssetsByLogSource("", "web-01"); err != nil || len(empty) != 0 {
		t.Fatalf("空来源应返回空：%+v err=%v", empty, err)
	}
}

// 采集值里的 logSource 不算数：那是脏数据，不是运维的声明。
func TestAssetsByLogSourceIgnoresDiscoveryValues(t *testing.T) {
	svc, _ := newTestService(t)
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: SourceDiscovery, Attrs: map[string]string{LogSourceKey: "applog"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.AssetsByLogSource("applog", "web-01")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("只应认人工值：%+v", got)
	}
}
