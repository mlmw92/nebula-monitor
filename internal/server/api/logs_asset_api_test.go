package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
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

// Pod 日志 → 资产联动：行上带容器身份时，响应要给出「命名空间|Pod → 容器资产」映射。
//
// 与主机来源的映射（assets）分开：容器日志要标的是那个 Pod，而不是"某台机器上的某类来源"。
func TestRoutes_LogsQueryReturnsPodAssetMapping(t *testing.T) {
	a, store := newLogsTestAPI(t)
	svc := attachAssetService(t, a)
	declarePodAsset(t, svc, "https://10.0.0.9:6443", "nebula-demo", "web-1", "web-01")

	now := time.Now().UnixMilli()
	if _, _, _, err := store.Append(model.LogBatch{
		Source: "podlog", Node: "web-01",
		Origin: &model.LogOrigin{Namespace: "nebula-demo", Pod: "web-1", Container: "nginx"},
		Lines:  []model.LogLine{{Ts: now - 60_000, Text: "error: pod line", Pattern: "err"}},
	}); err != nil {
		t.Fatalf("造 Pod 日志失败：%v", err)
	}

	body := decodeBody(t, logsQuery(a, logsReader(), ""))
	lines, _ := body["lines"].([]any)
	var podLine map[string]any
	for _, raw := range lines {
		line, _ := raw.(map[string]any)
		if line["source"] == "podlog" {
			podLine = line
		}
	}
	if podLine == nil {
		t.Fatalf("Pod 日志应被检索到：%v", body)
	}
	origin, _ := podLine["origin"].(map[string]any)
	if origin["namespace"] != "nebula-demo" || origin["pod"] != "web-1" || origin["container"] != "nginx" {
		t.Fatalf("行上应带容器身份：%v", podLine)
	}

	pods, _ := body["podAssets"].(map[string]any)
	if len(pods) != 1 {
		t.Fatalf("只应有本页出现过的容器身份映射：%v", pods)
	}
	refs, ok := pods["nebula-demo|web-1"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("容器身份应映射到一条资产：%v", pods)
	}
	ref, _ := refs[0].(map[string]any)
	if ref["typeKey"] != asset.TypePod || ref["naturalKey"] != asset.PodNaturalKey("https://10.0.0.9:6443", "nebula-demo", "web-1") {
		t.Fatalf("资产引用不符：%v", ref)
	}
	// 普通文件日志（行上没有身份）不该出现在容器映射里
	if _, ok := pods["|"]; ok {
		t.Fatalf("没有身份的行不应产生映射：%v", pods)
	}
}

// 台账里没有对应 Pod（或未注入台账）时，日志照常返回、只是没有映射：
// 资产是补充信息，不该让"日志查到了"变成"页面报错"。
func TestRoutes_LogsQueryPodMappingIsOptional(t *testing.T) {
	a, store := newLogsTestAPI(t)
	attachAssetService(t, a) // 台账是空的：没有任何 Pod 资产
	now := time.Now().UnixMilli()
	if _, _, _, err := store.Append(model.LogBatch{
		Source: "podlog", Node: "web-01",
		Origin: &model.LogOrigin{Namespace: "nebula-demo", Pod: "web-1", Container: "nginx"},
		Lines:  []model.LogLine{{Ts: now - 60_000, Text: "error: pod line"}},
	}); err != nil {
		t.Fatal(err)
	}
	body := decodeBody(t, logsQuery(a, logsReader(), ""))
	if _, ok := body["podAssets"]; ok {
		t.Fatalf("没有对应资产时不应有映射字段：%v", body)
	}
	if lines, _ := body["lines"].([]any); len(lines) == 0 {
		t.Fatal("日志本身应照常返回")
	}
}

// `pods=<命名空间>|<Pod>`：只看某个容器的日志（容器身份是协议字段，不走字段过滤通道）。
func TestRoutes_LogsQueryPodFilter(t *testing.T) {
	a, store := newLogsTestAPI(t)
	now := time.Now().UnixMilli()
	appendPod := func(ns, pod, text string, ts int64) {
		t.Helper()
		if _, _, _, err := store.Append(model.LogBatch{
			Source: "podlog", Node: "web-01",
			Origin: &model.LogOrigin{Namespace: ns, Pod: pod, Container: "app"},
			Lines:  []model.LogLine{{Ts: ts, Text: text}},
		}); err != nil {
			t.Fatalf("造数据失败：%v", err)
		}
	}
	appendPod("nebula-demo", "web-1", "error: web-1 line", now-60_000)
	appendPod("nebula-demo", "web-2", "error: web-2 line", now-70_000)

	// 不带 pods：本页既有普通文件日志、也有容器日志
	all := decodeLogResult(t, logsQuery(a, logsReader(), ""))
	if len(all.Lines) < 3 {
		t.Fatalf("不带过滤时应返回全部日志：%+v", all.Lines)
	}

	// 只带一个容器
	one := decodeLogResult(t, logsQuery(a, logsReader(), "?pods=nebula-demo%7Cweb-1"))
	if len(one.Lines) != 1 || one.Lines[0].Text != "error: web-1 line" {
		t.Fatalf("应按容器过滤：%+v", one.Lines)
	}
	if one.Lines[0].Origin == nil || one.Lines[0].Origin.Pod != "web-1" {
		t.Fatalf("命中行应带容器身份：%+v", one.Lines[0])
	}

	// 多个容器是「或」
	two := decodeLogResult(t, logsQuery(a, logsReader(), "?pods=nebula-demo%7Cweb-1&pods=nebula-demo%7Cweb-2"))
	if len(two.Lines) != 2 {
		t.Fatalf("多个容器应「或」命中：%+v", two.Lines)
	}

	// 非法形态必须明确报错，而不是"退化成不限"（后者会让人以为过滤生效了）
	for _, bad := range []string{"?pods=web-1", "?pods=nebula-demo%7C", "?pods=%7Cweb-1", "?pods=nebula_demo%7Cweb-1"} {
		rec := logsQuery(a, logsReader(), bad)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d（%s）", bad, rec.Code, rec.Body.String())
		}
	}
}

// declarePodAsset 落一个容器资产（命名空间是属性、节点是归属主机）。
func declarePodAsset(t *testing.T, svc *asset.Service, cluster, ns, name, node string) {
	t.Helper()
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypePod, NaturalKey: asset.PodNaturalKey(cluster, ns, name),
		Name: name, Node: node, Source: asset.SourceDiscovery,
		Attrs: map[string]string{"namespace": ns},
	}); err != nil {
		t.Fatalf("落容器资产失败: %v", err)
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
