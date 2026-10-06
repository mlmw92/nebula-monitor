package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
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

func TestRoutes_InspectReadRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	if _, err := svc.RunInspect(asset.InspectScope{}, "admin"); err != nil {
		t.Fatal(err)
	}
	seedAsset(t, svc, asset.TypeHost, "web-01", "web-01", "web-01", map[string]string{"cpuCores": "8"})
	seedAsset(t, svc, asset.TypeHost, "db-01", "db-01", "db-01", map[string]string{"cpuCores": "16"})
	run, err := svc.RunInspect(asset.InspectScope{}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	principal := restrictedPrincipal([]string{"inspect:read"}, "g1")
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(principal, http.MethodGet, path, ""))
		return rec
	}
	runs := get("/api/v1/inspect/runs?limit=1")
	if runs.Code != http.StatusOK {
		t.Fatalf("runs status = %d: %s", runs.Code, runs.Body.String())
	}
	var listing struct {
		Runs []inspectRunView `json:"runs"`
	}
	if err := json.Unmarshal(runs.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Runs) != 1 || listing.Runs[0].Assets != 2 || listing.Runs[0].Findings != 1 {
		t.Fatalf("g1 should only see two assets and one finding: %+v", listing.Runs)
	}
	findings := get("/api/v1/inspect/runs/" + strconv.FormatInt(run.ID, 10) + "/findings")
	if findings.Code != http.StatusOK {
		t.Fatalf("findings status = %d: %s", findings.Code, findings.Body.String())
	}
	var result struct {
		Findings []inspectFindingView `json:"findings"`
	}
	if err := json.Unmarshal(findings.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 1 || result.Findings[0].Node != "web-01" {
		t.Fatalf("g1 leaked findings: %+v", result.Findings)
	}
}

func TestRoutes_InspectOldRunAndEmptyScopeAreHidden(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	if _, err := svc.RunInspect(asset.InspectScope{}, "admin"); err != nil {
		t.Fatal(err)
	}
	seedAsset(t, svc, asset.TypeHost, "db-01", "db-01", "db-01", map[string]string{"cpuCores": "16"})
	run, err := svc.RunInspect(asset.InspectScope{Filter: asset.ListFilter{Node: "db-01"}}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/inspect/runs/" + strconv.FormatInt(run.ID, 10) + "/findings"
	for _, p := range []*auth.Principal{
		restrictedPrincipal([]string{"inspect:read"}, "g1"),
		restrictedPrincipal([]string{"inspect:read"}),
	} {
		for _, target := range []string{"/api/v1/inspect/runs", path} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(p, http.MethodGet, target, ""))
			if target == path && rec.Code != http.StatusNotFound {
				t.Fatalf("scope-out finding status = %d: %s", rec.Code, rec.Body.String())
			}
			if target != path && strings.Contains(rec.Body.String(), "node:db-01") {
				t.Fatalf("scope-out run leaked: %s", rec.Body.String())
			}
		}
	}
}

func TestRoutes_InspectBaselineWriteCannotReplaceOtherGroup(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	db, found, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})
	if err != nil || !found {
		t.Fatalf("db asset: %v %v", found, err)
	}
	web, found, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if err != nil || !found {
		t.Fatalf("web asset: %v %v", found, err)
	}
	if _, err := svc.SetBaseline(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"assets:write"}, "g1"), method,
			"/api/v1/assets/"+strconv.FormatInt(web.ID, 10)+"/baseline", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s should not replace g2 baseline, got %d: %s", method, rec.Code, rec.Body.String())
		}
	}
	items, err := svc.Baselines()
	if err != nil || len(items) != 1 || items[0].AssetID != db.ID {
		t.Fatalf("g2 baseline changed: %+v %v", items, err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"inspect:read"}, "g1"), http.MethodGet, "/api/v1/inspect/baselines", ""))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "db-01") {
		t.Fatalf("g2 baseline leaked: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRoutes_InspectDoesNotExposeOtherGroupBaselineValue(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	if _, err := svc.SetBaseline(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
	seedAsset(t, svc, asset.TypeHost, "db-01", "db-01", "db-01", map[string]string{"cpuCores": "secret-g2"})
	if _, err := svc.SetBaseline(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunInspect(asset.InspectScope{}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"inspect:read"}, "g1"), http.MethodGet,
		"/api/v1/inspect/runs/"+strconv.FormatInt(run.ID, 10)+"/findings", ""))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "secret-g2") {
		t.Fatalf("other group baseline disclosed: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRoutes_InspectScopedRunDoesNotCompareOtherGroupBaseline(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	seedAsset(t, svc, asset.TypeHost, "db-01", "db-01", "db-01", map[string]string{"cpuCores": "secret-g2"})
	if _, err := svc.SetBaseline(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, "admin"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"inspect:run"}, "g1"), http.MethodPost, "/api/v1/inspect/runs", ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("run status %d: %s", rec.Code, rec.Body.String())
	}
	var run inspectRunView
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Findings != 0 {
		t.Fatalf("out-of-scope baseline influenced run: %+v", run)
	}
}

// 不存在的巡检记录：全局用户也应得到 404，而不是 200 + 空差异。
// 「记录不存在」与「本次没有差异」必须是两种结果，否则前端无法区分，且与受限分支语义分叉。
func TestRoutes_InspectUnknownRunIsNotFound(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("inspect:read"), http.MethodGet,
		"/api/v1/inspect/runs/9999/findings", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知巡检记录应 404，实际 %d（%s）", rec.Code, rec.Body.String())
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
