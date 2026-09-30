package alert

import (
	"testing"

	"github.com/nebula/monitor/internal/server/metrics"
)

// 本文件是「规则模板」的守卫。设计意图：模板是用户「不知道该怎么配」时的抄写范本，
// 一旦模板本身是错的（指标名不存在、方向配反、服务类型没注册），用户抄过去只会得到
// 一条**永不触发**的规则——而永不触发是静默的，没人会来报 bug。
// 因此这几条不变量必须由测试固定，而不是靠写模板时的小心。

// TestTemplateMetricsAreRegistered 模板引用的指标必须登记在指标目录中。
//
// 目录条目的名字由 internal/server/metrics 的守卫保证「真的有人产出」，
// 于是本用例把「模板 → 采集器」这条链补齐：模板抄错了名字，这里直接失败。
func TestTemplateMetricsAreRegistered(t *testing.T) {
	for _, tpl := range DefaultTemplates() {
		if tpl.Metric == "" {
			continue // 场景化规则（离线/主从切换/集群损坏/安全事件）不用指标
		}
		meta, ok := metrics.Meta(tpl.Metric)
		if !ok {
			t.Errorf("模板 %q 引用了未登记的指标 %s：规则会永远不触发，且不报错", tpl.Name, tpl.Metric)
			continue
		}
		if meta.Title == "" {
			t.Errorf("模板 %q 的指标 %s 缺中文标题", tpl.Name, tpl.Metric)
		}
	}
}

// TestTemplateOperatorMatchesWorseDirection 模板的比较方向必须与指标的「变差方向」一致。
//
// 这是最容易配错、也最难发现的一类错：给「证书剩余天数」设 `> 15` 语法完全合法，
// 但语义是「剩余天数大于 15 就告警」——证书要过期了反而不告警。用户只有在证书真过期那天
// 才会发现这条规则从来没生效。指标目录里的 WorseWhen 就是为这一条守卫而存在的。
func TestTemplateOperatorMatchesWorseDirection(t *testing.T) {
	for _, tpl := range DefaultTemplates() {
		meta, ok := metrics.Meta(tpl.Metric)
		if !ok || meta.WorseWhen == "" {
			continue // 方向未标注的指标（如 Controller 数用 != 判定）不做强制
		}
		op := tpl.Operator
		switch meta.WorseWhen {
		case metrics.WorseHigh:
			if op != ">" && op != ">=" {
				t.Errorf("模板 %q：指标 %s 是「越大越糟」，运算符应为 > 或 >=，实际 %q", tpl.Name, tpl.Metric, op)
			}
		case metrics.WorseLow:
			if op != "<" && op != "<=" {
				t.Errorf("模板 %q：指标 %s 是「越小越糟」，运算符应为 < 或 <=，实际 %q", tpl.Name, tpl.Metric, op)
			}
		}
	}
}

// TestTemplatesPassValidation 模板必须能原样通过服务端校验。
//
// 否则「从模板新建」会在提交时被拒绝——用户看到的是自己刚点的那一下报错，
// 而问题在模板本身（例如 service_down 用了一个注册表里没有的服务类型）。
func TestTemplatesPassValidation(t *testing.T) {
	for _, tpl := range DefaultTemplates() {
		if err := ValidateRule(tpl); err != nil {
			t.Errorf("模板 %q 无法通过校验: %v", tpl.Name, err)
		}
	}
}

// TestTemplatesHaveGroupAndDescription 每条模板都要有分组与说明。
//
// 分组决定选择器里的归类（没有分组会在下拉里堆成一坨，60 条没法找），
// 说明承载「为什么是这个阈值、该怎么调」——这正是「不知道该怎么配」的答案本身。
func TestTemplatesHaveGroupAndDescription(t *testing.T) {
	for _, tpl := range DefaultTemplates() {
		if tpl.TemplateGroup == "" {
			t.Errorf("模板 %q 缺少 TemplateGroup（选择器无法归类）", tpl.Name)
		}
		if tpl.Desc == "" {
			t.Errorf("模板 %q 缺少 Desc（用户看不到阈值依据）", tpl.Name)
		}
	}
}

// TestTemplateNamesUnique 模板名必须唯一：播种与「从模板新建」都以名字为准，
// 重名会让其中一条被静默吞掉（播种按名字去重、选择器里两条一模一样）。
func TestTemplateNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tpl := range DefaultTemplates() {
		if seen[tpl.Name] {
			t.Errorf("模板名重复：%q", tpl.Name)
		}
		seen[tpl.Name] = true
	}
}

// TestSeedTemplatesAreSubset 自动播种的清单必须是模板库的子集，且保持「少而通用」。
//
// 名字拼错不会报错，只会静默地少播种一条；而播种条目一旦膨胀到几十条，
// 升级用户的规则列表就会被"本机根本不存在的对象"的规则淹掉——所以两条都断言。
func TestSeedTemplatesAreSubset(t *testing.T) {
	all := map[string]bool{}
	for _, tpl := range DefaultTemplates() {
		all[tpl.Name] = true
	}
	if len(seededTemplateNames) > 25 {
		t.Errorf("自动播种 %d 条过于激进：请只保留「跨环境普遍成立、无需改阈值」的规则（其余留在模板库）", len(seededTemplateNames))
	}
	if len(seededTemplateNames) < 8 {
		t.Errorf("自动播种只剩 %d 条，全新安装的告警中心会显得没用起来", len(seededTemplateNames))
	}
	for _, name := range seededTemplateNames {
		if !all[name] {
			t.Errorf("播种清单中的 %q 不在模板库中：拼写错误会让这条内置规则静默缺失", name)
		}
	}
	// 播种清单应覆盖各类「服务离线」与「主机离线」，这些是告警中心的基本盘
	seeded := map[string]bool{}
	for _, n := range seededTemplateNames {
		seeded[n] = true
	}
	for _, must := range []string{"主机离线", "MySQL 服务离线", "磁盘使用率过高"} {
		if !seeded[must] {
			t.Errorf("「%s」是基本盘规则，应当自动播种", must)
		}
	}
}
