package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 全库关系视图两个接口的用例（批次 22）。
//
// 接口层要盯的四件事：路由与权限点接对了、范围裁剪在 HTTP 这一层确实生效、
// 参数写坏时**报错而不是静默放宽**、以及载荷形状（total 与 truncated 都要在）。

func linkStatsFixture(t *testing.T) (*API, *asset.Service) {
	t.Helper()
	a, svc := assetTestAPI(t)
	// seedAsset 已经建了 web-01（g1）与它上面的 redis 实例，这里补一条宿主边
	inst := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	host := asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}
	if err := svc.LinkDiscovered(inst, host, asset.LinkRunsOn); err != nil {
		t.Fatalf("准备宿主边失败: %v", err)
	}
	return a, svc
}

func TestHandleAssetLinkStats(t *testing.T) {
	a, _ := linkStatsFixture(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/assets/link-stats", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if total, _ := body["totalLinks"].(float64); total != 1 {
		t.Fatalf("totalLinks 应为 1，实际 %v", body["totalLinks"])
	}
	groups, _ := body["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("应有 1 个分组，实际 %d：%s", len(groups), rec.Body.String())
	}
	first, _ := groups[0].(map[string]any)
	if first["fromType"] != asset.TypeMiddlewareInst || first["kind"] != string(asset.LinkRunsOn) ||
		first["toType"] != asset.TypeHost || first["source"] != string(asset.SourceDiscovery) {
		t.Fatalf("分组维度不符：%#v", first)
	}
	if first["count"].(float64) != 1 {
		t.Fatalf("分组计数应为 1，实际 %v", first["count"])
	}
	// 无边资产至少包含刚建完还没接关系的资产（db-01 等），必须是数字而不是 null
	if _, ok := body["assetsWithoutLinks"].(float64); !ok {
		t.Fatalf("assetsWithoutLinks 应为数字：%#v", body["assetsWithoutLinks"])
	}
}

// 范围裁剪在接口层同样生效：实例在 web-01（g1），限定到 g2 的账号一条关系都不该看到。
func TestHandleAssetLinkStatsRespectsScope(t *testing.T) {
	a, _ := linkStatsFixture(t)
	mux := newRoutesMux(a)

	inScope := httptest.NewRecorder()
	mux.ServeHTTP(inScope, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"),
		http.MethodGet, "/api/v1/assets/link-stats", ""))
	if body := decodeBody(t, inScope); body["totalLinks"].(float64) != 1 {
		t.Fatalf("g1 范围内应有 1 条边，实际 %v", body["totalLinks"])
	}

	outScope := httptest.NewRecorder()
	mux.ServeHTTP(outScope, reqWith(restrictedPrincipal([]string{"assets:read"}, "g2"),
		http.MethodGet, "/api/v1/assets/link-stats", ""))
	body := decodeBody(t, outScope)
	if body["totalLinks"].(float64) != 0 {
		t.Fatalf("g2 范围内不该看到任何边，实际 %v", body["totalLinks"])
	}
	if groups, _ := body["groups"].([]any); len(groups) != 0 {
		t.Fatalf("范围外不该有分组，实际 %s", outScope.Body.String())
	}
}

func TestHandleAssetTopologyByFilter(t *testing.T) {
	a, _ := linkStatsFixture(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"),
		http.MethodGet, "/api/v1/assets/topology?kind=runs_on", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if total, _ := body["total"].(float64); total != 1 {
		t.Fatalf("total 应为 1，实际 %v", body["total"])
	}
	if trunc, ok := body["truncated"].(bool); !ok || trunc {
		t.Fatalf("这么小的图不该截断（truncated 必须是布尔值）：%#v", body["truncated"])
	}
	if nodes, _ := body["nodes"].([]any); len(nodes) != 2 {
		t.Fatalf("应有 2 个节点，实际 %d", len(nodes))
	}
	edges, _ := body["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("应有 1 条边，实际 %d", len(edges))
	}
	if e, _ := edges[0].(map[string]any); e["kind"] != "runs_on" || e["source"] != "discovery" {
		t.Fatalf("边的种类/来源不符：%#v", e)
	}

	// 筛选到没有的边 → 空图（仍是 200）
	empty := httptest.NewRecorder()
	mux.ServeHTTP(empty, reqWith(globalPrincipal("assets:read"),
		http.MethodGet, "/api/v1/assets/topology?source=manual", ""))
	if got := decodeBody(t, empty); got["total"].(float64) != 0 {
		t.Fatalf("人工边应为 0 条，实际 %v", got["total"])
	}
}

// 参数写坏要报错，不能静默放宽：用户以为在看"依赖"，其实看到了全部关系。
func TestHandleAssetTopologyRejectsUnknownFilter(t *testing.T) {
	a, _ := linkStatsFixture(t)
	mux := newRoutesMux(a)

	for _, target := range []string{
		"/api/v1/assets/topology?kind=nope",
		"/api/v1/assets/topology?source=whatever",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, target, ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d（%s）", target, rec.Code, rec.Body.String())
		}
	}
}

// 两个接口都要 assets:read；没有权限时 403 而不是空结果（空结果会被当成"没有关系"）。
func TestAssetLinkStatsRequiresReadPermission(t *testing.T) {
	a, _ := linkStatsFixture(t)
	mux := newRoutesMux(a)

	for _, target := range []string{"/api/v1/assets/link-stats", "/api/v1/assets/topology"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:write"), http.MethodGet, target, ""))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 无 assets:read 时应 403，实际 %d", target, rec.Code)
		}
	}
}
