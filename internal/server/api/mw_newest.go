package api

// newestSampleKept 记录并判断「同一实例的新样本是否应被采纳」。
//
// 背景：中间件的存活指标（`<mw>_instance_up` 等）上带有只在采集成功时才写入的标签
// （version / role / status …），Prometheus 会把 up=0 与 up=1 视为**两条不同序列**。
// 实例离线期间写下的 up=0 在恢复后仍处于即时查询的回看窗口内，与恢复后的 up=1 并存。
// 若直接按键覆盖（last-wins）或不覆盖（first-wins），实例列表的在线状态就会取决于
// 时序库返回序列的顺序，表现为「实例已恢复但仍显示离线」。
//
// 约定：同一 node|instance 只采纳数据点时间戳最新的一条 —— 与 Redis 实例接口、
// 告警引擎的 role 聚合保持一致。
func newestSampleKept(latest map[string]int64, key string, ts int64) bool {
	if prev, ok := latest[key]; ok && ts <= prev {
		return false
	}
	latest[key] = ts
	return true
}
