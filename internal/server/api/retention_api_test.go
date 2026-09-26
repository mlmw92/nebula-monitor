package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/retention"
)

// TestRoutes_Retention_PermissionGate 保留策略属系统配置：缺 system:config 一律拒绝。
func TestRoutes_Retention_PermissionGate(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("dashboard:read")

	cases := []struct{ method, target string }{
		{http.MethodGet, "/api/v1/system/retention"},
		{http.MethodPut, "/api/v1/system/retention"},
		{http.MethodPost, "/api/v1/system/retention/cleanup"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, tc.method, tc.target, "{}"))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s 应 403，got %d", tc.method, tc.target, rec.Code)
		}
	}
}

// TestRoutes_Retention_NilManager 未注入管理器时：查询可用（返回默认策略），写入类返回 503。
func TestRoutes_Retention_NilManager(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("system:config")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/system/retention", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("查询应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	cfg, _ := decodeBody(t, rec)["config"].(map[string]any)
	if cfg == nil || cfg["acksDays"] != float64(retention.DefaultAcksDays) {
		t.Fatalf("应返回默认策略：%v", cfg)
	}

	for _, target := range []string{"/api/v1/system/retention", "/api/v1/system/retention/cleanup"} {
		method := http.MethodPut
		if target != "/api/v1/system/retention" {
			method = http.MethodPost
		}
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, method, target, "{}"))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 应 503，got %d", method, target, rec.Code)
		}
	}
}

// TestRoutes_Retention_SaveAndCleanup 注入真实管理器：保存策略并回显，清理在「天数均为 0」时说明原因。
func TestRoutes_Retention_SaveAndCleanup(t *testing.T) {
	a := scopeTestAPI(t)
	acks := alert.NewAckStore(filepath.Join(t.TempDir(), "acks.json"))
	mgr, err := retention.New(filepath.Join(t.TempDir(), "retention.yaml"), retention.DefaultConfig(), acks, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建保留策略失败: %v", err)
	}
	a.retention = mgr
	mux := newRoutesMux(a)
	p := globalPrincipal("system:config")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPut, "/api/v1/system/retention",
		`{"enabled":false,"acksDays":30,"reportsDays":0,"intervalHours":6}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	cfg, _ := decodeBody(t, rec)["config"].(map[string]any)
	if cfg["enabled"] != false || cfg["acksDays"] != float64(30) || cfg["intervalHours"] != float64(6) {
		t.Fatalf("应回显保存后的策略：%v", cfg)
	}

	// 报告保留为 0 → 该类不清理；处置记录保留 30 天但记录未超期 → 无删除
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/system/retention/cleanup", "{}"))
	if rec.Code != http.StatusOK {
		t.Fatalf("清理应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	res := decodeBody(t, rec)
	if res["acksRemoved"] != float64(0) || res["reportFilesRemoved"] != float64(0) {
		t.Fatalf("不应误删数据：%v", res)
	}
	if _, ok := res["at"].(float64); !ok {
		t.Fatalf("应返回清理时间：%v", res)
	}
}
