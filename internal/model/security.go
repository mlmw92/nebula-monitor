package model

// 安全事件与基线检查相关模型，随 ReportPayload 上报，由 Server 端结构化存储与告警。
// 注意：敏感文件（如 /etc/shadow）仅上传 SHA256 哈希，绝不上传文件内容或口令。

// 安全事件类别常量。
const (
	// SecurityCatSSHBruteforce SSH 暴力破解检测。
	SecurityCatSSHBruteforce = "ssh_bruteforce"
	// SecurityCatSSHAudit SSH 登录审计（成功/失败来源 IP）。
	SecurityCatSSHAudit = "ssh_audit"
	// SecurityCatFIM 文件完整性监测（新增/修改/删除）。
	SecurityCatFIM = "fim"
	// SecurityCatProcessAnomaly 异常进程 / 反弹 shell 检测。
	SecurityCatProcessAnomaly = "process_anomaly"
	// SecurityCatSudoAudit sudo 提权与命令审计。
	SecurityCatSudoAudit = "sudo_audit"
	// SecurityCatBan fail2ban 封禁/解封动作事件（由 nebula 专属 action 审计文件增量采集）。
	SecurityCatBan = "cat_ban"
)

// SecurityEvent 单条安全事件，由 Agent 采集并随上报体提交，Server 端持久化与告警。
type SecurityEvent struct {
	ID             string            `json:"id"`                       // 事件唯一 ID（node|category|hash）
	Node           string            `json:"node"`                     // 节点名
	NodeIP         string            `json:"nodeIp,omitempty"`         // 节点 IP（服务器 IP）
	Category       string            `json:"category"`                 // 类别：见 SecurityCat* 常量
	Severity       Severity          `json:"severity"`                 // 严重级别
	Message        string            `json:"message"`                  // 人类可读描述
	Detail         map[string]string `json:"detail,omitempty"`         // 结构化细节（不含敏感内容）
	SourceIP       string            `json:"sourceIp,omitempty"`       // 来源 IP（SSH/sudo 场景）
	SourceLocation string            `json:"sourceLocation,omitempty"` // 来源 IP 属地（国家/省份/城市，由 Server 端经 ip2region 补全）
	User           string            `json:"user,omitempty"`           // 关联账户（不含口令）
	Timestamp      int64             `json:"timestamp"`                // 事件时间（毫秒）
	// BanInfo 仅当 Category 为 SecurityCatBan 时存在，描述封禁/解封细节。
	BanInfo *BanInfo `json:"banInfo,omitempty"`
}

// BanInfo 描述一次 fail2ban 封禁或解封动作的细节。
// 通过 nebula 专属 fail2ban action 写入受控 JSONL 审计文件，由 Agent 增量采集后上报。
type BanInfo struct {
	// Action 动作：ban（封禁）或 unban（解封）。
	Action string `json:"action"`
	// IP 被封禁或解封的来源 IP。
	IP string `json:"ip"`
	// Jail fail2ban jail 名称（如 nebula-monitor-sshd）。
	Jail string `json:"jail"`
	// Failures 触发封禁的失败次数（ban 时由 action 变量提供，可空）。
	Failures string `json:"failures,omitempty"`
	// Reason 可选的原因说明。
	Reason string `json:"reason,omitempty"`
}

// SecurityBaselineItem 单项基线检查结果。
type SecurityBaselineItem struct {
	Key      string   `json:"key"`              // 检查项标识（如 ssh_root_login）
	Name     string   `json:"name"`             // 检查项名称（中文）
	Pass     bool     `json:"pass"`             // 是否通过
	Severity Severity `json:"severity"`         // 未通过时的严重级别
	Weight   float64  `json:"weight"`           // 该项在总评分中的权重
	Score    float64  `json:"score"`            // 该项得分（0 或 weight）
	Detail   string   `json:"detail,omitempty"` // 检查说明（可选）
}

// SecurityBaseline 主机安全基线检查结果，含 0-100 合规评分与逐项明细。
type SecurityBaseline struct {
	Node        string                 `json:"node"`                  // 节点名
	NodeIP      string                 `json:"nodeIp,omitempty"`      // 节点 IP（服务器 IP）
	DisplayName string                 `json:"displayName,omitempty"` // 节点别名（用户自定义显示名，优先于主机名展示）
	Score       float64                `json:"score"`                 // 合规评分 0-100
	Items       []SecurityBaselineItem `json:"items"`                 // 各项检查结果
	CheckedAt   int64                  `json:"checkedAt"`             // 检查时间（毫秒）
}

// 受控 fail2ban 入侵防御（仅管理 nebula-monitor-sshd 专属 jail）相关模型。
//
// 设计原则（安全优先）：
//   - 服务端下发结构化、带唯一 ID 与参数的持久化指令，不使用裸字符串命令；
//   - 仅创建并管理 nebula-monitor-sshd 专属 jail 与 action，绝不覆盖用户 jail.local 或既有 jail；
//   - “停用防护”只撤销 nebula 专属配置并重载 fail2ban，不卸载 fail2ban 软件包；
//   - 白名单仅覆盖回环、真实操作人来源 IP、必要 server 地址与显式配置的应急 CIDR。

// 防护指令动作。
const (
	// DefenseActionEnable 启用（安装并启用 nebula 专属 SSH jail）。
	DefenseActionEnable = "enable"
	// DefenseActionDisable 停用（仅移除 nebula 专属配置，不卸载 fail2ban）。
	DefenseActionDisable = "disable"
	// DefenseActionStatus 仅查询状态，不改变配置。
	DefenseActionStatus = "status"
)

// 防护任务状态机。
const (
	// DefenseStateQueued 已创建，等待 Agent 在下次 report 时领取。
	DefenseStateQueued = "queued"
	// DefenseStateDelivered 已随 report 响应下发给 Agent。
	DefenseStateDelivered = "delivered"
	// DefenseStateRunning Agent 已领取并开始执行。
	DefenseStateRunning = "running"
	// DefenseStateSucceeded 执行成功。
	DefenseStateSucceeded = "succeeded"
	// DefenseStateFailed 执行失败，Message 含可读原因。
	DefenseStateFailed = "failed"
	// DefenseStateExpired 超过 ExpiresAt 仍未完成，自动回收。
	DefenseStateExpired = "expired"
)

// DefenseCommand 是 Server 下发给 Agent 的结构化防护指令。
// 兼容保留旧 ReportResponse.Command（仅用于 upgrade）字符串语义，不破坏旧 Agent。
type DefenseCommand struct {
	// ID 指令唯一标识，Agent 据此做幂等处理。
	ID string `json:"id"`
	// Type 动作：enable / disable / status。
	Type string `json:"type"`
	// Node 目标节点名。
	Node string `json:"node"`
	// IgnoreIPs 精确白名单（CIDR 或单 IP），由服务端从真实操作人来源与必要地址生成。
	IgnoreIPs []string `json:"ignoreIPs,omitempty"`
	// CreatedAt 创建时间（毫秒）。
	CreatedAt int64 `json:"createdAt"`
	// ExpiresAt 过期时间（毫秒）；过期后 Agent 不再执行且服务端回收。
	ExpiresAt int64 `json:"expiresAt"`
}

// DefenseCommandResult 是 Agent 对指令的执行结果回执。
type DefenseCommandResult struct {
	// CommandID 对应的 DefenseCommand.ID。
	CommandID string `json:"commandId"`
	// State running / succeeded / failed。
	State string `json:"state"`
	// Message 人类可读的执行摘要或失败原因。
	Message string `json:"message,omitempty"`
	// UpdatedAt 回执时间（毫秒）。
	UpdatedAt int64 `json:"updatedAt"`
}

// DefenseStatus 描述节点上 nebula 托管 SSH 防护的当前状态。
type DefenseStatus struct {
	// Node 节点名。
	Node string `json:"node"`
	// Supported 环境是否满足（Linux + systemd + sshd + fail2ban 可安装）。
	Supported bool `json:"supported"`
	// Installed fail2ban 是否已安装（不论是否由 nebula 托管）。
	Installed bool `json:"installed"`
	// Running fail2ban 服务是否运行。
	Running bool `json:"running"`
	// ManagedJail nebula 专属 jail 是否已启用。
	ManagedJail bool `json:"managedJail"`
	// Jails 当前启用的 jail 列表（含非 nebula 托管项，仅展示）。
	Jails []string `json:"jails,omitempty"`
	// BannedIPs 当前 nebula jail 仍处于封禁状态的 IP 快照；不代表历史封禁事件。
	BannedIPs []string `json:"bannedIps,omitempty"`
	// FirewallAction 实际加载的 Fail2Ban 原生防火墙 action 名称。
	FirewallAction string `json:"firewallAction,omitempty"`
	// FirewallVerified 表示已确认 jail 加载了原生防火墙 action，而非仅审计 action。
	FirewallVerified bool `json:"firewallVerified"`
	// Message 不支持或异常时的可读说明。
	Message string `json:"message,omitempty"`
	// UpdatedAt 状态采集时间（毫秒）。
	UpdatedAt int64 `json:"updatedAt"`
}

// ClientCapability 描述 Agent 端能力，用于前端判断是否展示防护操作入口。
// 由 Agent 在 ReportPayload.Capabilities 中上报，新增字段默认 false（旧 Agent 兼容）。
type ClientCapability struct {
	// Defense 是否支持结构化防护指令（enable/disable/status）。
	Defense bool `json:"defense,omitempty"`
	// LogSources 是 Agent **本机已配置**的日志来源 id 清单（C2 集中日志）。
	//
	// Server 用它判断上行的日志来源是否属于该节点：节点声明过清单之后，
	// 清单之外的来源会被拒（避免悄悄往新的目录里写）。
	LogSources []string `json:"logSources,omitempty"`
}

// ReportPayload 扩展：新增 Capabilities 与 DefenseResult 字段（不影响既有字段）。
// 见 metric.go 中 ReportPayload 的定义，本类型仅作说明已在 metric.go 同步修改。
