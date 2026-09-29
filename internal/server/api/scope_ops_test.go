package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRoutes_AuditViewAndExportSplit 审计的「查看」与「导出」已按权限点分离：
// 查看需 audit:read；导出（/audit/export 或旧的 ?format=csv）需 audit:export。
func TestRoutes_AuditViewAndExportSplit(t *testing.T) {
	cases := []struct {
		name     string
		perms    []string
		target   string
		wantCode int
		wantCSV  bool
	}{
		{"查看（audit:read）", []string{"audit:read"}, "/api/v1/audit/events", http.StatusOK, false},
		{"旧调用方带 format=csv 但只有 audit:read", []string{"audit:read"}, "/api/v1/audit/events?format=csv", http.StatusForbidden, false},
		{"旧调用方带 format=csv 且有 audit:export", []string{"audit:read", "audit:export"}, "/api/v1/audit/events?format=csv", http.StatusOK, true},
		{"新导出路由（audit:export）", []string{"audit:export"}, "/api/v1/audit/export", http.StatusOK, true},
		{"新导出路由缺权限", []string{"audit:read"}, "/api/v1/audit/export", http.StatusForbidden, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := scopeTestAPI(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(globalPrincipal(tc.perms...), http.MethodGet, tc.target, ""))
			if rec.Code != tc.wantCode {
				t.Fatalf("code = %d，want %d（body=%s）", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantCSV && rec.Header().Get("Content-Disposition") == "" {
				t.Fatal("导出响应缺少 CSV Content-Disposition")
			}
		})
	}
}

// TestRoutes_BatchE_PermissionPoints 批次 E 各接口的权限点不可互相顶替。
func TestRoutes_BatchE_PermissionPoints(t *testing.T) {
	cases := []struct {
		name   string
		method string
		target string
		perm   string
	}{
		{"安装信息含 Agent 密钥，需 agent:secret:read", http.MethodGet, "/api/v1/install-info", "agent:upgrade"},
		{"地理库属系统配置，system:upgrade 不可顶替", http.MethodGet, "/api/v1/system/geoip", "system:upgrade"},
		{"系统升级需 system:upgrade", http.MethodGet, "/api/v1/system/upgrade/current", "system:config"},
		{"仪表盘写入需 dashboard:write", http.MethodPost, "/api/v1/dashboards", "dashboard:read"},
		{"安全事件需 security:read", http.MethodGet, "/api/v1/security/events", "middleware:read"},
		{"下发防护指令需 security:write", http.MethodPost, "/api/v1/security/defense/web-01/enable", "security:read"},
		{"防护任务列表需 security:read", http.MethodGet, "/api/v1/security/defense/tasks", "alerts:read"},
		{"拨测写入需 probe:write", http.MethodPost, "/api/v1/dialtest/tasks", "probe:read"},
		{"报告下载属导出，需 report:export", http.MethodGet, "/api/v1/report/download", "report:read"},
		{"代理状态需 agent:read", http.MethodGet, "/api/v1/proxy/status", "alerts:read"},
		{"大屏配置需 system:config", http.MethodGet, "/api/v1/screen/config", "dashboard:read"},
		{"品牌配置写入需 system:config", http.MethodPut, "/api/v1/ui/settings", "dashboard:write"},
		{"日志检索需 logs:read", http.MethodGet, "/api/v1/logs?q=x", "alerts:read"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := scopeTestAPI(t)
			mux := newRoutesMux(a)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(globalPrincipal(tc.perm), tc.method, tc.target, ""))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("持有 %s 不应放行 %s %s，code = %d", tc.perm, tc.method, tc.target, rec.Code)
			}
		})
	}
}

// TestRoutes_DefenseNodePathParam_ScopeEnforced 安全防护接口使用 {node} 路径参数，同样受资源范围约束。
func TestRoutes_DefenseNodePathParam_ScopeEnforced(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"security:read"}, "g1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/security/defense/status/db-01", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("范围外节点应 403，code = %d", rec.Code)
	}

	// 范围内节点：不应因授权被拒（未启用防护时为 400，但绝不能是 403）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/security/defense/status/web-01", ""))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("范围内节点不应 403：%s", rec.Body.String())
	}
}

// TestRoutes_AlertTestNeedsNotifyWrite 测试告警会写事件并向通知渠道发消息，因此路由复用 notify:write：
// 只有 alerts:write 不足以触发，避免「能管规则就能发通知」；未启用认证仍沿用兼容放行。
func TestRoutes_AlertTestNeedsNotifyWrite(t *testing.T) {
	// 持 alerts:write 但无 notify:write → 403，且恰好 1 条授权拒绝审计。
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("alerts:write"), http.MethodPost, "/api/v1/alerts/test", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未授权不应触发通知: %d", rec.Code)
	}
	if events := a.audit.List(1, "", ""); len(events) != 1 {
		t.Fatalf("应恰好记录 1 条授权拒绝审计，实际 %d", len(events))
	}

	// 持 notify:write → 进入 handler（scopeTestAPI 的 engine 为 nil，期望 500）。
	a2 := scopeTestAPI(t)
	mux2 := newRoutesMux(a2)
	rec = httptest.NewRecorder()
	mux2.ServeHTTP(rec, reqWith(globalPrincipal("notify:write"), http.MethodPost, "/api/v1/alerts/test", ""))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("有权应进入 handler（engine=nil 返回 500），code = %d，body = %s", rec.Code, rec.Body.String())
	}
	if events := a2.audit.List(1, "", ""); len(events) != 0 {
		t.Fatalf("有权调用不应写授权拒绝审计，实际 %d", len(events))
	}

	// 未启用认证（authStore=nil）→ 兼容放行至 handler（engine=nil 返回 500）。
	base := scopeTestAPI(t)
	a3 := &API{nodeMgr: base.nodeMgr} // authStore 为 nil = 未启用登录认证
	mux3 := newRoutesMux(a3)
	rec = httptest.NewRecorder()
	mux3.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/alerts/test", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("未启用认证应放行至 handler（engine=nil 返回 500），code = %d，body = %s", rec.Code, rec.Body.String())
	}
}

// TestRoutes_VersionStaysOpenForLoggedInUsers 版本接口不设权限点（侧栏展示依赖），任何登录用户可读。
func TestRoutes_VersionStaysOpenForLoggedInUsers(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"audit:read"}, "g1"), http.MethodGet, "/api/v1/version", ""))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("版本接口不应 403（侧栏版本展示依赖）")
	}
}
