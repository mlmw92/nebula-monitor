package alert

import "testing"

// TestLatestPerInstanceKeepsNewestSample 守住「实例已恢复但告警不消」的根因：
//
// 同一实例的存活指标可能同时存在两条 series（up=0 与 up=1 的标签集不同，例如仅成功
// 采集时才带 version/role 标签），离线期间写下的 up=0 在实例恢复后仍位于即时查询的
// 回看窗口内。若两条都参与服务离线评估，同一轮里会先判离线触发、再判在线恢复，
// 结果取决于 map 遍历顺序 → 告警反复抖动或一直不消。
//
// 修复后：同一 instance 只保留数据点时间戳最新的一条。
func TestLatestPerInstanceKeepsNewestSample(t *testing.T) {
	samples := []metricSample{
		{instance: "10.0.0.10:3306", value: 0, ts: 1000}, // 离线期间写入的陈旧序列
		{instance: "10.0.0.10:3306", value: 1, ts: 2000}, // 恢复后写入的新序列
	}
	got := latestPerInstance(samples)
	if len(got) != 1 {
		t.Fatalf("同实例应只保留一条样本，实际 %d 条：%+v", len(got), got)
	}
	if got[0].value != 1 {
		t.Fatalf("应保留时间戳最新的样本（up=1），实际 %+v", got[0])
	}
}

// TestLatestPerInstanceKeepsOfflineWhenNewest 实例仍在离线时，最新样本为 up=0，不应被旧的 up=1 顶掉。
func TestLatestPerInstanceKeepsOfflineWhenNewest(t *testing.T) {
	samples := []metricSample{
		{instance: "10.0.0.10:6379", value: 1, ts: 1000},
		{instance: "10.0.0.10:6379", value: 0, ts: 3000},
	}
	got := latestPerInstance(samples)
	if len(got) != 1 || got[0].value != 0 {
		t.Fatalf("离线状态应保留最新样本（up=0），实际 %+v", got)
	}
}

// TestLatestPerInstanceKeepsDistinctInstances 不同实例互不影响，且保持首次出现顺序。
func TestLatestPerInstanceKeepsDistinctInstances(t *testing.T) {
	samples := []metricSample{
		{instance: "a", value: 1, ts: 100},
		{instance: "b", value: 0, ts: 100},
		{instance: "a", value: 0, ts: 50},
	}
	got := latestPerInstance(samples)
	if len(got) != 2 {
		t.Fatalf("应为 2 个实例各一条样本，实际 %+v", got)
	}
	if got[0].instance != "a" || got[0].value != 1 || got[1].instance != "b" {
		t.Fatalf("样本顺序或取值不符：%+v", got)
	}
}

// TestLatestPerInstanceEmpty 空输入不应 panic。
func TestLatestPerInstanceEmpty(t *testing.T) {
	if got := latestPerInstance(nil); len(got) != 0 {
		t.Fatalf("空输入应返回空结果，实际 %+v", got)
	}
}
