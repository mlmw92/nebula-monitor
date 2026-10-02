package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// 分页：total 必须是符合条件的资产总数（而不是当前页长度），且翻页内容与 total 自洽。
// 依据：1.29.1 的 total 直接取当前页长度，前端无法据此做出真正的分页器。
func TestHandleAssetsPaginationTotalAndPages(t *testing.T) {
	a, svc := assetTestAPI(t)
	for _, name := range []string{"web-03", "web-04", "web-05"} {
		seedAsset(t, svc, asset.TypeHost, name, name, "web-02", nil)
	}
	// 全库共 6 条：web-01、db-01、redis、web-03、web-04、web-05
	readPage := func(query string) (int, []assetView) {
		t.Helper()
		req := assetReq(globalPrincipal("assets:read"), "/api/v1/assets"+query)
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
		return body.Total, body.Assets
	}

	total, page1 := readPage("?limit=2&offset=0")
	if total != 6 {
		t.Fatalf("total 应为全量 6（与页大小无关），实际 %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页应得 2 条，实际 %d", len(page1))
	}

	if _, page2 := readPage("?limit=2&offset=2"); len(page2) != 2 {
		t.Fatalf("第二页应得 2 条，实际 %d", len(page2))
	} else if page2[0].ID == page1[0].ID || page2[1].ID == page1[1].ID {
		t.Fatal("翻页出现了重复资产")
	}

	if _, page3 := readPage("?limit=2&offset=4"); len(page3) != 2 {
		t.Fatalf("第三页应得 2 条，实际 %d", len(page3))
	}
	if _, page4 := readPage("?limit=2&offset=6"); len(page4) != 0 {
		t.Fatalf("越界页应为空，实际 %d", len(page4))
	}
}

// 受限用户的分页：范围必须下推到查询条件——total 只数范围内资产，
// 且每页都装满（不是「取一页再过滤」留下空位）。
func TestHandleAssetsScopedPaginationFillsEveryPage(t *testing.T) {
	a, svc := assetTestAPI(t)
	// g1（web-01/web-02）共 4 条：web-01、redis(web-01)、web-02、nginx(web-01)；g2 另有 db-01。
	seedAsset(t, svc, asset.TypeHost, "web-02", "web-02", "web-02", nil)
	seedAsset(t, svc, asset.TypeMiddlewareInst, "nginx:10.0.0.1:80", "web-01-nginx", "web-01", nil)

	read := func(query string) (int, []assetView) {
		t.Helper()
		req := assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), "/api/v1/assets"+query)
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
		for _, item := range body.Assets {
			if item.Node != "web-01" && item.Node != "web-02" {
				t.Fatalf("出现了范围外资产: %+v", item)
			}
		}
		return body.Total, body.Assets
	}

	total, page1 := read("?limit=2&offset=0")
	if total != 4 {
		t.Fatalf("total 应为范围内的 4 条（不得数到范围外的 db-01），实际 %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("受限用户每页也应装满 2 条，实际 %d", len(page1))
	}
	if _, page2 := read("?limit=2&offset=2"); len(page2) != 2 {
		t.Fatalf("第二页应得 2 条，实际 %d", len(page2))
	}
}

// 派生列（状态 / 来源 / 责任人 / 最近上报 / 展示 ID）全部由既有数据推导，不落库。
// 状态三态在接口层测试里用纯函数覆盖：夹具的资产都是刚写入的，构造不出「失联」。
func TestAssetDerivedFieldsAreComputedNotStored(t *testing.T) {
	staleBefore := int64(1000)
	// 只带采集值的资产（属性 map 的键遵循 asset 包 key@source 的约定，这里只为 ValueFrom 用）。
	// 注意 SeenAt 与属性时间戳是**两个维度**：属性 UpdatedAt 只在值变化时前进，
	// 而 SeenAt 每轮上报都前进——失联判定必须用后者（否则值长期不变的资产会被误判失联）。
	withSeen := func(seen int64) asset.Asset {
		return asset.Asset{
			ID: 7, TypeKey: asset.TypeHost, NaturalKey: "srv-01", SeenAt: seen,
			Attrs: map[string]asset.Attr{
				"os@" + string(asset.SourceDiscovery): {
					Key: "os", Value: "CentOS 7.9", Source: asset.SourceDiscovery, UpdatedAt: 2000,
				},
			},
		}
	}
	if got := assetStatusOf(withSeen(2000), staleBefore); got != asset.StatusOnline {
		t.Fatalf("刚上报应为 online，实际 %q", got)
	}
	if got := assetStatusOf(withSeen(500), staleBefore); got != asset.StatusMissing {
		t.Fatalf("超阈值未上报应为 missing，实际 %q", got)
	}
	// 属性时间戳停在 100（值很久没变），但 SeenAt 新鲜 → 仍应在线。
	// 这正是「中间件资产全部显示失联」那个故障的判定：不能拿属性时间戳当最近上报。
	staleAttrsFreshSeen := asset.Asset{
		ID: 8, TypeKey: asset.TypeHost, NaturalKey: "srv-02", SeenAt: 2000,
		Attrs: map[string]asset.Attr{
			"os@" + string(asset.SourceDiscovery): {
				Key: "os", Value: "CentOS 7.9", Source: asset.SourceDiscovery, UpdatedAt: 100,
			},
		},
	}
	if got := assetStatusOf(staleAttrsFreshSeen, staleBefore); got != asset.StatusOnline {
		t.Fatalf("值长期不变、但上报新鲜时应为 online，实际 %q", got)
	}
	manualOnly := asset.Asset{Attrs: map[string]asset.Attr{
		asset.OwnerKey + "@" + string(asset.SourceManual): {
			Key: asset.OwnerKey, Value: "张三", Source: asset.SourceManual, UpdatedAt: 900,
		},
	}}
	if got := assetStatusOf(manualOnly, staleBefore); got != asset.StatusArchived {
		t.Fatalf("纯人工建档不应算失联，应为 archived，实际 %q", got)
	}

	view := toAssetView(withSeen(2000), staleBefore)
	if view.DisplayID != "ast_7" {
		t.Fatalf("展示 ID 应为 ast_7，实际 %q", view.DisplayID)
	}
	if view.LastSeenAt != 2000 {
		t.Fatalf("最近上报应取 SeenAt，实际 %d", view.LastSeenAt)
	}
	if view.Source != asset.SourceFilterAuto || view.Owner != "" || view.ConflictCount != 0 {
		t.Fatalf("无人工值应为 auto 且无责任人：%+v", view)
	}

	if got := assetSourceKind(2, 0); got != asset.SourceFilterManual {
		t.Fatalf("有人工值且无冲突应为 manual，实际 %q", got)
	}
	if got := assetSourceKind(2, 1); got != asset.SourceFilterMixed {
		t.Fatalf("存在同字段双来源应为 mixed，实际 %q", got)
	}
}

// 摘要与列表必须同源：顶部数字点进列表看到的条数就是摘要上的数字；资源范围同样生效。
func TestHandleAssetSummaryMatchesListAndScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	// web-01（g1，可见）补一条责任人与一条与采集值冲突的人工值
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: asset.SourceManual, Actor: "alice",
		Attrs: map[string]string{asset.OwnerKey: "张三", "cpuCores": "8"},
	}); err != nil {
		t.Fatalf("写入人工值失败: %v", err)
	}

	readSummary := func(p *auth.Principal) asset.Stats {
		t.Helper()
		req := assetReq(p, "/api/v1/assets/summary")
		w := httptest.NewRecorder()
		a.handleAssetSummary(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，响应 %s", w.Code, w.Body.String())
		}
		var stats asset.Stats
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
			t.Fatalf("解析摘要失败: %v", err)
		}
		return stats
	}
	readListTotal := func(p *auth.Principal) int {
		t.Helper()
		req := assetReq(p, "/api/v1/assets")
		w := httptest.NewRecorder()
		a.handleAssets(w, req)
		var body struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析列表失败: %v", err)
		}
		return body.Total
	}

	global := globalPrincipal("assets:read")
	stats := readSummary(global)
	if stats.Total != readListTotal(global) {
		t.Fatalf("摘要总数(%d)必须等于列表总数(%d)", stats.Total, readListTotal(global))
	}
	if stats.Total != 3 || stats.Conflict != 1 || stats.NoOwner != 2 || stats.Missing != 0 {
		t.Fatalf("全局摘要不符：%+v", stats)
	}
	// 冲突资产能通过列表下钻查到（摘要与列表同一套条件）
	req := assetReq(global, "/api/v1/assets?conflict=true")
	w := httptest.NewRecorder()
	a.handleAssets(w, req)
	var drilled struct {
		Assets []assetView `json:"assets"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &drilled); err != nil {
		t.Fatalf("解析下钻结果失败: %v", err)
	}
	if len(drilled.Assets) != 1 || drilled.Assets[0].ConflictCount != 1 || drilled.Assets[0].Owner != "张三" {
		t.Fatalf("冲突下钻结果不符：%+v", drilled.Assets)
	}

	restricted := restrictedPrincipal([]string{"assets:read"}, "g1")
	scoped := readSummary(restricted)
	if scoped.Total != readListTotal(restricted) || scoped.Total != 2 {
		t.Fatalf("受限用户摘要(%d)应等于其列表总数(%d)且为 2", scoped.Total, readListTotal(restricted))
	}
	// 状态/来源取值非法要给出 400，而不是静默忽略筛选条件（那会让人以为"筛了但没生效"）
	bad := httptest.NewRecorder()
	a.handleAssetSummary(bad, assetReq(global, "/api/v1/assets/summary?status=bogus"))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法状态取值应返回 400，实际 %d", bad.Code)
	}
}

// 恢复采集值：删掉人工值、保留采集值、写字段级变更记录；无可恢复字段时报 400。
func TestHandleAssetUpdateResetsManualValue(t *testing.T) {
	a, svc := assetTestAPI(t)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	// 先制造一个冲突字段（cpuCores 采集值 4 + 人工值 8）
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: asset.SourceManual, Actor: "alice", Attrs: map[string]string{"cpuCores": "8"},
	}); err != nil {
		t.Fatalf("写入人工值失败: %v", err)
	}

	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPut, "/api/v1/assets",
		assetWriteBody{ResetAttrs: []string{"cpuCores"}})
	req.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("恢复采集值应返回 200，实际 %d，响应 %s", w.Code, w.Body.String())
	}
	var view assetView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 关键点：响应里必须已经是恢复后的状态，而不是恢复前的对象
	if got := view.Values["cpuCores"]; got != "4" {
		t.Fatalf("恢复后生效值应回落为采集值 4，实际 %q", got)
	}
	if view.ConflictCount != 0 || view.Source != asset.SourceFilterAuto {
		t.Fatalf("恢复后不应再有冲突：%+v", view)
	}

	history, err := svc.History(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, 0)
	if err != nil {
		t.Fatalf("查询变更历史失败: %v", err)
	}
	if history[0].Field != "cpuCores" || history[0].Old != "8" || history[0].New != "" {
		t.Fatalf("恢复应留下变更记录，实际 %+v", history[0])
	}

	// 再恢复一次：没有人工值了，必须明确失败而不是假装成功
	again := assetWriteReq(globalPrincipal("assets:write"), http.MethodPut, "/api/v1/assets",
		assetWriteBody{ResetAttrs: []string{"cpuCores"}})
	again.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	w = httptest.NewRecorder()
	a.handleAssetUpdate(w, again)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("无可恢复字段应返回 400，实际 %d", w.Code)
	}
}

// 关联关系：范围外的对端不返回——一条边足以暴露范围外资产的名字。
func TestHandleAssetLinksSkipsOutOfScopePeer(t *testing.T) {
	a, svc := assetTestAPI(t)
	redisRef := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	// 范围内的对端（web-01，g1）与范围外的对端（db-01，g2）各建一条边
	if err := svc.LinkDiscovered(redisRef, asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, asset.LinkRunsOn); err != nil {
		t.Fatalf("建立范围内关联失败: %v", err)
	}
	if err := svc.LinkDiscovered(redisRef, asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"}, asset.LinkRunsOn); err != nil {
		t.Fatalf("建立范围外关联失败: %v", err)
	}
	redis, _, _ := svc.Get(redisRef)

	read := func(p *auth.Principal) []assetLinkView {
		t.Helper()
		req := assetReq(p, "/api/v1/assets/"+strconv.FormatInt(redis.ID, 10)+"/links")
		req.SetPathValue("id", strconv.FormatInt(redis.ID, 10))
		w := httptest.NewRecorder()
		a.handleAssetLinks(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 = %d，响应 %s", w.Code, w.Body.String())
		}
		var body struct {
			Links []assetLinkView `json:"links"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("解析关联失败: %v", err)
		}
		return body.Links
	}

	global := read(globalPrincipal("assets:read"))
	if len(global) != 2 {
		t.Fatalf("全局用户应看到 2 条边，实际 %d", len(global))
	}
	for _, l := range global {
		if l.Kind != string(asset.LinkRunsOn) || l.Direction != "out" || l.PeerKey == "" {
			t.Fatalf("边的形态不符：%+v", l)
		}
	}

	scoped := read(restrictedPrincipal([]string{"assets:read"}, "g1"))
	if len(scoped) != 1 || scoped[0].PeerKey != "web-01" {
		t.Fatalf("受限用户只应看到范围内对端，实际 %+v", scoped)
	}

	// 反方向也要能取到：从主机看「被谁依赖」，方向标记为 in
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	req := assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), "/api/v1/assets/"+strconv.FormatInt(host.ID, 10)+"/links")
	req.SetPathValue("id", strconv.FormatInt(host.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetLinks(w, req)
	var body struct {
		Links []assetLinkView `json:"links"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析入边失败: %v", err)
	}
	if len(body.Links) != 1 || body.Links[0].Direction != "in" || body.Links[0].PeerKey != "redis:127.0.0.1:6379" {
		t.Fatalf("主机侧应看到方向为 in 的边，实际 %+v", body.Links)
	}
}

// 受限但没有任何可见节点：恒空结果 + 0 条，绝不退化成「不过滤」。
func TestHandleAssetsRestrictedWithoutVisibleNodesIsEmpty(t *testing.T) {
	a, _ := assetTestAPI(t)
	req := assetReq(restrictedPrincipal([]string{"assets:read"}, "g-unknown"), "/api/v1/assets")
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
	if body.Total != 0 || len(body.Assets) != 0 {
		t.Fatalf("无可见节点应返回空结果，实际 total=%d len=%d", body.Total, len(body.Assets))
	}
}

// 人工维护关联的完整生命周期：认领 → 逻辑删除 → 采集不复活 → 取消抑制后可重建。
//
// 「人工优先」的规则本身在 asset 包的 TestManualLinkWinsOverDiscovery 里逐条钉过；
// 这里验的是接口把该语义原样暴露出来：寻址（含 direction）、返回值形态、
// 以及**删除必须是逻辑删除**这个关键点。
func TestHandleAssetLinkWriteLifecycle(t *testing.T) {
	a, svc := assetTestAPI(t)
	hostRef := asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}
	instRef := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	inst, _, _ := svc.Get(instRef)
	host, _, _ := svc.Get(hostRef)

	type linksPayload struct {
		Links      []assetLinkView           `json:"links"`
		Suppressed []assetSuppressedLinkView `json:"suppressed"`
	}
	global := globalPrincipal("assets:write")

	linkPath := func(id int64) string {
		return "/api/v1/assets/" + strconv.FormatInt(id, 10) + "/links"
	}
	read := func() linksPayload {
		t.Helper()
		req := assetReq(global, linkPath(inst.ID))
		req.SetPathValue("id", strconv.FormatInt(inst.ID, 10))
		w := httptest.NewRecorder()
		a.handleAssetLinks(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("读关联状态码 = %d，响应 %s", w.Code, w.Body.String())
		}
		var out linksPayload
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析关联失败: %v", err)
		}
		return out
	}
	// DELETE 的寻址只能走查询串（请求体在 HTTP 语义里没有定义）；POST / restore 走 JSON 体。
	write := func(h func(http.ResponseWriter, *http.Request), assetID int64, method string, body assetLinkWriteBody) (int, linksPayload) {
		t.Helper()
		path := linkPath(assetID)
		var req *http.Request
		if method == http.MethodDelete {
			q := url.Values{}
			for k, v := range map[string]string{
				"toType": body.ToType, "toKey": body.ToKey, "kind": body.Kind, "direction": body.Direction,
			} {
				if v != "" {
					q.Set(k, v)
				}
			}
			req = assetWriteReq(global, method, path+"?"+q.Encode(), nil)
		} else {
			req = assetWriteReq(global, method, path, body)
		}
		req.SetPathValue("id", strconv.FormatInt(assetID, 10))
		w := httptest.NewRecorder()
		h(w, req)
		var out linksPayload
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// 采集先建一条：初始来源是 discovery
	if err := svc.LinkDiscovered(instRef, hostRef, asset.LinkRunsOn); err != nil {
		t.Fatalf("采集建立关联失败: %v", err)
	}
	if got := read(); len(got.Links) != 1 || got.Links[0].Source != string(asset.SourceDiscovery) {
		t.Fatalf("采集建立的边来源应为 discovery，实际 %+v", got.Links)
	}

	body := assetLinkWriteBody{ToType: asset.TypeHost, ToKey: "web-01", Kind: string(asset.LinkRunsOn)}

	// 1) 人工认领同一条边 → 升级为 manual
	code, payload := write(a.handleAssetLinkCreate, inst.ID, http.MethodPost, body)
	if code != http.StatusOK {
		t.Fatalf("人工建边状态码 = %d", code)
	}
	if len(payload.Links) != 1 || payload.Links[0].Source != string(asset.SourceManual) {
		t.Fatalf("人工认领后来源应为 manual，实际 %+v", payload.Links)
	}

	// 2) 删除 = 逻辑删除：边消失、进 suppressed、记下操作人
	code, payload = write(a.handleAssetLinkDelete, inst.ID, http.MethodDelete, body)
	if code != http.StatusOK {
		t.Fatalf("删除状态码 = %d", code)
	}
	if len(payload.Links) != 0 {
		t.Fatalf("删除后不应还有可见的边，实际 %+v", payload.Links)
	}
	if len(payload.Suppressed) != 1 || payload.Suppressed[0].PeerKey != "web-01" {
		t.Fatalf("删除后应留下一条抑制记录，实际 %+v", payload.Suppressed)
	}
	if payload.Suppressed[0].CreatedBy == "" {
		t.Fatal("抑制记录应记下操作人")
	}

	// 3) 关键点：采集再上报同一条边不会把它复活
	if err := svc.LinkDiscovered(instRef, hostRef, asset.LinkRunsOn); err != nil {
		t.Fatalf("采集重复上报不应报错: %v", err)
	}
	if got := read(); len(got.Links) != 0 {
		t.Fatalf("被人工抑制的边不应被采集重建，实际 %+v", got.Links)
	}

	// 4) 取消抑制后采集才能重建，且回到 discovery
	if code, payload = write(a.handleAssetLinkRestore, inst.ID, http.MethodPost, body); code != http.StatusOK {
		t.Fatalf("取消抑制状态码 = %d", code)
	}
	if len(payload.Suppressed) != 0 {
		t.Fatalf("取消抑制后不应还有抑制记录，实际 %+v", payload.Suppressed)
	}
	if err := svc.LinkDiscovered(instRef, hostRef, asset.LinkRunsOn); err != nil {
		t.Fatalf("取消抑制后采集建立关联失败: %v", err)
	}
	if got := read(); len(got.Links) != 1 || got.Links[0].Source != string(asset.SourceDiscovery) {
		t.Fatalf("重建的边应回到 discovery，实际 %+v", got.Links)
	}

	// 5) direction=in：以主机为基准删掉「实例 → 主机」这条入边
	if code, _ = write(a.handleAssetLinkDelete, host.ID, http.MethodDelete, assetLinkWriteBody{
		ToType: asset.TypeMiddlewareInst, ToKey: instRef.NaturalKey,
		Kind: string(asset.LinkRunsOn), Direction: "in",
	}); code != http.StatusOK {
		t.Fatalf("按入边删除状态码 = %d", code)
	}
	if got := read(); len(got.Links) != 0 {
		t.Fatalf("入边也应能被删除，实际 %+v", got.Links)
	}

	// 6) 寻址参数负例：未知类型 / 缺对端 / 非法方向
	for _, bad := range []assetLinkWriteBody{
		{ToType: asset.TypeHost, ToKey: "web-01", Kind: "not_a_kind"},
		{ToType: asset.TypeHost, Kind: string(asset.LinkRunsOn)},
		{ToType: asset.TypeHost, ToKey: "web-01", Kind: string(asset.LinkRunsOn), Direction: "sideways"},
	} {
		if code, _ := write(a.handleAssetLinkCreate, inst.ID, http.MethodPost, bad); code != http.StatusBadRequest {
			t.Fatalf("非法寻址应返回 400，实际 %d（body=%+v）", code, bad)
		}
	}
	// 自关联由服务层拒绝
	if code, _ := write(a.handleAssetLinkCreate, inst.ID, http.MethodPost, assetLinkWriteBody{
		ToType: asset.TypeMiddlewareInst, ToKey: instRef.NaturalKey, Kind: string(asset.LinkDependsOn),
	}); code != http.StatusBadRequest {
		t.Fatalf("自关联应返回 400，实际 %d", code)
	}

	// 范围外的对端一律 404：否则可以用「建边是否成功」探测范围外资产是否存在
	req := assetWriteReq(restrictedPrincipal([]string{"assets:write"}, "g1"), http.MethodPost, linkPath(inst.ID),
		assetLinkWriteBody{ToType: asset.TypeHost, ToKey: "db-01", Kind: string(asset.LinkRunsOn)})
	req.SetPathValue("id", strconv.FormatInt(inst.ID, 10))
	w := httptest.NewRecorder()
	a.handleAssetLinkCreate(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("对范围外对端建边应返回 404，实际 %d（响应 %s）", w.Code, w.Body.String())
	}

	// 反向钉一次「DELETE 只认查询串」：体里给的寻址不生效，必须 400 而不是"悄悄成功"。
	// 若有人"顺手"让它也读请求体，中间设备丢弃 DELETE 体时就会退化成"点了删除没反应"。
	noQuery := assetWriteReq(global, http.MethodDelete, linkPath(inst.ID), body)
	noQuery.SetPathValue("id", strconv.FormatInt(inst.ID, 10))
	wNoQuery := httptest.NewRecorder()
	a.handleAssetLinkDelete(wNoQuery, noQuery)
	if wNoQuery.Code != http.StatusBadRequest {
		t.Fatalf("DELETE 不应从请求体读寻址，期望 400，实际 %d", wNoQuery.Code)
	}
}
