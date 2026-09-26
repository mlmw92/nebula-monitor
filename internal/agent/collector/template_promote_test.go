package collector

import (
	"context"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// 本文件针对 promoteLabel（把标签值提升为指标名）。
//
// 动机（取自真实形态）：Nacos 把「不同含义」全塞进一个指标族，靠 name 标签区分——
// nacos_monitor{module="config",name="configCount"} / {name="getConfig"} / {name="longPolling"}……
// 不提升的话，它们在「指标浏览」里全挤在 nacos_monitor 一个名字下：无法分别看趋势，也无法按含义配告警。

const nacosSample = `# HELP nacos_monitor Nacos monitor metrics
# TYPE nacos_monitor gauge
nacos_monitor{module="config",name="configCount"} 3
nacos_monitor{module="config",name="getConfig"} 120
nacos_monitor{module="naming",name="longPolling"} 2
nacos_monitor{module="naming"} 7
nacos_monitor_created 1700000000
nacos_jvm_memory_used_bytes{area="heap"} 5.1e+08
jvm_gc_pause_seconds_sum 0.2
`

// promoteRun 用给定模板 id 与规则跑一次采集。
// id 会作为指标名前缀（EnsurePrefix 幂等：样本已是 <id>_ 开头时不重复添加），
// 所以断言里必须用「id + 前缀后的名字」。
func promoteRun(t *testing.T, id, body string, rules template.Rules) []model.Metric {
	t.Helper()
	cfg := template.Config{
		ID:      id,
		Kind:    template.KindPrometheusExporter,
		Targets: []template.Target{{Addr: promServer(t, body).URL}},
		Rules:   rules,
	}
	if err := template.ValidateAll([]template.Config{cfg}); err != nil {
		t.Fatalf("测试配置不合法：%v", err)
	}
	return NewTemplateRunner("test-node").CollectTemplate(context.Background(), cfg)
}

// TestPromoteLabel_NamesByLabelValue 核心行为：按标签取值拆成各自独立的指标名，
// 并删除该标签（它已经进了指标名，留着会让同一含义出现两处）。
func TestPromoteLabel_NamesByLabelValue(t *testing.T) {
	rules := template.Rules{
		Keep:         "^nacos_",
		Drop:         "_created$",
		PromoteLabel: []template.PromoteLabelRule{{Match: "^nacos_monitor$", Label: "name"}},
	}
	got := byName(promoteRun(t, "nacos", nacosSample, rules))

	for name, want := range map[string]float64{
		"nacos_monitor_configCount": 3,
		"nacos_monitor_getConfig":   120,
		"nacos_monitor_longPolling": 2,
	} {
		if len(got[name]) != 1 {
			t.Fatalf("应产出 %s 一条，实际 %v", name, namesOf(nacosMonitorMetrics(got)))
		}
		if got[name][0].Value != want {
			t.Errorf("%s 取值 = %v，want %v", name, got[name][0].Value, want)
		}
	}

	// 没有 name 标签的样本：保持原名（不提升也不丢数据）
	if len(got["nacos_monitor"]) != 1 {
		t.Errorf("无 name 标签的样本应保持原名 nacos_monitor，实际：%v", namesOf(nacosMonitorMetrics(got)))
	}

	// 其它标签保留（module 是定位维度的有效信息）
	lbl := got["nacos_monitor_longPolling"][0].Labels
	if lbl["module"] != "naming" {
		t.Errorf("module 标签应保留，got %v", lbl)
	}
	if _, ok := lbl["name"]; ok {
		t.Errorf("已提升的标签应从标签集中移除，got %v", lbl)
	}
	// 与提升无关的指标不受影响
	if len(got["nacos_jvm_memory_used_bytes"]) != 1 {
		t.Errorf("不匹配规则的指标应原样保留：%v", namesOf(nacosMonitorMetrics(got)))
	}
	assertNoDuplicateSeries(t, nacosMonitorMetrics(got))
}

// TestPromoteLabel_SanitizesLabelValue 标签值来自外部系统，可能含指标名不允许的字符。
func TestPromoteLabel_SanitizesLabelValue(t *testing.T) {
	body := "mq_value{k=\"a/b\"} 1\nmq_value{k=\"x y\"} 2\nmq_value{k=\"1.2\"} 3\n"
	got := byName(promoteRun(t, "mq", body, template.Rules{
		PromoteLabel: []template.PromoteLabelRule{{Match: "^mq_value$", Label: "k"}},
	}))
	for _, name := range []string{"mq_value_a_b", "mq_value_x_y", "mq_value_1_2"} {
		if len(got[name]) != 1 {
			t.Fatalf("应产出合法名 %s，实际 %v", name, namesOf(got["mq_value"]))
		}
	}
}

// TestPromoteLabel_UnrepresentableValueKeepsSample 取值无法表示（空）时不提升、也不丢数据：
// 宁可留着一个未拆分的样本，也不能把它变成非法指标名或直接吞掉。
func TestPromoteLabel_UnrepresentableValueKeepsSample(t *testing.T) {
	body := "mq_value 5\nmq_value{k=\"\"} 6\n"
	got := byName(promoteRun(t, "mq", body, template.Rules{
		PromoteLabel: []template.PromoteLabelRule{{Match: "^mq_value$", Label: "k"}},
	}))
	ms := got["mq_value"]
	if len(ms) != 2 {
		t.Fatalf("两个样本都应保留（标签不同，不构成重复序列），got %d：%+v", len(ms), ms)
	}
	if ms[1].Labels["k"] != "" {
		t.Errorf("未提升时标签原样保留，got %v", ms[1].Labels)
	}
}

// TestPromoteLabel_FirstMatchWins 多条规则都匹配时首条生效（与 rename / aggregate 一致），
// 否则「哪条生效」取决于声明顺序之外的东西，无法排查。
func TestPromoteLabel_FirstMatchWins(t *testing.T) {
	body := "mq_value{k1=\"a\",k2=\"b\"} 1\n"
	got := byName(promoteRun(t, "mq", body, template.Rules{
		PromoteLabel: []template.PromoteLabelRule{
			{Match: "^mq_.*$", Label: "k1"},
			{Match: "^mq_value$", Label: "k2"},
		},
	}))
	if len(got["mq_value_a"]) != 1 {
		t.Fatalf("应按首条规则用 k1 提升，实际 %v", namesOf(got["mq_value"]))
	}
	if got["mq_value_a"][0].Labels["k2"] != "b" {
		t.Errorf("未提升的标签应保留，got %v", got["mq_value_a"][0].Labels)
	}
}

// TestPromoteLabel_SanitizeCollisionIsGuarded 净化后撞名（a/b 与 a.b 都变成 a_b）时，
// 必须由冲突护栏兜住：只保留一条并告警，绝不写入互相覆盖的多条。
func TestPromoteLabel_SanitizeCollisionIsGuarded(t *testing.T) {
	body := "mq_value{k=\"a/b\"} 1\nmq_value{k=\"a.b\"} 2\n"
	got := byName(promoteRun(t, "mq", body, template.Rules{
		PromoteLabel: []template.PromoteLabelRule{{Match: "^mq_value$", Label: "k"}},
	}))
	ms := got["mq_value_a_b"]
	if len(ms) != 1 {
		t.Fatalf("撞名后只应保留一条，got %d", len(ms))
	}
	if ms[0].Value != 1 {
		t.Fatalf("应稳定保留第一条（1），got %v", ms[0].Value)
	}
}

// nacosMonitorMetrics 取全部原始产出（便于断言失败时看清实际名字）。
func nacosMonitorMetrics(got map[string][]model.Metric) []model.Metric {
	out := make([]model.Metric, 0, len(got))
	for _, ms := range got {
		out = append(out, ms...)
	}
	return out
}
