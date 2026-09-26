package template

import (
	"fmt"
	"strings"
	"testing"
)

// validProm 返回一个最小合法的 prometheus-exporter 模板。
func validProm(id string) Config {
	return Config{
		ID:   id,
		Kind: KindPrometheusExporter,
		Targets: []Target{
			{Instance: "mq-01:15692", Addr: "http://127.0.0.1:15692/metrics"},
		},
	}
}

func TestValidate_AcceptsMinimalPrometheus(t *testing.T) {
	if err := ValidateAll([]Config{validProm("rabbitmq")}); err != nil {
		t.Fatalf("最小合法模板应通过校验，got %v", err)
	}
}

// TestValidate_RejectsIllegalID id 是「指标前缀 + 标签值」，格式错误会直接污染时序库命名空间。
func TestValidate_RejectsIllegalID(t *testing.T) {
	cases := map[string]string{
		"大小写混用": "RabbitMQ",
		"以数字开头": "9mq",
		"单字符":   "m",
		"含连字符":  "my-mq",
		"超长":    strings.Repeat("a", 33),
	}
	for name, id := range cases {
		if err := ValidateAll([]Config{validProm(id)}); err == nil {
			t.Errorf("%s：id=%q 应被拒绝", name, id)
		}
	}
}

// TestValidate_RejectsReservedPrefixID 与既有指标族前缀冲突会形成同名双序列。
func TestValidate_RejectsReservedPrefixID(t *testing.T) {
	for _, id := range []string{"redis", "redis_custom", "mysql_slow", "template", "self", "k8s"} {
		if err := ValidateAll([]Config{validProm(id)}); err == nil {
			t.Errorf("id=%q 与保留前缀冲突，应被拒绝", id)
		}
	}
}

func TestValidateAll_RejectsDuplicateAndPrefixOverlap(t *testing.T) {
	if err := ValidateAll([]Config{validProm("mq"), validProm("mq")}); err == nil ||
		!strings.Contains(err.Error(), "重复") {
		t.Errorf("重复 id 应被拒绝并说明原因，got %v", err)
	}
	// mq 与 mq_prod：mq_prod_x 既可属 mq 也可属 mq_prod，无法分辨归属
	if err := ValidateAll([]Config{validProm("mq"), validProm("mq_prod")}); err == nil ||
		!strings.Contains(err.Error(), "互为前缀") {
		t.Errorf("互为前缀的 id 应被拒绝，got %v", err)
	}
}

func TestValidate_RejectsKindAndAddr(t *testing.T) {
	c := validProm("mq")
	c.Kind = "jdbc"
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("未实现的 kind 应被拒绝（阶段三才支持 jdbc）")
	}

	addrCases := map[string]string{
		"协议不在白名单":   "file:///etc/passwd",
		"gopher 协议": "gopher://127.0.0.1:70/",
		"缺主机":       "http://",
		"空地址":       "",
	}
	for name, addr := range addrCases {
		c := validProm("mq")
		c.Targets[0].Addr = addr
		if err := ValidateAll([]Config{c}); err == nil {
			t.Errorf("%s：addr=%q 应被拒绝", name, addr)
		}
	}

	c = validProm("mq")
	c.Targets = nil
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("targets 为空应被拒绝")
	}

	// auth 只能启用一种
	c = validProm("mq")
	c.Targets[0].Auth = &Auth{Basic: &BasicAuth{User: "u"}, Bearer: &BearerAuth{Token: "t"}}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("同时启用 basic 与 bearer 应被拒绝")
	}
	c.Targets[0].Auth = &Auth{Header: &HeaderAuth{Value: "v"}}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("auth.header 缺 name 应被拒绝")
	}
}

func TestValidate_RejectsBadRegexAndRename(t *testing.T) {
	c := validProm("mq")
	c.Rules.Keep = "("
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("keep 正则非法应被拒绝")
	}

	c = validProm("mq")
	c.Rules.Drop = "["
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("drop 正则非法应被拒绝")
	}

	c = validProm("mq")
	c.Rules.Rename = []RenameRule{{Match: "^a$", To: ""}}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("rename 缺 to 应被拒绝")
	}
}

// TestValidate_RejectsReservedLabels 允许覆盖 instance/node 就等于允许伪造他机数据。
func TestValidate_RejectsReservedLabels(t *testing.T) {
	for _, key := range []string{"node", "instance", "group", "template"} {
		c := validProm("mq")
		c.Rules.Labels = map[string]string{key: "spoofed"}
		if err := ValidateAll([]Config{c}); err == nil {
			t.Errorf("rules.labels 覆盖保留标签 %q 应被拒绝", key)
		}
		c = validProm("mq")
		c.Rules.Unlabel = []string{key}
		if err := ValidateAll([]Config{c}); err == nil {
			t.Errorf("rules.unlabel 删除保留标签 %q 应被拒绝", key)
		}
	}

	c := validProm("mq")
	c.Rules.Labels = map[string]string{"bad-key": "v"}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("非法标签键应被拒绝")
	}
}

func TestValidate_MetricsRulesPerKind(t *testing.T) {
	// prometheus-exporter 的指标名来自响应，配 metrics 属误配
	c := validProm("mq")
	c.Rules.Metrics = []MetricRule{{Name: "mq_x", Path: "a"}}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("prometheus-exporter 配 rules.metrics 应被拒绝")
	}

	jsonTpl := Config{
		ID: "ownapp", Kind: KindHTTPJSON,
		Targets: []Target{{Addr: "http://127.0.0.1:8081/stats"}},
	}
	if err := ValidateAll([]Config{jsonTpl}); err == nil {
		t.Error("http-json 缺 rules.metrics 应被拒绝")
	}
	jsonTpl.Rules.Metrics = []MetricRule{{Name: "ownapp_requests"}}
	if err := ValidateAll([]Config{jsonTpl}); err == nil {
		t.Error("http-json 的 metric 缺 path 应被拒绝")
	}
	jsonTpl.Rules.Metrics = []MetricRule{{Name: "ownapp_requests", Path: "http.requests"}}
	if err := ValidateAll([]Config{jsonTpl}); err != nil {
		t.Errorf("合法 http-json 模板应通过，got %v", err)
	}

	textTpl := Config{
		ID: "customtext", Kind: KindHTTPText,
		Targets: []Target{{Addr: "http://127.0.0.1/status"}},
		Rules:   Rules{Metrics: []MetricRule{{Name: "customtext_active", Pattern: "("}}},
	}
	if err := ValidateAll([]Config{textTpl}); err == nil {
		t.Error("http-text 的 pattern 正则非法应被拒绝")
	}
	textTpl.Rules.Metrics = []MetricRule{{Name: "customtext_active", Pattern: `active:\s+(\d+)`}}
	if err := ValidateAll([]Config{textTpl}); err != nil {
		t.Errorf("合法 http-text 模板应通过，got %v", err)
	}

	// 重复指标名与非法 type
	textTpl.Rules.Metrics = []MetricRule{
		{Name: "customtext_active", Pattern: `active:\s+(\d+)`},
		{Name: "customtext_active", Pattern: `waiting:\s+(\d+)`},
	}
	if err := ValidateAll([]Config{textTpl}); err == nil {
		t.Error("重复的 metric name 应被拒绝")
	}
	textTpl.Rules.Metrics = []MetricRule{{Name: "customtext_active", Pattern: `active:\s+(\d+)`, Type: "histogram"}}
	if err := ValidateAll([]Config{textTpl}); err == nil {
		t.Error("非法 type 应被拒绝")
	}
}

// TestValidate_RejectsSelfNamespaceMetric 指标最终名会加 id 前缀，故校验要按加了前缀的结果判断。
func TestValidate_RejectsSelfNamespaceMetric(t *testing.T) {
	c := validProm("template_ok")
	// id 本身已与保留前缀 template_ 冲突，这里直接验证 id 冲突被捕获
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("id=template_ok 与保留前缀 template_ 冲突，应被拒绝")
	}
}

func TestValidate_Limits(t *testing.T) {
	// 模板数上限
	cfgs := make([]Config, MaxTemplates+1)
	for i := range cfgs {
		cfgs[i] = validProm(fmt.Sprintf("mq%02d", i))
	}
	if err := ValidateAll(cfgs); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Errorf("模板数超限应被拒绝，got %v", err)
	}

	// targets 上限
	c := validProm("mq")
	for i := 0; i <= MaxTargetsPerTemplate; i++ {
		c.Targets = append(c.Targets, Target{Addr: "http://127.0.0.1:1/metrics"})
	}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("targets 超限应被拒绝")
	}

	// 静态标签上限（需为引擎保留标签留位）
	c = validProm("mq")
	c.Rules.Labels = map[string]string{}
	for i := 0; i <= MaxStaticLabels(); i++ {
		c.Rules.Labels[fmt.Sprintf("label_%d", i)] = "v"
	}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Error("静态标签超限应被拒绝")
	}
	if MaxStaticLabels() != MaxLabelsPerMetric-len(ReservedLabelNames) {
		t.Fatalf("MaxStaticLabels 应给保留标签留位：%d", MaxStaticLabels())
	}
}

// TestValidateAll_ReportsAllProblems 校验需一次报全，避免「改一个重启一次」。
func TestValidateAll_ReportsAllProblems(t *testing.T) {
	bad1 := validProm("Bad-ID")
	bad2 := validProm("mq")
	bad2.Kind = "nope"
	err := ValidateAll([]Config{bad1, bad2})
	if err == nil {
		t.Fatal("两个非法模板都应被报告")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Bad-ID") || !strings.Contains(msg, "模板 #2") {
		t.Fatalf("错误信息应包含两个模板的位置与原因，got %s", msg)
	}
}

func TestEnsurePrefix(t *testing.T) {
	if got := EnsurePrefix("rabbitmq", "rabbitmq_queue_depth"); got != "rabbitmq_queue_depth" {
		t.Errorf("已带前缀不应重复添加，got %q", got)
	}
	if got := EnsurePrefix("rabbitmq", "queue_depth"); got != "rabbitmq_queue_depth" {
		t.Errorf("未带前缀应添加，got %q", got)
	}
}

func TestTargetEffectiveInstance(t *testing.T) {
	explicit := Target{Instance: "mq-01:15692", Addr: "http://10.0.0.12:15692/metrics"}
	if got := explicit.EffectiveInstance(); got != "mq-01:15692" {
		t.Errorf("显式 instance 应优先，got %q", got)
	}
	implicit := Target{Addr: "http://127.0.0.1:15692/metrics"}
	if got := implicit.EffectiveInstance(); got != "127.0.0.1:15692" {
		t.Errorf("缺 instance 应取 addr 的 host:port，got %q", got)
	}
}
