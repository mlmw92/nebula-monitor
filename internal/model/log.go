package model

import (
	"context"
	"regexp"
	"strings"
)

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
//
// **一批 = 一个文件**（采集器逐文件读、逐文件上行），因此"这批日志来自哪个容器"
// 只需要在批次上写一次，不必逐行重复。
type LogBatch struct {
	Node   string    `json:"node"`            // 节点名（写入存储文件名，供检索按节点过滤）
	Group  string    `json:"group,omitempty"` // 节点分组（便于按范围检索）
	Source string    `json:"source"`          // 来源 id（对应 agent.yaml 里的 logSources[].id）
	Lines  []LogLine `json:"lines"`           // 本批日志行
	// Origin 是容器身份（仅 logSources[].podLogs 开启的来源才有）。
	//
	// **只上报解析出的身份，不上报路径**：路径本身携带被监控机的目录结构，
	// 而"这条日志属于哪个 Pod"才是检索与联动需要的（见文件头隐私说明）。
	Origin *LogOrigin `json:"origin,omitempty"`
}

// LogOrigin 是一条日志所属的容器身份。
//
// 三个字段都必须来自**可信来源**（采集侧从 kubelet 的固定路径格式解析），
// 服务端会做字符集与长度校验后才落库：这些值会进入存储、界面与资产联动，
// 一条日志的正文绝不允许决定自己"属于哪个 Pod"（否则可以伪造归属，
// 把自己的日志标到别人的资产上）。
type LogOrigin struct {
	Namespace string `json:"namespace,omitempty"`
	Pod       string `json:"pod,omitempty"`
	Container string `json:"container,omitempty"`
}

// Empty 判断身份是否为空（三个字段都空即视为没有身份）。
func (o *LogOrigin) Empty() bool {
	return o == nil || (o.Namespace == "" && o.Pod == "" && o.Container == "")
}

// LogSink 接收某个文件本轮读到的行（采集器**逐文件**调用它：一批 = 一个文件）。
//
// 放在 model 而不是采集器包里：它是「采集 → 上行」两侧共同的契约，
// 上行实现（logship）不该为了拿一个函数类型去依赖采集器。
//
// origin 为 nil 表示普通文件日志；非 nil 时表示这些行来自该容器。
// 返回值区分两类「没上传成功」：限额丢弃（Dropped > 0，正常结果、计入 reason 标签）
// 与真正失败（error，计入 reason=unreachable）。
type LogSink func(ctx context.Context, source string, origin *LogOrigin, lines []LogLine) (LogSinkResult, error)

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

// LogPatternMetricName 返回某来源某模式对应的指标名：`<来源>_log_<模式>_total`。
//
// 放在 model 里而不是各侧拼字符串：Agent 侧**产出**这个指标、Server 侧用它**建告警规则**，
// 两边拼法必须逐字一致——不一致的症状是"规则配好了却永远没有数据"，
// 而没有任何一处会报错（这正是模式名必须限制字符集的原因，见 LogPatternNamePattern）。
func LogPatternMetricName(source, pattern string) string {
	return source + "_log_" + pattern + "_total"
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
	// Fields 是结构化字段的**精确**匹配（键值全等才命中，为空表示不限）。
	//
	// 与 Keyword 的分工：关键词在原文里找子串，字段在解析出的键值上做等值判断——
	// 「status=500」用关键词会命中任何含该串的行（包括别的字段的值），用字段才是语义正确的筛法。
	Fields map[string]string
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
	// Fields 是从原文里提取出的结构化字段（JSON 对象或 key=value 对，见 logparse）。
	// 提不到就是 nil——**原文永远在 Text 里**，字段只是附加的检索维度，不是替代品。
	Fields map[string]string `json:"fields,omitempty"`
	// Origin 是这条日志所属的容器身份（普通文件日志为空）。
	// 落库时随行一起写：检索时要按它把行标到 Pod 资产上，事后无法从别处补。
	Origin *LogOrigin `json:"origin,omitempty"`
}

// LogOriginPartPattern 是容器身份各段的合法形态：k8s 的 DNS-1123 标签/子域
// （小写字母数字与 `-`、`.`，首尾必须是字母数字）。长度上限见 MaxLogOriginPartLen。
//
// 与来源名/字段名同一取向：**服务端与采集侧必须用同一条规则**，且这条规则要足够严——
// 这些值会进入存储、界面与资产联动，宽松的字符集等于把注入面直接开到台账上。
var LogOriginPartPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

// MaxLogOriginPartLen 是身份各段的长度上限（k8s 的 DNS 子域上限 253）。
const MaxLogOriginPartLen = 253

// NormalizeLogOrigin 校验并规范化容器身份。
//
// 返回 (nil, true) 表示"没有身份"——这是**正常情况**（普通文件日志不该被拒绝）。
// 返回 (nil, false) 表示身份非法：调用方应当**拒绝这批日志**，而不是"丢掉身份继续收"。
// 为什么宁可拒绝：身份决定这条日志被标到哪个 Pod 资产上，一个不可信的身份比没有身份
// 更危险（伪造归属会让运维在别人的资产下看到自己的日志）；而合法的 Agent 永远不会发出非法身份。
//
// 大小写先折叠为小写：k8s 名字本就是小写，个别运行时/镜像上报大写时按同一口径归一是
// 无害的，但**校验必须发生在归一之后**，否则等于放宽了字符集。
func NormalizeLogOrigin(o *LogOrigin) (*LogOrigin, bool) {
	if o == nil {
		return nil, true
	}
	out := LogOrigin{
		Namespace: strings.ToLower(strings.TrimSpace(o.Namespace)),
		Pod:       strings.ToLower(strings.TrimSpace(o.Pod)),
		Container: strings.ToLower(strings.TrimSpace(o.Container)),
	}
	if out.Namespace == "" && out.Pod == "" && out.Container == "" {
		return nil, true
	}
	for _, part := range []string{out.Namespace, out.Pod, out.Container} {
		if part == "" {
			// 只给一半身份（例如有 Pod 没 namespace）：无法唯一定位，视为非法。
			// 定位不到资产是可接受的降级，**定位错**不是。
			return nil, false
		}
		if len(part) > MaxLogOriginPartLen || !LogOriginPartPattern.MatchString(part) {
			return nil, false
		}
	}
	return &out, true
}

// LogFieldNamePattern 是结构化字段名的合法形态：字母或下划线开头，允许点号（嵌套）与连字符。
//
// 与来源名/模式名一样，服务端与检索侧必须用同一条规则：字段名会出现在查询参数里，
// 两边不一致就会出现「写入时接受、查询时匹配不上」这种只在现场才暴露的问题。
var LogFieldNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)

// IsValidLogFieldName 判断字段名是否合法（见 LogFieldNamePattern）。
func IsValidLogFieldName(s string) bool {
	return LogFieldNamePattern.MatchString(s)
}
