package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// promServer 启动一个返回固定响应的假端点，测试结束自动关闭。
func promServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// byName 按指标名分组，便于精确断言。
func byName(ms []model.Metric) map[string][]model.Metric {
	out := map[string][]model.Metric{}
	for _, m := range ms {
		out[m.Name] = append(out[m.Name], m)
	}
	return out
}

func TestTemplate_PrometheusMapping(t *testing.T) {
	srv := promServer(t, `
# HELP rabbitmq_queue_messages 队列消息数
rabbitmq_queue_messages{queue="orders",job="rabbitmq"} 42
rabbitmq_queue_messages{queue="pay",job="rabbitmq"} 7
rabbitmq_queue_consumers_total 3
rabbitmq_process_resident_memory_bytes 12345
go_goroutines 10
`)
	tpl := template.Config{
		ID:   "rabbitmq",
		Kind: template.KindPrometheusExporter,
		Targets: []template.Target{
			{Instance: "mq-01:15692", Addr: srv.URL + "/metrics"},
		},
		Rules: template.Rules{
			Keep:    "^rabbitmq_",
			Drop:    `_total$`,
			Rename:  []template.RenameRule{{Match: `^rabbitmq_queue_messages$`, To: "rabbitmq_queue_depth"}},
			Labels:  map[string]string{"cluster": "prod"},
			Unlabel: []string{"job"},
		},
	}

	out := NewTemplateRunner("test-node").CollectTemplate(context.Background(), tpl)
	got := byName(out)

	// 2 条改名后的队列深度 + 1 条内存 + 1 条 up；go_goroutines 被 keep 过滤，_total 被 drop
	if len(out) != 4 {
		t.Fatalf("期望 4 条指标，实际 %d 条：%v", len(out), namesOf(out))
	}
	depth := got["rabbitmq_queue_depth"]
	if len(depth) != 2 {
		t.Fatalf("期望 2 条 rabbitmq_queue_depth，实际 %d", len(depth))
	}
	if len(got["rabbitmq_process_resident_memory_bytes"]) != 1 {
		t.Errorf("未被 drop 的指标应保留：%v", namesOf(out))
	}
	if _, ok := got["rabbitmq_queue_consumers_total"]; ok {
		t.Error("drop 规则未生效")
	}
	if _, ok := got["go_goroutines"]; ok {
		t.Error("keep 规则未生效")
	}

	// 标签：响应自带 queue 保留，job 被 unlabel，静态 cluster 追加，引擎标签注入
	labels := depth[0].Labels
	if labels["queue"] == "" || labels["cluster"] != "prod" {
		t.Errorf("标签集不符：%v", labels)
	}
	if _, ok := labels["job"]; ok {
		t.Errorf("unlabel 未生效：%v", labels)
	}
	if labels["node"] != "test-node" || labels["template"] != "rabbitmq" || labels["instance"] != "mq-01:15692" {
		t.Errorf("引擎标签缺失或被覆盖：%v", labels)
	}

	up := got[template.UpMetricName]
	if len(up) != 1 || up[0].Value != 1 {
		t.Fatalf("期望 1 条 up=1，实际 %v", up)
	}
	if up[0].Labels["instance"] != "mq-01:15692" || up[0].Labels["template"] != "rabbitmq" {
		t.Errorf("up 指标标签不符：%v", up[0].Labels)
	}
}

// namesOf 便于失败信息阅读。
func namesOf(ms []model.Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

// TestTemplate_PrefixNotDoubled id 常与指标族同名，前缀不应重复添加。
func TestTemplate_PrefixNotDoubled(t *testing.T) {
	srv := promServer(t, "rabbitmq_a 1\nqueue_depth 2\n")
	tpl := template.Config{
		ID:      "rabbitmq",
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Addr: srv.URL + "/metrics"}},
	}
	got := byName(NewTemplateRunner("n").CollectTemplate(context.Background(), tpl))
	if len(got["rabbitmq_a"]) != 1 {
		t.Errorf("已带前缀的名字不应重复添加：%v", namesOf(flatten(got)))
	}
	if len(got["rabbitmq_queue_depth"]) != 1 {
		t.Errorf("未带前缀的名字应添加模板前缀：%v", namesOf(flatten(got)))
	}
}

func flatten(m map[string][]model.Metric) []model.Metric {
	var out []model.Metric
	for _, v := range m {
		out = append(out, v...)
	}
	return out
}

func TestTemplate_JSONMapping(t *testing.T) {
	srv := promServer(t, `{"http":{"requests":{"total":1234},"errors":{"total":"7"}},`+
		`"worker":{"queue":{"size":5}},"nodes":[{"value":1},{"value":2}],`+
		`"ok":true,"nul":null}`)
	tpl := template.Config{
		ID:      "ownapp",
		Kind:    template.KindHTTPJSON,
		Targets: []template.Target{{Instance: "app-01:8081", Addr: srv.URL + "/stats"}},
		Rules: template.Rules{Metrics: []template.MetricRule{
			{Name: "ownapp_requests_total", Path: "http.requests.total"},
			{Name: "ownapp_errors_total", Path: "http.errors.total"}, // 字符串数字
			{Name: "ownapp_queue_depth", Path: "worker.queue.size"},
			{Name: "ownapp_node2", Path: "nodes[1].value"}, // 数组下标
			{Name: "ownapp_ok", Path: "ok"},                // 布尔 → 1
			{Name: "ownapp_absent", Path: "nope.here"},     // 路径缺失 → 不产出
			{Name: "ownapp_null", Path: "nul"},             // null → 不产出
		}},
	}

	out := NewTemplateRunner("n").CollectTemplate(context.Background(), tpl)
	got := byName(out)

	if len(out) != 6 { // 5 条数据 + 1 条 up
		t.Fatalf("期望 6 条指标，实际 %d：%v", len(out), namesOf(out))
	}
	checks := map[string]float64{
		"ownapp_requests_total": 1234,
		"ownapp_errors_total":   7,
		"ownapp_queue_depth":    5,
		"ownapp_node2":          2,
		"ownapp_ok":             1,
	}
	for name, want := range checks {
		if len(got[name]) != 1 || got[name][0].Value != want {
			t.Errorf("%s 期望 %v，实际 %v", name, want, got[name])
		}
	}
	if _, ok := got["ownapp_absent"]; ok {
		t.Error("路径缺失不应产出指标")
	}
	if _, ok := got["ownapp_null"]; ok {
		t.Error("null 值不应产出指标")
	}
}

func TestTemplate_TextMapping(t *testing.T) {
	srv := promServer(t, "Active connections: 291 \nserver accepts handled requests\n 16630948 16630948 31070465 \nReading: 6 Writing: 179 Waiting: 106 \n")
	tpl := template.Config{
		ID:      "customtext",
		Kind:    template.KindHTTPText,
		Targets: []template.Target{{Addr: srv.URL + "/status"}},
		Rules: template.Rules{Metrics: []template.MetricRule{
			{Name: "customtext_active", Pattern: `Active connections:\s+(\d+)`},
			{Name: "customtext_waiting", Pattern: `Waiting:\s+(\d+)`},
			{Name: "customtext_absent", Pattern: `NoSuchField:\s+(\d+)`}, // 未命中 → 不产出
		}},
	}

	got := byName(NewTemplateRunner("n").CollectTemplate(context.Background(), tpl))
	if len(got["customtext_active"]) != 1 || got["customtext_active"][0].Value != 291 {
		t.Errorf("customtext_active 取值错误：%v", got["customtext_active"])
	}
	if len(got["customtext_waiting"]) != 1 || got["customtext_waiting"][0].Value != 106 {
		t.Errorf("customtext_waiting 取值错误：%v", got["customtext_waiting"])
	}
	if _, ok := got["customtext_absent"]; ok {
		t.Error("未命中的 pattern 不应产出指标")
	}
}

// TestTemplate_TargetFailureIsolationAndUpZero 失败语义：只产出该 target 的 up=0，
// 不产出其它指标（避免上一轮的值被误读为当前值），且不影响同模板的其它 target。
func TestTemplate_TargetFailureIsolationAndUpZero(t *testing.T) {
	alive := promServer(t, "tmpl_alive_metric 5\n")
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadAddr := dead.URL + "/metrics"
	dead.Close() // 关闭后连接被拒

	tpl := template.Config{
		ID:   "tmpl",
		Kind: template.KindPrometheusExporter,
		Targets: []template.Target{
			{Instance: "alive:1", Addr: alive.URL + "/metrics"},
			{Instance: "dead:2", Addr: deadAddr},
		},
	}
	out := NewTemplateRunner("n").CollectTemplate(context.Background(), tpl)

	perInstance := map[string][]model.Metric{}
	for _, m := range out {
		perInstance[m.Labels["instance"]] = append(perInstance[m.Labels["instance"]], m)
	}

	if len(perInstance["dead:2"]) != 1 {
		t.Fatalf("失败 target 应只产出 up 指标，实际 %v", namesOf(perInstance["dead:2"]))
	}
	if perInstance["dead:2"][0].Name != template.UpMetricName || perInstance["dead:2"][0].Value != 0 {
		t.Fatalf("失败 target 应产出 up=0，实际 %+v", perInstance["dead:2"][0])
	}
	if len(perInstance["alive:1"]) != 2 {
		t.Fatalf("正常 target 应不受影响（数据 + up），实际 %v", namesOf(perInstance["alive:1"]))
	}
}

func TestTemplate_Non200AndBadJSONAreUpZero(t *testing.T) {
	srv500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv500.Close)
	badJSON := promServer(t, "{not json")

	tpl := template.Config{
		ID:   "tmpl",
		Kind: template.KindHTTPJSON,
		Targets: []template.Target{
			{Instance: "e500", Addr: srv500.URL},
			{Instance: "badjson", Addr: badJSON.URL},
		},
		Rules: template.Rules{Metrics: []template.MetricRule{{Name: "tmpl_x", Path: "a"}}},
	}
	out := NewTemplateRunner("n").CollectTemplate(context.Background(), tpl)
	if len(out) != 2 {
		t.Fatalf("两个失败 target 各产出 1 条，实际 %d：%v", len(out), namesOf(out))
	}
	for _, m := range out {
		if m.Name != template.UpMetricName || m.Value != 0 {
			t.Errorf("非 200 / JSON 解析失败都应 up=0，实际 %+v", m)
		}
	}
}

// TestTemplate_MetricsCapTruncatesButKeepsUp 基数护栏：截断数据指标，但存活指标必须保留
// （它是「模板是否在产出」的唯一健康信号，被挤掉就只剩静默无数据）。
func TestTemplate_MetricsCapTruncatesButKeepsUp(t *testing.T) {
	var b strings.Builder
	for i := 0; i < template.MaxMetricsPerTemplate+50; i++ {
		fmt.Fprintf(&b, "tmpl_metric_%d %d\n", i, i)
	}
	srv := promServer(t, b.String())
	tpl := template.Config{
		ID:      "tmpl",
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Instance: "big:1", Addr: srv.URL + "/metrics"}},
	}

	out := NewTemplateRunner("n").CollectTemplate(context.Background(), tpl)
	if len(out) != template.MaxMetricsPerTemplate+1 {
		t.Fatalf("期望截断到 %d 条数据 + 1 条 up，实际 %d", template.MaxMetricsPerTemplate, len(out))
	}
	last := out[len(out)-1]
	if last.Name != template.UpMetricName || last.Value != 1 {
		t.Fatalf("截断后 up 应仍在末尾且为 1，实际 %+v", last)
	}
}

// TestTemplate_LabelGuards 标签护栏：保留标签不可被覆盖、超长值截断、标签数有上限。
func TestTemplate_LabelGuards(t *testing.T) {
	long := strings.Repeat("x", template.MaxLabelValueLen+100)
	var extra []string
	for i := 0; i < 30; i++ {
		extra = append(extra, fmt.Sprintf("l%d=\"v%d\"", i, i))
	}
	body := fmt.Sprintf("tmpl_metric{%s,long=\"%s\"} 1\n", strings.Join(extra, ","), long)
	srv := promServer(t, body)

	tpl := template.Config{
		ID:      "tmpl",
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Instance: "l:1", Addr: srv.URL + "/metrics"}},
		Rules: template.Rules{
			// 配置里写了保留标签（校验器会拦，这里是运行期兜底），不得覆盖引擎标签
			Labels: map[string]string{"node": "fake", "instance": "fake", "template": "fake"},
		},
	}
	out := NewTemplateRunner("test-node").CollectTemplate(context.Background(), tpl)
	var m model.Metric
	for _, x := range out {
		if x.Name == "tmpl_metric" {
			m = x
			break
		}
	}
	if m.Name == "" {
		t.Fatal("未产出 tmpl_metric")
	}
	if m.Labels["node"] != "test-node" || m.Labels["instance"] != "l:1" || m.Labels["template"] != "tmpl" {
		t.Fatalf("保留标签被配置覆盖：%v", m.Labels)
	}
	if len(m.Labels) > template.MaxLabelsPerMetric {
		t.Fatalf("标签数超过上限 %d：%d", template.MaxLabelsPerMetric, len(m.Labels))
	}
	if v := m.Labels["long"]; len(v) > template.MaxLabelValueLen {
		t.Fatalf("标签值未截断：%d 字节", len(v))
	}
}

// TestTemplate_EndpointUnreachableIsUpZero 不可达端点：只产出 up=0。
func TestTemplate_EndpointUnreachableIsUpZero(t *testing.T) {
	srv := promServer(t, "tmpl_x 1\n")
	addr := srv.URL + "/metrics"
	srv.Close()

	tpl := template.Config{
		ID:      "tmpl",
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Instance: "gone:1", Addr: addr}},
	}
	out := NewTemplateRunner("n").CollectTemplate(context.Background(), tpl)
	if len(out) != 1 || out[0].Name != template.UpMetricName || out[0].Value != 0 {
		t.Fatalf("不可达端点应只产出 up=0，实际 %+v", out)
	}
}

// TestCollectAll_TemplateTasksAppended 每个模板挂一个独立任务（隔离优先），
// 且任务名带模板 id 便于定位；模板为空时不追加任务（由 TestCollectAll_TaskListIsComplete 对照）。
func TestCollectAll_TemplateTasksAppended(t *testing.T) {
	base := newTestCollector(t, 0)
	var res Result
	var mu sync.Mutex
	baseCount := len(base.tasks(&res, &mu))

	tpls := []template.Config{
		{ID: "rabbitmq", Kind: template.KindPrometheusExporter, Targets: []template.Target{{Addr: "http://127.0.0.1:15692/metrics"}}},
		{ID: "clickhouse", Kind: template.KindPrometheusExporter, Targets: []template.Target{{Addr: "http://127.0.0.1:9363/metrics"}}},
	}
	cfg := config.CollectorToggle{CPU: true, Memory: true, Disk: true}
	c := New("test-node", "default", nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		config.SecurityConfig{}, 0, tpls)

	tasks := c.tasks(&res, &mu)
	if len(tasks) != baseCount+len(tpls) {
		t.Fatalf("任务数应为基础 %d + 模板 %d，实际 %d", baseCount, len(tpls), len(tasks))
	}
	names := map[string]bool{}
	for _, task := range tasks {
		names[task.name] = true
	}
	for _, tpl := range tpls {
		if !names["template:"+tpl.ID] {
			t.Errorf("缺少模板任务 template:%s", tpl.ID)
		}
	}
}

// TestCollector_ApplyDeliveredHotSwap Server 下发的模板无需重启即生效：
// tasks() 每轮重建任务表，换掉模板集后下一轮就有对应任务。
// 同时覆盖「本机未配任何模板的 Agent 也能接受下发」——执行器按需创建，不依赖启动时有模板。
func TestCollector_ApplyDeliveredHotSwap(t *testing.T) {
	c := newTestCollector(t, 0) // 构造时不带任何模板
	var res Result
	var mu sync.Mutex
	before := len(c.tasks(&res, &mu))

	delivered := template.Config{
		ID:      "rabbitmq",
		Kind:    template.KindPrometheusExporter,
		Groups:  []string{"mq"},
		Targets: []template.Target{{Addr: "http://127.0.0.1:15692/metrics"}},
	}
	if err := c.ApplyDelivered([]template.Config{delivered}, 5); err != nil {
		t.Fatalf("下发应成功：%v", err)
	}
	if got := c.TemplateRevision(); got != 5 {
		t.Fatalf("版本号 = %d，want 5", got)
	}

	tasks := c.tasks(&res, &mu)
	if len(tasks) != before+1 {
		t.Fatalf("下发后应多出一个模板任务：before=%d after=%d", before, len(tasks))
	}
	found := false
	for _, task := range tasks {
		if task.name == "template:rabbitmq" {
			found = true
		}
	}
	if !found {
		t.Fatal("缺少 template:rabbitmq 任务")
	}

	// 空集合下发 = 清空（该分组已无模板），任务表应回到基线
	if err := c.ApplyDelivered(nil, 6); err != nil {
		t.Fatalf("清空下发应成功：%v", err)
	}
	if got := len(c.tasks(&res, &mu)); got != before {
		t.Fatalf("清空后任务数应回到 %d，got %d", before, got)
	}
}

// TestCollector_ApplyDeliveredRejectsInvalid 非法下发必须**保留现有模板**：
// 模板下发属运维便利功能，绝不能因它把采集打断（宁可继续用旧配置）。
func TestCollector_ApplyDeliveredRejectsInvalid(t *testing.T) {
	c := newTestCollector(t, 0)
	good := template.Config{
		ID:      "rabbitmq",
		Kind:    template.KindPrometheusExporter,
		Groups:  []string{"mq"},
		Targets: []template.Target{{Addr: "http://127.0.0.1:15692/metrics"}},
	}
	if err := c.ApplyDelivered([]template.Config{good}, 3); err != nil {
		t.Fatalf("下发应成功：%v", err)
	}

	// id 与保留指标族前缀冲突（DSL 校验会拒）
	bad := good
	bad.ID = "redis"
	if err := c.ApplyDelivered([]template.Config{bad}, 4); err == nil {
		t.Fatal("非法模板应被拒绝")
	}
	if got := c.TemplateRevision(); got != 3 {
		t.Fatalf("被拒后版本号不应前进，got %d", got)
	}
	if list := c.Templates(); len(list) != 1 || list[0].ID != "rabbitmq" {
		t.Fatalf("应保留原有模板，got %+v", list)
	}
}

// TestCollectTemplate_CtxCanceledNoFetch 已取消的 ctx 直接返回 up=0，不发请求。
func TestCollectTemplate_CtxCanceledNoFetch(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	t.Cleanup(srv.Close)

	tpl := template.Config{
		ID:      "tmpl",
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Addr: srv.URL}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := NewTemplateRunner("n").CollectTemplate(ctx, tpl)
	if hit {
		t.Error("ctx 已取消时不应发起请求")
	}
	if len(out) != 1 || out[0].Value != 0 {
		t.Fatalf("ctx 已取消应产出 up=0，实际 %+v", out)
	}
}
