package model

import "regexp"

// C2 集中日志的共享数据类型（Agent 采集侧与 Server 存储/检索侧共用）。
//
// 设计取舍：日志行里**不携带**「来源路径」这类只有被监控机才知道的细节，
// 只带「来源 id + 节点 + 时间 + 命中模式 + 文本」——检索页需要的最小集合，
// 也避免把被监控机的目录结构暴露到界面上。

// LogLine 是一条（或合并后的多行）日志。
type LogLine struct {
	// Ts 是日志行的时间戳（毫秒）。无法从行内解析时用采集时刻兜底，
	// 因此它表达的是「近似时间」，排序够用、不能当权威时间源。
	Ts int64 `json:"ts"`
	// Pattern 是命中的模式名（来自配置的 patterns[].name）；全量上传（all: true）时为空。
	Pattern string `json:"pattern,omitempty"`
	// Text 是日志内容（单行，或合并后的多行；超长会被截断）。
	Text string `json:"text"`
}

// LogBatch 是一次上行请求携带的日志批次。
type LogBatch struct {
	Node   string    `json:"node"`            // 节点名（写入存储文件名，供检索按节点过滤）
	Group  string    `json:"group,omitempty"` // 节点分组（便于按范围检索）
	Source string    `json:"source"`          // 来源 id（对应 agent.yaml 里的 logSources[].id）
	Lines  []LogLine `json:"lines"`           // 本批日志行
}

// LogAppendResult 是一次落盘的结果（Server 回给 Agent，Agent 据此把丢弃记进指标）。
type LogAppendResult struct {
	Accepted int    `json:"accepted"`
	Dropped  int    `json:"dropped"`
	Reason   string `json:"reason,omitempty"` // 丢弃原因，写入 log_dropped_total 的 reason 标签
}

// LogSinkResult 是 Agent 侧「交给上传实现」的结果。
//
// 与 LogAppendResult 分开的原因：这里多一种「服务端都没收到」的情形（网络失败），
// 由实现返回 error 表达；而限额丢弃（每日上限、限速）是**正常的**结果，必须计数但不算故障——
// 否则限速一旦触发，日志里会每次刷一条「上传失败」，把真正的问题淹掉。
type LogSinkResult struct {
	Dropped int
	Reason  string
}

// LogSourceNamePattern 是日志来源名的合法形态：小写字母开头，只含小写字母/数字/下划线。
//
// 它同时是存储分片名（会成为文件路径的一段）与指标前缀，因此 Server 与 Agent 必须用同一条规则：
// 两边不一致就会出现「Agent 认为合法、Server 拒绝」这种只在现场才暴露的问题。
var LogSourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// IsValidLogSourceName 判断来源名是否合法（见 LogSourceNamePattern）。
func IsValidLogSourceName(s string) bool {
	return LogSourceNamePattern.MatchString(s)
}

// LogPatternNamePattern 是日志模式名的合法形态。
//
// 为什么必须限制字符集：模式名会**拼进指标名**（`<来源>_log_<模式>_total`，见下面的说明），
// 非法字符会产出无法查询的指标名——而「指标名写错」的症状是静默无数据，不是报错。
var LogPatternNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)

// IsValidLogPatternName 判断模式名是否合法（见 LogPatternNamePattern）。
func IsValidLogPatternName(s string) bool {
	return LogPatternNamePattern.MatchString(s)
}

// LogQuery 是检索请求（Server 侧解析查询参数后传入存储层）。
type LogQuery struct {
	From    int64    // 起始时间（毫秒，含）
	To      int64    // 结束时间（毫秒，含）
	Keyword string   // 子串匹配（与 Regex 二选一，Regex 优先）
	Regex   string   // 正则匹配
	Nodes   []string // 节点过滤（为空表示不限）
	Sources []string // 来源过滤（为空表示不限）
	Limit   int      // 命中上限（达到即停止扫描并标记 truncated）
}

// LogQueryResult 是检索结果。
type LogQueryResult struct {
	Lines []LogHit `json:"lines"`
	// Truncated 表示「命中上限用尽而提前停止」——必须回给前端。
	// 静默截断会让人以为「日志就这么多」，从而得出错误结论。
	Truncated bool `json:"truncated"`
	// Cursor 是继续翻页的游标（truncated 为 false 时为空）。
	// 不透明字符串：前端只需原样回传，不解析、不构造。
	Cursor string `json:"cursor,omitempty"`
	// 以下三项是扫描诊断：解释「为什么没有结果」——是确实没有，还是扫描预算先用完了。
	ScannedBytes int64 `json:"scannedBytes"`
	ScannedLines int64 `json:"scannedLines"`
	Files        int   `json:"files"` // 参与扫描的文件数
}

// LogHit 是检索命中的一条日志（附带定位信息）。
type LogHit struct {
	Ts      int64  `json:"ts"`
	Node    string `json:"node"`
	Source  string `json:"source"`
	Pattern string `json:"pattern,omitempty"`
	Text    string `json:"text"`
}
