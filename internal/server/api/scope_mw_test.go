package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/storage"
)

// fakeStore 仅实现测试所需的即时查询：按指标名返回预设序列，其余方法留空。
type fakeStore struct {
	series map[string][]model.Series
}

var _ storage.Storage = (*fakeStore)(nil)

func (f *fakeStore) Write([]model.Metric) error { return nil }

func (f *fakeStore) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}

func (f *fakeStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}

func (f *fakeStore) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}

func (f *fakeStore) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, nil
}

func (f *fakeStore) QueryAllLatest(name string, _ map[string]string) ([]model.Series, error) {
	return f.series[name], nil
}

func (f *fakeStore) Close() error    { return nil }
func (f *fakeStore) Backend() string { return "fake" }

// nginxUpSeries 构造两台主机上各一个 Nginx 实例的在线序列（web-01→g1，db-01→g2）。
func nginxUpSeries() map[string][]model.Series {
	return map[string][]model.Series{
		"nginx_instance_up": {
			{Labels: map[string]string{"node": "web-01", "instance": "127.0.0.1:80"}, Points: []model.Point{{Value: 1}}},
			{Labels: map[string]string{"node": "db-01", "instance": "127.0.0.1:8080"}, Points: []model.Point{{Value: 1}}},
		},
	}
}

// TestNodeInScope 节点范围判定的边界：未启用认证/全局范围恒真；不可归属节点受限用户不可见。
func TestNodeInScope(t *testing.T) {
	a := scopeTestAPI(t)

	if !a.nodeInScope(nil, "db-01") {
		t.Error("未启用认证（p 为 nil）应恒为 true")
	}
	if !a.nodeInScope(globalPrincipal("middleware:read"), "db-01") {
		t.Error("全局范围应恒为 true")
	}

	p := restrictedPrincipal([]string{"middleware:read"}, "g1")
	if !a.nodeInScope(p, "web-01") {
		t.Error("范围内节点应为 true")
	}
	if a.nodeInScope(p, "db-01") {
		t.Error("范围外节点应为 false")
	}
	if a.nodeInScope(p, "not-exist") {
		t.Error("不可归属节点（不存在）受限用户应为 false")
	}

	// nodeMgr 未注入时受限用户不可归属
	bare := &API{}
	if bare.nodeInScope(p, "web-01") {
		t.Error("nodeMgr 未注入时应视为不可归属")
	}
}

// TestNodeOfKey 从实例键中取节点名。
func TestNodeOfKey(t *testing.T) {
	cases := map[string]string{
		"web-01|127.0.0.1:6379": "web-01",
		"db-01|inst|nodeName":   "db-01",
		"|127.0.0.1:80":         "",
		"no-pipe":               "",
		"":                      "",
	}
	for in, want := range cases {
		if got := nodeOfKey(in); got != want {
			t.Errorf("nodeOfKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRoutes_NginxAccessSummary_FilteredByScope 实例列表端到端按资源范围过滤（走真实路由表）。
func TestRoutes_NginxAccessSummary_FilteredByScope(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &fakeStore{series: nginxUpSeries()}
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"middleware:read"}, "g1"), http.MethodGet, "/api/v1/middleware/nginx/access/summary", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，body = %s", rec.Code, rec.Body.String())
	}
	insts, _ := decodeBody(t, rec)["instances"].([]any)
	if len(insts) != 1 {
		t.Fatalf("受限用户应只看到 1 个实例，实际 %d：%s", len(insts), rec.Body.String())
	}
	if got := insts[0].(map[string]any)["node"]; got != "web-01" {
		t.Fatalf("实例归属节点不符: %v", got)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/middleware/nginx/access/summary", ""))
	if insts, _ := decodeBody(t, rec)["instances"].([]any); len(insts) != 2 {
		t.Fatalf("全局用户应看到 2 个实例，实际 %d", len(insts))
	}
}

// TestRoutes_K8sInstances_FilteredByScope K8s 集群列表按资源范围过滤。
func TestRoutes_K8sInstances_FilteredByScope(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &fakeStore{series: map[string][]model.Series{
		"k8s_cluster_up": {
			{Labels: map[string]string{"node": "web-01", "instance": "prod", "name": "prod"}, Points: []model.Point{{Value: 1}}},
			{Labels: map[string]string{"node": "db-01", "instance": "stage", "name": "stage"}, Points: []model.Point{{Value: 1}}},
		},
	}}
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"middleware:read"}, "g1"), http.MethodGet, "/api/v1/middleware/k8s/instances", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，body = %s", rec.Code, rec.Body.String())
	}
	clusters, _ := decodeBody(t, rec)["clusters"].([]any)
	if len(clusters) != 1 {
		t.Fatalf("受限用户应只看到 1 个集群，实际 %d：%s", len(clusters), rec.Body.String())
	}
	if got := clusters[0].(map[string]any)["instance"]; got != "prod" {
		t.Fatalf("集群 instance 不符: %v", got)
	}
}

// TestRoutes_Middleware_MissingPermission 中间件接口在缺 middleware:read 时 403。
func TestRoutes_Middleware_MissingPermission(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &fakeStore{series: nginxUpSeries()}
	mux := newRoutesMux(a)

	// 只有 nodes:read，不足以读取中间件
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/middleware/nginx/access/summary", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d，want 403", rec.Code)
	}
	if events := a.audit.List(1, "", ""); len(events) != 1 {
		t.Fatalf("应记录授权拒绝审计，实际 %d 条", len(events))
	}
}

// TestRoutes_Middleware_AuthDisabled 未启用认证时中间件接口保持放行。
func TestRoutes_Middleware_AuthDisabled(t *testing.T) {
	base := scopeTestAPI(t)
	a := &API{nodeMgr: base.nodeMgr, store: &fakeStore{series: nginxUpSeries()}}
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/middleware/nginx/access/summary", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，want 200", rec.Code)
	}
	if insts, _ := decodeBody(t, rec)["instances"].([]any); len(insts) != 2 {
		t.Fatalf("未启用认证应看到全部 2 个实例，实际 %d", len(insts))
	}
}

// TestFilterByNodeScope_GlobalReturnsSameSlice 全局范围应零成本原样返回（同一底层数组）。
func TestFilterByNodeScope_GlobalReturnsSameSlice(t *testing.T) {
	a := scopeTestAPI(t)
	items := []string{"web-01|a", "db-01|b"}

	got := filterByNodeScope(a, nil, items, nodeOfKey)
	if len(got) != 2 {
		t.Fatalf("未启用认证应原样返回，实际 %v", got)
	}
	got = filterByNodeScope(a, globalPrincipal("middleware:read"), items, nodeOfKey)
	if len(got) != 2 {
		t.Fatalf("全局范围应原样返回，实际 %v", got)
	}
	got = filterByNodeScope(a, restrictedPrincipal([]string{"middleware:read"}, "g1"), items, nodeOfKey)
	if len(got) != 1 || got[0] != "web-01|a" {
		t.Fatalf("受限范围过滤结果不符: %v", got)
	}
}

// TestBuiltinRoles_HaveMiddlewareRead 只读与运维角色应能查看中间件（否则页面直接空白）。
func TestBuiltinRoles_HaveMiddlewareRead(t *testing.T) {
	for _, r := range auth.BuiltinRoles() {
		if r.Name != auth.RoleReadOnly && r.Name != auth.RoleOpsAdmin {
			continue
		}
		found := false
		for _, p := range r.Permissions {
			if p == "middleware:read" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("内置角色 %s 缺少 middleware:read", r.Name)
		}
	}
}
