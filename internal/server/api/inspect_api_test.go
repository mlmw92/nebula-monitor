package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 触发巡检需要 inspect:run：它与 assets:write 分开，因为巡检只给结论、不改配置。
// 首次巡检应只建立基线（baselined = 资产数）且 0 条差异——「数据不足 ≠ 不合规」。
func TestRoutes_InspectRunRequiresPermissionAndBaselinesFirst(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(globalPrincipal("inspect:read"), http.MethodPost, "/api/v1/inspect/runs", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 inspect:run 应 403，实际 %d", denied.Code)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("inspect:read", "inspect:run"), http.MethodPost, "/api/v1/inspect/runs", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("触发巡检应 201，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["scope"] != "all" {
		t.Fatalf("默认范围应为 all，实际 %v", body["scope"])
	}
	// 夹具共 3 个资产：web-01、db-01、redis 实例
	if body["assets"].(float64) != 3 || body["baselined"].(float64) != 3 || body["findings"].(float64) != 0 {
		t.Fatalf("首次巡检应建立 3 个基线且 0 差异：%v", body)
	}
	if body["truncated"] != false {
		t.Fatalf("资产数未超上限，不应标注截断：%v", body)
	}
}

// 巡检范围复用台账筛选（含资源范围下推）：受限用户只会检到自己范围内的资产。
func TestRoutes_InspectRunRespectsScope(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	// g1 = web-01 / web-02；夹具里 g1 名下有 web-01 与它上面的 redis 实例
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"inspect:run", "inspect:read"}, "g1"),
		http.MethodPost, "/api/v1/inspect/runs", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("应 201，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["assets"].(float64) != 2 {
		t.Fatalf("g1 范围内应只有 2 个资产，实际 %v", body["assets"])
	}
	if body["scope"] != "scope:mine" {
		t.Fatalf("受限巡检的范围描述应为 scope:mine，实际 %v", body["scope"])
	}
}

// 期望值（标杆）用 assets:write 维护，且受资源范围约束：范围外按 404（不做 403 区分）。
func TestRoutes_AssetBaselineRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	dbHost, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})

	ok := httptest.NewRecorder()
	mux.ServeHTTP(ok, reqWith(restrictedPrincipal([]string{"assets:write"}, "g1"), http.MethodPost,
		"/api/v1/assets/"+strconv.FormatInt(host.ID, 10)+"/baseline", ""))
	if ok.Code != http.StatusOK {
		t.Fatalf("范围内设标杆应 200，实际 %d（%s）", ok.Code, ok.Body.String())
	}

	out := httptest.NewRecorder()
	mux.ServeHTTP(out, reqWith(restrictedPrincipal([]string{"assets:write"}, "g1"), http.MethodPost,
		"/api/v1/assets/"+strconv.FormatInt(dbHost.ID, 10)+"/baseline", ""))
	if out.Code != http.StatusNotFound {
		t.Fatalf("范围外资产应 404，实际 %d", out.Code)
	}

	list := httptest.NewRecorder()
	mux.ServeHTTP(list, reqWith(globalPrincipal("inspect:read"), http.MethodGet, "/api/v1/inspect/baselines", ""))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "web-01") {
		t.Fatalf("标杆列表应含 web-01，实际 %d（%s）", list.Code, list.Body.String())
	}

	del := httptest.NewRecorder()
	mux.ServeHTTP(del, reqWith(restrictedPrincipal([]string{"assets:write"}, "g1"), http.MethodDelete,
		"/api/v1/assets/"+strconv.FormatInt(host.ID, 10)+"/baseline", ""))
	if del.Code != http.StatusOK {
		t.Fatalf("清除标杆应 200，实际 %d（%s）", del.Code, del.Body.String())
	}
}

// 巡检结果读取：缺 inspect:read 被拒；非法记录 ID 明确 400（不静默返回空差异）。
func TestRoutes_InspectRunsAndFindingsRead(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/inspect/runs", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 inspect:read 应 403，实际 %d", denied.Code)
	}

	runs := httptest.NewRecorder()
	mux.ServeHTTP(runs, reqWith(globalPrincipal("inspect:read"), http.MethodGet, "/api/v1/inspect/runs", ""))
	if runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), "runs") {
		t.Fatalf("应返回巡检记录列表，实际 %d（%s）", runs.Code, runs.Body.String())
	}

	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, reqWith(globalPrincipal("inspect:read"), http.MethodGet,
		"/api/v1/inspect/runs/not-a-number/findings", ""))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法巡检记录 ID 应 400，实际 %d", bad.Code)
	}
}

// 快照列表属台账读取：范围外资产按 404。
func TestRoutes_AssetSnapshotsRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	dbHost, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"), http.MethodGet,
		"/api/v1/assets/"+strconv.FormatInt(dbHost.ID, 10)+"/snapshots", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("范围外资产的快照应 404，实际 %d", rec.Code)
	}
}
