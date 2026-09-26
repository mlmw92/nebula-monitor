package api

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/selfmon"
)

// healthTestStore 是 storage.Storage 的测试替身：可注入查询错误。
type healthTestStore struct {
	queryErr error
}

func (s *healthTestStore) Write([]model.Metric) error { return nil }

func (s *healthTestStore) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, s.queryErr
}

func (s *healthTestStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, s.queryErr
}

func (s *healthTestStore) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, s.queryErr
}

func (s *healthTestStore) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, s.queryErr
}

func (s *healthTestStore) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, s.queryErr
}

func (s *healthTestStore) Close() error { return nil }

func (s *healthTestStore) Backend() string { return "fake" }

// healthTestAPI 构造仅含健康探针所需依赖的 API（带真实 authStore，便于验证权限门）。
func healthTestAPI(t *testing.T, store *healthTestStore, mon *selfmon.Monitor) *API {
	t.Helper()
	a := scopeTestAPI(t)
	a.store = store
	a.selfmon = mon
	return a
}

// TestRoutes_HealthzOKAndPublic /healthz 永远 200，且无需登录（探针无法携带令牌）。
func TestRoutes_HealthzOKAndPublic(t *testing.T) {
	if !isPublicPath("/healthz") || !isPublicPath("/readyz") {
		t.Fatal("健康探针必须在公开白名单内（k8s/systemd/反代无法携带登录令牌）")
	}
	a := healthTestAPI(t, &healthTestStore{}, nil)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal(), http.MethodGet, "/healthz", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，want 200（body=%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}
	if body["version"] == "" || body["version"] == nil {
		t.Fatal("存活探针应报告版本")
	}
	if _, ok := body["uptimeSeconds"].(float64); !ok {
		t.Fatalf("应报告运行时长：%v", body["uptimeSeconds"])
	}
}

// TestRoutes_ReadyzTSDBFailure 时序库不可用 → 503 且指明失败原因。
func TestRoutes_ReadyzTSDBFailure(t *testing.T) {
	a := healthTestAPI(t, &healthTestStore{queryErr: errors.New("connection refused")}, nil)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal(), http.MethodGet, "/readyz", ""))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d，want 503", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["status"] != "degraded" {
		t.Fatalf("status = %v", body["status"])
	}
	checks, _ := body["checks"].(map[string]any)
	tsdb, _ := checks["tsdb"].(map[string]any)
	if tsdb == nil || tsdb["ok"] != false {
		t.Fatalf("checks.tsdb 应标记失败：%v", checks)
	}
	if detail, _ := tsdb["detail"].(string); !strings.Contains(detail, "查询失败") {
		t.Fatalf("应说明失败原因：%v", tsdb)
	}
}

// TestRoutes_ReadyzHealthy 依赖正常时 200，并回显后端类型便于排查。
func TestRoutes_ReadyzHealthy(t *testing.T) {
	a := healthTestAPI(t, &healthTestStore{}, nil)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal(), http.MethodGet, "/readyz", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，want 200（body=%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	checks, _ := body["checks"].(map[string]any)
	tsdb, _ := checks["tsdb"].(map[string]any)
	if tsdb == nil || tsdb["ok"] != true || tsdb["detail"] != "fake" {
		t.Fatalf("checks.tsdb 不符：%v", checks)
	}
	// 未启用告警（未注入评估周期）时不应出现 alertEngine 项，避免误报
	if _, exists := checks["alertEngine"]; exists {
		t.Fatalf("未启用告警时不应检查评估节拍：%v", checks)
	}
}

// TestRoutes_ReadyzAlertEngineStale 评估循环停摆（或首次评估迟迟未完成）→ 503。
func TestRoutes_ReadyzAlertEngineStale(t *testing.T) {
	mon := selfmon.New("test")
	mon.SetEvalInterval(30 * time.Second)
	a := healthTestAPI(t, &healthTestStore{}, mon)
	a.startedAt = time.Now().Add(-time.Hour) // 已运行 1 小时却从未评估
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal(), http.MethodGet, "/readyz", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("首次评估未完成应 503，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	checks, _ := decodeBody(t, rec)["checks"].(map[string]any)
	engine, _ := checks["alertEngine"].(map[string]any)
	if engine == nil || engine["ok"] != false {
		t.Fatalf("checks.alertEngine 应标记失败：%v", checks)
	}

	// 完成一轮评估后恢复正常
	mon.AddEval()
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal(), http.MethodGet, "/readyz", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("评估正常后应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	checks, _ = decodeBody(t, rec)["checks"].(map[string]any)
	engine, _ = checks["alertEngine"].(map[string]any)
	if engine == nil || engine["ok"] != true {
		t.Fatalf("checks.alertEngine 应标记正常：%v", checks)
	}
}

// TestRoutes_SelfStatusSnapshot 自监控快照接口：返回进程指标，且受 dashboard:read 约束。
func TestRoutes_SelfStatusSnapshot(t *testing.T) {
	mon := selfmon.New("1.24.0")
	mon.AddHTTPRequest(200)
	mon.AddHTTPRequest(500)
	a := healthTestAPI(t, &healthTestStore{}, mon)
	mux := newRoutesMux(a)

	// 缺权限 → 403
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("nodes:read"), http.MethodGet, "/api/v1/self/status", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 dashboard:read 应 403，got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("dashboard:read"), http.MethodGet, "/api/v1/self/status", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，want 200（body=%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["metricPrefix"] != selfmon.MetricPrefix {
		t.Fatalf("应告知指标前缀：%v", body["metricPrefix"])
	}
	metrics, _ := body["metrics"].(map[string]any)
	if metrics == nil {
		t.Fatalf("缺少 metrics：%v", body)
	}
	if metrics["version"] != "1.24.0" || metrics["node"] != mon.Node() {
		t.Fatalf("版本/节点不符：%v", metrics)
	}
	httpStats, _ := metrics["http"].(map[string]any)
	if httpStats["requests"] != float64(2) || httpStats["errors"] != float64(1) {
		t.Fatalf("HTTP 统计不符：%v", httpStats)
	}
}

// TestRoutes_SelfStatusWithoutMonitor 未注入收集器时接口仍可用（返回零值快照，不 panic）。
func TestRoutes_SelfStatusWithoutMonitor(t *testing.T) {
	a := healthTestAPI(t, &healthTestStore{}, nil)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("dashboard:read"), http.MethodGet, "/api/v1/self/status", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d，want 200（body=%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["metrics"].(map[string]any); !ok {
		t.Fatalf("应返回空快照对象：%v", body)
	}
}

// hijackableWriter 模拟支持 Hijack / Flush 的底层 ResponseWriter（WebSocket 升级所需）。
type hijackableWriter struct {
	http.ResponseWriter
	hijacked bool
	flushed  bool
}

func (h *hijackableWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	return nil, nil, nil
}

func (h *hijackableWriter) Flush() { h.flushed = true }

// TestMetricsMiddleware 计数、nil 直通，以及最关键的 Hijack/Flush 透传。
func TestMetricsMiddleware(t *testing.T) {
	if got := MetricsMiddleware(http.DefaultServeMux, nil); got != http.Handler(http.DefaultServeMux) {
		t.Fatal("未注入收集器时应原样返回，不做包装")
	}

	mon := selfmon.New("dev")
	handler := MetricsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/boom":
			w.WriteHeader(http.StatusInternalServerError)
		case "/ws":
			// /ws 的协议升级依赖 Hijack：包装后必须仍能取到并透传到底层
			h, ok := w.(http.Hijacker)
			if !ok {
				t.Error("包装后丢失 Hijack 能力：/ws 握手会全部失败")
				return
			}
			if _, _, err := h.Hijack(); err != nil {
				t.Errorf("Hijack 透传失败：%v", err)
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			} else {
				t.Error("包装后丢失 Flush 能力")
			}
		}
	}), mon)

	w := &hijackableWriter{ResponseWriter: httptest.NewRecorder()}
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if !w.hijacked || !w.flushed {
		t.Fatalf("Hijack/Flush 未透传到底层：hijacked=%v flushed=%v", w.hijacked, w.flushed)
	}

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	snap := mon.Snapshot()
	if snap.HTTP.Requests != 3 {
		t.Fatalf("HTTP 请求计数不符：%d", snap.HTTP.Requests)
	}
	if snap.HTTP.Errors != 1 {
		t.Fatalf("HTTP 错误计数不符：%d", snap.HTTP.Errors)
	}
}

// TestHealthTestAPIWiring healthTestAPI 自身不应引入额外副作用（防止测试辅助失真）。
func TestHealthTestAPIWiring(t *testing.T) {
	a := healthTestAPI(t, &healthTestStore{}, nil)
	if a.store == nil {
		t.Fatal("store 未注入")
	}
	// startedAt 由真实构造函数设置；测试内直接构造的 API 需显式给值才能测「首次评估」分支
	if !a.startedAt.IsZero() {
		t.Fatal("测试辅助不应预设 startedAt")
	}
}
