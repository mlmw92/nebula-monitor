package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/dialtest"
)

// 对外状态页（C3）。这组用例守两件事：
//  1. **它真的是免登录的**（否则「对外」就无从谈起）；
//  2. **它不泄露内部信息**——状态页是给外部人看的，target / 节点 / 错误原文都不该出现。

// statusFakeStore 是 storage.Storage 的替身：可预置「即时最新值」与「区间点」。
type statusFakeStore struct {
	latest map[string][]model.Series
	ranges map[string][]model.Series // key: metric|name
	fail   bool
}

func (s *statusFakeStore) Write([]model.Metric) error { return nil }
func (s *statusFakeStore) QueryRange(_, name string, labels map[string]string, _, _, _ int64) ([]model.Series, error) {
	if s.fail {
		return nil, errors.New("tsdb down")
	}
	return s.ranges[name+"|"+labels["name"]], nil
}
func (s *statusFakeStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, errors.New("not used")
}
func (s *statusFakeStore) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *statusFakeStore) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, nil
}
func (s *statusFakeStore) QueryAllLatest(name string, labels map[string]string) ([]model.Series, error) {
	if s.fail {
		return nil, errors.New("tsdb down")
	}
	return s.latest[name], nil
}
func (s *statusFakeStore) Close() error    { return nil }
func (s *statusFakeStore) Backend() string { return "fake" }

// statusFakeDialtest 是 DialtestProvider 的替身。
type statusFakeDialtest struct{ tasks []dialtest.Task }

func (f *statusFakeDialtest) List() []dialtest.Task { return f.tasks }
func (f *statusFakeDialtest) Get(id string) (dialtest.Task, bool) {
	for _, t := range f.tasks {
		if t.ID == id {
			return t, true
		}
	}
	return dialtest.Task{}, false
}
func (f *statusFakeDialtest) Create(t dialtest.Task) dialtest.Task { return t }
func (f *statusFakeDialtest) Update(dialtest.Task) error           { return nil }
func (f *statusFakeDialtest) Delete(string) error                  { return nil }
func (f *statusFakeDialtest) LastResults() map[string]dialtest.Result {
	return nil
}

// series 构造一条带 name 标签的序列。
func series(name string, points ...model.Point) model.Series {
	return model.Series{Labels: map[string]string{"name": name}, Points: points}
}

func pt(v float64) model.Point { return model.Point{Timestamp: model.NowMillis(), Value: v} }

// newStatusAPI 构造状态页测试环境：三个任务（对外+启用 / 未对外 / 对外但停用）。
func newStatusAPI(t *testing.T, store *statusFakeStore) *API {
	t.Helper()
	a := scopeTestAPI(t) // 带真实 authStore（用于证明「免登录」）
	a.store = store
	a.dialtest = &statusFakeDialtest{tasks: []dialtest.Task{
		{ID: "t1", Name: "官网首页", Type: dialtest.TaskTypeHTTPS, Enabled: true, Public: true,
			Target: "internal-web.corp:8443"},
		{ID: "t2", Name: "内部后台", Type: dialtest.TaskTypeHTTP, Enabled: true, Public: false,
			Target: "internal-admin.corp:8080"},
		{ID: "t3", Name: "已停用服务", Type: dialtest.TaskTypeTCP, Enabled: false, Public: true,
			Target: "internal-old.corp:3306"},
	}}
	return a
}

// getStatus 发起一次**不带任何身份**的状态页请求。
func getStatus(a *API) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(nil, http.MethodGet, "/api/v1/status", ""))
	return rec
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) publicStatus {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，got %d（%s）", rec.Code, rec.Body.String())
	}
	var out publicStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应解析失败：%v（%s）", err, rec.Body.String())
	}
	return out
}

// TestPublicStatus_NoLoginRequired 免登录可读——这正是「对外」的含义。
func TestPublicStatus_NoLoginRequired(t *testing.T) {
	a := newStatusAPI(t, &statusFakeStore{latest: map[string][]model.Series{
		"dial_test_up": {series("官网首页", pt(1))},
	}})
	if !isPublicPath(httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)) {
		t.Fatal("GET /api/v1/status 必须在公开白名单内（否则外部访问者拿到的是 401）")
	}
	if isPublicPath(httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)) {
		t.Fatal("只应放行 GET：同一路径的其它方法不该被顺带放行")
	}
	rec := getStatus(a)
	if rec.Code != http.StatusOK {
		t.Fatalf("无身份访问应 200，got %d", rec.Code)
	}
}

// TestPublicStatus_OnlyPublicEnabledTasks 只展示「显式勾选对外」且「启用中」的任务。
func TestPublicStatus_OnlyPublicEnabledTasks(t *testing.T) {
	a := newStatusAPI(t, &statusFakeStore{latest: map[string][]model.Series{
		"dial_test_up": {series("官网首页", pt(1)), series("内部后台", pt(1)), series("已停用服务", pt(1))},
	}})
	out := decodeStatus(t, getStatus(a))
	if len(out.Items) != 1 || out.Items[0].Name != "官网首页" {
		t.Fatalf("只应展示公开且启用的任务，got %+v", out.Items)
	}
}

// TestPublicStatus_LeaksNoInternals 对外页面**不得**出现 target / 内网主机名 / 错误原文。
//
// 这条是本功能最重要的用例：状态页无需登录，任何多带出去的信息都是对外泄露。
func TestPublicStatus_LeaksNoInternals(t *testing.T) {
	a := newStatusAPI(t, &statusFakeStore{latest: map[string][]model.Series{
		"dial_test_up":      {series("官网首页", pt(0))},
		"dial_test_latency": {series("官网首页", pt(12.3))},
	}})
	rec := getStatus(a)
	body := rec.Body.String()
	for _, leak := range []string{"internal-web.corp", "8443", "target", "Target", "node", "error"} {
		if strings.Contains(body, leak) {
			t.Fatalf("对外响应不应包含 %q：%s", leak, body)
		}
	}
	// 该有的信息要有（名称/状态/延迟）
	out := decodeStatus(t, rec)
	if out.Items[0].Name != "官网首页" || out.Items[0].Status != "down" || out.Items[0].LatencyMs != 12.3 {
		t.Fatalf("应给出名称、状态与延迟，got %+v", out.Items[0])
	}
}

// TestPublicStatus_OverallAndUptime 整体状态与可用率：可用率取近 24 小时窗口内的平均值，
// 同名多序列（多 target）取**最差**——「部分可用」对外应当算不正常。
func TestPublicStatus_OverallAndUptime(t *testing.T) {
	four := []model.Point{pt(1), pt(1), pt(0), pt(1)} // 75%
	store := &statusFakeStore{
		latest: map[string][]model.Series{"dial_test_up": {
			series("官网首页", pt(1)),
			series("内部后台", pt(1)),
		}},
		ranges: map[string][]model.Series{
			"dial_test_up|官网首页": {series("官网首页", four...)},
		},
	}
	a := newStatusAPI(t, store)
	// 让「内部后台」也对外，以便验证 partial
	a.dialtest.(*statusFakeDialtest).tasks[1].Public = true
	a.dialtest.(*statusFakeDialtest).tasks[1].Target = "x"

	out := decodeStatus(t, getStatus(a))
	if len(out.Items) != 2 {
		t.Fatalf("应展示 2 项，got %+v", out.Items)
	}
	if out.Items[0].Uptime != 75 {
		t.Fatalf("可用率应为 75，got %v", out.Items[0].Uptime)
	}
	if out.Items[1].Uptime != -1 {
		t.Fatalf("无区间数据时可用率应为 -1（前端显示「—」），got %v", out.Items[1].Uptime)
	}
	if out.Overall != "up" {
		t.Fatalf("两项都可用时整体应为 up，got %q", out.Overall)
	}

	// 一项变 down → partial
	store.latest["dial_test_up"] = []model.Series{series("官网首页", pt(1)), series("内部后台", pt(0))}
	if out := decodeStatus(t, getStatus(a)); out.Overall != "partial" {
		t.Fatalf("部分可用应为 partial，got %q", out.Overall)
	}
}

// TestPublicStatus_UnknownWhenTSDBAbsent 时序库不可用时要显示「未知」而不是「故障」——
// 否则我们自己的故障会被说成服务的故障（方向正好反了），而且页面不该 500。
func TestPublicStatus_UnknownWhenTSDBAbsent(t *testing.T) {
	a := newStatusAPI(t, &statusFakeStore{fail: true})
	out := decodeStatus(t, getStatus(a))
	if out.Overall != "unknown" {
		t.Fatalf("读不到数据时整体应为 unknown，got %q", out.Overall)
	}
	for _, it := range out.Items {
		if it.Status != "unknown" {
			t.Fatalf("读不到数据时单项应为 unknown，got %+v", it)
		}
	}
}

// TestPublicStatus_NoPublicTasksIsEmpty 没有勾选任何对外任务时：空列表 + unknown，而不是报错。
func TestPublicStatus_NoPublicTasksIsEmpty(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &statusFakeStore{}
	a.dialtest = &statusFakeDialtest{tasks: []dialtest.Task{{ID: "t1", Name: "内部服务", Enabled: true}}}
	out := decodeStatus(t, getStatus(a))
	if len(out.Items) != 0 || out.Overall != "unknown" {
		t.Fatalf("无对外任务时应返回空列表，got %+v", out)
	}
}
