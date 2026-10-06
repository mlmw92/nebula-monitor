package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

func impactPath(node, instance string) string {
	params := make([]string, 0, 2)
	if node != "" {
		params = append(params, "node="+node)
	}
	if instance != "" {
		params = append(params, "instance="+instance)
	}
	return "/api/v1/alerts/impact?" + strings.Join(params, "&")
}

// 告警的指标标签要能对到台账资产，并带上该资产的近期变更与波及范围。
func TestRoutes_AlertImpactMatchesHostAndInstance(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	// 制造一条变更：同一资产的采集值变了（只有真变化才记录）
	seedAsset(t, svc, asset.TypeHost, "web-01", "web-01", "web-01", map[string]string{"cpuCores": "16"})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet,
		impactPath("web-01", "127.0.0.1:6379"), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	matched, _ := body["matched"].([]any)
	if len(matched) != 2 {
		t.Fatalf("应同时命中主机与实例：%v", matched)
	}
	keys := map[string]bool{}
	for _, raw := range matched {
		item := raw.(map[string]any)
		keys[item["typeKey"].(string)+"|"+item["naturalKey"].(string)] = true
	}
	if !keys["host|web-01"] || !keys["middleware-instance|redis:127.0.0.1:6379"] {
		t.Fatalf("命中资产不符：%v", keys)
	}

	// 变更必须带资产身份（命中多条时否则说不清是哪条资产的变更）
	changes, _ := body["changes"].([]any)
	if len(changes) == 0 {
		t.Fatalf("应返回近期变更：%v", body)
	}
	first := changes[0].(map[string]any)
	if first["field"] != "cpuCores" || first["naturalKey"] != "web-01" {
		t.Fatalf("变更内容不符：%v", first)
	}

	// 波及范围以**主机**为中心（"这台机器出事会影响谁"的起点）
	topology, _ := body["topology"].(map[string]any)
	if topology == nil || topology["root"] != "host|web-01" {
		t.Fatalf("波及范围应以主机为中心：%v", topology)
	}
	if body["note"] != "" {
		t.Fatalf("命中资产时不应有提示：%v", body["note"])
	}
}

// 只给 node 或只给 instance 都要能用（告警不一定两者都有）。
func TestRoutes_AlertImpactPartialLabels(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	_ = svc

	for _, tc := range []struct {
		name     string
		target   string
		wantKeys []string
	}{
		{name: "只有主机名", target: impactPath("web-01", ""), wantKeys: []string{"host|web-01"}},
		{name: "只有实例地址", target: impactPath("", "127.0.0.1:6379"), wantKeys: []string{"middleware-instance|redis:127.0.0.1:6379"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, tc.target, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			matched, _ := body["matched"].([]any)
			if len(matched) != len(tc.wantKeys) {
				t.Fatalf("应命中 %d 条：%v", len(tc.wantKeys), matched)
			}
			item := matched[0].(map[string]any)
			got := item["typeKey"].(string) + "|" + item["naturalKey"].(string)
			if got != tc.wantKeys[0] {
				t.Fatalf("命中资产不符：%s", got)
			}
		})
	}
}

// 没匹配到资产时要给出**可操作**的说明，而不是一个空列表：
// "节点还没上报""实例地址写法不一致""资产被隐藏"是三件完全不同的事。
func TestRoutes_AlertImpactNoteWhenNothingMatched(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "未知主机", target: impactPath("no-such-host", ""), want: "主机资产"},
		{name: "未知实例", target: impactPath("", "9.9.9.9:1234"), want: "中间件实例"},
		{name: "两者都未知", target: impactPath("no-such-host", "9.9.9.9:1234"), want: "Agent 上报"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, tc.target, ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("未匹配到不是错误，应 200，实际 %d（%s）", rec.Code, rec.Body.String())
			}
			body := decodeBody(t, rec)
			if matched, _ := body["matched"].([]any); len(matched) != 0 {
				t.Fatalf("不应命中资产：%v", matched)
			}
			note, _ := body["note"].(string)
			if !strings.Contains(note, tc.want) {
				t.Fatalf("提示应说明原因（含 %q），实际 %q", tc.want, note)
			}
		})
	}
}

// 资源范围：范围外的资产不得出现在影响面里（与列表 / 详情同一口径）。
func TestRoutes_AlertImpactRespectsScope(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	// g2 的受限用户看 g1 的主机：不返回资产，但仍要给出提示（不是 404——
	// 影响面是补充信息，把"信息缺失"显示成错误会让用户以为接口坏了）
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"assets:read"}, "g2"), http.MethodGet,
		impactPath("web-01", ""), ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if matched, _ := body["matched"].([]any); len(matched) != 0 {
		t.Fatalf("范围外资产不得出现：%v", matched)
	}
	if body["note"] == "" {
		t.Fatal("应说明未匹配到资产")
	}
	if _, ok := body["topology"]; ok {
		t.Fatal("没有可见资产时不应给波及范围")
	}
}

// 参数与依赖边界：两者都不给 400；缺权限 403；未启用资产能力 503。
func TestRoutes_AlertImpactValidationAndPermission(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/alerts/impact", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("两个标签都不给应 400，实际 %d", rec.Code)
	}

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(globalPrincipal("alerts:read"), http.MethodGet, impactPath("web-01", ""), ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:read 应 403，实际 %d", denied.Code)
	}

	disabled := &API{}
	rec = httptest.NewRecorder()
	disabled.handleAlertImpact(rec, assetReq(globalPrincipal("assets:read"), impactPath("web-01", "")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用资产能力应 503，实际 %d", rec.Code)
	}
}
