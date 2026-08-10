package alert

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
)

// TestLoadRulesCanonical 校验规则文件采用顶层序列格式（与持久化格式一致）。
func TestLoadRulesCanonical(t *testing.T) {
	data := []byte(`
- name: "CPU 高使用率"
  metric: cpu_usage
  operator: ">"
  threshold: 85
  for: "1m"
  severity: warning
  enabled: true
`)
	var list []model.AlertRule
	if err := yaml.Unmarshal(data, &list); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 rule, got %d", len(list))
	}
	r := list[0]
	if r.Metric != "cpu_usage" || r.Operator != ">" || r.Threshold != 85 ||
		r.For != "1m" || r.Severity != model.Severity("warning") || !r.Enabled {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestParseHHMM(t *testing.T) {
	tests := map[string]int{
		"00:00": 0,
		"02:30": 150,
		"23:59": 1439,
		"2:30":  -1,
		"24:00": -1,
		"12:60": -1,
		"12:xx": -1,
	}
	for input, want := range tests {
		if got := parseHHMM(input); got != want {
			t.Errorf("parseHHMM(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestInQuietPeriodCrossDay(t *testing.T) {
	// 2026-08-10 23:00（周一）应命中周一 22:00-06:00。
	now := time.Date(2026, time.August, 10, 23, 0, 0, 0, time.Local).UnixMilli()
	if !inQuietPeriod([]model.QuietPeriod{{Days: []int{1}, Start: "22:00", End: "06:00"}}, now) {
		t.Fatal("expected cross-day quiet period to match")
	}
}

func TestValidateRuleRejectsInvalidInput(t *testing.T) {
	base := model.AlertRule{Name: "CPU", Metric: "cpu_usage", Operator: ">", Severity: model.SeverityWarning, For: "5m"}
	if err := ValidateRule(base); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	bad := base
	bad.For = "not-a-duration"
	if err := ValidateRule(bad); err == nil {
		t.Fatal("invalid duration accepted")
	}
	bad = base
	bad.QuietPeriods = []model.QuietPeriod{{Start: "02:00", End: "06:00"}}
	if err := ValidateRule(bad); err != nil {
		t.Fatalf("valid quiet period rejected: %v", err)
	}
}
