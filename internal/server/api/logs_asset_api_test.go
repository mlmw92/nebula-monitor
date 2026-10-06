package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 日志 → 资产联动（全景表 8-6 的联动侧）与「由日志建规则」（9-05）。

// attachAssetService 给已有 API 注入一个空台账（用临时库）。
func attachAssetService(t *testing.T, a *API) *asset.Service {
	t.Helper()
	store, err := asset.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("打开资产库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	svc := asset.NewService(store)
	a.SetAssetService(svc)
	return svc
}

// declareLogSource 给某资产声明日志来源（**人工值**：采集侧不会上报这个字段）。
func declareLogSource(t *testing.T, svc *asset.Service, typeKey, key, node, source string) {
	t.Helper()
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: typeKey, NaturalKey: key, Name: key, Node: node,
		Source: asset.SourceManual, Attrs: map[string]string{asset.LogSourceKey: source},
	}); err != nil {
		t.Fatalf("声明日志来源失败: %v", err)
	}
}

// 检索响应要带上「来源 + 节点 → 资产」映射，前端据此把日志标到资产上。
func TestRoutes_LogsQueryReturnsAssetMapping(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	svc := attachAssetService(t, a)
	declareLogSource(t, svc, asset.TypeHost, "web-01", "web-01", "applog")

	rec := logsQuery(a, logsReader(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	assets, _ := body["assets"].(map[string]any)
	if len(assets) == 0 {
		t.Fatalf("应返回来源 → 资产映射：%v", body)
	}
	refs, ok := assets["applog|web-01"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("web-01 的 applog 应映射到一条资产：%v", assets)
	}
	ref := refs[0].(map[string]any)
	if ref["naturalKey"] != "web-01" || ref["typeTitle"] != "主机" {
		t.Fatalf("资产引用不符：%v", ref)
	}
	// db-01 的日志没有对应资产：不应凭空出现映射
	if _, ok := assets["applog|db-01"]; ok {
		t.Fatalf("未声明来源的节点不应有映射：%v", assets)
	}
}

// 没注入台账（或没有声明）时，检索照常返回、只是没有映射：
// 资产是补充信息，不该让"日志查到了"变成"页面报错"。
func TestRoutes_LogsQueryWithoutAssetsStillWorks(t *testing.T) {
	a, _ := newLogsTestAPI(t) // 未注入资产服务
	rec := logsQuery(a, logsReader(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("未注入台账时检索应照常 200，实际 %d", rec.Code)
	}
	body := decodeBody(t, rec)
	if _, ok := body["assets"]; ok {
		t.Fatalf("没有台账时不应有映射字段：%v", body)
	}
	if lines, _ := body["lines"].([]any); len(lines) == 0 {
		t.Fatal("日志本身应照常返回")
	}
}

// 范围外的资产不得出现在映射里（与列表/详情同一口径）。
func TestRoutes_LogsQueryAssetMappingRespectsScope(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	svc := attachAssetService(t, a)
	// db-01 属 g2；给它的日志声明来源，然后用 g1 的身份查 g1 的日志
	declareLogSource(t, svc, asset.TypeHost, "db-01", "db-01", "applog")

	rec := logsQuery(a, restrictedPrincipal([]string{"logs:read"}, "g1"), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	assets, _ := body["assets"].(map[string]any)
	for key := range assets {
		if key == "applog|db-01" {
			t.Fatalf("范围外资产不得出现在映射里：%v", assets)
		}
	}
}

// 由日志模式生成规则模板：指标名必须由服务端拼（拼错的症状是"规则配好了却没有数据"）。
func TestRoutes_LogRuleTemplate(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(logsReader(), http.MethodGet,
		"/api/v1/logs/rule-template?source=applog&pattern=err&node=web-01", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["metric"] != "applog_log_err_total" {
		t.Fatalf("指标名不符：%v", body["metric"])
	}
	if body["operator"] != ">" || body["for"] != "5m" {
		t.Fatalf("应给出可直接保存的默认条件：%v", body)
	}
	// 从某台机器的日志建规则时把范围收到那台（避免同名来源的其它机器一起触发）
	if body["scope"] != "specified" {
		t.Fatalf("带 node 时应限定范围：%v", body)
	}
	nodes, _ := body["nodes"].([]any)
	if len(nodes) != 1 || nodes[0] != "web-01" {
		t.Fatalf("范围节点不符：%v", body["nodes"])
	}

	// 不带 node：作用于全部主机
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(logsReader(), http.MethodGet, "/api/v1/logs/rule-template?source=applog&pattern=err", ""))
	body = decodeBody(t, rec)
	if body["scope"] != "all" {
		t.Fatalf("不带 node 时应为全部主机：%v", body["scope"])
	}
}

// 非法来源名/模式名必须明确拒绝：它们会拼出查不到的指标名，而那种错误是**静默无数据**。
func TestRoutes_LogRuleTemplateRejectsInvalidNames(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	mux := newRoutesMux(a)
	for _, target := range []string{
		"/api/v1/logs/rule-template?source=AppLog&pattern=err",   // 来源名大写
		"/api/v1/logs/rule-template?source=applog&pattern=err-1", // 模式名含连字符
		"/api/v1/logs/rule-template?source=applog&pattern=",      // 缺模式名
		"/api/v1/logs/rule-template?source=&pattern=err",         // 缺来源名
		"/api/v1/logs/rule-template?source=applog&pattern=err%20x",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(logsReader(), http.MethodGet, target, ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d（%s）", target, rec.Code, rec.Body.String())
		}
	}
}

// 缺权限：模板接口与检索同权限（保存规则本身仍需 alerts:write）。
func TestRoutes_LogRuleTemplatePermission(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet,
		"/api/v1/logs/rule-template?source=applog&pattern=err", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 logs:read 应 403，实际 %d", rec.Code)
	}
}
