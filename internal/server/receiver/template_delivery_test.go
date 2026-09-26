package receiver

import (
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// fakeTemplates 是 TemplateProvider 的测试替身。
type fakeTemplates struct {
	list []template.Config
	rev  uint64
}

func (f *fakeTemplates) Snapshot() ([]template.Config, uint64) { return f.list, f.rev }

func tpl(id string, groups ...string) template.Config {
	return template.Config{
		ID:     id,
		Kind:   template.KindPrometheusExporter,
		Groups: groups,
		Targets: []template.Target{
			{Addr: "http://127.0.0.1:15692/metrics"},
		},
	}
}

func capsWith(rev uint64) *model.ClientCapability {
	return &model.ClientCapability{Templates: true, TemplateRevision: rev}
}

// TestAttachDeliveredTemplates_DeliversScoped 只下发该分组适用的模板。
func TestAttachDeliveredTemplates_DeliversScoped(t *testing.T) {
	provider := &fakeTemplates{
		rev: 3,
		list: []template.Config{
			tpl("rabbitmq", "mq", "default"),
			tpl("clickhouse", "db"),
			tpl("etcd", "k8s"),
		},
	}
	resp := map[string]interface{}{"status": "ok"}

	attachDeliveredTemplates(resp, capsWith(2), "mq", provider)

	if resp["templateRevision"] != uint64(3) {
		t.Fatalf("应带上版本号，got %v", resp["templateRevision"])
	}
	got, ok := resp["templates"].([]template.Config)
	if !ok {
		t.Fatalf("响应缺少 templates：%v", resp)
	}
	if len(got) != 1 || got[0].ID != "rabbitmq" {
		t.Fatalf("只应下发 mq 分组的模板，got %+v", got)
	}
}

// TestAttachDeliveredTemplates_SkipsWhenRevisionCurrent 版本号一致即不重发
// （模板可达数 KB，每轮心跳都带会随节点数成倍放大）。
func TestAttachDeliveredTemplates_SkipsWhenRevisionCurrent(t *testing.T) {
	provider := &fakeTemplates{rev: 3, list: []template.Config{tpl("rabbitmq", "mq")}}
	resp := map[string]interface{}{"status": "ok"}

	attachDeliveredTemplates(resp, capsWith(3), "mq", provider)

	if _, ok := resp["templates"]; ok {
		t.Fatalf("版本一致时不应下发：%v", resp)
	}
}

// TestAttachDeliveredTemplates_RequiresCapability 旧 Agent 未声明能力则不下发。
func TestAttachDeliveredTemplates_RequiresCapability(t *testing.T) {
	provider := &fakeTemplates{rev: 3, list: []template.Config{tpl("rabbitmq", "mq")}}

	cases := map[string]*model.ClientCapability{
		"未上报 capabilities": nil,
		"未声明 Templates":    {Defense: true},
	}
	for name, caps := range cases {
		resp := map[string]interface{}{"status": "ok"}
		attachDeliveredTemplates(resp, caps, "mq", provider)
		if _, ok := resp["templates"]; ok {
			t.Errorf("%s：不应下发", name)
		}
	}
}

// TestAttachDeliveredTemplates_NoProvider 未注入下发源时行为与阶段一一致（不下发）。
func TestAttachDeliveredTemplates_NoProvider(t *testing.T) {
	resp := map[string]interface{}{"status": "ok"}
	attachDeliveredTemplates(resp, capsWith(0), "mq", nil)
	if _, ok := resp["templates"]; ok {
		t.Fatalf("未注入 provider 时不应下发：%v", resp)
	}
}

// TestAttachDeliveredTemplates_EmptySetClears 分组内已无模板时下发空数组：
// Agent 必须收到「清空」这个事实，否则会一直跑着已被删除的模板。
func TestAttachDeliveredTemplates_EmptySetClears(t *testing.T) {
	provider := &fakeTemplates{rev: 7, list: []template.Config{tpl("rabbitmq", "mq")}}
	resp := map[string]interface{}{"status": "ok"}

	// Agent 仍停在 6：说明它还在跑旧集合（例如模板刚被删），此时必须下发「空」而非跳过
	attachDeliveredTemplates(resp, capsWith(6), "no-such-group", provider)

	got, ok := resp["templates"].([]template.Config)
	if !ok {
		t.Fatalf("应下发空集合而非跳过：%v", resp)
	}
	if len(got) != 0 {
		t.Fatalf("该分组无模板，应下发空数组，got %+v", got)
	}
	if resp["templateRevision"] != uint64(7) {
		t.Fatalf("空集合也要带版本号，否则 Agent 无法记录已同步到哪一版")
	}
}

// TestTemplatesForGroup 分组匹配的细节：完全相等才算命中（避免 "mq" 误配 "mq2"），容忍空格。
func TestTemplatesForGroup(t *testing.T) {
	list := []template.Config{
		tpl("a", "mq"),
		tpl("b", "mq2"),
		tpl("c", " mq "),
		tpl("d"),
	}
	got := templatesForGroup(list, "mq")
	if len(got) != 2 {
		t.Fatalf("应命中 a 与 c（容忍空格），got %+v", got)
	}
	if got[0].ID != "a" || got[1].ID != "c" {
		t.Fatalf("命中结果不符：%+v", got)
	}
	if len(templatesForGroup(list, "other")) != 0 {
		t.Fatal("无匹配分组应返回空集合")
	}
}

// TestSetTemplateStore 注入后下发源可用（保持 receiver 的接线契约）。
func TestSetTemplateStore(t *testing.T) {
	r := &Receiver{}
	if r.templates != nil {
		t.Fatal("默认不应有下发源（未注入时行为与阶段一一致）")
	}
	r.SetTemplateStore(&fakeTemplates{rev: 1})
	if r.templates == nil {
		t.Fatal("注入未生效")
	}
}
