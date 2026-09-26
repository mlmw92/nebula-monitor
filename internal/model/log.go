package model

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
}

// LogHit 是检索命中的一条日志（附带定位信息）。
type LogHit struct {
	Ts      int64  `json:"ts"`
	Node    string `json:"node"`
	Source  string `json:"source"`
	Pattern string `json:"pattern,omitempty"`
	Text    string `json:"text"`
}
