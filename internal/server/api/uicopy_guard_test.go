package api

import (
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/metrics"
	"github.com/nebula/monitor/internal/server/ops"
)

// 界面文案守则守卫：下面的目录（指标字典 / 告警规则模板 / 下行动作目录）的标题与说明
// 会以**纯文本**渲染在界面上——前端用 {{ }} 插值，不解析 Markdown。因此写进去的
// Markdown 强调标记会原样显示成 `**文字**`：用户看到的是星号，而不是加粗或高亮。
//
// 为什么值得单独立一条守卫：这类缺陷不影响任何功能、不会让既有测试变红、也不报错，
// 只有真的打开页面才看得见（本仓库就出现过：动作说明里的「**默认不可用**」、
// 指标说明里的「越**小**越糟」、模板说明里的「**方向是越小越糟**」）。
// 中文语境下要强调，用书名号/引号（「」）或直接把话写清楚即可。
//
// 不在校验范围：邮件 / 钉钉 / 飞书 / 企业微信的告警正文——那条链路上 Markdown 是
// **会被渲染**的（见 alert/notifier.go 的 `### 监控告警` 与 `**级别**`）。
func TestCatalogCopyHasNoMarkdownMarkup(t *testing.T) {
	t.Run("指标字典", func(t *testing.T) {
		for _, m := range metrics.List() {
			checkNoMarkup(t, "指标 "+m.Name, m.Title, m.Unit, m.Desc)
		}
	})

	t.Run("告警规则模板", func(t *testing.T) {
		for _, tpl := range alert.DefaultTemplates() {
			checkNoMarkup(t, "模板 "+tpl.Name, tpl.Name, tpl.TemplateGroup, tpl.Desc)
		}
	})

	t.Run("下行动作目录", func(t *testing.T) {
		for _, a := range ops.Catalog() {
			checkNoMarkup(t, "动作 "+a.Kind, a.Kind, a.Title, a.Group, a.Desc)
			for _, p := range a.Params {
				checkNoMarkup(t, "动作 "+a.Kind+" 的参数 "+p.Name, p.Title, p.Desc, p.Example)
			}
		}
	})
}

func checkNoMarkup(t *testing.T, where string, fields ...string) {
	t.Helper()
	for _, s := range fields {
		if strings.Contains(s, "*") {
			t.Errorf("%s 的界面文案含 Markdown 标记（会原样显示成星号）：%q", where, s)
		}
	}
}
