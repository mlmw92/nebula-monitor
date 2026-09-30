package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
)

// assetTestAPI 在 scopeTestAPI 的基础上接入真实资产台账（临时库）。
// 两组成员沿用 scopeTestAPI 的既有夹具：web-01/web-02 → g1，db-01 → g2。
func assetTestAPI(t *testing.T) (*API, *asset.Service) {
	t.Helper()
	a := scopeTestAPI(t)
	store, err := asset.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("打开资产库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := asset.NewService(store)
	a.SetAssetService(svc)

	seedAsset(t, svc, asset.TypeHost, "web-01", "web-01", "web-01", map[string]string{"cpuCores": "4"})
	seedAsset(t, svc, asset.TypeHost, "db-01", "db-01", "db-01", nil)
	seedAsset(t, svc, asset.TypeMiddlewareInst, "redis:127.0.0.1:6379", "dev-redis", "web-01", nil)
	return a, svc
}

func seedAsset(t *testing.T, svc *asset.Service, typeKey, naturalKey, name, node string, attrs map[string]string) {
	t.Helper()
	if attrs == nil {
		attrs = map[string]string{}
	}
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: typeKey, NaturalKey: naturalKey, Name: name, Node: node, Attrs: attrs,
	}); err != nil {
		t.Fatalf("准备资产 %s 失败: %v", naturalKey, err)
	}
}

// assetReq 构造带授权身份的 GET 请求。路径参数由调用方用 SetPathValue 补齐。
func assetReq(p *auth.Principal, target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	return req.WithContext(context.WithValue(req.Context(), principalContextKey{}, p))
}

// 受限用户只应看到范围内节点的资产，且 total 不得泄露范围外资产量。
func TestHandleAssetsFiltersByScope(t *testing.T) {
	a, _ := assetTestAPI(t)
	req := assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), "/api/v1/assets")
	w := httptest.NewRecorder()
	a.handleAssets(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", w.Code, w.Body.String())
	}
	var body struct {
		Assets []assetView `json:"assets"`
		Total  int         `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// g1 下可见：web-01 主机 + 其上的 redis；db-01 属 g2 不可见。
	if body.Total != 2 || len(body.Assets) != 2 {
		t.Fatalf("g1 应可见 2 个资产，实际 total=%d len=%d", body.Total, len(body.Assets))
	}
	for _, item := range body.Assets {
		if item.Node != "web-01" {
			t.Fatalf("出现了范围外资产: %+v", item)
		}
	}
}

// 全局范围用户可见全部资产，且生效值（人工优先）与来源都在响应里。
func TestHandleAssetsGlobalScopeReturnsAllWithSources(t *testing.T) {
	a, svc := assetTestAPI(t)
	// 追加一条人工值：验证 values 取人工值、attrs 同时保留两个来源。
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: asset.SourceManual, Actor: "alice", Attrs: map[string]string{"cpuCores": "8"},
	}); err != nil {
		t.Fatalf("写入人工值失败: %v", err)
	}

	req := assetReq(globalPrincipal("assets:read"), "/api/v1/assets?type=host")
	w := httptest.NewRecorder()
	a.handleAssets(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	var body struct {
		Assets []assetView `json:"assets"`
		Total  int         `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if body.Total != 2 {
		t.Fatalf("按类型过滤后应得 2 个主机资产，实际 %d", body.Total)
	}
	for _, item := range body.Assets {
		if item.NaturalKey != "web-01" {
			continue
		}
		if got := item.Values["cpuCores"]; got != "8" {
			t.Fatalf("生效值应优先人工值（8），实际 %q", got)
		}
		sources := map[string]bool{}
		for _, attr := range item.Attrs {
			if attr.Key == "cpuCores" {
				sources[attr.Source] = true
			}
		}
		if !sources["discovery"] || !sources["manual"] {
			t.Fatalf("采集值与人工值应并存，实际来源集合 %+v", sources)
		}
	}
}

// 范围外资产按「不存在」返回：不区分 403，避免用状态码探测范围外资源。
func TestHandleAssetDetailOutOfScopeIs404(t *testing.T) {
	a, svc := assetTestAPI(t)
	dbAsset, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})
	if err != nil || !ok {
		t.Fatalf("准备 db-01 资产失败: ok=%v err=%v", ok, err)
	}

	req := assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), "/api/v1/assets/"+strconv.FormatInt(dbAsset.ID, 10))
	req.SetPathValue("id", strconv.FormatInt(dbAsset.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetDetail(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("范围外资产应返回 404，实际 %d", w.Code)
	}
}

// 变更历史接口应返回字段级 diff，并同样受范围约束。
func TestHandleAssetHistoryReturnsFieldDiff(t *testing.T) {
	a, svc := assetTestAPI(t)
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Attrs: map[string]string{"cpuCores": "16"},
	}); err != nil {
		t.Fatalf("写入变更失败: %v", err)
	}
	host, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if err != nil || !ok {
		t.Fatalf("查询 web-01 失败: ok=%v err=%v", ok, err)
	}

	req := assetReq(globalPrincipal("assets:read"), "/api/v1/assets/"+strconv.FormatInt(host.ID, 10)+"/history")
	req.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetHistory(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", w.Code, w.Body.String())
	}
	var body struct {
		Records []assetChangeView `json:"records"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(body.Records) != 2 {
		t.Fatalf("应有建档 + 一次变更共 2 条记录，实际 %d", len(body.Records))
	}
	newest := body.Records[0]
	if newest.Field != "cpuCores" || newest.Old != "4" || newest.New != "16" || newest.Kind != "update" {
		t.Fatalf("最新记录应为字段级 diff，实际 %+v", newest)
	}
}

// 未注入资产服务时接口返回 503（与其它可选能力一致），而不是 panic 或空列表。
func TestHandleAssetsWithoutServiceReturns503(t *testing.T) {
	a := &API{}
	req := assetReq(globalPrincipal("assets:read"), "/api/v1/assets")
	w := httptest.NewRecorder()
	a.handleAssets(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用资产能力应返回 503，实际 %d", w.Code)
	}
}

// 真实路由表的权限负例：没有 assets:read 的用户拿不到资产列表。
func TestAssetsRouteRequiresPermission(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)

	// 缺少 assets:read：应被 permit 拦下
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, assetReq(restrictedPrincipal([]string{"nodes:read"}, "g1"), "/api/v1/assets"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺少 assets:read 应返回 403，实际 %d", denied.Code)
	}

	// 拥有 assets:read：应正常返回
	allowed := httptest.NewRecorder()
	mux.ServeHTTP(allowed, assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), "/api/v1/assets"))
	if allowed.Code != http.StatusOK {
		t.Fatalf("拥有 assets:read 应返回 200，实际 %d，响应 %s", allowed.Code, allowed.Body.String())
	}
}

// assetWriteReq 构造带 JSON 体与授权身份的写请求。
func assetWriteReq(p *auth.Principal, method, target string, body interface{}) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	return req.WithContext(context.WithValue(req.Context(), principalContextKey{}, p))
}

// 手工新建：以人工来源建档，建档记录带操作人；采集值不受影响。
func TestHandleAssetCreateStoresManualSourceAndActor(t *testing.T) {
	a, svc := assetTestAPI(t)

	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeMiddlewareInst, NaturalKey: "nginx:10.0.0.9:80", Name: "外部 nginx",
		Node: "web-01", Attrs: map[string]string{"owner": "alice", "": "ignored"},
	})
	w := httptest.NewRecorder()
	a.handleAssetCreate(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("新建应返回 201，实际 %d，响应 %s", w.Code, w.Body.String())
	}

	created, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "nginx:10.0.0.9:80"})
	if err != nil || !ok {
		t.Fatalf("新建的资产应可查到: ok=%v err=%v", ok, err)
	}
	if v, _ := created.ValueFrom("owner", asset.SourceManual); v != "alice" {
		t.Fatalf("人工属性应为 manual 来源，实际 %q", v)
	}
	if _, ok := created.Attrs["@manual"]; ok {
		t.Fatal("空属性键不应入库")
	}
	history, err := svc.History(asset.Ref{TypeKey: created.TypeKey, NaturalKey: created.NaturalKey}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if len(history) != 1 || history[0].Kind != asset.ChangeInitial || history[0].Source != asset.SourceManual {
		t.Fatalf("建档记录应为 manual 来源的 initial，实际 %+v", history)
	}
	if history[0].Actor != "admin" {
		t.Fatalf("建档记录应带操作人，实际 %q", history[0].Actor)
	}
}

// 重复新建返回 409：避免「以为是新建、其实覆盖了既有台账」。
func TestHandleAssetCreateConflictWhenExists(t *testing.T) {
	a, _ := assetTestAPI(t)
	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeHost, NaturalKey: "web-01", Node: "web-01",
	})
	w := httptest.NewRecorder()
	a.handleAssetCreate(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("已存在的资产应返回 409，实际 %d", w.Code)
	}
}

// 新建时的资源范围：范围外节点被拒；受限用户不能造「自己也看不见」的无归属资产。
func TestHandleAssetCreateScopeRules(t *testing.T) {
	a, _ := assetTestAPI(t)
	restricted := restrictedPrincipal([]string{"assets:write"}, "g1")

	outOfScope := assetWriteReq(restricted, http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeHost, NaturalKey: "db-02", Node: "db-01",
	})
	w := httptest.NewRecorder()
	a.handleAssetCreate(w, outOfScope)
	if w.Code != http.StatusForbidden {
		t.Fatalf("范围外节点应返回 403，实际 %d", w.Code)
	}

	noNode := assetWriteReq(restricted, http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeHost, NaturalKey: "web-09",
	})
	w = httptest.NewRecorder()
	a.handleAssetCreate(w, noNode)
	if w.Code != http.StatusForbidden {
		t.Fatalf("受限用户未指定归属节点应返回 403，实际 %d", w.Code)
	}

	inScope := assetWriteReq(restricted, http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeHost, NaturalKey: "web-09", Node: "web-02",
	})
	w = httptest.NewRecorder()
	a.handleAssetCreate(w, inScope)
	if w.Code != http.StatusCreated {
		t.Fatalf("范围内新建应返回 201，实际 %d，响应 %s", w.Code, w.Body.String())
	}
}

// 更新人工值：采集值保留、生效值取人工值、变更记录带字段级 diff。
func TestHandleAssetUpdateKeepsDiscoveryValue(t *testing.T) {
	a, svc := assetTestAPI(t)
	host, ok, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if err != nil || !ok {
		t.Fatalf("准备 web-01 失败: ok=%v err=%v", ok, err)
	}

	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPut, "/api/v1/assets", assetWriteBody{
		Name: "web-01（主站）", Attrs: map[string]string{"cpuCores": "16"},
	})
	req.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("更新应返回 200，实际 %d，响应 %s", w.Code, w.Body.String())
	}

	updated, _, err := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if v, _ := updated.Value("cpuCores"); v != "16" {
		t.Fatalf("生效值应为人工值 16，实际 %q", v)
	}
	if v, _ := updated.ValueFrom("cpuCores", asset.SourceDiscovery); v != "4" {
		t.Fatalf("采集值应保留为 4，实际 %q", v)
	}
	if updated.Name != "web-01（主站）" {
		t.Fatalf("名称应更新，实际 %q", updated.Name)
	}

	history, err := svc.History(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if history[0].Field == "" || history[0].Source != asset.SourceManual || history[0].Actor != "admin" {
		t.Fatalf("最新变更应为人工来源且带操作人，实际 %+v", history[0])
	}
}

// 更新接口不接受节点变更（范围锚点不可改），范围外资产按 404 返回。
func TestHandleAssetUpdateRejectsNodeChangeAndOutOfScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	dbHost, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})

	w := httptest.NewRecorder()
	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPut, "/api/v1/assets", assetWriteBody{
		Node: "web-02", Attrs: map[string]string{"cpuCores": "32"},
	})
	req.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	a.handleAssetUpdate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("变更归属节点应返回 400，实际 %d", w.Code)
	}

	w = httptest.NewRecorder()
	req = assetWriteReq(restrictedPrincipal([]string{"assets:write"}, "g1"), http.MethodPut, "/api/v1/assets", assetWriteBody{
		Attrs: map[string]string{"cpuCores": "32"},
	})
	req.SetPathValue("id", strconv.FormatInt(dbHost.ID, 10))
	a.handleAssetUpdate(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("范围外资产更新应返回 404，实际 %d", w.Code)
	}
}

// 真实路由的写权限负例：只有 assets:read 的用户不能改资产。
func TestAssetsWriteRouteRequiresWritePermission(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})

	req := assetWriteReq(restrictedPrincipal([]string{"assets:read"}, "g1"), http.MethodPut,
		"/api/v1/assets/"+strconv.FormatInt(host.ID, 10), assetWriteBody{Attrs: map[string]string{"cpuCores": "32"}})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:write 应返回 403，实际 %d", w.Code)
	}
}
