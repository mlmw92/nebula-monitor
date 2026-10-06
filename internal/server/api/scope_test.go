package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/node"
)

// scopeTestAPI 构造带两个分组、三个节点的 API：
//
//	web-01 / web-02 → g1；db-01 → g2
func scopeTestAPI(t *testing.T) *API {
	t.Helper()
	dir := t.TempDir()
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	for _, n := range []struct{ name, group string }{
		{"web-01", "g1"}, {"web-02", "g1"}, {"db-01", "g2"},
	} {
		mgr.Register(&model.ReportPayload{Node: n.name, Group: n.group, Mode: model.ModeCollect, IP: "10.0.0.1"})
	}
	mgr.AddGroup("g1", "web")
	mgr.AddGroup("g2", "db")

	store, err := auth.NewStore(filepath.Join(dir, "users.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &API{nodeMgr: mgr, authStore: store, audit: audit.New("")}
}

// restrictedPrincipal 构造限定到指定分组、拥有指定权限点的授权身份。
func restrictedPrincipal(perms []string, groups ...string) *auth.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return &auth.Principal{
		Username:    "ops1",
		Permissions: set,
		Scope:       auth.Scope{Mode: auth.ScopeRestricted, Groups: groups},
	}
}

// globalPrincipal 构造全局范围、拥有指定权限点的授权身份。
func globalPrincipal(perms ...string) *auth.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return &auth.Principal{
		Username:    "admin",
		Permissions: set,
		Scope:       auth.Scope{Mode: auth.ScopeGlobal},
	}
}

func reqWith(p *auth.Principal, method, target, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
}

// newRoutesMux 注册真实路由表（用于验证「路由确实被授权包装」而非只测包装器本身）。
func newRoutesMux(a *API) *http.ServeMux {
	mux := http.NewServeMux()
	a.RegisterRoutes(mux)
	return mux
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %v / %s", err, rec.Body.String())
	}
	return out
}

// TestRoutes_NodeDetail_ScopeEnforced 单节点接口：范围内 200，范围外 403。
func TestRoutes_NodeDetail_ScopeEnforced(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"nodes:read"}, "g1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/nodes/web-01", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("范围内节点应放行，code = %d，body = %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/nodes/db-01", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("范围外节点应 403，code = %d", rec.Code)
	}
}

// TestRoutes_QueryWithNodeParam_ScopeEnforced 查询类接口的 node 查询参数同样受范围约束。
func TestRoutes_QueryWithNodeParam_ScopeEnforced(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"nodes:read"}, "g1")

	// 只验证授权层：范围外节点在进入 handler 之前即被拒绝（无需真实时序库）。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/query/latest?node=db-01&metric=cpu_usage", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("范围外节点的查询应 403，code = %d，body = %s", rec.Code, rec.Body.String())
	}
}

// TestRoutes_NodeList_FilteredByScope 节点列表按资源范围过滤。
func TestRoutes_NodeList_FilteredByScope(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/nodes", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	nodes, _ := decodeBody(t, rec)["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("受限用户应只看到 2 个节点，实际 %d：%s", len(nodes), rec.Body.String())
	}
	for _, n := range nodes {
		if got := n.(map[string]any)["group"]; got != "g1" {
			t.Fatalf("列表中出现了范围外分组的节点: %v", got)
		}
	}

	// 全局范围用户应看到全部 3 个
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, "/api/v1/nodes", ""))
	if nodes, _ := decodeBody(t, rec)["nodes"].([]any); len(nodes) != 3 {
		t.Fatalf("全局用户应看到 3 个节点，实际 %d", len(nodes))
	}
}

// TestRoutes_GroupList_FilteredByScope 分组列表按资源范围过滤。
func TestRoutes_GroupList_FilteredByScope(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"groups:read"}, "g1"), http.MethodGet, "/api/v1/groups", ""))
	groups, _ := decodeBody(t, rec)["groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("受限用户应只看到 1 个分组，实际 %d：%s", len(groups), rec.Body.String())
	}
	if got := groups[0].(map[string]any)["name"]; got != "g1" {
		t.Fatalf("分组列表内容不符: %v", got)
	}
}

// TestRoutes_MissingPermission_DeniedAndAudited 缺权限点时 403 且写入授权拒绝审计。
func TestRoutes_MissingPermission_DeniedAndAudited(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal(nil, "g1"), http.MethodGet, "/api/v1/nodes", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d，want 403", rec.Code)
	}
	if events := a.audit.List(1, "", ""); len(events) != 1 {
		t.Fatalf("应记录 1 条授权拒绝审计，实际 %d", len(events))
	}
}

// TestRoutes_WritePermissionSeparatedFromRead 只读用户不得执行写操作（nodes:write ≠ nodes:read）。
func TestRoutes_WritePermissionSeparatedFromRead(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"nodes:read"}, "g1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodDelete, "/api/v1/nodes/web-01", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("nodes:read 不应满足 nodes:write，code = %d", rec.Code)
	}
	// 节点应仍在
	if _, ok := a.nodeMgr.GetNode("web-01"); !ok {
		t.Fatal("节点不应被删除")
	}
}

// TestRoutes_AuthDisabledAllowsAll 未启用登录认证时业务接口保持全放行（兼容策略 1）。
func TestRoutes_AuthDisabledAllowsAll(t *testing.T) {
	base := scopeTestAPI(t)
	a := &API{nodeMgr: base.nodeMgr} // authStore 为 nil = 未启用登录认证
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("未启用认证应放行，code = %d", rec.Code)
	}
	if nodes, _ := decodeBody(t, rec)["nodes"].([]any); len(nodes) != 3 {
		t.Fatalf("未启用认证应看到全部节点，实际 %d", len(nodes))
	}
}

// TestRoutes_BatchUpgrade_ScopeDenied 批量升级：请求中含范围外节点时整体拒绝并回报分组。
func TestRoutes_BatchUpgrade_ScopeDenied(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"agent:upgrade"}, "g1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/nodes/upgrade", `{"names":["web-01","db-01"]}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("含范围外节点的批量升级应 403，code = %d，body = %s", rec.Code, rec.Body.String())
	}
	groups, _ := decodeBody(t, rec)["groups"].([]any)
	if len(groups) != 1 || groups[0] != "g2" {
		t.Fatalf("应回报范围外分组 [g2]，实际 %v", groups)
	}
}

// TestWS_Authorize topic 级授权与节点范围校验（WebSocket 握手前完成）。
func TestWS_Authorize(t *testing.T) {
	a := scopeTestAPI(t)
	called := false
	h := a.wsAuthorize(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	cases := []struct {
		name string
		req  *http.Request
		want int
		pass bool
	}{
		{
			name: "未知 topic 拒绝",
			req:  reqWith(globalPrincipal("nodes:read", "alerts:read"), http.MethodGet, "/ws?topic=unknown", ""),
			want: http.StatusBadRequest,
		},
		{
			name: "metrics 缺 node 参数",
			req:  reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=metrics", ""),
			want: http.StatusBadRequest,
		},
		{
			name: "metrics 访问范围外节点",
			req:  reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=metrics&node=db-01", ""),
			want: http.StatusForbidden,
		},
		{
			name: "alerts 缺 alerts:read",
			req:  reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=alerts", ""),
			want: http.StatusForbidden,
		},
		{
			name: "metrics 范围内节点放行",
			req:  reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=metrics&node=web-01", ""),
			want: http.StatusOK,
			pass: true,
		},
		{
			name: "alerts 有权限放行",
			req:  reqWith(globalPrincipal("alerts:read"), http.MethodGet, "/ws?topic=alerts", ""),
			want: http.StatusOK,
			pass: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called = false
			rec := httptest.NewRecorder()
			h(rec, tc.req)
			if rec.Code != tc.want {
				t.Fatalf("code = %d，want %d（body=%s）", rec.Code, tc.want, rec.Body.String())
			}
			if called != tc.pass {
				t.Fatalf("handler 被调用 = %v，want %v", called, tc.pass)
			}
		})
	}
}

// TestAssetAllowedNodesFailsClosedWithoutNodeManager 受限身份遇到「节点管理器缺失」时必须
// fail-closed：返回 nil 会被下游（asset.ListFilter.Nodes / InspectRunsInNodes 等）解释成
// 「全局、不过滤」，把资源范围校验变成越权旁路。与 nodeInScope 的取向保持一致。
func TestAssetAllowedNodesFailsClosedWithoutNodeManager(t *testing.T) {
	a := &API{} // 故意不注入 nodeMgr
	got := a.assetAllowedNodes(restrictedPrincipal([]string{"assets:read"}, "g1"))
	if got == nil {
		t.Fatal("受限身份在缺少节点管理器时不得返回 nil（nil 表示不过滤）")
	}
	if len(got) != 0 {
		t.Fatalf("受限身份在缺少节点管理器时应得到空集合，实际 %v", got)
	}
	if a.assetAllowedNodes(globalPrincipal("assets:read")) != nil {
		t.Fatal("全局范围不应做节点限制")
	}
	if a.assetAllowedNodes(nil) != nil {
		t.Fatal("未启用认证时不应做节点限制")
	}
}

// TestWS_Authorize_AuthDisabled 未启用认证时 WebSocket 订阅保持放行（仅做 topic 合法性校验）。
func TestWS_Authorize_AuthDisabled(t *testing.T) {
	base := scopeTestAPI(t)
	a := &API{nodeMgr: base.nodeMgr}
	called := false
	h := a.wsAuthorize(func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/ws?topic=metrics&node=web-01", nil))
	if !called {
		t.Fatalf("未启用认证应放行，code = %d", rec.Code)
	}
}
