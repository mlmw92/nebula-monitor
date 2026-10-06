package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
)

// 未认证不得读取审计记录。该约束现已由路由级 permit 包装承担（见 RegisterRoutes），
// 而非 handler 内部自检；authStore 未注入（认证未启用）时按兼容策略 1 放行。
func TestHandleAuditEventsRequiresAuthentication(t *testing.T) {
	a := &API{auth: config.AuthConfig{Enabled: true}, audit: audit.New("")}
	rec := httptest.NewRecorder()
	a.permit(a.handleAuditEvents, "audit:read")(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("认证未启用（authStore 未注入）应放行，status = %d", rec.Code)
	}

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "users.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	withStore := &API{auth: config.AuthConfig{Enabled: true}, audit: audit.New(""), authStore: store}
	rec = httptest.NewRecorder()
	withStore.permit(withStore.handleAuditEvents, "audit:read")(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("已启用认证且无身份时应 401，status = %d", rec.Code)
	}
}

func TestHandleAuditEventsReturnsFilteredEvents(t *testing.T) {
	store := audit.New("")
	if err := store.Record(audit.Event{User: "admin", Method: http.MethodPost, Path: "/api/v1/rules", Status: http.StatusOK, Succeeded: true, Category: "management", Action: "create_or_apply /api/v1/rules"}); err != nil {
		t.Fatal(err)
	}
	a := &API{auth: config.AuthConfig{Enabled: false}, audit: store}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?limit=10&user=admin&path=rules&category=management", nil)
	rec := httptest.NewRecorder()
	a.handleAuditEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Events []audit.Event `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Action == "" {
		t.Fatalf("unexpected events: %#v", body.Events)
	}
}

func TestHandleAuditExportIgnoresPageOffsetAndLimit(t *testing.T) {
	store := audit.New("")
	for i := 0; i < 3; i++ {
		if err := store.Record(audit.Event{User: "admin", Method: http.MethodPost, Path: "/api/v1/assets", Category: "management"}); err != nil {
			t.Fatal(err)
		}
	}
	a := &API{audit: store}
	// 列表停留在末页且每页只看一条：导出仍应覆盖当前筛选的全部记录。
	rec := httptest.NewRecorder()
	a.handleAuditExport(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/export?limit=1&offset=2&category=management", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 { // 表头 + 三条记录
		t.Fatalf("exported %d CSV rows, want 4", len(rows))
	}
}

func TestRoutes_AuditExportRequiresIndependentPermission(t *testing.T) {
	a, _ := permitTestAPI(t)
	mux := newRoutesMux(a)
	if err := a.audit.Record(audit.Event{User: "admin", Path: "/api/v1/rules"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		perms      []string
		want       int
	}{
		{"读审计不等于导出", "/api/v1/audit/export", []string{"audit:read"}, http.StatusForbidden},
		{"导出权限可下载", "/api/v1/audit/export", []string{"audit:export"}, http.StatusOK},
		{"旧格式入口仍需双重授权", "/api/v1/audit/events?format=csv", []string{"audit:read"}, http.StatusForbidden},
		{"仅导出不能经旧入口绕开读权限", "/api/v1/audit/events?format=csv", []string{"audit:export"}, http.StatusForbidden},
		{"双重授权允许旧入口", "/api/v1/audit/events?format=csv", []string{"audit:read", "audit:export"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, withPrincipal(httptest.NewRequest(http.MethodGet, tc.path, nil), tc.perms...))
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestHandleAuditEventsExportsCSV(t *testing.T) {
	store := audit.New("")
	if err := store.Record(audit.Event{User: "admin", Method: http.MethodPost, Path: "/api/v1/rules", Status: http.StatusOK, Succeeded: true, Category: "management", Action: "create_or_apply /api/v1/rules"}); err != nil {
		t.Fatal(err)
	}
	a := &API{auth: config.AuthConfig{Enabled: false}, audit: store}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events?format=csv&category=management", nil)
	rec := httptest.NewRecorder()
	a.handleAuditEvents(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Disposition"); got == "" {
		t.Fatal("expected CSV content disposition")
	}
	if !strings.Contains(rec.Body.String(), "category") || !strings.Contains(rec.Body.String(), "create_or_apply") {
		t.Fatalf("unexpected CSV body: %s", rec.Body.String())
	}
}
