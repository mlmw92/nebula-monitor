package asset

import (
	"errors"
	"testing"
)

// 配置项模型（asset_types.schema）的用例。
//
// 重心在三条"错了也不会报错"的性质：
//   - **存量行为零变化**：没有模型时巡检/快照的字段集与历史逐字相同（升级不能改结论）；
//   - **改了模型立刻生效**：把某字段配成运行态字段后，它不再进差异清单，且**不被报成"缺失"**
//     （配置项没被删，只是不再比对了）；
//   - **坏数据不阻断**：手写坏的 schema 要按内置默认跑，而不是让台账/巡检起不来。

// inspectFields 跑一次巡检，把差异项按字段名索引返回（用例只关心"哪个字段报了差异"）。
func inspectFields(t *testing.T, svc *Service, fields []string) map[string]InspectFinding {
	t.Helper()
	run, err := svc.RunInspect(InspectScope{Fields: fields}, "tester")
	if err != nil {
		t.Fatalf("巡检失败: %v", err)
	}
	items, err := svc.InspectFindings(run.ID, 0)
	if err != nil {
		t.Fatalf("读差异项失败: %v", err)
	}
	out := map[string]InspectFinding{}
	for _, f := range items {
		out[f.Field] = f
	}
	return out
}

// applyHostAttrs 按采集来源覆盖一台主机的属性（值变了才会产生差异）。
func applyHostAttrs(t *testing.T, svc *Service, name string, attrs map[string]string) {
	t.Helper()
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: name, Name: name, Node: name,
		Source: SourceDiscovery, Attrs: attrs,
	}); err != nil {
		t.Fatalf("提交观测失败: %v", err)
	}
}

// 没有模型时，字段范围与历史逐字相同：显式 fields 就按它，空则"全部减运行态"。
func TestTypeModelKeepsLegacyFocusBehavior(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{
		"cpuCores": "8", "env": "prod", "up": "1", "status": "online",
	})

	host, ok, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil || !ok {
		t.Fatalf("取资产失败: %v ok=%v", err, ok)
	}

	// 空 fields：全部减去内置的运行态字段（DefaultRuntimeFields）
	got := svc.focusFields(host, nil)
	if _, has := got["up"]; has {
		t.Fatalf("运行态字段 up 应被排除，实际 %v", got)
	}
	if _, has := got["status"]; has {
		t.Fatalf("运行态字段 status 应被排除，实际 %v", got)
	}
	if got["cpuCores"] != "8" || got["env"] != "prod" {
		t.Fatalf("普通字段应保留，实际 %v", got)
	}

	// 显式 fields：只按它取，**不再**排除运行态字段（那是调用方点名的）
	got = svc.focusFields(host, []string{"up", "cpuCores"})
	if len(got) != 2 || got["up"] != "1" || got["cpuCores"] != "8" {
		t.Fatalf("显式字段集应原样生效，实际 %v", got)
	}
}

// 把某字段配成运行态字段后：它不再进差异清单，也**不被报成"缺失"**；清空模型即恢复。
// 这是设计件里那条决定性判据。
func TestTypeModelRuntimeFieldExclusionTakesEffect(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "env": "prod"})
	inspectFields(t, svc, nil) // 首次只建基线

	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "16", "env": "prod"})
	if got := inspectFields(t, svc, nil); got["cpuCores"] == (InspectFinding{}) {
		t.Fatal("未配模型时，cpuCores 变化应产生差异")
	} else if got["cpuCores"].Kind != FindingChanged {
		t.Fatalf("应为 changed，实际 %s", got["cpuCores"].Kind)
	}

	// 把 cpuCores 配成运行态字段：此后它不该再参与比对
	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{RuntimeFields: []string{"cpuCores"}}); err != nil {
		t.Fatalf("保存模型失败: %v", err)
	}
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "32", "env": "prod"})
	got := inspectFields(t, svc, nil)
	if f, has := got["cpuCores"]; has {
		t.Fatalf("已配成运行态字段，不该再产生差异，实际 kind=%s expected=%q actual=%q", f.Kind, f.Expected, f.Actual)
	}

	// 清空模型（回到内置默认）：恢复比对
	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{}); err != nil {
		t.Fatalf("清空模型失败: %v", err)
	}
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "64", "env": "prod"})
	if got = inspectFields(t, svc, nil); got["cpuCores"].Kind != FindingChanged {
		t.Fatalf("清空模型后应恢复比对，实际 %v", got)
	}
}

// 关注字段集：只比对点名的字段，其余变化不产生差异（也不产生"缺失"）。
func TestTypeModelFocusFieldsLimitComparison(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "env": "prod"})
	inspectFields(t, svc, nil)

	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{FocusFields: []string{"env"}}); err != nil {
		t.Fatalf("保存模型失败: %v", err)
	}
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "16", "env": "prod"})
	got := inspectFields(t, svc, nil)
	if f, has := got["cpuCores"]; has {
		t.Fatalf("cpuCores 不在关注字段集里，不该报差异（尤其不该报缺失），实际 kind=%s", f.Kind)
	}

	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "16", "env": "staging"})
	if got = inspectFields(t, svc, nil); got["env"].Kind != FindingChanged {
		t.Fatalf("关注字段 env 的变化应报差异，实际 %v", got)
	}
}

// 属性说明只影响展示：它既不改变比对的字段集，也不改变快照内容。
func TestTypeModelAttrMetaDoesNotAffectComparison(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "up": "1"})
	host, _, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatalf("取资产失败: %v", err)
	}
	before := svc.focusFields(host, nil)

	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{
		AttrMeta: map[string]AttrMeta{"cpuCores": {Title: "CPU 核数", Unit: "核", Note: "采集自 /proc/cpuinfo"}},
	}); err != nil {
		t.Fatalf("保存模型失败: %v", err)
	}
	after := svc.focusFields(host, nil)
	if len(before) != len(after) {
		t.Fatalf("属性说明不该改变比对的字段集：%v -> %v", before, after)
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("属性说明不该改变取值：%s %q -> %q", k, v, after[k])
		}
	}
}

// 校验与边界：空键、自相矛盾、未知类型、坏 schema。
func TestTypeModelValidationAndFallback(t *testing.T) {
	svc, store := newTestService(t)

	// 同一个键既在关注字段、又在运行态字段里：两句话自相矛盾，必须拒
	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{
		RuntimeFields: []string{"cpuCores"}, FocusFields: []string{"cpuCores"},
	}); err == nil {
		t.Fatal("focus 与 runtime 含同一个键应被拒")
	}
	// 空白键
	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{RuntimeFields: []string{"  "}}); err == nil {
		t.Fatal("空白字段键应被拒")
	}
	// 未知类型：要能区分出来（接口层据此回 404 而不是 400）
	if err := svc.UpdateTypeModel("no-such-type", TypeSchema{}); !errors.Is(err, ErrTypeNotFound) {
		t.Fatalf("未知类型应返回 ErrTypeNotFound，实际 %v", err)
	}

	// 手工写一份坏 schema：读模型时按内置默认，**不报错**、更不让巡检起不来
	if _, err := store.db.Exec(`UPDATE asset_types SET schema='{"runtimeFields": [' WHERE key=?`, TypeHost); err != nil {
		t.Fatalf("写入坏 schema 失败: %v", err)
	}
	svc.invalidateTypeSchemas()
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "up": "1"})
	host, _, err := svc.Get(Ref{TypeKey: TypeHost, NaturalKey: "web-01"})
	if err != nil {
		t.Fatalf("取资产失败: %v", err)
	}
	got := svc.focusFields(host, nil)
	if _, has := got["up"]; has {
		t.Fatalf("坏 schema 应退回内置默认（up 仍被排除），实际 %v", got)
	}
	if got["cpuCores"] != "8" {
		t.Fatalf("坏 schema 下普通字段仍应参与比对，实际 %v", got)
	}
}

// 模型变更后立刻生效（不需要重启）：改完马上跑巡检就用新口径。
func TestTypeModelTakesEffectWithoutRestart(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "env": "prod"})
	inspectFields(t, svc, nil)
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "16", "env": "prod"})

	// 先按显式 fields 验一次（走的是同一条读取路径），确认缓存不会钉住旧值
	if got := inspectFields(t, svc, []string{"cpuCores"}); got["cpuCores"].Kind != FindingChanged {
		t.Fatalf("显式字段集应报变更，实际 %v", got)
	}
	if err := svc.UpdateTypeModel(TypeHost, TypeSchema{RuntimeFields: []string{"cpuCores"}}); err != nil {
		t.Fatalf("保存模型失败: %v", err)
	}
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "32", "env": "prod"})
	if got := inspectFields(t, svc, nil); got["cpuCores"].Kind == FindingChanged {
		t.Fatalf("改完模型应立刻生效（不必重启），实际仍报变更：%v", got)
	}
}

// 类型画像：统计口径（资产数、覆盖度、来源分布）、随范围收窄、以及"没配过"的标记。
func TestTypeModelsStatsAndScope(t *testing.T) {
	svc, _ := newTestService(t)
	svc.now = func() int64 { return 1000 }
	applyHostAttrs(t, svc, "web-01", map[string]string{"cpuCores": "8", "env": "prod"})
	applyHostAttrs(t, svc, "db-01", map[string]string{"cpuCores": "4"})
	if _, err := svc.SetLabels(Ref{TypeKey: TypeHost, NaturalKey: "web-01"},
		map[string]string{"biz": "pay"}, nil, "alice", ""); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}
	// 人工改一个值：该键应同时有采集与人工两个来源
	if _, _, err := svc.Apply(Observation{
		TypeKey: TypeHost, NaturalKey: "web-01", Name: "web-01", Node: "web-01",
		Source: SourceManual, Actor: "alice", Attrs: map[string]string{"cpuCores": "16"},
	}); err != nil {
		t.Fatalf("人工修改失败: %v", err)
	}

	models, err := svc.TypeModels(ListFilter{}, nil)
	if err != nil {
		t.Fatalf("读取模型失败: %v", err)
	}
	var host *TypeModel
	for i := range models {
		if models[i].Key == TypeHost {
			host = &models[i]
		}
	}
	if host == nil {
		t.Fatalf("应包含 host 类型，实际 %d 条", len(models))
	}
	if host.Assets != 2 {
		t.Fatalf("host 资产数应为 2，实际 %d", host.Assets)
	}
	if !host.Builtin {
		t.Fatal("host 应是内置类型")
	}
	if host.Ephemeral {
		t.Fatal("host 不是短命对象")
	}
	if !host.Schema.UsesDefaultRuntimeFields() {
		t.Fatal("没配过时应标记为用内置默认")
	}
	byKey := map[string]TypeAttrStat{}
	for _, st := range host.Attrs {
		byKey[st.Key] = st
	}
	if st := byKey["cpuCores"]; st.Assets != 2 || st.Discovery != 2 || st.Manual != 1 {
		t.Fatalf("cpuCores 的覆盖与来源分布不符：%#v", st)
	}
	if st := byKey["env"]; st.Assets != 1 || st.Manual != 0 {
		t.Fatalf("env 只应有一台资产有：%#v", st)
	}

	// 短命对象也要出现在模型里（但被标出来）
	var pod *TypeModel
	for i := range models {
		if models[i].Key == TypePod {
			pod = &models[i]
		}
	}
	if pod == nil || !pod.Ephemeral {
		t.Fatalf("Pod 类型应在模型里且标记为短命对象，实际 %+v", pod)
	}

	// 按节点范围收窄统计（模型本身不受限）
	scoped, err := svc.TypeModels(ListFilter{Nodes: []string{"web-01"}}, nil)
	if err != nil {
		t.Fatalf("按范围读模型失败: %v", err)
	}
	for _, m := range scoped {
		if m.Key != TypeHost {
			continue
		}
		if m.Assets != 1 {
			t.Fatalf("按节点收窄后 host 资产数应为 1，实际 %d", m.Assets)
		}
		for _, st := range m.Attrs {
			if st.Key == "env" && st.Assets != 1 {
				t.Fatalf("env 在范围内应仍是 1，实际 %d", st.Assets)
			}
		}
	}
}

// 「疑似同义键」按"小写 + 去分隔符"归一：camelCase / snake_case / kebab / 点号都算同一种写法。
// 但**语义相近而写法不同**的（cpu 与 cpuCores）不会被猜成一组——这条边界要钉住，
// 免得以后被当成"没做全"，也免得有人往这里加语义猜测（自动合并是不可逆的数据破坏）。
func TestAttrSynonymGroups(t *testing.T) {
	groups := AttrSynonymGroups([]string{"cpu_cores", "cpu-cores", "CPU.Cores", "cpuCores", "cpu", "env"})
	if len(groups) != 1 {
		t.Fatalf("应只有一组写法变体，实际 %v", groups)
	}
	if len(groups[0]) != 4 {
		t.Fatalf("cpu_cores / cpu-cores / CPU.Cores / cpuCores 是同一个字段的四种写法，应成一组，实际 %v", groups[0])
	}
	for _, g := range groups {
		for _, k := range g {
			// cpu 与 cpuCores 归一后不同形（cpu vs cpucores）：语义相近但不是写法变体，不猜
			if k == "cpu" || k == "env" {
				t.Fatalf("不该被归入这一组：%v", g)
			}
		}
	}
	// 没有同义键时返回空（界面据此不显示这一段）
	if got := AttrSynonymGroups([]string{"cpu", "env"}); len(got) != 0 {
		t.Fatalf("无同义键时应返回空，实际 %v", got)
	}
}
