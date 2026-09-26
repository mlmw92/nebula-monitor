package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/nebula/monitor/internal/server/auth"
)

// collabBody 构造处置请求体（rule 用 RuleName，与前端 ackKey 口径一致）。
func collabBody(host string, startsAt int64, extra string) string {
	body := `{"rule":"CPU 使用率过高","host":"` + host + `","instance":"cpu_usage","startsAt":` + strconv.FormatInt(startsAt, 10)
	if extra != "" {
		body += "," + extra
	}
	return body + "}"
}

func collabAck(t *testing.T, h http.Handler, p *auth.Principal, path, body string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, reqWith(p, http.MethodPost, path, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s 应 200，got %d（body=%s）", path, rec.Code, rec.Body.String())
	}
	ack, _ := decodeBody(t, rec)["ack"].(map[string]any)
	if ack == nil {
		t.Fatalf("%s 应回写最新处置记录：%s", path, rec.Body.String())
	}
	return ack
}

// TestRoutes_AlertCollab_Flow 完整处置流：认领（指派 + 评论）→ 关闭 → 重新打开 → 追加评论。
func TestRoutes_AlertCollab_Flow(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("alerts:read", "alerts:write")

	// 认领 + 指派 + 附带评论
	ack := collabAck(t, mux, p, "/api/v1/alerts/ack", collabBody("web-01", 1000, `"assignee":"ops2","comment":"我先看"`))
	if ack["status"] != "ack" || ack["assignee"] != "ops2" {
		t.Fatalf("认领结果不符：%v", ack)
	}
	if comments, _ := ack["comments"].([]any); len(comments) != 1 {
		t.Fatalf("应记录 1 条评论：%v", ack["comments"])
	}

	// 关闭（带原因）
	closed := collabAck(t, mux, p, "/api/v1/alerts/close", collabBody("web-01", 1000, `"reason":"误报"`))
	if closed["status"] != "closed" || closed["closeReason"] != "误报" {
		t.Fatalf("关闭结果不符：%v", closed)
	}
	if comments, _ := closed["comments"].([]any); len(comments) != 1 {
		t.Fatalf("关闭不应清空评论：%v", closed["comments"])
	}

	// 重新打开：回到待处理，清空处理人与关闭原因，保留评论
	reopened := collabAck(t, mux, p, "/api/v1/alerts/reopen", collabBody("web-01", 1000, ""))
	if reopened["status"] != "pending" {
		t.Fatalf("重新打开后应为 pending：%v", reopened)
	}
	if v, ok := reopened["assignee"]; ok && v != "" {
		t.Fatalf("重新打开应清空处理人：%v", v)
	}
	if comments, _ := reopened["comments"].([]any); len(comments) != 1 {
		t.Fatalf("重新打开应保留评论历史：%v", reopened["comments"])
	}

	// 追加评论（评论接口必须便于单独调用）
	withComment := collabAck(t, mux, p, "/api/v1/alerts/comment", collabBody("web-01", 1000, `"comment":"已升级内核"`))
	if comments, _ := withComment["comments"].([]any); len(comments) != 2 {
		t.Fatalf("评论应追加为 2 条：%v", withComment["comments"])
	}
}

// TestRoutes_AlertCollab_StateAffectsPendingView 处置状态与「待处理」视图联动：
// 认领后移出待处理列表，重新打开后回到列表。这是协作流真正的用户可见效果。
func TestRoutes_AlertCollab_StateAffectsPendingView(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("alerts:read", "alerts:write")

	countPending := func() int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/alerts?state=active", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("待处理列表应 200，got %d", rec.Code)
		}
		alerts, _ := decodeBody(t, rec)["alerts"].([]any)
		return len(alerts)
	}

	if got := countPending(); got != 2 {
		t.Fatalf("初始待处理应为 2 条，got %d", got)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/alerts/ack", collabBody("web-01", 1000, "")))
	if rec.Code != http.StatusOK {
		t.Fatalf("认领应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	if got := countPending(); got != 1 {
		t.Fatalf("认领后待处理应为 1 条，got %d", got)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/alerts/reopen", collabBody("web-01", 1000, "")))
	if rec.Code != http.StatusOK {
		t.Fatalf("重新打开应 200，got %d", rec.Code)
	}
	if got := countPending(); got != 2 {
		t.Fatalf("重新打开后待处理应恢复为 2 条，got %d", got)
	}
}

// TestRoutes_AlertCollab_ScopeDenied 资源范围外节点的处置被拒绝并记入审计。
func TestRoutes_AlertCollab_ScopeDenied(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"alerts:write"}, "g1") // 只能看 g1（web-01）

	for _, path := range []string{"/api/v1/alerts/ack", "/api/v1/alerts/close", "/api/v1/alerts/reopen", "/api/v1/alerts/comment"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, http.MethodPost, path, collabBody("db-01", 2000, `"comment":"x"`)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 对范围外节点应 403，got %d（body=%s）", path, rec.Code, rec.Body.String())
		}
	}
	if events := a.audit.List(10, "", ""); len(events) == 0 {
		t.Fatal("越权处置应记录审计")
	}
	if len(a.acks.Map()) != 0 {
		t.Fatalf("越权处置不应落库，实际 %d 条", len(a.acks.Map()))
	}
}

// TestRoutes_AlertCollab_Validation 必填项与空评论校验。
func TestRoutes_AlertCollab_Validation(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("alerts:read", "alerts:write")

	cases := []struct {
		name string
		path string
		body string
	}{
		{"缺少 host", "/api/v1/alerts/ack", `{"rule":"r","startsAt":1000}`},
		{"缺少 startsAt", "/api/v1/alerts/ack", `{"rule":"r","host":"web-01"}`},
		{"请求体非法", "/api/v1/alerts/close", `{`},
		{"空评论", "/api/v1/alerts/comment", collabBody("web-01", 1000, `"comment":"   "`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, reqWith(p, http.MethodPost, tc.path, tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应 400，got %d（body=%s）", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRoutes_AlertCollab_PermissionGate 处置属写操作：只有 alerts:read 必须被拒。
func TestRoutes_AlertCollab_PermissionGate(t *testing.T) {
	a := alertTestAPI(t)
	mux := newRoutesMux(a)

	for _, path := range []string{"/api/v1/alerts/ack", "/api/v1/alerts/close", "/api/v1/alerts/reopen", "/api/v1/alerts/comment"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(globalPrincipal("alerts:read"), http.MethodPost, path, collabBody("web-01", 1000, `"comment":"x"`)))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 缺 alerts:write 应 403，got %d", path, rec.Code)
		}
		if len(a.acks.Map()) != 0 {
			t.Fatal("被拒请求不应落库")
		}
	}
}
