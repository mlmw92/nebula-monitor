package asset

import "testing"

// 业务范围（授权用的标签选择器）在存储层的用例。
//
// 它与"用户筛选用的 Label"写法相近但语义不同：一个是用户想筛什么、一个是授权允许看什么。
// 两者混起来的后果是范围过滤被筛选条件抹掉——那是一条越权捷径，所以这里逐条钉住
// （维度内取或、与其它条件取且、以及与 Nodes 同一套三态）。

func TestListFilterLabelSelectors(t *testing.T) {
	svc, _ := newTestService(t)
	for _, n := range []string{"web-01", "web-02", "web-03"} {
		if _, _, err := svc.Apply(hostObservation(n, map[string]string{"cpu": "8"})); err != nil {
			t.Fatalf("写入 %s 失败: %v", n, err)
		}
	}
	setLabels := func(host string, labels map[string]string) {
		t.Helper()
		if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: host}, labels, nil, "alice"); err != nil {
			t.Fatalf("写标签 %s 失败: %v", host, err)
		}
	}
	setLabels("web-01", map[string]string{"biz": "pay", "env": "prod"})
	setLabels("web-02", map[string]string{"biz": "risk", "env": "prod"})
	// web-03 不打任何标签：它不属于任何业务范围

	pay := []LabelSelector{{Key: "biz", Value: "pay"}}
	risk := []LabelSelector{{Key: "biz", Value: "risk"}}
	count := func(f ListFilter) int {
		t.Helper()
		n, err := svc.Count(f)
		if err != nil {
			t.Fatalf("Count(%+v) 失败: %v", f, err)
		}
		return n
	}

	// ① 单个选择器：只有带该标签的资产可见
	if n := count(ListFilter{LabelSelectors: pay}); n != 1 {
		t.Fatalf("按 biz=pay 应命中 1 条，实际 %d", n)
	}
	// ② 维度内取或（与节点分组维度内取并集同构）
	if n := count(ListFilter{LabelSelectors: append(append([]LabelSelector{}, pay...), risk...)}); n != 2 {
		t.Fatalf("两个选择器应命中 2 条，实际 %d", n)
	}
	// ③ 没打标签的资产不在任何业务范围里（fail-closed 的另一面：打标签是授权的必要条件）
	if n := count(ListFilter{LabelSelectors: pay}); n == 3 {
		t.Fatal("未打标签的资产不应因为「没有标签」而被算进范围")
	}
	// ④ 与用户筛选条件之间是**且**：用户筛 biz=pay、授权是 biz=risk ⇒ 空
	if n := count(ListFilter{Label: "biz:pay", LabelSelectors: risk}); n != 0 {
		t.Fatalf("筛选与授权是且的关系，应为 0，实际 %d", n)
	}
	// ⑤ 与节点范围之间也是且
	if n := count(ListFilter{Nodes: []string{"web-02"}, LabelSelectors: pay}); n != 0 {
		t.Fatalf("节点与业务范围是且的关系，应为 0，实际 %d", n)
	}
	if n := count(ListFilter{Nodes: []string{"web-01", "web-02"}, LabelSelectors: pay}); n != 1 {
		t.Fatalf("两个节点里只有 web-01 属于 biz=pay，应为 1，实际 %d", n)
	}
	// ⑥ 值不同即不同范围（一个键多个值不是"前缀匹配"）
	if n := count(ListFilter{LabelSelectors: []LabelSelector{{Key: "biz", Value: "pa"}}}); n != 0 {
		t.Fatalf("标签值必须精确匹配，实际 %d", n)
	}
	// ⑦ 键不同即不同维度
	if n := count(ListFilter{LabelSelectors: []LabelSelector{{Key: "env", Value: "pay"}}}); n != 0 {
		t.Fatalf("键不同不应命中，实际 %d", n)
	}

	// 三态：nil = 不过滤；非 nil 空 = 恒不匹配（与 Nodes 同一套写法）
	if n := count(ListFilter{}); n != 3 {
		t.Fatalf("nil 表示该维度不生效，应看到全部 3 条，实际 %d", n)
	}
	if n := count(ListFilter{LabelSelectors: []LabelSelector{}}); n != 0 {
		t.Fatalf("受限但没有选择器必须恒不匹配（fail-closed），实际 %d", n)
	}

	// 列表 / 计数 / 摘要必须共用同一条件，否则「共 N 条」与能翻到的条数会对不上
	items, err := svc.List(ListFilter{LabelSelectors: pay})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(items) != 1 || items[0].NaturalKey != "web-01" {
		t.Fatalf("列表结果与计数不一致：%+v", items)
	}
	all, err := svc.ListAll(ListFilter{LabelSelectors: pay})
	if err != nil {
		t.Fatalf("ListAll 失败: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("导出（ListAll）也必须受业务范围约束，实际 %d 条", len(all))
	}
	st, err := svc.Stats(ListFilter{LabelSelectors: pay}, 0)
	if err != nil {
		t.Fatalf("Stats 失败: %v", err)
	}
	if st.Total != 1 {
		t.Fatalf("摘要总数也必须同一套条件，实际 %d", st.Total)
	}
}

// 候选值（范围配置表单的下拉）也必须按两个维度收窄。
//
// 实测发现的真实缺口：受限到 biz=pay 的账号调 `label-values?key=biz` 拿到了 ["pay","risk"]——
// 列表与详情都挡住了，却从下拉里漏出"我的机器上还有别人业务的标签值"。
func TestDistinctLabelValuesScoped(t *testing.T) {
	svc, _ := newTestService(t)
	for _, n := range []string{"web-01", "web-02"} {
		if _, _, err := svc.Apply(hostObservation(n, map[string]string{"cpu": "8"})); err != nil {
			t.Fatalf("写入 %s 失败: %v", n, err)
		}
	}
	set := func(host string, labels map[string]string) {
		t.Helper()
		if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: host}, labels, nil, "alice"); err != nil {
			t.Fatalf("打标签 %s 失败: %v", host, err)
		}
	}
	set("web-01", map[string]string{"biz": "pay", "env": "prod"})
	set("web-02", map[string]string{"biz": "risk", "env": "test"})

	pay := []LabelSelector{{Key: "biz", Value: "pay"}}
	values := func(key string, nodes []string, sels []LabelSelector) []string {
		t.Helper()
		got, err := svc.DistinctLabelValues(key, nodes, sels)
		if err != nil {
			t.Fatalf("DistinctLabelValues 失败: %v", err)
		}
		return got
	}

	// 未限制：两种取值都在
	if got := values("biz", nil, nil); len(got) != 2 {
		t.Fatalf("不受限时应有两个取值，实际 %v", got)
	}
	// 业务范围收窄后：只应看到自己范围内的那一个
	if got := values("biz", nil, pay); len(got) != 1 || got[0] != "pay" {
		t.Fatalf("受限到 biz=pay 时只应看到 pay，实际 %v", got)
	}
	// 换一个键也一样：只列**范围内资产**上的取值
	if got := values("env", nil, pay); len(got) != 1 || got[0] != "prod" {
		t.Fatalf("受限资产的 env 只应有 prod，实际 %v", got)
	}
	// 节点维度同样生效
	if got := values("biz", []string{"web-02"}, nil); len(got) != 1 || got[0] != "risk" {
		t.Fatalf("限定到 web-02 时只应有 risk，实际 %v", got)
	}
	// 三态：nil = 不过滤、非 nil 空 = 恒不可见
	if got := values("biz", nil, []LabelSelector{}); len(got) != 0 {
		t.Fatalf("业务维度限定了却没有取值 → 无候选，实际 %v", got)
	}
	if got := values("biz", []string{}, nil); len(got) != 0 {
		t.Fatalf("节点维度限定了却没有可见节点 → 无候选，实际 %v", got)
	}
	if got := values("", nil, nil); got != nil {
		t.Fatalf("空键应返回 nil（调用方负责校验），实际 %v", got)
	}
}
