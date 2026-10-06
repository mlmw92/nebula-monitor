package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// topologyOf 取某资产的 id（夹具里用自然键定位）。
func topologyAssetID(t *testing.T, svc *asset.Service, typeKey, naturalKey string) int64 {
	t.Helper()
	item, found, err := svc.Get(asset.Ref{TypeKey: typeKey, NaturalKey: naturalKey})
	if err != nil || !found {
		t.Fatalf("夹具资产缺失：%s/%s（found=%v err=%v）", typeKey, naturalKey, found, err)
	}
	return item.ID
}

// 关系图接口：载荷要能直接喂给前端画图——节点带身份/类型标题/状态，
// 边两端用与表格一致的 `typeKey|naturalKey` 寻址。
func TestRoutes_AssetTopologyPayload(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	webID := topologyAssetID(t, svc, asset.TypeHost, "web-01")
	if err := svc.LinkDiscovered(
		asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"},
		asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, asset.LinkRunsOn); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet,
		"/api/v1/assets/"+itoa(webID)+"/topology", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["root"] != "host|web-01" {
		t.Fatalf("root 应为 host|web-01，实际 %v", body["root"])
	}
	nodes, _ := body["nodes"].([]any)
	edges, _ := body["edges"].([]any)
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("应为两个节点一条边：nodes=%d edges=%d", len(nodes), len(edges))
	}
	first, _ := nodes[0].(map[string]any)
	if first["root"] != true || first["key"] != "host|web-01" || first["typeTitle"] != "主机" {
		t.Fatalf("中心节点字段不符：%v", first)
	}
	if first["status"] != asset.StatusOnline {
		t.Fatalf("刚上报的资产应为 online：%v", first["status"])
	}
	edge, _ := edges[0].(map[string]any)
	if edge["kind"] != "runs_on" || edge["source"] != string(asset.SourceDiscovery) {
		t.Fatalf("边应带类型与来源：%v", edge)
	}
	if edge["from"] != "middleware-instance|redis:127.0.0.1:6379" || edge["to"] != "host|web-01" {
		t.Fatalf("边两端寻址不符：%v", edge)
	}
}

// 跳数与节点数越界一律夹紧而不是 400：它们只影响"看多大范围"，
// 不该让整张图打不开（与列表 limit 的既有约定一致）。
func TestRoutes_AssetTopologyClampsParams(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	webID := topologyAssetID(t, svc, asset.TypeHost, "web-01")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet,
		"/api/v1/assets/"+itoa(webID)+"/topology?depth=99&limit=99999", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("越界参数应被夹紧而不是报错，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if got := body["depth"].(float64); int(got) != asset.MaxTopologyDepth {
		t.Fatalf("depth 应夹紧到 %d，实际 %v", asset.MaxTopologyDepth, got)
	}
}

// 资源范围：受限用户不得在图里看到范围外的邻居，也不得看到跨范围的边。
func TestRoutes_AssetTopologyRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	webID := topologyAssetID(t, svc, asset.TypeHost, "web-01")
	// 跨范围的一条边：web-01 上的实例 depends_on db-01（g2）
	if err := svc.LinkDiscovered(
		asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"},
		asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, asset.LinkDependsOn); err != nil {
		t.Fatal(err)
	}

	// g1 的受限用户：只能看到 web-01 与它上面的实例
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"), http.MethodGet,
		"/api/v1/assets/"+itoa(webID)+"/topology", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	for _, raw := range body["nodes"].([]any) {
		node := raw.(map[string]any)
		if node["naturalKey"] == "db-01" {
			t.Fatalf("范围外节点出现在图里：%v", node)
		}
	}
	for _, raw := range body["edges"].([]any) {
		edge := raw.(map[string]any)
		if edge["to"] == "host|db-01" || edge["from"] == "host|db-01" {
			t.Fatalf("跨范围的边出现在图里：%v", edge)
		}
	}

	// 中心本身在范围外 → 与「看不到」同语义：404
	dbID := topologyAssetID(t, svc, asset.TypeHost, "db-01")
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"), http.MethodGet,
		"/api/v1/assets/"+itoa(dbID)+"/topology", ""))
	if denied.Code != http.StatusNotFound {
		t.Fatalf("范围外的中心应 404，实际 %d", denied.Code)
	}
}

// 权限与不存在：缺 assets:read 403；未知 id 404。
func TestRoutes_AssetTopologyPermissionAndMissing(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	webID := topologyAssetID(t, svc, asset.TypeHost, "web-01")

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(globalPrincipal("assets:write"), http.MethodGet,
		"/api/v1/assets/"+itoa(webID)+"/topology", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:read 应 403，实际 %d", denied.Code)
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, reqWith(globalPrincipal("assets:read"), http.MethodGet,
		"/api/v1/assets/99999/topology", ""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("未知资产应 404，实际 %d", missing.Code)
	}

	// 未注入资产能力 → 503（与其它资产接口一致）
	disabled := &API{}
	rec := httptest.NewRecorder()
	disabled.handleAssetTopology(rec, assetReq(globalPrincipal("assets:read"), "/api/v1/assets/1/topology"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用资产能力应 503，实际 %d", rec.Code)
	}
}
