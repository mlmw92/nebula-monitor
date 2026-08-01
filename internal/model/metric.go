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
	Node           string            `json:"node"`                     // 主机名
	IP             string            `json:"ip"`                       // 主机 IP
	OS             string            `json:"os"`                       // 操作系统
	Arch           string            `json:"arch"`                     // CPU 架构
	Group          string            `json:"group"`                    // 节点分组
	Secret         string            `json:"secret,omitempty"`         // 接入授权密钥（启用 agentAuth 时校验）
	Labels         map[string]string `json:"labels,omitempty"`         // 自定义标签
	Version        string            `json:"version,omitempty"`        // Agent 版本号
	BinSHA256      string            `json:"binSHA256,omitempty"`      // Agent 二进制自身 SHA256（升级成功判定依据，与版本号解耦）
	HostInfo       HostInfo          `json:"hostInfo,omitempty"`       // 主机系统与硬件信息
	Metrics        []Metric          `json:"metrics"`                  // 指标列表
	Processes      []ProcessStat     `json:"processes,omitempty"`      // 资源占用 Top 进程
	RedisInstances    []RedisInstance    `json:"redisInstances,omitempty"`    // Redis 实例元信息（不含密码）
	MySQLInstances    []MySQLInstance    `json:"mysqlInstances,omitempty"`    // MySQL 实例元信息
	PostgresInstances []PostgresInstance `json:"postgresInstances,omitempty"` // PostgreSQL 实例元信息
	NginxInstances    []NginxInstance    `json:"nginxInstances,omitempty"`    // Nginx 实例元信息
	KafkaInstances    []KafkaInstance    `json:"kafkaInstances,omitempty"`    // Kafka 实例元信息
	DockerInstances   []DockerInstance   `json:"dockerInstances,omitempty"`   // Docker 容器元信息
	RocketMQInstances []RocketMQInstance `json:"rocketmqInstances,omitempty"` // RocketMQ 实例元信息
	K8sInstances      []K8sInstance      `json:"k8sInstances,omitempty"`      // Kubernetes 集群元信息
	ReportAt          int64              `json:"reportAt"`                    // 上报时间（毫秒）
}

// RedisInstanceConfig 是 Agent 本地配置的 Redis 实例连接信息。
// 密码字段 Password 的 JSON tag 为 "-"，确保不上报 Server（密码不离开主机）。
type RedisInstanceConfig struct {
	Name         string `yaml:"name"`         // 实例别名（用于展示）
	Addr         string `yaml:"addr"`         // Redis 地址 host:port
	Password     string `yaml:"password"`     // 认证密码（不上报，json:"-")
	DB           int    `yaml:"db"`           // 采集的 DB 号（直连模式可选）
	Topology     string `yaml:"topology"`     // standalone|replication|sentinel|cluster
	SentinelName string `yaml:"sentinelName"` // 哨兵模式下监控的 master 名称
	ExporterURL  string `yaml:"exporterURL"`  // exporter 模式的 /metrics URL（留空走直连）
}

// RedisInstance 是上报给 Server 的 Redis 实例元信息（不含密码），供 Web 只读展示。
type RedisInstance struct {
	Instance  string `json:"instance"`            // 实例地址 host:port
	Name      string `json:"name"`                // 实例别名
	Node      string `json:"node"`                // 采集 Agent 节点名
	Role      string `json:"role"`                // master|slave|sentinel|replica
	Topology  string `json:"topology"`            // standalone|replication|sentinel|cluster
	Group     string `json:"group"`               // 拓扑分组名（cluster/sentinel 用 cfg.Name，standalone 同名归组）
	ReplicaOf string `json:"replicaOf,omitempty"` // 主从关系：slave/replica 的 master 地址
	Version   string `json:"version"`             // Redis 版本（来自 INFO Server）
	Up        bool   `json:"up"`                  // 是否可达
}

// ---- MySQL ----

// MySQLInstanceConfig 是 Agent 本地配置的 MySQL 实例连接信息。密码不上报。
type MySQLInstanceConfig struct {
	Name        string `yaml:"name"`        // 实例别名
	Addr        string `yaml:"addr"`        // MySQL 地址 host:port
	User        string `yaml:"user"`        // 用户名
	Password    string `yaml:"password"`    // 密码（json:"-"）
	Topology    string `yaml:"topology"`    // standalone|replication
	ExporterURL string `yaml:"exporterURL"` // exporter 模式的 /metrics URL（留空走直连）
}

// MySQLInstance 是上报给 Server 的 MySQL 实例元信息。
type MySQLInstance struct {
	Instance  string `json:"instance"`  // host:port
	Name      string `json:"name"`      // 实例别名
	Node      string `json:"node"`      // 采集 Agent 节点名
	Role      string `json:"role"`      // master|slave
	Topology  string `json:"topology"`  // standalone|replication
	Group     string `json:"group"`     // 分组名
	ReplicaOf string `json:"replicaOf,omitempty"`
	Version   string `json:"version"`   // MySQL 版本
	Up        bool   `json:"up"`
}

// ---- PostgreSQL ----

// PostgresInstanceConfig 是 Agent 本地配置的 PostgreSQL 实例连接信息。密码不上报。
type PostgresInstanceConfig struct {
	Name        string `yaml:"name"`
	Addr        string `yaml:"addr"`      // host:port
	Database    string `yaml:"database"`  // 连接的数据库名
	User        string `yaml:"user"`
	Password    string `yaml:"password"`  // json:"-"
	SSLMode     string `yaml:"sslMode"`   // disable|require|verify-ca|verify-full
	Topology    string `yaml:"topology"`  // standalone|replication
	ExporterURL string `yaml:"exporterURL"`
}

// PostgresInstance 是上报给 Server 的 PostgreSQL 实例元信息。
type PostgresInstance struct {
	Instance  string `json:"instance"`
	Name      string `json:"name"`
	Node      string `json:"node"`
	Role      string `json:"role"`     // master|standby
	Topology  string `json:"topology"`
	Group     string `json:"group"`
	ReplicaOf string `json:"replicaOf,omitempty"`
	Version   string `json:"version"`  // PostgreSQL 版本
	Database  string `json:"database"` // 连接的数据库名
	Up        bool   `json:"up"`
}

// ---- Nginx ----

// NginxInstanceConfig 是 Agent 本地配置的 Nginx 实例连接信息。
type NginxInstanceConfig struct {
	Name        string `yaml:"name"`
	Addr        string `yaml:"addr"`        // host:port
	StatusPath  string `yaml:"statusPath"`  // stub_status 路径，如 /nginx_status
	ExporterURL string `yaml:"exporterURL"` // VTS exporter URL
}

// NginxInstance 是上报给 Server 的 Nginx 实例元信息。
type NginxInstance struct {
	Instance string `json:"instance"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Group    string `json:"group"`
	Version  string `json:"version"` // nginx 版本（从响应头或 stub_status 获取）
	Up       bool   `json:"up"`
}

// ---- Kafka ----

// KafkaInstanceConfig 是 Agent 本地配置的 Kafka 实例连接信息。
type KafkaInstanceConfig struct {
	Name        string `yaml:"name"`
	Addr        string `yaml:"addr"`        // Broker 地址 host:port
	Version     string `yaml:"version"`     // Kafka 版本（用于展示）
	ExporterURL string `yaml:"exporterURL"` // JMX exporter URL
}

// KafkaInstance 是上报给 Server 的 Kafka 实例元信息。
type KafkaInstance struct {
	Instance string `json:"instance"` // Broker 地址
	Name     string `json:"name"`
	Node     string `json:"node"`
	Group    string `json:"group"`   // 集群分组名
	Role     string `json:"role"`    // broker|controller
	Version  string `json:"version"` // Kafka 版本
	Up       bool   `json:"up"`
}

// ---- Docker ----

// DockerInstanceConfig 是 Agent 本地配置的 Docker 连接信息。
type DockerInstanceConfig struct {
	Name string `yaml:"name"`
	Addr string `yaml:"addr"` // Docker daemon 地址，如 unix:///var/run/docker.sock 或 tcp://10.0.0.1:2375
}

// DockerInstance 是上报给 Server 的 Docker 容器元信息。
type DockerInstance struct {
	Instance     string `json:"instance"`     // 容器 ID（短 ID）
	Name         string `json:"name"`         // 容器名
	Node         string `json:"node"`
	Group        string `json:"group"`
	Image        string `json:"image"`        // 镜像名
	Status       string `json:"status"`       // running|paused|exited
	Up           bool   `json:"up"`
}

// ---- RocketMQ ----

// RocketMQInstanceConfig 是 Agent 本地配置的 RocketMQ 实例连接信息。
type RocketMQInstanceConfig struct {
	Name        string `yaml:"name"`
	Addr        string `yaml:"addr"`        // NameServer 地址 host:port
	ExporterURL string `yaml:"exporterURL"` // Prometheus exporter URL
}

// RocketMQInstance 是上报给 Server 的 RocketMQ 实例元信息。
type RocketMQInstance struct {
	Instance string `json:"instance"` // NameServer/Broker 地址
	Name     string `json:"name"`
	Node     string `json:"node"`
	Group    string `json:"group"`   // 集群名
	Role     string `json:"role"`    // nameserver|broker
	Version  string `json:"version"` // RocketMQ 版本
	Up       bool   `json:"up"`
}

// ---- Kubernetes ----

// K8sInstanceConfig 是 Agent 本地配置的 Kubernetes 集群连接信息。
// Kubeconfig/Token 等凭据仅存 Agent 本地，不上报 Server（K8sInstance 结构本身不含凭据字段）。
type K8sInstanceConfig struct {
	Name          string `yaml:"name"`          // 集群别名（用于展示）
	APIServer     string `yaml:"apiServer"`     // apiserver 地址 https://host:6443；留空则从 kubeconfig 取
	Kubeconfig    string `yaml:"kubeconfig"`    // kubeconfig 文件路径（仅存本地，不上报）
	Token         string `yaml:"token"`         // ServiceAccount Bearer Token（仅存本地，不上报）
	InsecureTLS   bool   `yaml:"insecureTLS"`   // 是否跳过 apiserver 证书校验
	MetricsServer bool   `yaml:"metricsServer"` // 是否启用 metrics-server 采集 Node 资源用量
	ExporterURL   string `yaml:"exporterURL"`   // kube-state-metrics 的 /metrics URL（留空走直连）
}

// K8sInstance 是上报给 Server 的 Kubernetes 集群元信息（不含凭据），供 Web 只读展示。
type K8sInstance struct {
	Instance string `json:"instance"` // apiserver 地址（唯一键）
	Name     string `json:"name"`     // 集群别名
	Node     string `json:"node"`     // 采集 Agent 节点名
	Group    string `json:"group"`    // 集群分组名（= cfg.Name）
	Version  string `json:"version"`  // K8s server 版本（来自 /version）
	Up       bool   `json:"up"`       // apiserver 是否可达
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
	CPUModel    string       `json:"cpuModel,omitempty"`
	CPUCores    int          `json:"cpuCores,omitempty"`
	MemoryTotal uint64       `json:"memoryTotal,omitempty"`
	DiskTotal   uint64       `json:"diskTotal,omitempty"`
	DiskUsed    uint64       `json:"diskUsed,omitempty"`
	Disks       []DiskStat   `json:"disks,omitempty"`
	BootTime    int64        `json:"bootTime,omitempty"`    // 系统启动时间（Unix 秒），用于计算运行天数
	OnlineUsers []OnlineUser `json:"onlineUsers,omitempty"` // 当前在线用户会话
}

// Node 表示一个被监控节点。
type Node struct {
	Hostname    string            `json:"hostname"`              // 主机名
	DisplayName string            `json:"displayName,omitempty"` // 自定义显示名/别名（不修改 Agent 上报的真实主机名）
	IP          string            `json:"ip"`                    // 主机 IP
	OS          string            `json:"os"`                    // 操作系统
	Arch        string            `json:"arch"`                  // CPU 架构
	Group       string            `json:"group"`                 // 分组
	Labels      map[string]string `json:"labels,omitempty"`      // 自定义标签
	Version     string            `json:"version,omitempty"`     // Agent 版本号
	HostInfo    HostInfo          `json:"hostInfo,omitempty"`    // 主机系统与硬件信息
	Status      string            `json:"status"`                // online/offline
	LastSeen    int64             `json:"lastSeen"`              // 最近心跳（毫秒）
	CreatedAt   int64             `json:"createdAt"`             // 注册时间（毫秒）
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
	ID          string   `json:"id"`          // 规则 ID
	Name        string   `json:"name"`        // 规则名称
	Metric      string   `json:"metric"`      // 指标名，如 cpu_usage
	Operator    string   `json:"operator"`    // 比较运算符: > >= < <= == !=
	Threshold   float64  `json:"threshold"`   // 阈值
	For         string   `json:"for"`         // 持续时间，如 "5m"
	Severity    Severity `json:"severity"`    // 严重级别
	Group       string   `json:"group"`       // 作用的节点分组（空表示全部）
	Scope       string   `json:"scope"`       // 应用范围：all（全部主机，默认）或 specified（指定主机）
	Nodes       []string `json:"nodes"`       // 指定主机列表（scope=specified 时生效）
	Notify      []string `json:"notify"`      // 通知渠道：email/webhook/dingtalk/feishu/wecom，空表示全部已启用渠道
	Enabled     bool     `json:"enabled"`     // 是否启用
	Silenced    bool     `json:"silenced"`    // 是否静默（临时停止评估触发）
	SilenceUntil int64   `json:"silenceUntil,omitempty"` // 静默截止时间（毫秒），0 表示不限时
	CreatedAt   int64    `json:"createdAt"`   // 创建时间（毫秒）
	UpdatedAt   int64    `json:"updatedAt"`   // 更新时间（毫秒）
}

// MaintenanceWindow 维护窗口，启用时全局抑制告警通知（事件仍记录）。
type MaintenanceWindow struct {
	Enabled bool   `json:"enabled"` // 是否启用
	Start   int64  `json:"start"`   // 开始时间（毫秒）
	End     int64  `json:"end"`     // 结束时间（毫秒）
	Reason  string `json:"reason"`  // 维护原因
}

// AlertEvent 告警事件（触发或恢复时产生）。
type AlertEvent struct {
	ID        string     `json:"id"`                 // 事件 ID
	RuleID    string     `json:"ruleId"`             // 规则 ID
	RuleName  string     `json:"ruleName"`           // 规则名称
	Node      string     `json:"node"`               // 节点名
	NodeIP    string     `json:"nodeIp,omitempty"`   // 节点 IP（便于在通知渠道中展示）
	Instance  string     `json:"instance,omitempty"` // Redis 等多实例指标的实例标签
	Metric    string     `json:"metric"`             // 指标名
	Value     float64    `json:"value"`              // 触发值
	Operator  string     `json:"operator"`           // 运算符
	Threshold float64    `json:"threshold"`          // 阈值
	Severity  Severity   `json:"severity"`           // 严重级别
	State       AlertState `json:"state"`                  // 状态 firing/resolved
	Message     string     `json:"message"`                // 描述
	StartsAt    int64      `json:"startsAt"`               // 触发时间（毫秒）
	EndsAt      int64      `json:"endsAt"`                 // 恢复时间（毫秒）
	Suppressed  bool       `json:"suppressed,omitempty"`   // 是否被抑制（被更高优先级告警压制）
	SuppressedBy string   `json:"suppressedBy,omitempty"`  // 抑制来源规则名/标识
	GroupKey    string     `json:"groupKey,omitempty"`     // 分组键（相同键的告警合并为一组通知）
}

// NowMillis 返回当前毫秒时间戳。
func NowMillis() int64 {
	return time.Now().UnixMilli()
}
