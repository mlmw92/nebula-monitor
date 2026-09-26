package template

import (
	"strings"
	"testing"
)

// promoteLabel 把某个标签的取值提升为指标名的一部分（Nacos 的
// nacos_monitor{module="config",name="longPolling"} 就属于这种「一族多含义」形态）。
//
// 它的失败方式很隐蔽——不是报错，而是「指标名悄悄变了」或「写了却没生效」，
// 因此校验必须严：能静态发现的矛盾一律在启动期拒绝。
func TestValidate_PromoteLabel(t *testing.T) {
	valid := func() Config {
		c := validProm("mq")
		c.Rules.PromoteLabel = []PromoteLabelRule{{Match: "^mq_monitor$", Label: "name"}}
		return c
	}

	if err := ValidateAll([]Config{valid()}); err != nil {
		t.Fatalf("合法 promoteLabel 应通过校验，got %v", err)
	}

	cases := []struct {
		name string
		mut  func(c *Config)
		want string // 期望错误信息里出现的关键词
	}{
		{"match 为空", func(c *Config) { c.Rules.PromoteLabel[0].Match = "" }, "match 不能为空"},
		{"match 正则非法", func(c *Config) { c.Rules.PromoteLabel[0].Match = "^mq[[" }, "正则非法"},
		{"label 为空", func(c *Config) { c.Rules.PromoteLabel[0].Label = "" }, "label 不能为空"},
		{"label 非法键", func(c *Config) { c.Rules.PromoteLabel[0].Label = "a b" }, "非法"},
		// 提升保留标签等于允许伪造 node/instance 来源，必须拒绝
		{"label 为保留标签", func(c *Config) { c.Rules.PromoteLabel[0].Label = "instance" }, "保留标签"},
		// 与 unlabel 同时配置 = 提升后又被删掉，提升静默失效：这种自相矛盾要挡在启动期
		{"label 与 unlabel 冲突", func(c *Config) { c.Rules.Unlabel = []string{"name"} }, "unlabel"},
		{"match 重复", func(c *Config) {
			c.Rules.PromoteLabel = append(c.Rules.PromoteLabel, PromoteLabelRule{Match: "^mq_monitor$", Label: "module"})
		}, "重复"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := ValidateAll([]Config{c})
			if err == nil {
				t.Fatalf("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestValidate_PromoteLabelOnlyForPrometheus http-json / http-text 每个规则只产出一条序列、
// 且响应里的标签一概不参与（只有静态标签），提升无从谈起 → 静默忽略会让用户以为生效了，故拒绝。
func TestValidate_PromoteLabelOnlyForPrometheus(t *testing.T) {
	c := Config{
		ID:      "mq",
		Kind:    KindHTTPJSON,
		Targets: []Target{{Addr: "http://127.0.0.1:8080/metrics"}},
		Rules: Rules{
			Metrics:      []MetricRule{{Name: "depth", Path: "depth"}},
			PromoteLabel: []PromoteLabelRule{{Match: "^mq_monitor$", Label: "name"}},
		},
	}
	if err := ValidateAll([]Config{c}); err == nil {
		t.Fatal("非 prometheus-exporter 配置 promoteLabel 应被拒绝")
	}
}

// TestSanitizeMetricSegment 标签值来自外部系统，可能含指标名不允许的字符。
func TestSanitizeMetricSegment(t *testing.T) {
	cases := map[string]string{
		"configCount": "configCount",
		"longPolling": "longPolling",
		"get_config":  "get_config",
		"a/b":         "a_b",
		"x y":         "x_y",
		"1.2":         "1_2",
		"a-b:c":       "a_b_c",
		"  spaced  ":  "spaced",
		"":            "", // 空值无法表示 → 调用方保持样本原样
		"   ":         "",
		// 整段没有字母数字（中文取值、纯符号）→ 只剩一串下划线：既无信息又极易与别的取值撞名，
		// 视为无法表示（保持样本原样，而不是产出一个含义不明的指标名）
		"配置数": "",
		"___": "",
		"///": "",
	}
	for in, want := range cases {
		if got := SanitizeMetricSegment(in); got != want {
			t.Errorf("SanitizeMetricSegment(%q) = %q, want %q", in, got, want)
		}
	}
}
