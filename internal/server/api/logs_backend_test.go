package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logstore"
)

// stubLogStore 是接口层的日志后端替身：只用来断言**错误分类**，
// 真实检索语义由 logstore 包的契约用例覆盖（那两个适配器跑同一套断言）。
type stubLogStore struct {
	res      model.LogQueryResult
	queryErr error
	cap      logstore.Capability
}

func (s stubLogStore) Append(model.LogBatch) (int, int, string, error) { return 0, 0, "", nil }
func (s stubLogStore) Query(model.LogQuery, logstore.Cursor) (model.LogQueryResult, error) {
	return s.res, s.queryErr
}
func (s stubLogStore) Backend() string                 { return "stub" }
func (s stubLogStore) Sources() []string               { return nil }
func (s stubLogStore) FieldNames(string) []string      { return nil }
func (s stubLogStore) Capability() logstore.Capability { return s.cap }

// 后端不可用要回 502：运维看到 400 会去改查询条件，而真实情况是"日志后端连不上"。
// 这两类错误必须能分开，否则错误码会把排查方向指反。
func TestRoutes_LogsQueryBackendUnavailable(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("logs:read")

	a.SetLogStore(stubLogStore{queryErr: fmt.Errorf("%w: 连接被拒绝", logstore.ErrBackendUnavailable)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs", ""))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("后端不可用应 502，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if body := decodeBody(t, rec); body["error"] == nil {
		t.Fatalf("应返回错误信息：%v", body)
	}

	// 查询本身的问题（非法正则、游标不属于当前后端）仍是 400
	a.SetLogStore(stubLogStore{queryErr: errors.New("游标不属于当前日志后端")})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("查询本身的问题应 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 正常路径仍是 200
	a.SetLogStore(stubLogStore{res: model.LogQueryResult{Lines: []model.LogHit{}}})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("正常检索应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// 能力端点（批次 23）：如实报告"能做什么、存了什么"。
//
// 两处最要紧：① 本地后端要有扫描预算、外部后端必须是 null（"没有逐文件扫描"不等于"预算无穷大"）；
// ② **探测失败要带出来**——把它显示成 0，用户会以为日志丢了，那正是这个端点要避免的误导。
func TestRoutes_LogsBackendCapability(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)
	p := globalPrincipal("logs:read")

	// 未启用集中日志 → 503（与检索同一取向：能力未启用就说未启用）
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs/backend", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用应 503，实际 %d", rec.Code)
	}

	// 本地后端：无索引、有预算、给出真实存了什么
	a.SetLogStore(stubLogStore{cap: logstore.Capability{
		Backend:    logstore.BackendLocal,
		ScanBudget: &logstore.ScanBudget{Bytes: 64 << 20, Lines: 200000},
		Notes:      []string{"本地后端不做倒排索引"},
		Storage: logstore.StorageStats{
			Sources: 3, Nodes: 2, OldestDay: "2026-10-01", NewestDay: "2026-10-07", Bytes: 4096,
		},
	}})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs/backend", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["backend"] != logstore.BackendLocal {
		t.Fatalf("backend = %v", body["backend"])
	}
	caps, _ := body["capabilities"].(map[string]any)
	if caps["fullTextIndex"] != false {
		t.Fatalf("本地后端不该声称有全文索引：%#v", caps["fullTextIndex"])
	}
	budget, _ := caps["scanBudget"].(map[string]any)
	if budget == nil || budget["lines"].(float64) != 200000 {
		t.Fatalf("本地后端应给出扫描预算：%#v", caps["scanBudget"])
	}
	storage, _ := body["storage"].(map[string]any)
	if storage["sources"].(float64) != 3 || storage["oldestDay"] != "2026-10-01" {
		t.Fatalf("storage 不符：%#v", storage)
	}

	// 外部后端：有索引、**没有**扫描预算；探测失败要带 probeError
	a.SetLogStore(stubLogStore{cap: logstore.Capability{
		Backend: logstore.BackendVictoriaLogs, FullTextIndex: true, FieldIndex: true,
		Notes:   []string{"历史本地分片不会自动迁入"},
		Storage: logstore.StorageStats{Err: "探测外部后端失败: 连接被拒绝"},
	}})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/logs/backend", ""))
	body = decodeBody(t, rec)
	caps, _ = body["capabilities"].(map[string]any)
	if caps["fullTextIndex"] != true {
		t.Fatalf("外部后端应声称有全文索引：%#v", caps)
	}
	if caps["scanBudget"] != nil {
		t.Fatalf("外部后端不该有扫描预算（null 而不是 0）：%#v", caps["scanBudget"])
	}
	if notes, _ := caps["notes"].([]any); len(notes) != 1 {
		t.Fatalf("notes 应原样带出：%#v", caps["notes"])
	}
	storage, _ = body["storage"].(map[string]any)
	if s, _ := storage["probeError"].(string); !strings.Contains(s, "连接被拒绝") {
		t.Fatalf("探测失败必须带出来，实际 %#v", storage["probeError"])
	}

	// 权限：没有 logs:read 时 403
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/logs/backend", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("无 logs:read 应 403，实际 %d", rec.Code)
	}
}
