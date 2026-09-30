package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// newRocketMQExporterServer 启动一个返回指定 exposition 文本的假 exporter。
func newRocketMQExporterServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collectRocketMQExporter(t *testing.T, addr, exporterURL string) ([]model.Metric, []model.RocketMQInstance) {
	t.Helper()
	c := NewRocketMQCollector("node-a", []model.RocketMQInstanceConfig{{
		Name: "dev-rocketmq", Addr: addr, ExporterURL: exporterURL,
	}})
	return c.CollectCtx(context.Background())
}

// RocketMQ exporter 原生不暴露 rocketmq_instance_up，成功拉到 RocketMQ 指标后
// 必须补出该存活指标，否则平台实例列表会一直显示离线（本次修复的回归守卫）。
func TestRocketMQCollectorExporterSynthesizesInstanceUp(t *testing.T) {
	srv := newRocketMQExporterServer(t, "rocketmq_broker_tps{cluster=\"DefaultCluster\"} 2\n")
	metrics, instances := collectRocketMQExporter(t, "127.0.0.1:9876", srv.URL+"/metrics")

	if len(instances) != 1 || !instances[0].Up {
		t.Fatalf("实例应在线，实际 %+v", instances)
	}
	if instances[0].Name != "dev-rocketmq" || instances[0].Group != "dev-rocketmq" || instances[0].Role != "nameserver" {
		t.Fatalf("实例元信息不符：%+v", instances[0])
	}

	var up *model.Metric
	for i := range metrics {
		if metrics[i].Name == "rocketmq_instance_up" {
			up = &metrics[i]
		}
	}
	if up == nil {
		t.Fatalf("缺少 rocketmq_instance_up 指标：%+v", metrics)
	}
	if up.Value != 1 {
		t.Fatalf("存活指标应为 1，实际 %v", up.Value)
	}
	// instance 标签经归一化（回环地址替换为本机真实 IP），与实例元信息保持一致即可。
	for key, want := range map[string]string{
		"node": "node-a", "instance": instances[0].Instance, "name": "dev-rocketmq",
		"group": "dev-rocketmq", "role": "nameserver",
	} {
		if got := up.Labels[key]; got != want {
			t.Errorf("标签 %s = %q，期望 %q", key, got, want)
		}
	}
	if !strings.HasSuffix(up.Labels["instance"], ":9876") {
		t.Errorf("instance 标签应保留端口，实际 %q", up.Labels["instance"])
	}
}

// exporter 自带 rocketmq_instance_up 时不得重复追加。
func TestRocketMQCollectorExporterKeepsNativeInstanceUp(t *testing.T) {
	srv := newRocketMQExporterServer(t, "rocketmq_instance_up{version=\"5.3.1\"} 0\n")
	metrics, instances := collectRocketMQExporter(t, "127.0.0.1:9876", srv.URL)

	count := 0
	for _, m := range metrics {
		if m.Name == "rocketmq_instance_up" {
			count++
			if m.Value != 0 {
				t.Errorf("原生指标值应保留为 0，实际 %v", m.Value)
			}
		}
	}
	if count != 1 {
		t.Fatalf("rocketmq_instance_up 应恰好 1 条，实际 %d：%+v", count, metrics)
	}
	if len(instances) != 1 || !instances[0].Up || instances[0].Version != "5.3.1" {
		t.Fatalf("拉取成功应视为在线并带版本：%+v", instances)
	}
}

// 拉取成功但没有 RocketMQ 指标（空响应、只有注释、只有无关指标）时不得伪造在线。
func TestRocketMQCollectorExporterEmptyExpositionStaysOffline(t *testing.T) {
	bodies := map[string]string{
		"空响应":  "",
		"仅注释":  "# HELP rocketmq_x example\n# TYPE rocketmq_x gauge\n",
		"无关指标": "process_cpu_seconds_total 0.5\n",
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := newRocketMQExporterServer(t, body)
			metrics, instances := collectRocketMQExporter(t, "127.0.0.1:9876", srv.URL)

			if len(instances) != 1 || instances[0].Up {
				t.Fatalf("应判离线，实际 %+v", instances)
			}
			for _, m := range metrics {
				if m.Name == "rocketmq_instance_up" {
					t.Fatalf("不应产生存活指标：%+v", m)
				}
			}
		})
	}
}

// exporter 不可达或返回非 2xx 时判离线，且不产生任何指标。
func TestRocketMQCollectorExporterRequestFailureIsOffline(t *testing.T) {
	cases := map[string]string{
		"端口不可达": "http://127.0.0.1:1/metrics",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "rocketmq_instance_up 1\n")
	}))
	t.Cleanup(srv.Close)
	cases["HTTP 500"] = srv.URL

	for name, url := range cases {
		t.Run(name, func(t *testing.T) {
			metrics, instances := collectRocketMQExporter(t, "127.0.0.1:9876", url)
			if len(instances) != 1 || instances[0].Up {
				t.Fatalf("应判离线，实际 %+v", instances)
			}
			if len(metrics) != 0 {
				t.Fatalf("不应产生指标，实际 %+v", metrics)
			}
		})
	}
}

// 只保留 rocketmq_ 前缀指标，并保留原始标签与取值。
func TestRocketMQCollectorExporterFiltersAndPreservesMetrics(t *testing.T) {
	body := "go_goroutines 5\nrocketmq_broker_tps{cluster=\"c1\"} 4.25\nrocketmq_producer_count 2\n"
	srv := newRocketMQExporterServer(t, body)
	metrics, _ := collectRocketMQExporter(t, "127.0.0.1:9876", srv.URL)

	byName := map[string]model.Metric{}
	for _, m := range metrics {
		byName[m.Name] = m
	}
	if _, ok := byName["go_goroutines"]; ok {
		t.Fatalf("非 rocketmq_ 前缀指标应被过滤：%+v", metrics)
	}
	tps, ok := byName["rocketmq_broker_tps"]
	if !ok || tps.Value != 4.25 || tps.Labels["cluster"] != "c1" {
		t.Fatalf("业务指标应保留原始取值与标签：%+v", tps)
	}
	if tps.Node != "node-a" || tps.Labels["node"] != "node-a" || !strings.HasSuffix(tps.Labels["instance"], ":9876") {
		t.Fatalf("业务指标应带节点与实例标签：%+v", tps.Labels)
	}
	if tps.Timestamp <= 0 {
		t.Fatalf("业务指标应有时间戳：%+v", tps)
	}
	if _, ok := byName["rocketmq_producer_count"]; !ok {
		t.Fatalf("应解析全部 rocketmq_ 指标：%+v", metrics)
	}
}

// 空配置或已取消的 ctx 不应发起采集。
func TestRocketMQCollectorExporterNoWork(t *testing.T) {
	if metrics, instances := NewRocketMQCollector("node-a", nil).CollectCtx(context.Background()); len(metrics) != 0 || len(instances) != 0 {
		t.Fatalf("空配置应无结果：metrics=%+v instances=%+v", metrics, instances)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewRocketMQCollector("node-a", []model.RocketMQInstanceConfig{{
		Name: "dev-rocketmq", Addr: "127.0.0.1:9876", ExporterURL: "http://127.0.0.1:1/metrics",
	}})
	if metrics, instances := c.CollectCtx(ctx); len(metrics) != 0 || len(instances) != 0 {
		t.Fatalf("ctx 已取消应跳过采集：metrics=%+v instances=%+v", metrics, instances)
	}
}
