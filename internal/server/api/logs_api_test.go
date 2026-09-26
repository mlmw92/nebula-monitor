package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/logstore"
)

// 集中日志检索接口（C2 子批次 C）。
//
// 最要紧的一条：**范围过滤必须在扫描前收窄节点**，且显式指定 `nodes` 参数不能绕过范围。
// 否则「指定别人的节点名」就成了越权读取日志（日志内容比指标敏感得多）。

// newLogsTestAPI 构造注入了日志存储的 API，并造好两天的日志数据。
//
// 节点沿用 scopeTestAPI 的三个：web-01 / web-02 → g1；db-01 → g2。
func newLogsTestAPI(t *testing.T) (*API, *logstore.Store) {
	t.Helper()
	a := scopeTestAPI(t)
	store := logstore.New(t.TempDir(), 0)
	a.SetLogStore(store)

	now := time.Now().UnixMilli()
	seed := func(node, text string, ts int64) {
		t.Helper()
		if _, _, _, err := store.Append(model.LogBatch{
			Source: "applog", Node: node,
			Lines: []model.LogLine{{Ts: ts, Text: text, Pattern: "err"}},
		}); err != nil {
			t.Fatalf("造数据失败：%v", err)
		}
	}
	seed("web-01", "error: web-01 old", now-30*60*1000)
	seed("web-01", "error: web-01 new", now-5*60*1000)
	seed("db-01", "error: db-01 secret", now-10*60*1000)
	return a, store
}

// logsReader 构造「全局范围 + logs:read」的身份（本文件里最常见的调用方）。
func logsReader() *auth.Principal { return globalPrincipal("logs:read") }

// logsQuery 发起一次检索请求（时间范围默认取最近 1 小时）。
func logsQuery(a *API, p *auth.Principal, extra string) *httptest.ResponseRecorder {
	target := "/api/v1/logs" + extra
	rec := httptest.NewRecorder()
	req := reqWith(p, http.MethodGet, target, "")
	newRoutesMux(a).ServeHTTP(rec, req)
	return rec
}

func decodeLogResult(t *testing.T, rec *httptest.ResponseRecorder) model.LogQueryResult {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，got %d（%s）", rec.Code, rec.Body.String())
	}
	var res model.LogQueryResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("响应解析失败：%v（%s）", err, rec.Body.String())
	}
	return res
}

// TestLogsAPI_QueryBasics 全局范围用户能看到全部，且按时间倒序。
func TestLogsAPI_QueryBasics(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	res := decodeLogResult(t, logsQuery(a, logsReader(), "?q=error"))
	if len(res.Lines) != 3 {
		t.Fatalf("应命中 3 条，got %v", res.Lines)
	}
	if res.Lines[0].Text != "error: web-01 new" {
		t.Fatalf("应按时间倒序（最新在前），got %q", res.Lines[0].Text)
	}
	if res.Truncated {
		t.Fatalf("未达上限不应截断：%+v", res)
	}
	// 关键词不命中时是空结果（不是错误）
	res = decodeLogResult(t, logsQuery(a, logsReader(), "?q=no-such-word"))
	if len(res.Lines) != 0 {
		t.Fatalf("应无命中，got %v", res.Lines)
	}
}

// TestLogsAPI_ScopeCannotBeBypassed 限定 g1 的用户：
//   - 不带 nodes 参数时看不到 g2（db-01）的日志；
//   - **显式指定 nodes=db-01 同样看不到**（这是关键的一条）。
func TestLogsAPI_ScopeCannotBeBypassed(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	p := restrictedPrincipal([]string{"logs:read"}, "g1")

	res := decodeLogResult(t, logsQuery(a, p, ""))
	for _, l := range res.Lines {
		if l.Node == "db-01" {
			t.Fatalf("g1 用户不应看到 db-01 的日志：%+v", res.Lines)
		}
	}
	if len(res.Lines) != 2 {
		t.Fatalf("g1 用户应看到 web-01 的 2 条，got %v", res.Lines)
	}

	// 显式越权指定：结果必须为空，且不能报错泄露「有这条日志但你没权限」
	res = decodeLogResult(t, logsQuery(a, p, "?nodes=db-01"))
	if len(res.Lines) != 0 {
		t.Fatalf("显式指定范围外节点必须返回空结果，got %+v", res.Lines)
	}
	// 混合指定：只保留有权的那部分
	res = decodeLogResult(t, logsQuery(a, p, "?nodes=db-01,web-01"))
	for _, l := range res.Lines {
		if l.Node != "web-01" {
			t.Fatalf("混合指定应只保留有权限的节点，got %+v", res.Lines)
		}
	}
	if len(res.Lines) != 2 {
		t.Fatalf("混合指定应得到 web-01 的 2 条，got %v", res.Lines)
	}
}

// TestLogsAPI_BadParams 参数非法一律 400，且不把内部细节抛给前端。
func TestLogsAPI_BadParams(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	p := logsReader()
	cases := []struct{ name, query string }{
		{"from 非数字", "?from=abc"},
		{"to 非数字", "?to=abc"},
		{"from 晚于 to", "?from=2000000000000&to=1000000000000"},
		{"正则非法", "?regex=" + "("},
		{"正则过长", "?regex=" + strings.Repeat("a", 300)},
		{"关键词过长", "?q=" + strings.Repeat("a", 300)},
		{"游标非法", "?cursor=!!!not-base64!!!"},
		{"limit 非数字", "?limit=abc"},
	}
	for _, c := range cases {
		rec := logsQuery(a, p, c.query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，got %d（%s）", c.name, rec.Code, rec.Body.String())
		}
	}
	// 合法但极大/极小的 limit 应被夹紧而不是报错
	for _, ok := range []string{"?limit=999999", "?limit=0", "?limit=-5"} {
		if rec := logsQuery(a, p, ok); rec.Code != http.StatusOK {
			t.Fatalf("%s：应被夹紧而不是报错，got %d", ok, rec.Code)
		}
	}
}

// TestLogsAPI_DisabledWithoutStore 未注入存储 = 该能力关闭，明确回 503。
func TestLogsAPI_DisabledWithoutStore(t *testing.T) {
	a := scopeTestAPI(t) // 不注入日志存储
	rec := logsQuery(a, logsReader(), "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用应回 503，got %d", rec.Code)
	}
}

// TestLogsAPI_MiddlewareOnlyExemptsAgentPost 真实中间件 + 真实路由表的回归守卫。
//
// 守的是一个**只在生产暴露**的坑：公开白名单曾按「路径前缀」放行，而 /api/v1/logs 既是
// Agent 上行（POST，走 X-Agent-Secret）又是浏览器检索（GET，走登录 + logs:read）。
// 按前缀放行的后果是：登录中间件对整条路径不解析 token → 没有 Principal →
// permit 直接 401（浏览器永远查不了日志），且依赖 Principal 的节点范围过滤静默失效。
func TestLogsAPI_MiddlewareOnlyExemptsAgentPost(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	mk := func(name string, roles []string) int64 {
		t.Helper()
		if err := a.authStore.CreateUser(auth.User{
			Username: name, Roles: roles,
			Scope: auth.Scope{Mode: auth.ScopeGlobal}, Status: auth.StatusEnabled,
		}, "Passw0rd!", "admin"); err != nil {
			t.Fatalf("CreateUser(%s): %v", name, err)
		}
		p := a.authStore.GetPrincipal(name)
		if p == nil {
			t.Fatalf("应能取到 %s 的授权身份", name)
		}
		return p.TokenVersion
	}
	opsTV := mk("ops", []string{auth.RoleOpsAdmin})       // 内置运维角色：默认含 logs:read
	alertTV := mk("alert", []string{auth.RoleAlertAdmin}) // 告警角色：刻意不含 logs:read

	cfg := config.AuthConfig{Enabled: true, Secret: "s3cret"}
	h := AuthMiddleware(newRoutesMux(a), cfg, a.authStore)
	call := func(method, target, token string, hdr map[string]string) int {
		req := httptest.NewRequest(method, target, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 1) Agent 上行（POST）免登录：不能是 401（该路由属于 receiver，不在此 mux 上，故走到 404）
	if code := call(http.MethodPost, "/api/v1/logs", "", map[string]string{"X-Agent-Secret": "s3cret"}); code == http.StatusUnauthorized {
		t.Fatal("POST /api/v1/logs 是 Agent 上行，不该被登录中间件拦下（否则日志传不上来）")
	}
	// 2) 浏览器 GET 必须带登录态
	if code := call(http.MethodGet, "/api/v1/logs", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("无凭据的 GET /api/v1/logs 应 401，got %d", code)
	}
	// 3) 有登录态但缺 logs:read → 403（同时说明 Principal 确实被挂上了）
	if code := call(http.MethodGet, "/api/v1/logs?q=x", genToken("alert", alertTV, cfg.Secret), nil); code != http.StatusForbidden {
		t.Fatalf("缺 logs:read 应 403，got %d", code)
	}
	// 4) 具备 logs:read → 200
	if code := call(http.MethodGet, "/api/v1/logs?q=x", genToken("ops", opsTV, cfg.Secret), nil); code != http.StatusOK {
		t.Fatalf("具备 logs:read 应 200，got %d", code)
	}
}

// TestLogsAPI_CursorPaging 翻页：第二页接着第一页，不重不漏。
func TestLogsAPI_CursorPaging(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	first := decodeLogResult(t, logsQuery(a, logsReader(), "?limit=2"))
	if len(first.Lines) != 2 || !first.Truncated || first.Cursor == "" {
		t.Fatalf("首页应满 2 条并给出游标：%+v", first)
	}
	second := decodeLogResult(t, logsQuery(a, logsReader(), "?limit=2&cursor="+first.Cursor))
	if second.Truncated {
		t.Fatalf("第二页不该再截断：%+v", second)
	}
	if len(second.Lines) != 1 {
		t.Fatalf("第二页应有 1 条，got %v", second.Lines)
	}
	seen := map[string]bool{}
	for _, l := range append(first.Lines, second.Lines...) {
		if seen[l.Text] {
			t.Fatalf("翻页出现重复：%q", l.Text)
		}
		seen[l.Text] = true
	}
	if len(seen) != 3 {
		t.Fatalf("两页合计应覆盖全部 3 条：%v", seen)
	}
	// 游标需要 URL 编码才能安全放进查询串（前端用 URLSearchParams 会自动做）
	if strings.ContainsAny(first.Cursor, "+/=") {
		t.Fatalf("游标应为 URL 安全字符（RawURLEncoding），got %q", first.Cursor)
	}
}
