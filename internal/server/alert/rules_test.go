package alert

import (
	"testing"

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
