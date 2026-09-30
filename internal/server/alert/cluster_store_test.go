package alert

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// grTestStore 是 storage.Storage 的最小测试替身：按指标名返回预置的即时查询序列。
type grTestStore struct{ instant map[string][]model.Series }

func (s *grTestStore) Write([]model.Metric) error { return nil }
func (s *grTestStore) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *grTestStore) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *grTestStore) QueryInstant(_, name string, _ map[string]string) ([]model.Series, error) {
	return s.instant[name], nil
}
func (s *grTestStore) QueryInstantWithLookback(_, name string, _ map[string]string, _ time.Duration) ([]model.Series, error) {
	return s.instant[name], nil
}
func (s *grTestStore) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *grTestStore) Close() error    { return nil }
func (s *grTestStore) Backend() string { return "fake" }

func grPoint(ts int64, v float64) model.Point { return model.Point{Timestamp: ts, Value: v} }

// GR 组视图只采纳观察者「最新一轮」上报的成员：
// 刚发生分裂时，即时查询的回看窗口里仍留有上一轮"看到 3 个成员"的序列，
// 若把它们并进来，分裂最长会被掩盖一个回看窗口——而这正是最需要灵敏的时刻。
func TestGrClusterMetaForNodeIgnoresStaleViewMembers(t *testing.T) {
	observer := "10.0.0.10:3307"
	store := &grTestStore{instant: map[string][]model.Series{
		metricGRViewMember: {
			// 最新一轮（ts=2000）：该观察者只看到自己
			{Labels: map[string]string{"node": "n1", "instance": observer, "member": "mysql-gr-1"},
				Points: []model.Point{grPoint(1000, 1), grPoint(2000, 1)}},
			// 上一轮（ts=1000）：曾看到另外两个成员，仍在回看窗口内
			{Labels: map[string]string{"node": "n1", "instance": observer, "member": "mysql-gr-2"},
				Points: []model.Point{grPoint(1000, 1)}},
			{Labels: map[string]string{"node": "n1", "instance": observer, "member": "mysql-gr-3"},
				Points: []model.Point{grPoint(1000, 1)}},
		},
	}}

	_, views, _ := grClusterMetaForNode(store, "n1")
	got := views[observer]
	if len(got) != 1 || got[0] != "mysql-gr-1" {
		t.Fatalf("应只采纳最新一轮的视图成员，实际 %v", got)
	}

	// 端到端：该视图应被判定为组视图分裂
	fault := ClassifyClusterFault([]ClusterMember{
		{Node: "n1", Instance: observer, Group: "g", Role: "primary", Topology: "cluster", UP: true, Value: 1, View: got},
		{Node: "n1", Instance: "10.0.0.10:3308", Group: "g", Role: "primary", Topology: "cluster", UP: true, Value: 1,
			View: []string{"mysql-gr-2"}},
		{Node: "n1", Instance: "10.0.0.10:3309", Group: "g", Role: "primary", Topology: "cluster", UP: true, Value: 1,
			View: []string{"mysql-gr-3"}},
	})
	if fault == "" {
		t.Fatal("各自为组的视图应被判为组视图分裂")
	}
}

// 同一轮的多个成员（时间戳相同）必须全部保留，不能只留一个。
func TestGrClusterMetaForNodeKeepsWholeLatestCycle(t *testing.T) {
	observer := "10.0.0.10:3307"
	var series []model.Series
	for _, m := range []string{"mysql-gr-1", "mysql-gr-2", "mysql-gr-3"} {
		series = append(series, model.Series{
			Labels: map[string]string{"node": "n1", "instance": observer, "member": m},
			Points: []model.Point{grPoint(2000, 1)},
		})
	}
	_, views, _ := grClusterMetaForNode(&grTestStore{instant: map[string][]model.Series{
		metricGRViewMember: series,
	}}, "n1")
	if len(views[observer]) != 3 {
		t.Fatalf("同一轮 3 个成员都应保留，实际 %v", views[observer])
	}
}

// 单主/多主模式同样取最新样本：切模式后旧样本不能继续生效。
func TestGrClusterMetaForNodeModeUsesNewestSample(t *testing.T) {
	instance := "10.0.0.10:3307"
	store := &grTestStore{instant: map[string][]model.Series{
		metricGRSinglePrimaryMode: {{
			Labels: map[string]string{"node": "n1", "instance": instance},
			Points: []model.Point{grPoint(1000, 1), grPoint(2000, 0)}, // 由单主切到多主
		}},
	}}
	modes, _, _ := grClusterMetaForNode(store, "n1")
	mode := modes[instance]
	if mode == nil || *mode {
		t.Fatalf("应取最新样本（多主=false），实际 %v", mode)
	}
}

// 组视图指标取值 → 成员状态的还原必须与 agent 侧编码一致（1=ONLINE/0.5=RECOVERING/0=其他），
// 否则「成员未就绪」判定会基于错误状态。
func TestGrStateTextEncoding(t *testing.T) {
	for value, want := range map[float64]string{1: "ONLINE", 0.5: "RECOVERING", 0: "OFFLINE"} {
		if got := grStateText(value); got != want {
			t.Errorf("取值 %v 还原为 %q，期望 %q", value, got, want)
		}
	}
}

// 成员状态必须取最新一轮：切模式/重启后旧状态只能在回看窗口里残留。
func TestGrClusterMetaForNodeStatesUseLatestCycle(t *testing.T) {
	observer := "10.0.0.10:3307"
	store := &grTestStore{instant: map[string][]model.Series{
		metricGRViewMember: {{
			Labels: map[string]string{"node": "n1", "instance": observer, "member": "mysql-gr-2"},
			// 旧一轮 RECOVERING(0.5) → 最新一轮 ONLINE(1)
			Points: []model.Point{grPoint(1000, 0.5), grPoint(2000, 1)},
		}},
	}}
	_, _, states := grClusterMetaForNode(store, "n1")
	if got := states[observer]["mysql-gr-2"]; got != "ONLINE" {
		t.Fatalf("成员状态应取最新一轮（ONLINE），实际 %q", got)
	}
}
