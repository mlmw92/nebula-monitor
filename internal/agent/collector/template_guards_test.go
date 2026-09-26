package collector

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/template"
)

// 本机护栏在 Agent 侧的纵深防御：Server 侧已按节点能力过滤，但「机器自身的同意」不能只依赖中心——
// 一个被入侵或误配的 Server 不应该能把整批机器变成 root 执行器。

func newGuardedCollector(t *testing.T, g config.TemplateGuardsConfig) *Collector {
	t.Helper()
	cfg := config.CollectorToggle{CPU: true}
	return New("test-node", "default", nil, cfg,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		config.SecurityConfig{}, time.Second, nil, g)
}

func execTpl(id string, groups ...string) template.Config {
	return template.Config{
		ID:      id,
		Kind:    template.KindExec,
		Groups:  groups,
		Targets: []template.Target{{Command: "/bin/echo", Args: []string{"1"}}},
		Rules:   template.Rules{Metrics: []template.MetricRule{{Name: "v", Pattern: `(\d+)`}}},
	}
}

func httpTpl(id string, groups ...string) template.Config {
	return template.Config{
		ID:      id,
		Kind:    template.KindPrometheusExporter,
		Groups:  groups,
		Targets: []template.Target{{Addr: "http://127.0.0.1:15692/metrics"}},
	}
}

// TestApplyDelivered_DropsUnpermittedKinds 未放行的取数方式：即使 Server 下发也一律丢弃，
// 但**不影响其它模板**——一台机器不该因为别人的 exec 模板而丢掉自己所有的模板。
func TestApplyDelivered_DropsUnpermittedKinds(t *testing.T) {
	c := newGuardedCollector(t, config.TemplateGuardsConfig{}) // 三类全关
	if err := c.ApplyDelivered([]template.Config{execTpl("job", "default"), httpTpl("mq", "default")}, 1); err != nil {
		t.Fatalf("丢弃未放行模板后不应报错：%v", err)
	}
	got := c.Templates()
	if len(got) != 1 || got[0].ID != "mq" {
		t.Fatalf("只应保留网络取数模板，got %+v", got)
	}
	// 版本号照常推进：否则 Server 每轮都会重发整份模板（模板可达数 KB）。
	// 本机放行护栏需要改 agent.yaml 并重启 Agent，届时 revision 归 0，Server 自然重发——
	// 因此「之后才放行」的场景不会卡住。
	if c.TemplateRevision() != 1 {
		t.Fatalf("版本号应照常推进，got %d", c.TemplateRevision())
	}
}

// TestApplyDelivered_AppliesWhenGuardEnabled 放行后应完整应用（含护栏类模板）。
func TestApplyDelivered_AppliesWhenGuardEnabled(t *testing.T) {
	g := config.TemplateGuardsConfig{
		Exec: config.GuardRule{Enabled: true, Allow: []string{"/bin/echo"}},
	}
	c := newGuardedCollector(t, g)
	if err := c.ApplyDelivered([]template.Config{execTpl("job", "default"), httpTpl("mq", "default")}, 2); err != nil {
		t.Fatalf("got %v", err)
	}
	if got := c.Templates(); len(got) != 2 {
		t.Fatalf("放行后应全部应用，got %+v", got)
	}
}

// TestApplyDelivered_InvalidStillKeepsOldTemplates 丢弃未放行模板后仍要做整体校验：
// 校验失败继续保留旧模板（模板下发属运维便利功能，不能因此把采集打断）。
func TestApplyDelivered_InvalidStillKeepsOldTemplates(t *testing.T) {
	c := newGuardedCollector(t, config.TemplateGuardsConfig{})
	if err := c.ApplyDelivered([]template.Config{httpTpl("mq", "default")}, 1); err != nil {
		t.Fatalf("初次下发应成功：%v", err)
	}
	bad := httpTpl("bad", "default")
	bad.Targets[0].Addr = "ftp://127.0.0.1/x" // 非法协议
	if err := c.ApplyDelivered([]template.Config{bad}, 2); err == nil {
		t.Fatal("非法下发应报错")
	}
	if got := c.Templates(); len(got) != 1 || got[0].ID != "mq" {
		t.Fatalf("非法下发后应保留旧模板，got %+v", got)
	}
	if c.TemplateRevision() != 1 {
		t.Fatalf("版本号不应推进，got %d", c.TemplateRevision())
	}
}
