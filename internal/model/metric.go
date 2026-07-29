// Package model 定义贯穿采集/上报/存储/查询/前端的统一数据模型。
package model

import "time"

// Metric 是单个指标采样点，贯穿 Agent 采集→上报→Server 写 VM→查询→前端全链路。
// 映射到 VictoriaMetrics 时：Name 作 __name__，Node/Labels 合并为样本标签。
type Metric struct {
	Node      string            `json:"node"`      // 主机名
	Name      string            `json:"name"`      // 指标名，如 cpu_usage、mem_used_percent
	Labels    map[string]string `json:"labels"`    // 附加标签，如 group/disk/iface
	Value     float64           `json:"value"`     // 采样值
	Timestamp int64             `json:"timestamp"` // 毫秒时间戳
}

// Point 是查询返回的单个时序数据点。
type Point struct {
	Timestamp int64   `json:"timestamp"` // 毫秒时间戳
	Value     float64 `json:"value"`     // 指标值
}

// Series 是一条时序序列（含标签集）及其数据点。
type Series struct {
	Labels map[string]string `json:"labels"` // 序列标签集
	Points []Point           `json:"points"` // 数据点列表
}

// ProcessStat 表示单个进程的资源占用快照（用于进程 TOP 榜）。
type ProcessStat struct {
	PID  int32   `json:"pid"`  // 进程 PID
	Name string  `json:"name"` // 进程名/命令
	CPU  float64 `json:"cpu"`  // CPU 占用百分比
	Mem  float64 `json:"mem"`  // 内存占用百分比
}

// ReportPayload 是 Agent 上报的请求体。
type ReportPayload struct {
	Node      string            `json:"node"`                // 主机名
	IP        string            `json:"ip"`                  // 主机 IP
	OS        string            `json:"os"`                  // 操作系统
	Arch      string            `json:"arch"`                // CPU 架构
	Group     string            `json:"group"`               // 节点分组
	Secret    string            `json:"secret,omitempty"`    // 接入授权密钥（启用 agentAuth 时校验）
	Labels    map[string]string `json:"labels,omitempty"`    // 自定义标签
	Version   string            `json:"version,omitempty"`   // Agent 版本号
	HostInfo  HostInfo          `json:"hostInfo,omitempty"`  // 主机系统与硬件信息
	Metrics   []Metric          `json:"metrics"`             // 指标列表
	Processes []ProcessStat     `json:"processes,omitempty"` // 资源占用 Top 进程
	ReportAt  int64             `json:"reportAt"`            // 上报时间（毫秒）
}

// DiskStat 表示单个真实文件系统的容量与使用率。
type DiskStat struct {
	Mountpoint  string  `json:"mountpoint"`
	Device      string  `json:"device,omitempty"`
	Fstype      string  `json:"fstype,omitempty"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"usedPercent"`
}

// OnlineUser 表示当前通过 SSH 登录的用户会话信息。
type OnlineUser struct {
	User     string `json:"user"`     // 用户名
	Terminal string `json:"terminal"` // 终端（如 pts/0、tty1）
	From     string `json:"from"`     // 登录来源（IP 或主机名）
	LoginAt  string `json:"loginAt"`  // 登录时间（格式化字符串）
}

// HostInfo 表示主机系统与硬件信息。
type HostInfo struct {
	CPUModel    string     `json:"cpuModel,omitempty"`
	CPUCores    int        `json:"cpuCores,omitempty"`
	MemoryTotal uint64     `json:"memoryTotal,omitempty"`
	DiskTotal   uint64     `json:"diskTotal,omitempty"`
	DiskUsed    uint64     `json:"diskUsed,omitempty"`
	Disks       []DiskStat `json:"disks,omitempty"`
	BootTime    int64      `json:"bootTime,omitempty"`   // 系统启动时间（Unix 秒），用于计算运行天数
	OnlineUsers []OnlineUser `json:"onlineUsers,omitempty"` // 当前在线用户会话
}

// Node 表示一个被监控节点。
type Node struct {
	Hostname  string            `json:"hostname"`           // 主机名
	DisplayName string          `json:"displayName,omitempty"` // 自定义显示名/别名（不修改 Agent 上报的真实主机名）
	IP        string            `json:"ip"`                 // 主机 IP
	OS        string            `json:"os"`                 // 操作系统
	Arch      string            `json:"arch"`               // CPU 架构
	Group     string            `json:"group"`              // 分组
	Labels    map[string]string `json:"labels,omitempty"`   // 自定义标签
	Version   string            `json:"version,omitempty"`  // Agent 版本号
	HostInfo  HostInfo          `json:"hostInfo,omitempty"` // 主机系统与硬件信息
	Status    string            `json:"status"`             // online/offline
	LastSeen  int64             `json:"lastSeen"`           // 最近心跳（毫秒）
	CreatedAt int64             `json:"createdAt"`          // 注册时间（毫秒）
}

// Group 表示节点分组。
type Group struct {
	Name        string `json:"name"`        // 分组名
	Description string `json:"description"` // 分组描述
	CreatedAt   int64  `json:"createdAt"`   // 创建时间（毫秒）
}

// Severity 告警严重级别。
type Severity string

const (
	SeverityCritical Severity = "critical" // 紧急
	SeverityWarning  Severity = "warning"  // 警告
	SeverityInfo     Severity = "info"     // 信息
)

// AlertState 告警状态。
type AlertState string

const (
	AlertStatePending  AlertState = "pending"  // 待触发（未达持续时间）
	AlertStateFiring   AlertState = "firing"   // 已触发
	AlertStateResolved AlertState = "resolved" // 已恢复
)

// AlertRule 阈值告警规则。
type AlertRule struct {
	ID        string   `json:"id"`        // 规则 ID
	Name      string   `json:"name"`      // 规则名称
	Metric    string   `json:"metric"`    // 指标名，如 cpu_usage
	Operator  string   `json:"operator"`  // 比较运算符: > >= < <= == !=
	Threshold float64  `json:"threshold"` // 阈值
	For       string   `json:"for"`       // 持续时间，如 "5m"
	Severity  Severity `json:"severity"`  // 严重级别
	Group     string   `json:"group"`     // 作用的节点分组（空表示全部）
	Notify    []string `json:"notify"`    // 通知渠道：email/webhook/dingtalk/feishu/wecom，空表示全部已启用渠道
	Enabled   bool     `json:"enabled"`   // 是否启用
	CreatedAt int64    `json:"createdAt"` // 创建时间（毫秒）
	UpdatedAt int64    `json:"updatedAt"` // 更新时间（毫秒）
}

// AlertEvent 告警事件（触发或恢复时产生）。
type AlertEvent struct {
	ID        string     `json:"id"`        // 事件 ID
	RuleID    string     `json:"ruleId"`    // 规则 ID
	RuleName  string     `json:"ruleName"`  // 规则名称
	Node      string     `json:"node"`      // 节点名
	Metric    string     `json:"metric"`    // 指标名
	Value     float64    `json:"value"`     // 触发值
	Operator  string     `json:"operator"`  // 运算符
	Threshold float64    `json:"threshold"` // 阈值
	Severity  Severity   `json:"severity"`  // 严重级别
	State     AlertState `json:"state"`     // 状态 firing/resolved
	Message   string     `json:"message"`   // 描述
	StartsAt  int64      `json:"startsAt"`  // 触发时间（毫秒）
	EndsAt    int64      `json:"endsAt"`    // 恢复时间（毫秒）
}

// NowMillis 返回当前毫秒时间戳。
func NowMillis() int64 {
	return time.Now().UnixMilli()
}
