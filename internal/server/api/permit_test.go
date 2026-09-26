package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
)

// permitTestAPI 构造「已启用登录认证」的 API（authStore 非空）并附带审计存储。
func permitTestAPI(t *testing.T) (*API, *audit.Store) {
	t.Helper()
	store, err := auth.NewStore(filepath.Join(t.TempDir(), "users.yaml"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	au := audit.New("")
	return &API{authStore: store, audit: au}, au
}

// withPrincipal 把携带指定权限点的授权身份放入请求上下文。
func withPrincipal(r *http.Request, perms ...string) *http.Request {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	p := &auth.Principal{Username: "tester", Permissions: set}
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
}

// TestPermit_AuthDisabledAllowsAll 未启用登录认证时业务接口全放行（兼容策略 1：不得锁死既有部署）。
func TestPermit_AuthDisabledAllowsAll(t *testing.T) {
	a := &API{} // authStore == nil 表示未启用登录认证
	called := false
	h := a.permit(func(http.ResponseWriter, *http.Request) { called = true }, "nodes:write")
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/nodes/web-01/upgrade", nil))
	if !called {
		t.Fatal("未启用登录认证时应放行")
	}
}

// TestPermit_NoPrincipalReturns401 已启用认证但请求上下文无授权身份 → 401。
func TestPermit_NoPrincipalReturns401(t *testing.T) {
	a, _ := permitTestAPI(t)
	h := a.permit(func(http.ResponseWriter, *http.Request) { t.Fatal("不应放行") }, "nodes:read")
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

// TestPermit_MissingPermissionReturns403AndAudits 缺权限点 → 403，且写入授权拒绝审计。
func TestPermit_MissingPermissionReturns403AndAudits(t *testing.T) {
	a, au := permitTestAPI(t)
	h := a.permit(func(http.ResponseWriter, *http.Request) { t.Fatal("不应放行") }, "agent:upgrade")

	req := withPrincipal(httptest.NewRequest(http.MethodPost, "/api/v1/nodes/web-01/upgrade", nil), "nodes:read")
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	events := au.List(1, "", "")
	if len(events) != 1 {
		t.Fatalf("审计事件 = %d, want 1", len(events))
	}
	if events[0].Action != "deny" || events[0].Category != "authorization" {
		t.Fatalf("审计内容不符: %+v", events[0])
	}
}

// TestPermit_HasPermissionPasses 拥有权限点 → 放行。
func TestPermit_HasPermissionPasses(t *testing.T) {
	a, _ := permitTestAPI(t)
	called := false
	h := a.permit(func(http.ResponseWriter, *http.Request) { called = true }, "nodes:read")

	req := withPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/nodes", nil), "nodes:read")
	h(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("拥有权限时应放行")
	}
}

// TestPermit_OnlyExactPermissionPasses 权限点需精确匹配，相邻权限不得越权。
func TestPermit_OnlyExactPermissionPasses(t *testing.T) {
	a, _ := permitTestAPI(t)
	called := false
	// 仅有 nodes:read，访问要求 nodes:write 的接口应被拒。
	h := a.permit(func(http.ResponseWriter, *http.Request) { called = true }, "nodes:write")

	req := withPrincipal(httptest.NewRequest(http.MethodDelete, "/api/v1/nodes/web-01", nil), "nodes:read")
	rec := httptest.NewRecorder()
	h(rec, req)
	if called || rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, called = %v；nodes:read 不应满足 nodes:write", rec.Code, called)
	}
}

// TestPermit_AuthzSemanticsUnchanged 权限管理接口（authz）在未启用认证时仍返回 503，未被 permit 语义影响。
func TestPermit_AuthzSemanticsUnchanged(t *testing.T) {
	a := &API{}
	rec := httptest.NewRecorder()
	h := a.authz(func(http.ResponseWriter, *http.Request) { t.Fatal("不应放行") }, "users:manage")
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/users", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}
