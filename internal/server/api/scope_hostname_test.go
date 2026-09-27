package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hostnameScopedPaths 是以 hostname 作为唯一目标参数的四个业务路由。
var hostnameScopedPaths = []string{
	"/api/v1/query/listeners",
	"/api/v1/query/firewall",
	"/api/v1/query/firewall/status",
	"/api/v1/processes",
}

// TestRoutes_HostnameScope_OutOfScopeForbidden 受限用户访问范围外已注册节点（hostname=db-01）应 403 且写审计，
// 且不依赖对应缓存是否有值——授权在进入 handler 前完成。
func TestRoutes_HostnameScope_OutOfScopeForbidden(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path, func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path+"?hostname=db-01", ""))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
			if n := len(a.audit.List(10, "", "")); n != 1 {
				t.Fatalf("%s: expected exactly one denial audit, got %d", path, n)
			}
		})
	}
}

// TestRoutes_HostnameScope_UnknownHostnameForbidden 受限用户访问未注册节点（hostname=ghost）必须 403，
// 不能因为「未注册节点默认放行交 handler 404」的旧规则而进入 handler。
func TestRoutes_HostnameScope_UnknownHostnameForbidden(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path, func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path+"?hostname=ghost", ""))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
			if n := len(a.audit.List(10, "", "")); n != 1 {
				t.Fatalf("%s: expected exactly one denial audit, got %d", path, n)
			}
		})
	}
}

// TestRoutes_HostnameScope_ConflictingParamsBadRequest hostname 与 node 同时提供且矛盾时 400。
func TestRoutes_HostnameScope_ConflictingParamsBadRequest(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path, func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path+"?hostname=web-01&node=db-01", ""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRoutes_HostnameScope_MissingHostnameBadRequest 不带 hostname 参数应 400，不进入 handler。
func TestRoutes_HostnameScope_MissingHostnameBadRequest(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path, func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path, ""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRoutes_HostnameScope_AuthorizedHostnamePasses 范围内 hostname 不应因权限被拒绝，且 handler 真的按
// 该 hostname 完成查询（/api/v1/processes 缓存未命中时回退到 QueryInstant，用 fixture store 断言目标节点）。
func TestRoutes_HostnameScope_AuthorizedHostnamePasses(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path, func(t *testing.T) {
			a, s := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, path+"?hostname=web-01", ""))
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusBadRequest {
				t.Fatalf("%s: code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
			if path == "/api/v1/processes" {
				// 缓存未命中回退到 proc_cpu + proc_mem 两次 QueryInstant，均按 web-01 取数。
				if s.instantCalls == 0 || s.instantNode != "web-01" {
					t.Fatalf("%s: 应按 web-01 回退查询，instantCalls=%d instantNode=%q", path, s.instantCalls, s.instantNode)
				}
			}
		})
	}
}

// TestRoutes_HostnameScope_GlobalAndAuthDisabledUnaffected 全局身份可跨组访问，未启用认证时保持放行——
// permitHostname 新增的两条 400 分支不得改变这两类身份的既有行为。
func TestRoutes_HostnameScope_GlobalAndAuthDisabledUnaffected(t *testing.T) {
	for _, path := range hostnameScopedPaths {
		t.Run(path+"/global", func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, path+"?hostname=db-01", ""))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s: 全局身份应可跨组访问，code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
		})
		t.Run(path+"/auth-disabled", func(t *testing.T) {
			a, _ := metricScopeFixture(t)
			a.authStore = nil
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?hostname=ghost", nil))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s: 未启用认证应放行（含未注册节点），code = %d, body = %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRoutes_PathParamNode_ConflictingQueryParamsRejected 路径参数与查询参数（node / hostname）
// 值矛盾时 400，且不进入 handler——设计 §3 要求「带多个节点候选参数而值矛盾时拒绝请求，
// 不用优先级选择绕过检查」，不能因为命中了 {name} 就把查询参数静默忽略。
func TestRoutes_PathParamNode_ConflictingQueryParamsRejected(t *testing.T) {
	for _, suffix := range []string{"?node=db-01", "?hostname=db-01"} {
		t.Run(suffix, func(t *testing.T) {
			a := scopeTestAPI(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/nodes/web-01"+suffix, ""))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "节点参数冲突") {
				t.Fatalf("handler 不应被进入（应返回冲突错误）: %s", rec.Body.String())
			}
		})
	}
}

// TestRoutes_PathParamNode_SameQueryParamAllowed 路径参数与查询参数同值不算冲突，照常交给 handler。
func TestRoutes_PathParamNode_SameQueryParamAllowed(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/nodes/web-01?node=web-01", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "web-01") {
		t.Fatalf("handler 应被进入并返回节点详情: %s", rec.Body.String())
	}
}

// TestRoutes_WSAuthorize_UnknownNodeForbidden 受限身份对 WS 查询参数目标必须 fail-closed：
// 未注册节点（ghost）与范围外节点（db-01）都返回 403 而非握手成功（101）。
func TestRoutes_WSAuthorize_UnknownNodeForbidden(t *testing.T) {
	for _, tc := range []struct {
		node string
		want int
		pass bool
	}{
		{"ghost", http.StatusForbidden, false},
		{"db-01", http.StatusForbidden, false},
		{"web-01", http.StatusOK, true},
	} {
		t.Run(tc.node, func(t *testing.T) {
			a := scopeTestAPI(t)
			called := false
			h := a.wsAuthorize(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})
			rec := httptest.NewRecorder()
			h(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=metrics&node="+tc.node, ""))
			if rec.Code != tc.want {
				t.Fatalf("code = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
			if called != tc.pass {
				t.Fatalf("handler 被调用 = %v, want %v", called, tc.pass)
			}
			if tc.want == http.StatusForbidden {
				if n := len(a.audit.List(10, "", "")); n != 1 {
					t.Fatalf("expected exactly one denial audit, got %d", n)
				}
			}
		})
	}
}

// TestRoutes_WSAuthorize_UnknownNodeForbiddenOnRealRoute 在真实路由表上确认 /ws 未注册节点
// 返回 403（而非 101 握手成功）。node 为查询参数目标，授权层在握手前即拒绝。
func TestRoutes_WSAuthorize_UnknownNodeForbiddenOnRealRoute(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	a.RegisterWS(mux, nil)
	for _, node := range []string{"ghost", "db-01"} {
		t.Run(node, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/ws?topic=metrics&node="+node, ""))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRoutes_AlertsConflictingNode_BadRequest /alerts 的 hostname 与 node 矛盾时 400。
func TestRoutes_AlertsConflictingNode_BadRequest(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts?hostname=db-01&node=web-01", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRoutes_AlertsHostname_ScopedAndFiltered 仅提供 hostname 时按 hostname 做范围校验，
// 范围内放行并沿用既有结果过滤语义。
func TestRoutes_AlertsHostname_ScopedAndFiltered(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts?hostname=web-01", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, _ := decodeBody(t, rec)["alerts"].([]any)
	if len(got) != 1 || got[0].(map[string]any)["node"] != "web-01" {
		t.Fatalf("expected exactly one alert scoped to web-01: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts?hostname=db-01", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope hostname must 403: code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRoutes_AlertsQueryTarget_UnknownNodeForbidden /alerts 是查询参数目标（无路径参数），受限身份
// 访问未注册节点必须 fail-closed 403 且写审计（无论用 node= 还是 hostname=），不能像路径参数路由
// 那样静默放行、返回 200 空结果——那会让「节点是否已注册」变成一个可探测的 oracle。
func TestRoutes_AlertsQueryTarget_UnknownNodeForbidden(t *testing.T) {
	for _, suffix := range []string{"?node=ghost", "?hostname=ghost"} {
		t.Run(suffix, func(t *testing.T) {
			a := alertTestAPI(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts"+suffix, ""))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
			}
			if n := len(a.audit.List(10, "", "")); n != 1 {
				t.Fatalf("expected exactly one denial audit, got %d", n)
			}
		})
	}
}

// TestRoutes_PathParamNode_UnknownNodeStays404 路径参数形式的节点目标（{name}）保持既有 404 语义：
// 未注册节点不应被 checkNodeTarget 的 fail-closed 规则波及，仍交给 handler 判定（此处退化为节点管理器
// 直接返回 not-registered，验证行为未被本次改动影响）。
func TestRoutes_PathParamNode_UnknownNodeStays404(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/nodes/ghost", ""))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("路径参数未注册节点不应 403（应保持 404 语义），code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}
