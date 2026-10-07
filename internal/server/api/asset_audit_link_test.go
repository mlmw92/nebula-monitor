package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/audit"
)

// 「变更 ↔ 审计」关联在接口层的用例。
//
// 强关联的全部意义在于：一次请求里写下的资产变更与那条审计记录**共用一个关联 id**，
// 于是"这次操作到底改了什么"成为一个能回答的问题，而不是靠时间与操作人猜。
// 因此最该被钉住的是「两边拿到的是同一个 id」与「跳过去看不到范围外的东西」。

// TestAuditRequestIDSharedByChangesAndAuditRow 走完整链路：
// 审计中间件（发号）→ 资产写接口（随行）→ 变更记录（落库）→ 两侧互查。
func TestAuditRequestIDSharedByChangesAndAuditRow(t *testing.T) {
	a, svc := assetTestAPI(t)
	au := audit.New("")
	a.audit = au
	handler := AuditMiddleware(newRoutesMux(a), au)

	req := assetWriteReq(globalPrincipal("assets:write"), http.MethodPost, "/api/v1/assets", assetWriteBody{
		TypeKey: asset.TypeHost, NaturalKey: "link-01", Name: "link-01", Node: "web-01",
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("新建应返回 201，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// ① 审计行带上了关联 id。
	events, _, err := au.Query(audit.QueryFilter{Limit: 50})
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}
	requestID := ""
	for _, e := range events {
		if e.Path == "/api/v1/assets" && e.RequestID != "" {
			requestID = e.RequestID
		}
	}
	if requestID == "" {
		t.Fatal("这次写请求的审计行应带关联 id（空值意味着关联根本没建立）")
	}

	// ② 变更记录带的是**同一个** id（不是"各写各的"）。
	history, err := svc.History(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "link-01"}, 0)
	if err != nil {
		t.Fatalf("读取变更历史失败: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("新建资产应留下建档变更")
	}
	if history[0].RequestID != requestID {
		t.Fatalf("变更记录的关联 id 应为 %q，实际 %q", requestID, history[0].RequestID)
	}

	// ③ 反向：按这个 id 查得到这次操作改了什么。
	items, truncated, err := svc.ChangesByRequest(requestID, asset.ChangeScope{}, 0)
	if err != nil {
		t.Fatalf("按关联 id 查变更失败: %v", err)
	}
	if truncated || len(items) != 1 || items[0].NaturalKey != "link-01" {
		t.Fatalf("反向查询应命中 link-01 一条，实际 %d 条 truncated=%v", len(items), truncated)
	}

	// ④ 正向：审计列表能按它过滤，且响应里把 id 带出来（前端据此跳回来）。
	filtered := httptest.NewRecorder()
	a.handleAuditEvents(filtered, httptest.NewRequest(http.MethodGet,
		"/api/v1/audit/events?requestId="+requestID, nil))
	if filtered.Code != http.StatusOK {
		t.Fatalf("审计列表状态码 = %d，响应 %s", filtered.Code, filtered.Body.String())
	}
	body := decodeBody(t, filtered)
	if total, _ := body["total"].(float64); total != 1 {
		t.Fatalf("按关联 id 过滤应只剩 1 条审计，实际 %v", body["total"])
	}
	if list, _ := body["events"].([]any); len(list) != 1 {
		t.Fatalf("事件列表长度 = %d", len(list))
	} else if m, _ := list[0].(map[string]any); m["requestId"] != requestID {
		t.Fatalf("审计事件应回传关联 id 供前端跳转，实际 %#v", m["requestId"])
	}
}

// TestAuditRequestIDAbsentOnReadOnly 只读请求不该有 id：
// 它没有审计行，发号只会造出"指向空的关联"，而资产侧也就不会写上一个查不到的 id。
func TestAuditRequestIDAbsentOnReadOnly(t *testing.T) {
	handler := AuditMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := RequestID(r); got != "" {
			t.Fatalf("只读请求不该有请求 id，实际 %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}), audit.New(""))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil))
}

// TestAssetChangesByRequestEndpointRespectsScope 反向入口的范围约束：
// 同一次操作可能跨多个业务标签，受限用户只能看到自己范围内的那些字段变更。
func TestAssetChangesByRequestEndpointRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	const rid = "0f0e0d0c0b0a0908"

	if _, err := svc.SetLabels(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"},
		map[string]string{"biz": "pay"}, nil, "admin", rid); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}
	if _, err := svc.SetLabels(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"},
		map[string]string{"biz": "risk"}, nil, "admin", rid); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}
	mux := newRoutesMux(a)

	// 受限到 g1 + biz=pay：db-01 的那条不得出现（它既在 g2 又在 risk）。
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(bizPrincipal([]string{"assets:read"}, []string{"g1"}, "pay"),
		http.MethodGet, "/api/v1/assets/changes?requestId="+rid, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	changes, _ := body["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("业务范围内应只有 1 条变更，实际 %d 条：%s", len(changes), rec.Body.String())
	}
	if m, _ := changes[0].(map[string]any); m["naturalKey"] != "web-01" {
		t.Fatalf("只应看到 web-01，实际 %#v", m["naturalKey"])
	}

	// 全局身份：两条都在（说明上一条的"只有 1 条"确实是范围造成的）。
	all := httptest.NewRecorder()
	mux.ServeHTTP(all, reqWith(globalPrincipal("assets:read"),
		http.MethodGet, "/api/v1/assets/changes?requestId="+rid, ""))
	if body := decodeBody(t, all); len(body["changes"].([]any)) != 2 {
		t.Fatalf("全局身份应看到 2 条，响应 %s", all.Body.String())
	}

	// 缺 / 空 requestId 一律 400：空值对应的是采集侧的全部变更，放行等于整表返回。
	for _, target := range []string{
		"/api/v1/assets/changes",
		"/api/v1/assets/changes?requestId=",
		"/api/v1/assets/changes?requestId=%20%20",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, target, ""))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应返回 400，实际 %d", target, rec.Code)
		}
	}
}
