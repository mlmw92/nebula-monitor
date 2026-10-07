package logstore

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 日志后端标识。写进游标，用于拒绝「换后端后继续用旧游标」。
const (
	// BackendLocal 是自研分片落盘（默认后端，见 logstore.go）。
	BackendLocal = "local"
	// BackendVictoriaLogs 是外部后端 VictoriaLogs（见 victorialogs.go）。
	BackendVictoriaLogs = "victorialogs"
)

// ErrBackendUnavailable 表示**外部后端不可用**（连不上、超时、5xx），
// 与"查询本身有问题"（非法正则、游标不属于本后端）区分开。
//
// 为什么要分类：接口层据此回 502 而不是 400。运维看到 400 会去改查询条件，
// 而真实情况是"日志后端挂了"——错误码指错方向，比不给错误码更费时间。
var ErrBackendUnavailable = errors.New("日志后端不可用")

// normalizeLimit 把单页命中数夹到合法区间（与本地后端同一套边界）。
func normalizeLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

// LogStore 是集中日志的存储抽象（决策记录：docs/adr/0002-log-backend-abstraction.md）。
//
// 为什么等到现在才抽：只有一个适配器时，接口的形状只能来自猜测，是"假想接缝"。
// 本文件与 victorialogs.go **同时落地**，接口形状由两个真实适配器共同确定。
//
// 契约（两个适配器都必须满足，逐条由 backend_contract_test.go 覆盖）：
//
//   - 检索结果按**时间倒序**（同一毫秒内的相对顺序不作保证）；
//   - `Truncated` 表示"命中上限用尽而提前停止"，**必须回传**：静默截断会让人
//     以为"日志就这么多"，从而得出错误结论；此时游标必须非空；
//   - 游标对调用方**不透明**：前端原样回传，不解析、不构造（见 Cursor）；
//   - `Keyword` 是**子串**匹配、`Regex` 是正则匹配且优先于 Keyword、
//     `Fields` 是**精确等值**匹配（不是子串）——这三条语义是检索页的既有承诺，
//     换后端不得改变；
//   - `Nodes`/`Sources` 为空表示不限；
//   - 写入按批次给出接受/丢弃行数与丢弃原因：限额丢弃是**正常结果**，不是错误
//     （Agent 把它记进 log_dropped_total，而不是报"上传失败"）。
//
// 方法名沿用既有实现的 `Append(model.LogBatch)`，而不是 ADR 初稿里写的
// `Write([]model.LogEntry)`：ADR 自己的原则是"沿用现有方法名与模型类型"，
// 而实际调用面（receiver 上行）用的就是 Append + LogBatch。
type LogStore interface {
	// Append 写入一批日志，返回接受行数、丢弃行数与丢弃原因。
	Append(b model.LogBatch) (accepted, dropped int, reason string, err error)
	// Query 执行一次有界检索。cursor 为零值表示从头开始。
	Query(q model.LogQuery, cursor Cursor) (model.LogQueryResult, error)
	// Backend 返回后端标识（用于游标校验与界面提示）。
	Backend() string
	// Sources 列出已有日志的来源（有序），供界面的来源下拉与字段候选使用。
	Sources() []string
	// FieldNames 返回某来源已见过的结构化字段名（有序），供界面做筛选候选。
	FieldNames(source string) []string
	// Capability 报告"这个后端能做什么、存了什么"（批次 23）。
	//
	// 为什么由**实现**回答而不是接口层按配置推断：能力是后端自身的属性（本地没有索引、
	// VictoriaLogs 有），凭配置猜就成了"把配置当能力"，而这两者在运维眼里是同一句话。
	// 探测失败必须如实回报（见 StorageStats.Err），不能退化成 0。
	Capability() Capability
}

// ScanBudget 是本地后端"有界扫描"的预算：一次检索最多扫多少字节与多少行。
//
// 外部后端没有这个概念（`nil`）——不是"预算无穷大"，而是"逐文件扫描"这件事不存在。
type ScanBudget struct {
	Bytes int64
	Lines int
}

// StorageStats 是后端**实际**存了什么（探测所得，不是配置值）。
//
// 三态要分清：正常（Err 空）／没有日志（各项为 0 且 Err 空）／**探测不到**（Err 非空）。
// 把最后一类显示成 0，用户会以为日志丢了——这正是这条能力端点要避免的误导。
// 外部后端不掌握的项目（容量、保留期）留零值并由 Capability.Notes 说明，不编造。
type StorageStats struct {
	Sources   int
	Nodes     int
	OldestDay string // YYYY-MM-DD（本地后端按天分片，日期粒度就是它的天然粒度）
	NewestDay string
	Bytes     int64
	// Truncated 表示探测自身触到条目上限（目录很大时提前停）：报的是"至少这么多"。
	Truncated bool
	Err       string
}

// Capability 是一个日志后端"能做什么、存了什么"。
type Capability struct {
	Backend       string
	FullTextIndex bool
	FieldIndex    bool
	// ScanBudget 仅本地后端有；外部后端为 nil。
	ScanBudget *ScanBudget
	// Notes 是给运维看的一句话说明：能力边界、以及"哪些项为什么是空的"。
	Notes   []string
	Storage StorageStats
}

// VictoriaLogsOptions 是外部后端的连接参数。
type VictoriaLogsOptions struct {
	// Addr 是基址，如 http://127.0.0.1:9428。
	Addr string
	// QueryTimeout / WriteTimeout 为零时取默认值（30s / 10s）。
	QueryTimeout time.Duration
	WriteTimeout time.Duration
}

// BackendOptions 是日志后端的构造参数（由 server 配置映射而来）。
type BackendOptions struct {
	// Dir 是自研落盘的根目录；Backend 为 local 时留空表示**关闭该能力**。
	Dir string
	// MaxBytesPerDay 是自研落盘的单来源每日写入上限（字节）；≤0 取兜底值。
	MaxBytesPerDay int64
	// VictoriaLogs 是外部后端参数（Backend 为 victorialogs 时 addr 必填）。
	VictoriaLogs VictoriaLogsOptions
}

// NewBackend 依据配置创建日志存储后端。
//
// 返回 (nil, nil) 表示**该能力未启用**（本地后端未配置目录）：调用方据此让接口回 503，
// 而不是拿一个"永远写失败"的存储去假装启用。
//
// 注意返回值是接口：**不要把 nil 的具体类型赋给接口变量**——那会得到"非 nil 接口"，
// 调用方的 `if store == nil` 判断会失效（这个坑在本仓库的资产服务上已经踩过一次）。
func NewBackend(backend string, opts BackendOptions) (LogStore, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", BackendLocal:
		store := New(opts.Dir, opts.MaxBytesPerDay)
		if store == nil {
			return nil, nil
		}
		return store, nil
	case BackendVictoriaLogs:
		return NewVictoriaLogs(opts.VictoriaLogs)
	default:
		return nil, fmt.Errorf("不支持的日志后端: %q（可选: %s|%s）", backend, BackendLocal, BackendVictoriaLogs)
	}
}

// checkCursor 校验游标属于当前后端。
//
// 两种"没有后端标识"的情形必须区别对待：
//
//   - **零值游标**（没有任何续读位置）表示"从头开始"，任何后端都要接受——
//     接口首次打开一页时传的就是它；
//   - **有续读位置但没有后端标识**只可能来自接口引入之前（那时只有自研落盘），
//     因此按本地后端处理，让浏览器里存着的旧游标继续可用。
func checkCursor(c Cursor, backend string) error {
	if c.File == "" && c.Skip == 0 {
		return nil
	}
	if c.Backend == "" {
		if backend == BackendLocal {
			return nil
		}
		return cursorBackendError(backend)
	}
	if c.Backend != backend {
		return cursorBackendError(backend)
	}
	return nil
}

func cursorBackendError(current string) error {
	return fmt.Errorf("游标不属于当前日志后端（当前为 %s）：后端可能已切换，请重新查询", current)
}
