package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/metrics"
	"github.com/nebula/monitor/internal/server/storage"
)

// metricScopeStore substitutes only external TSDB reads; all routing, scope and auditing remain real.
type metricScopeStore struct {
	storage.Storage
	rangeCalls, instantCalls, allLatestCalls int
	rangeNode, instantNode                   string
	rangeLabels, instantLabels               map[string]string
	series                                   []model.Series
}

func (s *metricScopeStore) QueryRange(node, name string, labels map[string]string, start, end, step int64) ([]model.Series, error) {
	s.rangeCalls++
	s.rangeNode, s.rangeLabels = node, labels
	return s.series, nil
}

func (s *metricScopeStore) QueryInstant(node, name string, labels map[string]string) ([]model.Series, error) {
	s.instantCalls++
	s.instantNode, s.instantLabels = node, labels
	return s.series, nil
}

func (s *metricScopeStore) QueryAllLatest(name string, labels map[string]string) ([]model.Series, error) {
	s.allLatestCalls++
	return s.series, nil
}

func metricScopeFixture(t *testing.T) (*API, *metricScopeStore) {
	t.Helper()
	a := scopeTestAPI(t)
	s := &metricScopeStore{series: []model.Series{
		{Labels: map[string]string{"node": "db-01", "job": "database"}, Points: []model.Point{{Timestamp: 1000, Value: 2}}},
		{Labels: map[string]string{"node": "web-01", "job": "frontend"}, Points: []model.Point{{Timestamp: 1000, Value: 1}}},
		{Labels: map[string]string{"job": "unattributed"}, Points: []model.Point{{Timestamp: 1000, Value: 3}}},
		{Labels: map[string]string{"node": "ghost"}, Points: []model.Point{{Timestamp: 1000, Value: 4}}},
	}}
	a.store = s
	return a, s
}

const metricRangePath = "/api/v1/query/range?metric=cpu_usage&start=1000&end=61000&step=1000"

func TestMetricScope_ConflictingTargetsNeverQuery(t *testing.T) {
	for _, tc := range []struct{ name, suffix string }{
		{"node disagrees with label", "&node=web-01&labels.node=db-01"},
		{"reverse disagreement", "&node=db-01&labels.node=web-01"},
		{"duplicate node parameter", "&node=web-01&node=db-01"},
		{"duplicate label node parameter", "&labels.node=web-01&labels.node=db-01"},
		{"empty label node", "&node=web-01&labels.node="},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, s := metricScopeFixture(t)
			rec := httptest.NewRecorder()
			newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, metricRangePath+tc.suffix, ""))
			if rec.Code != http.StatusBadRequest || s.rangeCalls != 0 {
				t.Fatalf("conflicting target: status=%d rangeCalls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
			}
		})
	}
}

func TestMetricScope_LabelTargetMustBeAuthorizedAndAudited(t *testing.T) {
	for _, suffix := range []string{"&labels.node=db-01", "&node=ghost", "&labels.node=ghost"} {
		t.Run(suffix, func(t *testing.T) {
			a, s := metricScopeFixture(t)
			rec := httptest.NewRecorder()
			newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, metricRangePath+suffix, ""))
			if rec.Code != http.StatusForbidden || s.rangeCalls != 0 {
				t.Fatalf("unauthorized target: status=%d rangeCalls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
			}
			if n := len(a.audit.List(10, "", "")); n != 1 {
				t.Fatalf("expected exactly one denial audit, got %d", n)
			}
			if strings.Contains(rec.Body.String(), "g2") && strings.Contains(suffix, "ghost") {
				t.Fatal("unknown target must not disclose another node group")
			}
		})
	}
}

func TestMetricScope_LabelTargetPassedAsNodeWithoutDuplicateMatcher(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, metricRangePath+"&labels.node=web-01&labels.job=frontend", ""))
	if rec.Code != http.StatusOK || s.rangeCalls != 1 || s.rangeNode != "web-01" {
		t.Fatalf("label target: status=%d rangeCalls=%d node=%q body=%s", rec.Code, s.rangeCalls, s.rangeNode, rec.Body.String())
	}
	if len(s.rangeLabels) != 1 || s.rangeLabels["job"] != "frontend" {
		t.Fatalf("node must be removed from matcher labels, got %v", s.rangeLabels)
	}
	series, _ := decodeBody(t, rec)["series"].([]any)
	if len(series) != 1 || series[0].(map[string]any)["labels"].(map[string]any)["node"] != "web-01" {
		t.Fatalf("explicit node cannot expose other series: %s", rec.Body.String())
	}
}

func TestMetricScope_CrossNodeResultsOnlyIncludeRegisteredVisibleNodes(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, metricRangePath, ""))
	if rec.Code != http.StatusOK || s.rangeCalls != 1 || s.rangeNode != "" {
		t.Fatalf("cross-node query: status=%d calls=%d node=%q", rec.Code, s.rangeCalls, s.rangeNode)
	}
	series, _ := decodeBody(t, rec)["series"].([]any)
	if len(series) != 1 || series[0].(map[string]any)["labels"].(map[string]any)["node"] != "web-01" {
		t.Fatalf("outside/missing/unknown node labels must be removed: %s", rec.Body.String())
	}
}

func TestMetricScope_NoVisibleNodesSkipsStorage(t *testing.T) {
	for _, groups := range [][]string{nil, {"missing-group"}} {
		a, s := metricScopeFixture(t)
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, groups...), http.MethodGet, metricRangePath, ""))
		if rec.Code != http.StatusOK || s.rangeCalls != 0 || rec.Body.String() != "{\"series\":[]}\n" {
			t.Fatalf("no visible nodes: status=%d calls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
		}
	}
}

func TestMetricScope_GlobalAndAuthDisabledKeepAllSeries(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		a, s := metricScopeFixture(t)
		var req *http.Request
		if disabled {
			a.authStore = nil
			req = httptest.NewRequest(http.MethodGet, metricRangePath+"&node=ghost", nil)
		} else {
			req = reqWith(globalPrincipal("nodes:read"), http.MethodGet, metricRangePath+"&node=ghost", "")
		}
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || s.rangeCalls != 1 || s.rangeNode != "ghost" {
			t.Fatalf("unknown node stays available globally: status=%d calls=%d node=%q", rec.Code, s.rangeCalls, s.rangeNode)
		}
		series, _ := decodeBody(t, rec)["series"].([]any)
		if len(series) != 4 {
			t.Fatalf("global or disabled auth must retain original results: %s", rec.Body.String())
		}
	}
}

func TestMetricScope_GlobalStillRejectsConflictingTargets(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, metricRangePath+"&node=ghost&labels.node=db-01", ""))
	if rec.Code != http.StatusBadRequest || s.rangeCalls != 0 {
		t.Fatalf("global conflict: status=%d calls=%d", rec.Code, s.rangeCalls)
	}
}

func TestMetricScope_LatestRejectsConflictsBeforeQuery(t *testing.T) {
	for _, suffix := range []string{"&node=web-01&labels.node=db-01", "&node=web-01&labels.node=", "&node=web-01&node=db-01"} {
		a, s := metricScopeFixture(t)
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/query/latest?metric=cpu_usage"+suffix, ""))
		if rec.Code != http.StatusBadRequest || s.instantCalls != 0 {
			t.Fatalf("latest target conflict: status=%d calls=%d body=%s", rec.Code, s.instantCalls, rec.Body.String())
		}
	}
}

func TestMetricScope_LatestFiltersBeforeChoosingPoint(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/query/latest?metric=cpu_usage&labels.node=web-01&labels.job=frontend", ""))
	if rec.Code != http.StatusOK || s.instantCalls != 1 || s.instantNode != "web-01" || len(s.instantLabels) != 1 || s.instantLabels["job"] != "frontend" {
		t.Fatalf("latest: status=%d calls=%d node=%q labels=%v body=%s", rec.Code, s.instantCalls, s.instantNode, s.instantLabels, rec.Body.String())
	}
	body := decodeBody(t, rec)
	series, _ := body["series"].([]any)
	if len(series) != 1 || body["point"].(map[string]any)["value"] != float64(1) {
		t.Fatalf("point must come from visible web-01, not first outside series: %s", rec.Body.String())
	}
}

func TestMetricScope_LatestRequiresEffectiveNode(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, "/api/v1/query/latest?metric=cpu_usage", ""))
	if rec.Code != http.StatusBadRequest || s.instantCalls != 0 {
		t.Fatalf("latest without node: status=%d calls=%d", rec.Code, s.instantCalls)
	}
}

func TestMetricScope_VisibleNodesAndSeriesHelpers(t *testing.T) {
	a, s := metricScopeFixture(t)
	if !a.visibleMetricNodes(nil) || !a.visibleMetricNodes(globalPrincipal("nodes:read")) || a.visibleMetricNodes(restrictedPrincipal([]string{"nodes:read"})) || !a.visibleMetricNodes(restrictedPrincipal([]string{"nodes:read"}, "g1")) {
		t.Fatal("visibleMetricNodes must honor global, empty and registered group membership")
	}
	if got := len(a.visibleMetricSeries(nil, "web-01", s.series)); got != 4 {
		t.Fatalf("anonymous/global series = %d, want 4", got)
	}
	if got := len(a.visibleMetricSeries(globalPrincipal("nodes:read"), "web-01", s.series)); got != 4 {
		t.Fatalf("global series = %d, want 4", got)
	}
	p := restrictedPrincipal([]string{"nodes:read"}, "g1")
	if got := len(a.visibleMetricSeries(p, "", s.series)); got != 1 {
		t.Fatalf("restricted series = %d, want 1", got)
	}
	if got := len(a.visibleMetricSeries(p, "db-01", s.series)); got != 0 {
		t.Fatalf("explicit mismatched node series = %d, want 0", got)
	}
	otherVisible := model.Series{Labels: map[string]string{"node": "web-02"}, Points: []model.Point{{Timestamp: 1000, Value: 42}}}
	if got := a.visibleMetricSeries(p, "web-01", []model.Series{otherVisible, s.series[1]}); len(got) != 1 || got[0].Labels["node"] != "web-01" {
		t.Fatalf("explicit web-01 must exclude even another authorized node web-02: %v", got)
	}
}

const metricExportPath = "/api/v1/metrics/export?metric=cpu_usage&start=1000&end=61000&step=1000"

// TestMetricExportScope_RestrictedOnlyIncludesOwnedNode 受限用户跨节点导出只保留自身分组节点，
// 单序列时用 timestamp,value 表头（不得因为原始结果里有多节点而误用多序列表头）。
func TestMetricExportScope_RestrictedOnlyIncludesOwnedNode(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"metrics:export"}, "g1"), http.MethodGet, metricExportPath, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "db-01") || strings.Contains(body, "ghost") {
		t.Fatalf("导出越过资源范围: %s", body)
	}
	if !strings.Contains(body, "timestamp,value") {
		t.Fatalf("单序列应使用 timestamp,value 表头: %s", body)
	}
	// 钉住正文内容：单序列（仅 web-01，值为 1）应恰好输出一行数据且含 ",1"，
	// 若误纳入 ghost(4)/无节点序列(3) 会产生额外行，仅靠“不含节点名”无法察觉。
	lines := strings.Split(strings.TrimRight(strings.TrimPrefix(body, "\xEF\xBB\xBF"), "\n"), "\n")
	if len(lines) != 2 || lines[0] != "timestamp,value" {
		t.Fatalf("受限导出应为表头 + 恰好 1 行数据: %s", body)
	}
	if !strings.Contains(lines[1], ",1") {
		t.Fatalf("导出正文应来自自身节点 web-01（值为 1）: %s", body)
	}
	if s.rangeCalls != 1 {
		t.Fatalf("rangeCalls = %d, want 1", s.rangeCalls)
	}
}

// TestMetricExportScope_ConflictingNodeAndLabelsRejectedBeforeQuery node 与 labels 中的 node 冲突时 400，不查询存储。
func TestMetricExportScope_ConflictingNodeAndLabelsRejectedBeforeQuery(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"metrics:export"}, "g1"), http.MethodGet, metricExportPath+"&node=web-01&labels=node=db-01", ""))
	if rec.Code != http.StatusBadRequest || s.rangeCalls != 0 {
		t.Fatalf("conflicting export target: status=%d rangeCalls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
	}
}

// TestMetricExportScope_LabelNodeOutOfScopeForbiddenAndAudited labels=node=db-01 越权导出 403 且留审计。
func TestMetricExportScope_LabelNodeOutOfScopeForbiddenAndAudited(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"metrics:export"}, "g1"), http.MethodGet, metricExportPath+"&labels=node=db-01", ""))
	if rec.Code != http.StatusForbidden || s.rangeCalls != 0 {
		t.Fatalf("unauthorized export target: status=%d rangeCalls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
	}
	if n := len(a.audit.List(10, "", "")); n != 1 {
		t.Fatalf("expected exactly one denial audit, got %d", n)
	}
}

// TestMetricExportScope_EmptyGroupHeaderOnlyNoStorageQuery 受限空组导出只有标题行，且不查询存储。
func TestMetricExportScope_EmptyGroupHeaderOnlyNoStorageQuery(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"metrics:export"}), http.MethodGet, metricExportPath, ""))
	if rec.Code != http.StatusOK || s.rangeCalls != 0 {
		t.Fatalf("empty scope export: status=%d rangeCalls=%d body=%s", rec.Code, s.rangeCalls, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "timestamp,value") {
		t.Fatalf("expected header-only csv, got: %s", body)
	}
	lines := strings.Split(strings.TrimRight(strings.TrimPrefix(body, "\xEF\xBB\xBF"), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected only the header row, got %d lines: %s", len(lines), body)
	}
}

// activeFixtureMeta 取真实指标目录首条元数据，避免硬编码错误分类。
func activeFixtureMeta(t *testing.T) metrics.MetricMeta {
	t.Helper()
	list := metrics.List()
	if len(list) == 0 {
		t.Fatal("指标目录为空，无法构造测试")
	}
	return list[0]
}

// TestMetricActiveScope_RestrictedOnlyCountsOwnedNodeData 受限用户在跨节点扫描时，
// 只有自身分组节点存在数据才算 active；范围外节点数据不得计入。
func TestMetricActiveScope_RestrictedOnlyCountsOwnedNodeData(t *testing.T) {
	meta := activeFixtureMeta(t)
	a, s := metricScopeFixture(t)
	s.series = []model.Series{{Labels: map[string]string{"node": "db-01"}, Points: []model.Point{{Timestamp: 1000, Value: 2}}}}
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/metrics/active?category="+string(meta.Category), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	items, _ := decodeBody(t, rec)["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("目录未按分类返回条目: %s", rec.Body.String())
	}
	for _, it := range items {
		m := it.(map[string]any)
		if m["name"] == meta.Name && m["active"] != false {
			t.Fatalf("范围外节点数据不应计入 active: %v", m)
		}
	}
}

// TestMetricActiveScope_RestrictedActiveWhenOwnedNodeHasData 自身分组节点有数据时应为 active。
func TestMetricActiveScope_RestrictedActiveWhenOwnedNodeHasData(t *testing.T) {
	meta := activeFixtureMeta(t)
	a, s := metricScopeFixture(t)
	s.series = []model.Series{{Labels: map[string]string{"node": "web-01"}, Points: []model.Point{{Timestamp: 1000, Value: 1}}}}
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/metrics/active?category="+string(meta.Category), ""))
	items, _ := decodeBody(t, rec)["items"].([]any)
	found := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["name"] == meta.Name {
			found = true
			if m["active"] != true {
				t.Fatalf("自身分组节点有数据应为 active: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("目标指标未出现在结果中: %s", rec.Body.String())
	}
}

// TestMetricActiveScope_ExplicitNodeInactiveWhenOtherNodeSeriesReturned 明确指定节点时，
// 存储返回别的节点序列也应判定 inactive（不得因为跨节点数据存在而误判本节点为 active）。
func TestMetricActiveScope_ExplicitNodeInactiveWhenOtherNodeSeriesReturned(t *testing.T) {
	meta := activeFixtureMeta(t)
	a, s := metricScopeFixture(t)
	s.series = []model.Series{{Labels: map[string]string{"node": "db-01"}, Points: []model.Point{{Timestamp: 1000, Value: 2}}}}
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, "/api/v1/metrics/active?category="+string(meta.Category)+"&node=web-01", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	items, _ := decodeBody(t, rec)["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if m["name"] == meta.Name && m["active"] != false {
			t.Fatalf("明确节点返回别的节点序列时应 inactive: %v", m)
		}
	}
}

// TestMetricActiveScope_EmptyGroupSkipsStorage 空授权组不得触发任何存储查询。
func TestMetricActiveScope_EmptyGroupSkipsStorage(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}), http.MethodGet, "/api/v1/metrics/active", ""))
	if rec.Code != http.StatusOK || s.instantCalls != 0 {
		t.Fatalf("empty scope active: status=%d instantCalls=%d body=%s", rec.Code, s.instantCalls, rec.Body.String())
	}
	items, _ := decodeBody(t, rec)["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		if m["active"] != false {
			t.Fatalf("空授权组下所有指标都应 inactive: %v", m)
		}
	}
}

// TestMetricActiveScope_GlobalUnaffected 全局用户不改变 active 语义（跨节点任一序列存在即 active）。
func TestMetricActiveScope_GlobalUnaffected(t *testing.T) {
	meta := activeFixtureMeta(t)
	a, s := metricScopeFixture(t)
	s.series = []model.Series{{Labels: map[string]string{"node": "db-01"}, Points: []model.Point{{Timestamp: 1000, Value: 2}}}}
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, "/api/v1/metrics/active?category="+string(meta.Category), ""))
	items, _ := decodeBody(t, rec)["items"].([]any)
	found := false
	for _, it := range items {
		m := it.(map[string]any)
		if m["name"] == meta.Name {
			found = true
			if m["active"] != true {
				t.Fatalf("全局用户不应改变 active 语义: %v", m)
			}
		}
	}
	if !found {
		t.Fatalf("目标指标未出现在结果中: %s", rec.Body.String())
	}
}

const nodesLatestPath = "/api/v1/nodes/latest"

// TestNodesLatestScope_RestrictedExcludesOutOfScopeAndUnregistered 受限用户既不见范围外
// 已注册节点（db-01），也不见无法归属的未注册节点（ghost）。
func TestNodesLatestScope_RestrictedExcludesOutOfScopeAndUnregistered(t *testing.T) {
	a, _ := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}, "g1"), http.MethodGet, nodesLatestPath, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	out, _ := decodeBody(t, rec)["metrics"].(map[string]any)
	if _, ok := out["web-01"]; !ok {
		t.Fatalf("范围内节点应可见: %v", out)
	}
	if _, ok := out["db-01"]; ok {
		t.Fatalf("范围外已注册节点不得可见: %v", out)
	}
	if _, ok := out["ghost"]; ok {
		t.Fatalf("未注册节点不得可见: %v", out)
	}
}

// TestNodesLatestScope_EmptyGroupNoMetrics 空授权组得到空指标表，且因无任何可见节点
// 直接跳过全部 TSDB 聚合查询（与 catalog.go 的 skipStorage 一致，不为必然被剔除的结果付费）。
func TestNodesLatestScope_EmptyGroupNoMetrics(t *testing.T) {
	a, s := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"nodes:read"}), http.MethodGet, nodesLatestPath, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if s.allLatestCalls != 0 {
		t.Fatalf("空授权组不得触发任何 TSDB 聚合查询, allLatestCalls = %d", s.allLatestCalls)
	}
	out, _ := decodeBody(t, rec)["metrics"].(map[string]any)
	if len(out) != 0 {
		t.Fatalf("空授权组应得到空指标表: %v", out)
	}
}

// TestNodesLatestScope_GlobalKeepsUnregisteredNode 全局用户维持原有可见结果（含未注册节点）。
func TestNodesLatestScope_GlobalKeepsUnregisteredNode(t *testing.T) {
	a, _ := metricScopeFixture(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, nodesLatestPath, ""))
	out, _ := decodeBody(t, rec)["metrics"].(map[string]any)
	if _, ok := out["ghost"]; !ok {
		t.Fatalf("全局用户应维持原有可见结果（含未注册节点）: %v", out)
	}
	if _, ok := out["db-01"]; !ok {
		t.Fatalf("全局用户应看到全部已注册节点: %v", out)
	}
}
