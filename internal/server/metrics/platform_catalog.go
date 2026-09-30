package metrics

// init 注册「平台自身产出」的指标：主动探测（拨测 / 端口 / 证书）与集中日志采集管道。
//
// 与 middleware_catalog.go 的区别在于**谁在测**：中间件指标是从被监控对象身上读出来的，
// 这里的指标是平台主动发起的探测结果，或采集管道自己的运行状况。
//
// 产出方分两处：拨测在 `internal/server/dialtest`（Server 侧定时任务），
// 端口探测与日志在 `internal/agent/collector`（`port.go` / `logcollect.go`）。
func init() {
	// —— 拨测（HTTP / HTTPS / TCP / ICMP）——
	Register(MetricMeta{Name: "dial_test_up", Title: "拨测成功率", Category: CatProbe, Unit: "", Chart: ChartGauge,
		WorseWhen: WorseLow, Desc: "1=拨测成功、0=失败。任务在「拨测」页维护，按任务维度上报"})
	Register(MetricMeta{Name: "dial_test_latency", Title: "拨测延迟", Category: CatProbe, Unit: "ms", Chart: ChartLine,
		WorseWhen: WorseHigh, Desc: "端到端耗时（含 DNS 与 TLS 握手）"})
	Register(MetricMeta{Name: "dial_test_cert_expiry", Title: "证书剩余天数", Category: CatProbe, Unit: "天", Chart: ChartLine,
		WorseWhen: WorseLow, Desc: "仅 HTTPS 拨测产出。注意方向：越**小**越糟，阈值应配 `<`（如 < 15 天）——这条最常见的配错是设成 `>`，结果是规则永不触发"})
	Register(MetricMeta{Name: "port_up", Title: "端口可达", Category: CatProbe, Unit: "", Chart: ChartGauge,
		WorseWhen: WorseLow, Desc: "Agent 侧 TCP 探测：1=可建立连接、0=不可达"})
	Register(MetricMeta{Name: "port_latency", Title: "端口探测延迟", Category: CatProbe, Unit: "ms", Chart: ChartLine, WorseWhen: WorseHigh})

	// —— 集中日志 ——
	Register(MetricMeta{Name: "log_up", Title: "日志采集可用(1=正常)", Category: CatLog, Unit: "", Chart: ChartGauge,
		WorseWhen: WorseLow, Desc: "该来源本轮所有路径都读得到才为 1；为 0 是「配了却没有数据」的第一现场信号"})
	Register(MetricMeta{Name: "log_lines_total", Title: "日志读取行数(每轮)", Category: CatLog, Unit: "行", Chart: ChartLine,
		WorseWhen: WorseHigh, Desc: "**每轮采集的增量**而非累计值，因此阈值规则直接比较原始值即可，不要再套 rate/increase"})
	Register(MetricMeta{Name: "log_dropped_total", Title: "日志丢弃行数", Category: CatLog, Unit: "行", Chart: ChartLine,
		WorseWhen: WorseHigh, Desc: "带 reason 维度（单轮上限、偏移异常等）。丢弃按正常结果处理——它会增长，但不该是常态"})
	Register(MetricMeta{Name: "log_<模式>_total", Title: "日志模式匹配行数", Category: CatLog, Unit: "行", Chart: ChartLine,
		WorseWhen: WorseHigh, Dynamic: true,
		Desc: "名字由「来源 ID + 模式名」拼成：<来源>_log_<模式>_total，例如来源 applog、模式 err → applog_log_err_total。选了这个占位名请把 <模式> 替换成实际的模式名，否则规则查不到数据且不会报错"})
}
