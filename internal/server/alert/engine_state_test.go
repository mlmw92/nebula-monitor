package alert

import (
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// TestFiringKey_Format 去重键格式为 rule|node|instance，任一维度不同都必须区分开，
// 否则不同节点的同类告警会被误判为同一活跃告警（漏报或错报恢复）。
func TestFiringKey_Format(t *testing.T) {
	base := model.AlertEvent{RuleID: "r1", Node: "n1", Instance: "i1"}
	if got := firingKey(base); got != "r1|n1|i1" {
		t.Fatalf("firingKey = %q，want %q", got, "r1|n1|i1")
	}

	other := base
	other.Instance = "i2"
	if firingKey(base) == firingKey(other) {
		t.Fatal("不同实例不应被视为同一活跃告警")
	}
	other2 := base
	other2.Node = "n2"
	if firingKey(base) == firingKey(other2) {
		t.Fatal("不同节点不应被视为同一活跃告警")
	}
	other3 := base
	other3.RuleID = "r2"
	if firingKey(base) == firingKey(other3) {
		t.Fatal("不同规则不应被视为同一活跃告警")
	}
}

// TestTrackUntrackFiring 活跃索引的加入/移除往返，并回传原先的抑制状态。
func TestTrackUntrackFiring(t *testing.T) {
	e := &Engine{firing: map[string]*firingEntry{}}
	ev := model.AlertEvent{RuleID: "r1", Node: "n1", Instance: "i1", State: model.AlertStateFiring}

	e.trackFiringLocked(ev, true)
	fe, ok := e.firing[firingKey(ev)]
	if !ok || !fe.suppressed {
		t.Fatalf("跟踪失败: %+v", e.firing)
	}

	if !e.untrackFiringLocked(ev) {
		t.Fatal("移除时应回传原先的 suppressed=true")
	}
	if len(e.firing) != 0 {
		t.Fatalf("移除后索引应为空，实际 %d", len(e.firing))
	}
	if e.untrackFiringLocked(ev) {
		t.Fatal("重复移除应返回 false")
	}
}

// TestAlertLabels 抑制匹配用的标签视图：键名必须与事件字段一一对应。
func TestAlertLabels(t *testing.T) {
	ev := model.AlertEvent{
		RuleID:   "r1",
		RuleName: "CPU 使用率过高",
		Node:     "web-01",
		Instance: "cpu_usage",
		Severity: model.SeverityCritical,
		Metric:   "cpu_usage",
	}
	got := alertLabels(ev)
	want := map[string]string{
		"name":     "CPU 使用率过高",
		"rule":     "r1",
		"host":     "web-01",
		"node":     "web-01",
		"instance": "cpu_usage",
		"severity": "critical",
		"metric":   "cpu_usage",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("alertLabels[%q] = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("标签数量 = %d, want %d（新增键需同步本测试）", len(got), len(want))
	}
}

// TestEqualLabels keys 为空视为满足（不约束）；任一键取值不同即不满足。
func TestEqualLabels(t *testing.T) {
	a := map[string]string{"node": "web-01", "severity": "critical"}
	b := map[string]string{"node": "web-01", "severity": "warning"}

	if !equalLabels(a, nil, b) {
		t.Error("keys 为空应视为满足")
	}
	if !equalLabels(a, []string{"node"}, b) {
		t.Error("node 相同应满足")
	}
	if equalLabels(a, []string{"node", "severity"}, b) {
		t.Error("severity 不同不应满足")
	}
}

// TestComputeSuppressed 抑制链路：source/target/equal 三者都命中才抑制；
// equal 指定的维度不一致（如节点不同）时不得抑制。
func TestComputeSuppressed(t *testing.T) {
	store := NewInhibitStore(filepath.Join(t.TempDir(), "inhibit.yaml"))
	if err := store.Save([]InhibitRule{{
		Source: MatchSet{Match: map[string]string{"severity": "critical"}},
		Target: MatchSet{Match: map[string]string{"metric": "cpu_usage"}},
		Equal:  []string{"node"},
	}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	source := model.AlertEvent{
		RuleID: "r-src", RuleName: "磁盘将满", Node: "web-01",
		Metric: "disk_used_percent", Severity: model.SeverityCritical, State: model.AlertStateFiring,
	}
	e := &Engine{inhibit: store, firing: map[string]*firingEntry{}}
	e.firing[firingKey(source)] = &firingEntry{event: source}

	target := model.AlertEvent{
		RuleID: "r-tgt", RuleName: "CPU 使用率过高", Node: "web-01",
		Metric: "cpu_usage", Severity: model.SeverityWarning, State: model.AlertStateFiring,
	}

	supp, by := e.computeSuppressedLocked(target)
	if !supp || by != "磁盘将满" {
		t.Fatalf("同节点目标告警应被抑制，got suppressed=%v by=%q", supp, by)
	}

	// 换到另一节点：equal=node 不满足 → 不抑制
	other := target
	other.Node = "web-02"
	if supp, _ := e.computeSuppressedLocked(other); supp {
		t.Fatal("不同节点的目标告警不应被抑制")
	}

	// 自身不应被自己抑制
	if supp, _ := e.computeSuppressedLocked(source); supp {
		t.Fatal("源告警自身不应被标记为被抑制")
	}

	// 未配置抑制规则时恒不抑制
	bare := &Engine{firing: map[string]*firingEntry{}}
	if supp, _ := bare.computeSuppressedLocked(target); supp {
		t.Fatal("未配置规则时不应抑制")
	}
}

// TestRestoreActiveState_NormalRule 重启恢复的「普通规则」分支：
// 除写入统一 firing 索引外，还要回填 states 的持续计时（以原 StartsAt 为准）。
func TestRestoreActiveState_NormalRule(t *testing.T) {
	const firingAt = int64(1700000000000)
	storage := &captureAlertStorage{active: []model.Series{{
		Labels: map[string]string{
			"rule": "rule-1", "name": "CPU 使用率过高", "host": "node-1",
			"state": "firing", "severity": "critical", "metric": "cpu_usage",
		},
		Points: []model.Point{{Timestamp: firingAt, Value: 0}},
	}}}
	rules := &RulesStore{rules: map[string]model.AlertRule{}}
	engine := NewEngine(nil, nil, rules, NewVMAlertStore(storage), nil, nil, nil, 15, nil, nil, nil)

	key := "rule-1|node-1|"
	st, ok := engine.states[key]
	if !ok {
		t.Fatalf("普通规则应恢复 states 条目，实际 states=%+v", engine.states)
	}
	if !st.firing {
		t.Error("恢复后 firing 应为 true")
	}
	if st.aboveSince != firingAt || st.firedAt != firingAt {
		t.Errorf("持续计时应以原 StartsAt 为准: aboveSince=%d firedAt=%d", st.aboveSince, st.firedAt)
	}
	if _, ok := engine.firing["rule-1|node-1|"]; !ok {
		t.Error("所有告警类型都应写入统一 firing 索引")
	}
	if len(engine.dialActive) != 0 || len(engine.certActive) != 0 {
		t.Error("普通规则不应污染拨测/证书索引")
	}
}

// TestRestoreActiveState_PreservesSuppressed 恢复时需保留事件原有的抑制标记，
// 否则重启后会把被抑制的告警当成未抑制并重复通知。
func TestRestoreActiveState_PreservesSuppressed(t *testing.T) {
	storage := &captureAlertStorage{active: []model.Series{{
		Labels: map[string]string{
			"rule": "rule-1", "name": "CPU 使用率过高", "host": "node-1",
			"state": "firing", "severity": "warning", "metric": "cpu_usage", "suppressed": "true",
		},
		Points: []model.Point{{Timestamp: 100, Value: 0}},
	}}}
	rules := &RulesStore{rules: map[string]model.AlertRule{}}
	engine := NewEngine(nil, nil, rules, NewVMAlertStore(storage), nil, nil, nil, 15, nil, nil, nil)

	fe, ok := engine.firing["rule-1|node-1|"]
	if !ok {
		t.Fatalf("应恢复 firing 索引: %+v", engine.firing)
	}
	if !fe.suppressed {
		t.Error("恢复时应保留 suppressed 标记")
	}
}
