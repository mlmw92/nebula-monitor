package collector

import "testing"

// TestGRMemberStateValueMapping 锁定 GR 成员状态的数值映射：
// 它是 agent 与指标目录标题（internal/server/metrics/middleware_catalog.go）之间的对外契约，
// 改这里必须同步改标题，否则「指标浏览」的说明会与实际取值不符。
func TestGRMemberStateValueMapping(t *testing.T) {
	cases := map[string]float64{
		"ONLINE":      1,
		"RECOVERING":  0.5,
		"OFFLINE":     0,
		"ERROR":       0,
		"UNREACHABLE": 0,
	}
	for state, want := range cases {
		if got := grMemberStateValue(state); got != want {
			t.Errorf("状态 %s 映射为 %v，期望 %v", state, got, want)
		}
	}
}
