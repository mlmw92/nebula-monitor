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
		// C1 阶段二：采集项模板的读写权限点不可互相顶替
		{"模板列表需 middleware:read", http.MethodGet, "/api/v1/middleware/templates", "alerts:read"},
		{"模板新建需 middleware:write", http.MethodPost, "/api/v1/middleware/templates", "middleware:read"},
		{"模板校验需 middleware:write", http.MethodPost, "/api/v1/middleware/templates/validate", "middleware:read"},
		{"模板更新需 middleware:write", http.MethodPut, "/api/v1/middleware/templates/rabbitmq", "middleware:read"},
		{"模板删除需 middleware:write", http.MethodDelete, "/api/v1/middleware/templates/rabbitmq", "middleware:read"},
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
