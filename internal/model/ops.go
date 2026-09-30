package model

import "regexp"

// 内置下行动作标识。
//
// 放在 model 而不是各自一侧，是因为 Server 与 Agent 必须用**同一批字面量**：
// Server 用它们校验与下发，Agent 用它们分派。各写一份的下场是名字对不上，
// 而名字对不上的症状不是报错，是 Agent 回一句"未知动作"、任务永远不成功。
const (
	// OpsKindNodeDiagnostics 节点诊断包（只读）。
	OpsKindNodeDiagnostics = "node.diagnostics"
	// OpsKindSvcStatus 查询 systemd 单元状态（只读）。
	OpsKindSvcStatus = "svc.status"
	// OpsKindSvcRestart 重启 systemd 单元（写操作，需目标机器显式放行）。
	OpsKindSvcRestart = "svc.restart"
)

// OpsUnitPattern 是 systemd 单元名的白名单字符集，Server 与 Agent 共用。
//
// 两端都要校验，不是重复劳动：Server 校验保证"下发的东西是干净的"，
// Agent 校验保证"即使 Server 版本不对/被绕过，本机也不会执行意外的东西"——
// Agent 以 root 运行，它不能假定中心一定是对的。
//
// 只允许字母/数字/@/./_/- 并强制 .service 结尾：单元名会被拼进 exec 的参数（不经过 shell），
// 但不限制字符集就有可能把 `--now` 这类**选项**伪装成单元名传进去。
var OpsUnitPattern = regexp.MustCompile(`^[A-Za-z0-9@._-]+\.service$`)

// 下行操作（ops）通道的协议模型。
//
// 形态与既有的 defense / upgrade 指令链路同构：Server 把指令塞进 Agent **上报请求的响应体**，
// Agent 执行后随下一轮上报把结果送回。这一形态由 ADR-0003 决定（拒绝为下行新建长连接/隧道）：
// Agent 只做出站连接，Server 不需要知道 Agent 的可达地址，也不需要开放入站端口。
// 代价是「指令延迟 = 一个采集周期」（默认 15s），对运维动作完全够用。
//
// 兼容性（两端可独立升级）：
//   - 旧 Agent 的 ReportResponse 只解析已知字段，收到 ops 视为忽略——不执行、不报错；
//   - 旧 Server 不返回 ops——新 Agent 永远没有待执行指令。
//
// 指令本身**不是任意命令**：Kind 必须命中服务端动作目录（internal/server/ops 的白名单），
// 参数按目录里的规格校验；执行与否还取决于 Agent 本机的 guards.ops 开关。

// OpsCommand 是下发给单个 Agent 的一条操作指令。
type OpsCommand struct {
	ID   string `json:"id"`
	Node string `json:"node,omitempty"`
	// Kind 是动作标识（如 "svc.status"），必须命中服务端动作目录。
	Kind string `json:"kind"`
	// Params 是动作参数（如 unit=nginx）。值由服务端按目录规格校验后才下发。
	Params    map[string]string `json:"params,omitempty"`
	CreatedAt int64             `json:"createdAt,omitempty"`
	// ExpireAt 是过期时刻（毫秒）：超过它的指令不再下发（由 Take 与 ExpireOverdue 回收为 expired）。
	ExpireAt int64 `json:"expireAt,omitempty"`
}

// OpsResult 是 Agent 回执的执行结果。
type OpsResult struct {
	CommandID string `json:"commandId"`
	// State 取值见 OpsState*（running / succeeded / failed）。
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// Data 是动作产出的结构化结果（分节文本），键与值都应是可直接展示的短文本。
	Data map[string]string `json:"data,omitempty"`
	At   int64             `json:"at,omitempty"`
	// DurationMs 是执行耗时（毫秒），用于识别"指令发出去了但执行卡住"。
	DurationMs int64 `json:"durationMs,omitempty"`
}

// 操作任务状态机：queued → delivered → running → succeeded / failed / expired。
//
// 与防护任务（model.DefenseState*）刻意保持同一套字面量：两者都是「下行指令的生命周期」，
// 前端与运维只该学一套状态词。
const (
	// OpsStateQueued 已创建，等待 Agent 下一轮上报时领取。
	OpsStateQueued = "queued"
	// OpsStateDelivered 已被某轮上报领取，等待 Agent 开始执行。
	OpsStateDelivered = "delivered"
	// OpsStateRunning Agent 已开始执行。
	OpsStateRunning = "running"
	// OpsStateSucceeded 执行成功。
	OpsStateSucceeded = "succeeded"
	// OpsStateFailed 执行失败（失败原因在 Message 里）。
	OpsStateFailed = "failed"
	// OpsStateExpired 超时未完成，已被回收——**不是**"任务还在排队"。
	OpsStateExpired = "expired"
)
