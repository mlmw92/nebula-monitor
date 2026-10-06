package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logstore"
)

// stubLogStore 是接口层的日志后端替身：只用来断言**错误分类**，
// 真实检索语义由 logstore 包的契约用例覆盖（那两个适配器跑同一套断言）。
type stubLogStore struct {
	res      model.LogQueryResult
	queryErr error
}

func (s stubLogStore) Append(model.LogBatch) (int, int, string, error) { return 0, 0, "", nil }
func (s stubLogStore) Query(model.LogQuery, logstore.Cursor) (model.LogQueryResult, error) {
	return s.res, s.queryErr
}
func (s stubLogStore) Backend() string          { return "stub" }
func (s stubLogStore) Sources() []string        { return nil }
func (s stubLogStore) FieldNames(string) []string { return nil }

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
