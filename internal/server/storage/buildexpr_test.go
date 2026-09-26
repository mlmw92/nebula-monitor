package storage

import (
	"strings"
	"testing"
)

// TestBuildExpr_Valid 合法入参下表达式的拼装（node 为空表示跨节点查询）。
func TestBuildExpr_Valid(t *testing.T) {
	cases := []struct {
		name   string
		node   string
		metric string
		labels map[string]string
		want   string
	}{
		{"仅指标名", "", "cpu_usage", nil, "cpu_usage"},
		{"带节点", "web-01", "cpu_usage", nil, `cpu_usage{node="web-01"}`},
		{"带单个标签", "", "disk_used", map[string]string{"device": "/dev/sda1"}, `disk_used{device="/dev/sda1"}`},
		{"保留指标名", "", "__name__", nil, "__name__"},
		{"含冒号指标名", "", "job:rate", nil, "job:rate"},
		{"标签值可含冒号", "web-01", "tcp_conn", map[string]string{"instance": "127.0.0.1:6379"}, `tcp_conn{node="web-01",instance="127.0.0.1:6379"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildExpr(tc.node, tc.metric, tc.labels)
			if err != nil {
				t.Fatalf("buildExpr: %v", err)
			}
			if got != tc.want {
				t.Fatalf("buildExpr = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBuildExpr_MultiLabelOrderAgnostic 多标签时选择器顺序由 map 迭代决定，
// 语义等价即可，因此逐个断言包含关系（不断言整体字符串）。
func TestBuildExpr_MultiLabelOrderAgnostic(t *testing.T) {
	got, err := buildExpr("web-01", "cpu_usage", map[string]string{"env": "prod", "cpu": "0"})
	if err != nil {
		t.Fatalf("buildExpr: %v", err)
	}
	for _, want := range []string{"cpu_usage{", `node="web-01"`, `env="prod"`, `cpu="0"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("表达式 %q 缺少 %q", got, want)
		}
	}
	if strings.Count(got, ",") != 2 {
		t.Fatalf("应有 3 个选择器: %q", got)
	}
}

// TestBuildExpr_RejectsInjection 注入 payload 必须被转义为「单个字符串字面量」，
// 绝不能拼出额外的选择器或闭合花括号。
//
// 断言方式：期望值由测试内**独立实现**的转义规则（`\` → `\\`、`"` → `\"`）拼出，
// 与生产实现逐字符比对——即注入内容整体落在引号内、结构未被破坏。
func TestBuildExpr_RejectsInjection(t *testing.T) {
	injection := `x"} or up{__name__=~".*"}`
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(injection)

	t.Run("node 注入被转义", func(t *testing.T) {
		got, err := buildExpr(injection, "up", nil)
		if err != nil {
			t.Fatalf("node 作为值应被转义而非报错: %v", err)
		}
		want := `up{node="` + escaped + `"}`
		if got != want {
			t.Fatalf("注入未被完整转义\n got = %q\nwant = %q", got, want)
		}
	})

	t.Run("标签值注入被转义", func(t *testing.T) {
		got, err := buildExpr("", "up", map[string]string{"env": injection})
		if err != nil {
			t.Fatalf("标签值应被转义而非报错: %v", err)
		}
		want := `up{env="` + escaped + `"}`
		if got != want {
			t.Fatalf("注入未被完整转义\n got = %q\nwant = %q", got, want)
		}
	})

	t.Run("指标名注入被拒绝", func(t *testing.T) {
		if _, err := buildExpr("", `up{__name__=~".*"}`, nil); err == nil {
			t.Fatal("非法指标名应报错")
		}
	})

	t.Run("标签名注入被拒绝", func(t *testing.T) {
		if _, err := buildExpr("", "up", map[string]string{`a="x",b`: "1"}); err == nil {
			t.Fatal("非法标签名应报错")
		}
	})
}

// TestBuildExpr_RejectsInvalidInput 白名单校验的边界（指标名允许冒号，标签名不允许）。
func TestBuildExpr_RejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		node   string
		metric string
		labels map[string]string
	}{
		{"空指标名", "", "", nil},
		{"指标名数字开头", "", "1cpu", nil},
		{"指标名含空格", "", "cpu usage", nil},
		{"指标名含花括号", "", "cpu{", nil},
		{"指标名含连字符", "", "cpu-usage", nil},
		{"空标签名", "", "cpu_usage", map[string]string{"": "1"}},
		{"标签名数字开头", "", "cpu_usage", map[string]string{"0cpu": "1"}},
		{"标签名含冒号", "", "cpu_usage", map[string]string{"a:b": "1"}},
		{"标签名含连字符", "", "cpu_usage", map[string]string{"a-b": "1"}},
		{"标签值为空", "", "cpu_usage", map[string]string{"env": ""}},
		{"标签值含控制字符", "", "cpu_usage", map[string]string{"env": "a\x01b"}},
		{"node 含控制字符", "web\x01", "cpu_usage", nil},
		{"node 超长", strings.Repeat("x", 4097), "cpu_usage", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildExpr(tc.node, tc.metric, tc.labels); err == nil {
				t.Fatal("应报错但通过了")
			}
		})
	}
}

// TestBuildExpr_AllowsTabInValue 制表符是合法标签值字符（仅排除其它控制字符）。
func TestBuildExpr_AllowsTabInValue(t *testing.T) {
	if _, err := buildExpr("", "cpu_usage", map[string]string{"env": "a\tb"}); err != nil {
		t.Fatalf("制表符应被允许: %v", err)
	}
}

// TestQuotePromQLValue 反斜杠与双引号必须转义，其余字符原样保留。
func TestQuotePromQLValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain`, `"plain"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{`\"`, `"\\\""`},
		{`中文/值:1`, `"中文/值:1"`},
	}
	for _, tc := range cases {
		if got := quotePromQLValue(tc.in); got != tc.want {
			t.Errorf("quotePromQLValue(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestIsValidMetricNameAndLabelName 白名单字符集差异：指标名允许冒号，标签名不允许。
func TestIsValidMetricNameAndLabelName(t *testing.T) {
	for _, s := range []string{"cpu_usage", "__name__", ":leading", "trailing:", "a1", "A_Z9"} {
		if !isValidMetricName(s) {
			t.Errorf("isValidMetricName(%q) 应为合法", s)
		}
	}
	for _, s := range []string{"", "1a", "a-b", "a b", "a.b", "a{b", "a}b", "a,b"} {
		if isValidMetricName(s) {
			t.Errorf("isValidMetricName(%q) 应为非法", s)
		}
	}

	for _, s := range []string{"node", "_private", "a1"} {
		if !isValidLabelName(s) {
			t.Errorf("isValidLabelName(%q) 应为合法", s)
		}
	}
	for _, s := range []string{"", "1a", "a:b", "a-b", "a b"} {
		if isValidLabelName(s) {
			t.Errorf("isValidLabelName(%q) 应为非法", s)
		}
	}
}
