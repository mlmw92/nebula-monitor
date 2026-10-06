package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logparse"
)

// 结构化字段检索（C2 子批次 D）：接口层要点是**参数形态与错误必须明确**——
// 一个"查不到但也不报错"的字段过滤，用户会以为日志里真的没有。
func TestLogsQueryByFieldFilter(t *testing.T) {
	a, store := newLogsTestAPI(t)
	now := time.Now().UnixMilli()
	if _, _, _, err := store.Append(model.LogBatch{
		Source: "applog", Node: "web-01",
		Lines: []model.LogLine{
			{Ts: now - 60*1000, Text: `{"level":"error","status":500,"path":"/api/orders"}`},
			{Ts: now - 50*1000, Text: `{"level":"error","status":404,"path":"/api/orders"}`},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 单个字段
	res := decodeLogResult(t, logsQuery(a, logsReader(), "?q=error&field=status:500"))
	if len(res.Lines) != 1 {
		t.Fatalf("field=status:500 应命中 1 行，实际 %d：%+v", len(res.Lines), res.Lines)
	}
	if res.Lines[0].Fields["status"] != "500" || res.Lines[0].Fields["path"] != "/api/orders" {
		t.Fatalf("命中行应带回结构化字段：%+v", res.Lines[0].Fields)
	}

	// 多个条件是与关系
	if got := decodeLogResult(t, logsQuery(a, logsReader(), "?field=level:error&field=status:404")); len(got.Lines) != 1 {
		t.Fatalf("多条件应为与关系，实际 %d 行", len(got.Lines))
	}
	if got := decodeLogResult(t, logsQuery(a, logsReader(), "?field=level:error&field=status:503")); len(got.Lines) != 0 {
		t.Fatalf("不存在的组合应返回空结果，实际 %d 行", len(got.Lines))
	}
}

// 字段过滤同样受资源范围约束：不能靠加一个字段条件把范围外节点的日志捞出来。
func TestLogsQueryByFieldRespectsScope(t *testing.T) {
	a, store := newLogsTestAPI(t)
	now := time.Now().UnixMilli()
	if _, _, _, err := store.Append(model.LogBatch{
		Source: "applog", Node: "db-01",
		Lines: []model.LogLine{{Ts: now - 30*1000, Text: `{"level":"error","status":500}`}},
	}); err != nil {
		t.Fatal(err)
	}
	// g1 的受限用户（web-01 / web-02）加字段条件也看不到 db-01（g2）的日志
	res := decodeLogResult(t, logsQuery(a, restrictedPrincipal([]string{"logs:read"}, "g1"), "?field=status:500"))
	for _, line := range res.Lines {
		if line.Node == "db-01" {
			t.Fatalf("范围外节点的日志被字段过滤捞出来了：%+v", line)
		}
	}
}

// 参数校验：形态错误、字段名非法、值超长、条件过多都必须 400——
// 静默忽略会让用户以为"这些日志不存在"。
func TestLogsQueryFieldFilterValidation(t *testing.T) {
	a, _ := newLogsTestAPI(t)
	cases := []struct {
		name  string
		extra string
	}{
		{"缺少冒号", "?field=status500"},
		{"缺少键", "?field=:500"},
		{"缺少值", "?field=status:"},
		{"字段名以数字开头", "?field=1status:500"},
		{"字段名含空格", "?field=sta%20tus:500"},
		{"值超长", "?field=msg:" + strings.Repeat("x", logparse.MaxFieldValueBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := logsQuery(a, logsReader(), tc.extra)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应 400，实际 %d（%s）", rec.Code, rec.Body.String())
			}
		})
	}

	// 条件过多（超过上限）
	var parts []string
	for i := 0; i <= MaxLogFieldFilters; i++ {
		parts = append(parts, "field=f"+string(rune('a'+i))+":v")
	}
	rec := logsQuery(a, logsReader(), "?"+strings.Join(parts, "&"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("条件过多应 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// 字段名候选接口：只列服务端真的见过的名字（列"可能存在的字段"会让用户按查不到的名字去筛）。
func TestLogsFieldsEndpoint(t *testing.T) {
	a, store := newLogsTestAPI(t)
	now := time.Now().UnixMilli()
	if _, _, _, err := store.Append(model.LogBatch{
		Source: "applog", Node: "web-01",
		Lines: []model.LogLine{
			{Ts: now - 30*1000, Text: `{"level":"error","status":500}`},
			{Ts: now - 20*1000, Text: `{"level":"info","status":200}`},
		},
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := reqWith(logsReader(), http.MethodGet, "/api/v1/logs/fields?sources=applog", "")
	newRoutesMux(a).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	fields, _ := decodeBody(t, rec)["fields"].(map[string]any)
	names, _ := fields["applog"].([]any)
	got := map[string]bool{}
	for _, n := range names {
		got[n.(string)] = true
	}
	if !got["level"] || !got["status"] {
		t.Fatalf("字段候选应含 level 与 status，实际 %v", names)
	}
	if got["nosuchfield"] {
		t.Fatalf("不应列出没见过的字段：%v", names)
	}

	// 权限：缺 logs:read 拿不到
	denied := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(denied, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/logs/fields", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 logs:read 应 403，实际 %d", denied.Code)
	}
}
