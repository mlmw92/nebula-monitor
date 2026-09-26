package template

import (
	"net/url"
	"strings"
	"testing"
)

// TestPresetsAreValid 每个预设都必须通过 DSL 校验——预设是「我们替用户写好的配置」，
// 若它自己不合法，用户点一下就会撞上校验错误，是把成本转嫁给了用户。
//
// 注意预设的 groups 是空的：Server 侧要求「下发的模板必须声明生效分组」，
// 但预设只是待填模板，分组由用户在自己环境里选择（校验器不强制 groups，见 Config.Groups 注释）。
func TestPresetsAreValid(t *testing.T) {
	for _, p := range Presets() {
		if err := ValidateAll([]Config{p.Config}); err != nil {
			t.Errorf("预设 %s 不合法：%v", p.ID, err)
		}
	}
}

// TestPresetsAreCompatibleTogether 预设会被一起存在 Server 上，必须能共存：
// id 唯一、互不为前缀、且不与既有指标族前缀冲突（否则指标名无法分辨来源）。
func TestPresetsAreCompatibleTogether(t *testing.T) {
	all := make([]Config, 0, len(Presets()))
	for _, p := range Presets() {
		all = append(all, p.Config)
	}
	if err := ValidateAll(all); err != nil {
		t.Fatalf("预设集合不合法（id 冲突或互为前缀）：%v", err)
	}
	if len(all) < 4 {
		t.Fatalf("预设数量 %d 偏少，至少覆盖 4 个常见中间件", len(all))
	}
}

// TestPresetsMetadata 预设的元信息必须齐全（前端下拉与页面提示直接展示它们）。
func TestPresetsMetadata(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets() {
		if seen[p.ID] {
			t.Errorf("预设 id 重复：%s", p.ID)
		}
		seen[p.ID] = true

		if strings.TrimSpace(p.Title) == "" {
			t.Errorf("%s：缺少展示名", p.ID)
		}
		if strings.TrimSpace(p.Desc) == "" {
			t.Errorf("%s：缺少用途说明", p.ID)
		}
		// Note 记录前置条件与实测边界：没有它，用户会以为「建了就该有数据」，
		// 而 exporter 未启用/端口不通正是最常见的失败原因。
		if strings.TrimSpace(p.Note) == "" {
			t.Errorf("%s：缺少前置条件说明", p.ID)
		}
		if p.Config.ID != p.ID || p.Config.Title != p.Title {
			t.Errorf("%s：元信息与配置不一致", p.ID)
		}
		if p.Config.Kind != KindPrometheusExporter {
			t.Errorf("%s：预设应统一用 prometheus-exporter（当前 kind=%s）", p.ID, p.Config.Kind)
		}
		if len(p.Config.Groups) != 0 {
			t.Errorf("%s：预设不应预设生效分组（由用户按自己环境选择）", p.ID)
		}
		if len(p.Config.Targets) != 1 {
			t.Fatalf("%s：预设应给出一个默认目标", p.ID)
		}
		addr := p.Config.Targets[0].Addr
		u, err := url.Parse(addr)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			t.Errorf("%s：默认地址 %q 不是合法 URL", p.ID, addr)
		}
	}
}

// TestPresetByID 按 id 查预设（Web 端「从预设创建」就走这个入口）。
func TestPresetByID(t *testing.T) {
	if _, ok := PresetByID("rabbitmq"); !ok {
		t.Fatal("应能按 id 取到 rabbitmq 预设")
	}
	if _, ok := PresetByID("no-such"); ok {
		t.Fatal("不存在的 id 不应返回预设")
	}
}
