package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/mwreg"
	"github.com/nebula/monitor/internal/template"
)

// seriesStore 是 storage.Storage 的测试替身：按指标名返回预置序列，
// 并按指标名记录查询时用到的标签过滤条件——用于断言「存活指标按 template 过滤、摘要指标不过滤」。
type seriesStore struct {
	series         map[string][]model.Series
	labelsByMetric map[string]map[string]string
}

func (s *seriesStore) Write([]model.Metric) error { return nil }
func (s *seriesStore) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *seriesStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *seriesStore) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *seriesStore) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, nil
}
func (s *seriesStore) QueryAllLatest(name string, labels map[string]string) ([]model.Series, error) {
	if s.labelsByMetric == nil {
		s.labelsByMetric = map[string]map[string]string{}
	}
	s.labelsByMetric[name] = labels
	return s.series[name], nil
}

// labelsFor 返回某指标查询时用到的标签过滤条件（未查过则为 nil）。
func (s *seriesStore) labelsFor(metric string) map[string]string {
	if s.labelsByMetric == nil {
		return nil
	}
	return s.labelsByMetric[metric]
}
func (s *seriesStore) Close() error    { return nil }
func (s *seriesStore) Backend() string { return "fake" }

// templateSource 是模板快照的测试替身。
type templateSource struct{ list []template.Config }

func (s *templateSource) Snapshot() ([]template.Config, uint64) { return s.list, 1 }

func point(v float64) []model.Point { return []model.Point{{Value: v}} }

// rabbitmqTemplate 返回一个含摘要规则的模板。
func rabbitmqTemplate() template.Config {
	return template.Config{
		ID:      "rabbitmq",
		Title:   "RabbitMQ",
		Kind:    template.KindPrometheusExporter,
		Groups:  []string{"default"},
		Targets: []template.Target{{Instance: "mq-01:15692", Addr: "http://127.0.0.1:15692/metrics"}},
		Rules: template.Rules{
			Keep: "^rabbitmq_",
			Metrics: []template.MetricRule{
				{Name: "queue_depth", Label: "队列深度", Unit: "个"},
			},
		},
	}
}

// alertStoreStub 是 AlertStore 的测试替身（scopeTestAPI 不注入告警，总览会读它）。
type alertStoreStub struct{ active []model.AlertEvent }

func (s *alertStoreStub) Recent(int) []model.AlertEvent { return s.active }
func (s *alertStoreStub) Active() []model.AlertEvent    { return s.active }

// templateAPI 构造注入了「模板派生注册表 + 预置时序数据 + 一条模板指标告警」的 API。
func templateAPI(t *testing.T) (*API, *seriesStore) {
	t.Helper()
	a := scopeTestAPI(t)
	store := &seriesStore{series: map[string][]model.Series{
		"template_target_up": {
			{Labels: map[string]string{"node": "n1", "instance": "mq-01:15692", "template": "rabbitmq", "group": "default"}, Points: point(1)},
			{Labels: map[string]string{"node": "n1", "instance": "mq-02:15692", "template": "rabbitmq", "group": "default"}, Points: point(0)},
		},
		"rabbitmq_queue_depth": {
			{Labels: map[string]string{"node": "n1", "instance": "mq-01:15692", "template": "rabbitmq"}, Points: point(42)},
		},
	}}
	a.store = store
	// 一条模板指标上的活跃告警：用于验证「告警按类型前缀归集」对模板类型同样生效
	// （且不会被某个内置类型抢走——模板 id 与内置前缀互斥由校验器保证）
	a.alerts = &alertStoreStub{active: []model.AlertEvent{{Metric: "rabbitmq_queue_depth", Node: "n1"}}}
	a.SetMiddlewareRegistry(mwreg.New(&templateSource{list: []template.Config{rabbitmqTemplate()}}))
	return a, store
}

// TestMiddlewareOverview_IncludesTemplateType 总览的类型清单来自注册表，
// 因此「新增中间件只写模板」在总览卡片上自动生效。
func TestMiddlewareOverview_IncludesTemplateType(t *testing.T) {
	a, store := templateAPI(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/middleware/overview", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d（body=%s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		Total      int `json:"total"`
		AlertCount int `json:"alertCount"`
		Types      []struct {
			Type       string `json:"type"`
			Label      string `json:"label"`
			Kind       string `json:"kind"`
			Total      int    `json:"total"`
			Up         int    `json:"up"`
			Down       int    `json:"down"`
			AlertCount int    `json:"alertCount"`
			Summary    []struct {
				Key   string  `json:"key"`
				Label string  `json:"label"`
				Value float64 `json:"value"`
			} `json:"summary"`
		} `json:"types"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}

	var found bool
	for _, item := range resp.Types {
		if item.Type != "rabbitmq" {
			// 模板指标的告警不应被内置类型抢走
			if item.AlertCount != 0 {
				t.Errorf("内置类型 %s 不应归集模板指标的告警", item.Type)
			}
			continue
		}
		found = true
		if item.Label != "RabbitMQ" {
			t.Errorf("展示名应取模板 title，got %q", item.Label)
		}
		// 前端据此区分「内置类型」与「模板派生类型」（模板类型走通用 Tab 组件）
		if item.Kind != string(mwreg.KindTemplate) {
			t.Errorf("kind 应为 %q，got %q", mwreg.KindTemplate, item.Kind)
		}
		if item.Total != 2 || item.Up != 1 || item.Down != 1 {
			t.Errorf("实例统计不符：total=%d up=%d down=%d", item.Total, item.Up, item.Down)
		}
		if item.AlertCount != 1 {
			t.Errorf("模板类型应归集到自己的告警，got %d", item.AlertCount)
		}
		if len(item.Summary) != 1 || item.Summary[0].Key != "rabbitmq_queue_depth" || item.Summary[0].Value != 42 {
			t.Errorf("卡片摘要不符：%+v", item.Summary)
		}
	}
	if !found {
		t.Fatalf("总览缺少模板派生类型 rabbitmq：%s", rec.Body.String())
	}
	if resp.AlertCount != 1 {
		t.Errorf("总告警数 = %d，want 1", resp.AlertCount)
	}
	// 存活指标的查询必须带 template 过滤（所有模板共用 template_target_up，
	// 不过滤会让 A 模板的在线状态被 B 模板的实例影响）；摘要指标名已含模板 id，无需过滤
	if got := store.labelsFor("template_target_up"); got["template"] != "rabbitmq" {
		t.Errorf("存活指标查询未按 template 过滤：%v", got)
	}
	if got := store.labelsFor("rabbitmq_queue_depth"); got != nil {
		t.Errorf("摘要指标名已含模板前缀，不应再带标签过滤：%v", got)
	}
}

// TestMiddlewareTypeInstances_TemplateType 模板类型的通用实例接口。
func TestMiddlewareTypeInstances_TemplateType(t *testing.T) {
	a, store := templateAPI(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/middleware/rabbitmq/instances", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d（body=%s）", rec.Code, rec.Body.String())
	}
	// 关键：存活指标查询必须按 template 标签过滤（所有模板共用 template_target_up）
	if got := store.labelsFor("template_target_up"); got["template"] != "rabbitmq" {
		t.Fatalf("模板实例查询未按 template 标签过滤：%v", got)
	}

	var resp struct {
		Type      string `json:"type"`
		Label     string `json:"label"`
		Instances []struct {
			Instance string `json:"instance"`
			Node     string `json:"node"`
			Group    string `json:"group"`
			Up       bool   `json:"up"`
			Metrics  []struct {
				Key   string  `json:"key"`
				Label string  `json:"label"`
				Value float64 `json:"value"`
			} `json:"metrics"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if len(resp.Instances) != 2 {
		t.Fatalf("实例数 = %d，want 2（body=%s）", len(resp.Instances), rec.Body.String())
	}
	first := resp.Instances[0]
	if first.Instance != "mq-01:15692" || first.Group != "default" || !first.Up {
		t.Fatalf("首个实例不符：%+v", first)
	}
	if len(first.Metrics) != 1 || first.Metrics[0].Key != "rabbitmq_queue_depth" || first.Metrics[0].Value != 42 {
		t.Fatalf("实例摘要指标不符：%+v", first.Metrics)
	}
	// up=0 的实例仍应列出（离线实例必须可见）
	if resp.Instances[1].Up {
		t.Fatalf("第二个实例应为离线：%+v", resp.Instances[1])
	}
}

// TestMiddlewareTypeInstances_UnknownType 未知类型给出明确 404，而不是返回空列表
// （否则前端会显示「已配置但没有实例」，掩盖拼写错误）。
func TestMiddlewareTypeInstances_UnknownType(t *testing.T) {
	a, _ := templateAPI(t)
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/middleware/nosuchtype/instances", ""))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d，want 404（body=%s）", rec.Code, rec.Body.String())
	}
}

// TestMiddlewareOverview_WithoutRegistry 未注入注册表时退回内置 10 类（行为与改造前一致）。
func TestMiddlewareOverview_WithoutRegistry(t *testing.T) {
	a := scopeTestAPI(t)
	a.store = &seriesStore{}
	a.alerts = &alertStoreStub{}
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("middleware:read"), http.MethodGet, "/api/v1/middleware/overview", ""))

	var resp struct {
		Types []struct {
			Type string `json:"type"`
		} `json:"types"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if len(resp.Types) != 10 {
		t.Fatalf("未注入注册表时应只有内置 10 类，got %d", len(resp.Types))
	}
}
