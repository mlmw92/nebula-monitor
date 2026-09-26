package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
)

// fakeRules 是 RulesProvider 的测试替身：Get 恒返回未找到，用于验证「通过授权后进入 handler」的行为。
type fakeRules struct{}

func (fakeRules) List() []model.AlertRule                  { return nil }
func (fakeRules) Get(string) (model.AlertRule, bool)       { return model.AlertRule{}, false }
func (fakeRules) Create(r model.AlertRule) model.AlertRule { return r }
func (fakeRules) Update(model.AlertRule) error             { return nil }
func (fakeRules) Delete(string) error                      { return nil }
func (fakeRules) ImportRules([]model.AlertRule, bool) (int, int, error) {
	return 0, 0, nil
}

// fakeAlerts 是 AlertStore 的测试替身。
type fakeAlerts struct {
	active []model.AlertEvent
	recent []model.AlertEvent
}

func (f *fakeAlerts) Active() []model.AlertEvent    { return f.active }
func (f *fakeAlerts) Recent(int) []model.AlertEvent { return f.recent }

// alertEvents 两台主机各一条 firing 告警（web-01→g1，db-01→g2）。
func alertEvents() []model.AlertEvent {
	return []model.AlertEvent{
		{RuleID: "r1", RuleName: "CPU 使用率过高", Node: "web-01", Instance: "cpu_usage", State: model.AlertStateFiring, Severity: model.SeverityCritical, StartsAt: 1000},
		{RuleID: "r1", RuleName: "CPU 使用率过高", Node: "db-01", Instance: "cpu_usage", State: model.AlertStateFiring, Severity: model.SeverityCritical, StartsAt: 2000},
	}
}

// alertTestAPI 构造带告警替身与真实 AckStore 的 API。
func alertTestAPI(t *testing.T) *API {
	t.Helper()
	a := scopeTestAPI(t)
	a.alerts = &fakeAlerts{active: alertEvents(), recent: alertEvents()}
	a.acks = alert.NewAckStore(filepath.Join(t.TempDir(), "acks.json"))
	return a
}

// TestRoutes_Alerts_FilteredByScope 告警列表按节点资源范围过滤。
func TestRoutes_Alerts_FilteredByScope(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，body = %s", rec.Code, rec.Body.String())
	}
	got, _ := decodeBody(t, rec)["alerts"].([]any)
	if len(got) != 1 {
		t.Fatalf("受限用户应只看到 1 条告警，实际 %d：%s", len(got), rec.Body.String())
	}
	if node := got[0].(map[string]any)["node"]; node != "web-01" {
		t.Fatalf("告警归属节点不符: %v", node)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("alerts:read"), http.MethodGet, "/api/v1/alerts", ""))
	if got, _ := decodeBody(t, rec)["alerts"].([]any); len(got) != 2 {
		t.Fatalf("全局用户应看到 2 条告警，实际 %d", len(got))
	}
}

// TestRoutes_Alerts_NodeParamOutOfScopeDenied 显式指定范围外节点查询时直接 403（permitNode）。
func TestRoutes_Alerts_NodeParamOutOfScopeDenied(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts?node=db-01", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("范围外节点查询应 403，code = %d", rec.Code)
	}
}

// TestRoutes_AlertAck_ScopeEnforced 确认告警需落在资源范围内：范围外 403（含审计），范围内 200 并落库。
func TestRoutes_AlertAck_ScopeEnforced(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"alerts:write"}, "g1")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/alerts/ack", `{"rule":"r1","host":"db-01","instance":"cpu_usage","startsAt":2000}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("范围外节点的确认应 403，code = %d，body = %s", rec.Code, rec.Body.String())
	}
	if events := a.audit.List(1, "", ""); len(events) != 1 {
		t.Fatalf("应记录 1 条授权拒绝审计，实际 %d", len(events))
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/alerts/ack", `{"rule":"r1","host":"web-01","instance":"cpu_usage","startsAt":1000}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("范围内节点的确认应 200，code = %d，body = %s", rec.Code, rec.Body.String())
	}
	if len(a.acks.Map()) != 1 {
		t.Fatalf("确认记录应落库 1 条，实际 %d", len(a.acks.Map()))
	}
}

// TestRoutes_AlertAcks_FilteredByScope 确认记录列表按节点范围过滤。
func TestRoutes_AlertAcks_FilteredByScope(t *testing.T) {
	a := alertTestAPI(t)
	a.acks.Mark("r1", "web-01", "cpu_usage", 1000, "ops")
	a.acks.Mark("r1", "db-01", "cpu_usage", 2000, "ops")
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts/acks", ""))
	acks, _ := decodeBody(t, rec)["acks"].(map[string]any)
	if len(acks) != 1 {
		t.Fatalf("受限用户应只看到 1 条确认记录，实际 %d：%s", len(acks), rec.Body.String())
	}
	for k := range acks {
		// key 形如 rule|host|instance|startsAt，第 2 段（下标 1）为节点名
		parts := strings.Split(k, "|")
		if len(parts) < 2 || parts[1] != "web-01" {
			t.Fatalf("确认记录应只含 web-01，实际 key = %s", k)
		}
	}
}

// TestRoutes_AlertStats_FilteredByScope 统计口径与列表一致（只统计可见节点）。
func TestRoutes_AlertStats_FilteredByScope(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/alerts/stats", ""))
	if firing, _ := decodeBody(t, rec)["firing"].(float64); firing != 1 {
		t.Fatalf("受限用户 firing 应为 1，实际 %v：%s", firing, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("alerts:read"), http.MethodGet, "/api/v1/alerts/stats", ""))
	if firing, _ := decodeBody(t, rec)["firing"].(float64); firing != 2 {
		t.Fatalf("全局用户 firing 应为 2，实际 %v", firing)
	}
}

// TestRoutes_Notify_PermissionSeparated notify:read 不得用于写通知配置（notify:write 为高危权限点）。
func TestRoutes_Notify_PermissionSeparated(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"notify:read"}, "g1"), http.MethodPut, "/api/v1/notify", `{}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("notify:read 不应满足写操作，code = %d", rec.Code)
	}

	// 无 notify:read 时连读也拒绝
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:read"}, "g1"), http.MethodGet, "/api/v1/notify", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 notify:read 应 403，code = %d", rec.Code)
	}
}

// TestRoutes_Rules_SilenceNeedsSilenceWrite 规则临时静默需要 silence:write，而非 alerts:write。
func TestRoutes_Rules_SilenceNeedsSilenceWrite(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"alerts:write"}, "g1"), http.MethodPost, "/api/v1/rules/r1/toggle-silence", `{"silenced":true}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("alerts:write 不应满足静默操作，code = %d", rec.Code)
	}

	a.rules = fakeRules{}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"silence:write"}, "g1"), http.MethodPost, "/api/v1/rules/r1/toggle-silence", `{"silenced":true}`))
	// 规则不存在时应为 404（说明已通过授权、进入了 handler），而不是 403
	if rec.Code != http.StatusNotFound {
		t.Fatalf("silence:write 应放行至 handler 并返回 404，实际 code = %d，body = %s", rec.Code, rec.Body.String())
	}
}
