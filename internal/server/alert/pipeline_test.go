package alert

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func newPipelineTestStore(t *testing.T) *PipelineStore {
	t.Helper()
	return NewPipelineStore(filepath.Join(t.TempDir(), "alert_pipeline.yaml"))
}

func pipelineTestEvent() model.AlertEvent {
	return model.AlertEvent{
		ID:       "evt-1",
		RuleID:   "rule-cpu",
		RuleName: "CPU 过高",
		Severity: model.SeverityCritical,
		State:    model.AlertStateFiring,
		Node:     "web-01",
		Instance: "127.0.0.1:3306",
		Metric:   "cpu_usage",
		Value:    95.5,
		Message:  "原始描述",
		Labels:   map[string]string{"env": "prod", "region": "cn-north"},
	}
}

// TestPipelineApply_RelabelOps 覆盖 rename / delete / replace / set 四种标签操作。
func TestPipelineApply_RelabelOps(t *testing.T) {
	s := newPipelineTestStore(t)
	cfg := PipelineConfig{
		Relabels: []RelabelRule{
			{Op: "rename", Source: "node", Target: "host_name"},
			{Op: "delete", Source: "env"},
			{Op: "replace", Source: "region", Pattern: "^cn-(.*)$", Replace: "CN-$1"},
			{Op: "set", Target: "team", Value: "sre"},
		},
	}
	if err := s.Save(cfg); err != nil {
		t.Fatalf("保存管道配置失败: %v", err)
	}

	got := s.Apply(pipelineTestEvent()).Labels

	if _, ok := got["node"]; ok {
		t.Error("rename 后源标签 node 仍存在")
	}
	if got["host_name"] != "web-01" {
		t.Errorf("rename 结果错误（应为派生的节点名）: host_name=%q", got["host_name"])
	}
	if _, ok := got["env"]; ok {
		t.Error("delete 未删除 env")
	}
	if got["region"] != "CN-north" {
		t.Errorf("replace 结果错误: region=%q", got["region"])
	}
	if got["team"] != "sre" {
		t.Errorf("set 结果错误: team=%q", got["team"])
	}
}

// TestPipelineApply_EnrichWithCondition 覆盖 enrich 的条件注入。
func TestPipelineApply_EnrichWithCondition(t *testing.T) {
	s := newPipelineTestStore(t)
	cfg := PipelineConfig{
		Enrich: []EnrichRule{
			{Target: "owner", Value: "db-team", When: &MatchSet{Match: map[string]string{"env": "prod"}}},
			{Target: "tier", Value: "core"},
		},
	}
	if err := s.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	ev := pipelineTestEvent()
	got := s.Apply(ev).Labels
	if got["owner"] != "db-team" {
		t.Errorf("条件 enrich 未生效: owner=%q", got["owner"])
	}
	if got["tier"] != "core" {
		t.Errorf("无条件 enrich 未生效: tier=%q", got["tier"])
	}

	// 条件不满足时不应注入
	ev.Labels = map[string]string{"env": "dev"}
	if _, ok := s.Apply(ev).Labels["owner"]; ok {
		t.Error("条件不满足时仍注入了 owner")
	}
}

// TestPipelineApply_DoesNotMutateInput 变换必须作用于副本，不能污染调用方事件。
func TestPipelineApply_DoesNotMutateInput(t *testing.T) {
	s := newPipelineTestStore(t)
	if err := s.Save(PipelineConfig{Relabels: []RelabelRule{{Op: "set", Target: "team", Value: "sre"}}}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	ev := pipelineTestEvent()
	_ = s.Apply(ev)
	if _, ok := ev.Labels["team"]; ok {
		t.Fatal("Apply 修改了入参事件的标签（存在污染风险）")
	}
}

// TestPipelineRenderMessage_ChannelPreferred 渠道专属模板优先于通用模板。
func TestPipelineRenderMessage_ChannelPreferred(t *testing.T) {
	s := newPipelineTestStore(t)
	cfg := PipelineConfig{
		Templates: []TemplateRule{
			{Name: "通用", Template: "通用:{{.RuleName}}/{{.Node}}"},
			{Name: "钉钉", Channel: "dingtalk", Template: "钉钉:{{.RuleName}} {{.Severity}} {{.Value}}"},
		},
	}
	if err := s.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	ev := pipelineTestEvent()
	if got := s.RenderMessage(ev, "dingtalk"); got != "钉钉:CPU 过高 critical 95.5" && !strings.HasPrefix(got, "钉钉:CPU 过高") {
		t.Errorf("渠道模板未生效: %q", got)
	}
	if got := s.RenderMessage(ev, "email"); got != "通用:CPU 过高/web-01" {
		t.Errorf("通用模板未生效: %q", got)
	}
}

// TestPipelineRenderMessage_SeverityAndRuleFilter 级别 / 规则过滤生效，未命中时保留原描述。
func TestPipelineRenderMessage_SeverityAndRuleFilter(t *testing.T) {
	s := newPipelineTestStore(t)
	cfg := PipelineConfig{
		Templates: []TemplateRule{
			{Name: "仅紧急", Severity: []string{"critical"}, RuleIDs: []string{"rule-cpu"}, Template: "紧急:{{.Node}}"},
		},
	}
	if err := s.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	ev := pipelineTestEvent()
	if got := s.RenderMessage(ev, "email"); got != "紧急:web-01" {
		t.Errorf("级别+规则过滤未命中: %q", got)
	}

	ev.Severity = model.SeverityWarning
	if got := s.RenderMessage(ev, "email"); got != "原始描述" {
		t.Errorf("级别不匹配时应保留原描述，实际 %q", got)
	}

	ev.Severity = model.SeverityCritical
	ev.RuleID = "rule-mem"
	if got := s.RenderMessage(ev, "email"); got != "原始描述" {
		t.Errorf("规则不匹配时应保留原描述，实际 %q", got)
	}
}

// TestPipelineEmpty_Noop 空管道必须与改造前行为完全一致（回归保护）。
func TestPipelineEmpty_Noop(t *testing.T) {
	s := newPipelineTestStore(t)
	ev := pipelineTestEvent()

	got := s.Apply(ev)
	if len(got.Labels) != len(ev.Labels) {
		t.Errorf("空管道不应改变标签数: 期望 %d，实际 %d", len(ev.Labels), len(got.Labels))
	}
	for k, v := range ev.Labels {
		if got.Labels[k] != v {
			t.Errorf("空管道改变了标签 %s", k)
		}
	}
	if msg := s.RenderMessage(ev, "dingtalk"); msg != ev.Message {
		t.Errorf("空管道应返回原描述: %q", msg)
	}
}

// TestPipelineValidate_RejectsBadConfig 非法正则 / 模板 / 操作符必须在保存前被拒绝。
func TestPipelineValidate_RejectsBadConfig(t *testing.T) {
	s := newPipelineTestStore(t)

	cases := []struct {
		name string
		cfg  PipelineConfig
	}{
		{"非法正则", PipelineConfig{Relabels: []RelabelRule{{Op: "replace", Source: "a", Pattern: "([", Replace: "x"}}}},
		{"未知操作符", PipelineConfig{Relabels: []RelabelRule{{Op: "explode", Source: "a"}}}},
		{"rename 缺目标", PipelineConfig{Relabels: []RelabelRule{{Op: "rename", Source: "a"}}}},
		{"replace 缺源", PipelineConfig{Relabels: []RelabelRule{{Op: "replace", Pattern: "a", Replace: "b"}}}},
		{"set 缺目标", PipelineConfig{Relabels: []RelabelRule{{Op: "set", Value: "v"}}}},
		{"enrich 缺目标", PipelineConfig{Enrich: []EnrichRule{{Value: "v"}}}},
		{"非法模板", PipelineConfig{Templates: []TemplateRule{{Name: "x", Template: "{{.RuleName"}}}},
		{"模板缺内容", PipelineConfig{Templates: []TemplateRule{{Name: "x"}}}},
		{"when 非法正则", PipelineConfig{Relabels: []RelabelRule{{Op: "set", Target: "t", Value: "v", When: &MatchSet{MatchRegexp: map[string]string{"a": "(["}}}}}},
	}
	for _, c := range cases {
		if err := s.Validate(c.cfg); err == nil {
			t.Errorf("%s：期望校验失败，实际通过", c.name)
		}
		if err := s.Save(c.cfg); err == nil {
			t.Errorf("%s：Save 应拒绝非法配置", c.name)
		}
	}
}

// TestPipelineSave_HotReloadAndPersist 保存后立即热生效，且可从文件重新加载。
func TestPipelineSave_HotReloadAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alert_pipeline.yaml")
	s := NewPipelineStore(path)

	if err := s.Save(PipelineConfig{Relabels: []RelabelRule{{Op: "set", Target: "v", Value: "1"}}}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if s.Apply(pipelineTestEvent()).Labels["v"] != "1" {
		t.Fatal("保存后未热生效")
	}

	reloaded := NewPipelineStore(path)
	if reloaded.Apply(pipelineTestEvent()).Labels["v"] != "1" {
		t.Fatal("重新加载后配置丢失")
	}
	reloaded.Save(PipelineConfig{Relabels: []RelabelRule{{Op: "set", Target: "v", Value: "2"}}})
	if s.Apply(pipelineTestEvent()).Labels["v"] != "1" {
		t.Error("旧的 store 实例意外被新实例影响")
	}
}
