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

	// 容器/K8s 只读查询（见 docs/superpowers/specs/2026-09-30-container-observability-design.md §3.2）。
	//
	// 为什么走「下行指令」而不是 Server 直连 apiserver：K8s 凭据只存 Agent 本地、从不上报
	// （见 K8sInstanceConfig 的注释），Server 直连就必须把 kubeconfig/token 收上来，
	// 那是拿凭据边界换一点交互延迟——不值。代价是指令延迟 = 一个上报周期。
	//
	// 首批**只做只读**：exec 属 P2，需独立高危权限点与显式开启。

	// OpsKindContainerWorkloads 列工作负载（Deployment/StatefulSet/DaemonSet/Job）与副本状态（只读）。
	OpsKindContainerWorkloads = "container.workloads"
	// OpsKindContainerPods 列 Pod（状态、重启次数、所在节点、所属工作负载）（只读）。
	OpsKindContainerPods = "container.pods"
	// OpsKindContainerDescribe 单个对象的只读详情（敏感字段脱敏后返回）。
	OpsKindContainerDescribe = "container.describe"
	// OpsKindContainerEvents 命名空间/对象的事件列表（只读，按时间倒序）。
	OpsKindContainerEvents = "container.events"
	// OpsKindContainerLogs 拉取某个 Pod 的最近日志（只读）。
	//
	// 与其它容器动作的差别：产出是**业务自己写出的文本行**，而不是对象表格，
	// 内容可能含业务数据与凭据。因此行数与时间窗都必须有硬上限，且**不提供全量下载**——
	// 要看全量应该上机器或走日志后端，不该把平台当成 kubectl。
	OpsKindContainerLogs = "container.logs"

	// OpsKindFilePush 向目标机器分发一个文件（**写操作**，需目标机器显式放行）。
	//
	// 与 svc.restart 的差别在"写"的性质：重启是让某个已知单元回到它的标准状态，
	// 而分发是**用中心手上的内容替换掉机器上的一个文件**——后者更强，所以它不复用
	// guards.ops.write/units，而是有自己的一组开关（guards.ops.file.write + dirs）。
	// 同一台机器"允许我重启 nginx"不等于"允许我往它磁盘上写文件"。
	OpsKindFilePush = "file.push"
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

// 容器/K8s 查询参数的白名单字符集。与 OpsUnitPattern 同理，两端都校验：
// 这些值会被**拼进 apiserver 的 URL 路径**，不限制字符集就可能出现 `/api/v1/namespaces/../..`
// 或把 `?` `#` 塞进去改变请求语义。Server 保证"下发的干净"，Agent 保证"即使中心被绕过也不会去请求意外的路径"。
var (
	// OpsClusterPattern 是集群标识：取 Agent 本地 k8sInstances[].name，由运维自己起名，
	// 因此不套 K8s 的命名规则，只要求是标识符字符集且首字符为字母/数字。
	OpsClusterPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@._-]{0,63}$`)
	// OpsNamespacePattern 是 K8s 命名空间名（RFC1123 label）。
	OpsNamespacePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	// OpsObjectNamePattern 是 K8s 对象名（RFC1123 subdomain：Deployment/StatefulSet 名里允许 `.`）。
	OpsObjectNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?$`)
	// OpsContainerResourcePattern 是 container.describe 允许的资源类型白名单。
	//
	// 刻意**不含 secret**：虽然 describe 会做脱敏，但把 Secret 列进白名单等于给自己留一条
	// "脱敏规则漏一个字段就泄一次"的路。真要看 Secret 应该走 kubectl 而不是监控平台。
	OpsContainerResourcePattern = regexp.MustCompile(`^(pods|deployments|statefulsets|daemonsets|jobs|services|configmaps)$`)

	// OpsFilePathPattern 是文件分发**目标路径**的白名单。
	//
	// 必须绝对路径，且禁止 `..` 段与结尾斜杠：这个值会被 Agent 直接用于文件系统调用。
	// 禁止 `..` 靠"每段必须以字母/数字/下划线/短横线结尾"实现——`.` 与 `..` 都以点结尾，
	// 因此天然被排除（Go 的 RE2 没有前瞻断言，只能用构造性写法）。
	// 刻意不含空格与通配符：路径来自 Web 表单，宽松字符集只会让"看着对、实际写错地方"更难发现。
	OpsFilePathPattern = regexp.MustCompile(`^/(?:[A-Za-z0-9._-]*[A-Za-z0-9_-])(?:/(?:[A-Za-z0-9._-]*[A-Za-z0-9_-]))*$`)
	// OpsFileRefPattern 是上传文件的引用号（服务端生成，形如 obf-3）。
	OpsFileRefPattern = regexp.MustCompile(`^obf-[0-9]{1,9}$`)
	// OpsFileModePattern 是目标文件权限（四位八进制，如 0644 / 0600）。
	//
	// 只接受 0xxx：setuid/setgid/sticky（1xxx-7xxx）刻意不允许——分发一个 setuid 文件
	// 等价于远程提权，那不该是"顺手能做的事"。
	OpsFileModePattern = regexp.MustCompile(`^0[0-7]{3}$`)

	// OpsLogTailLinesPattern / OpsLogSinceSecondsPattern 是 container.logs 数值参数的白名单。
	//
	// 只放十进制数字：范围在 Agent 侧再校一次（见下面两个上限）。两道都要，
	// 因为这两个值会被拼进 apiserver 的查询串，宽松字符集等于给了一条注入路径。
	OpsLogTailLinesPattern   = regexp.MustCompile(`^[0-9]{1,4}$`)
	OpsLogSinceSecondsPattern = regexp.MustCompile(`^[0-9]{1,6}$`)
)

// 日志拉取的硬上限。取值依据：够定位一次故障（几十行上下文 + 最近一小时），
// 同时把回执体积压在几十 KB —— 回执要搭在下一轮上报的请求体里。
const (
	// OpsLogDefaultTailLines 是未指定行数时的默认值。
	OpsLogDefaultTailLines = 200
	// OpsLogMaxTailLines 是单次可拉取的最大行数。
	OpsLogMaxTailLines = 500
	// OpsLogMaxSinceSeconds 是单次可回溯的最大时间窗（24 小时）。
	OpsLogMaxSinceSeconds = 86400
)

// OpsFileMaxBytes 是单次分发的文件大小上限（256KiB）。
//
// 必须有硬上限：内容会被 base64 后塞进**一轮上报响应**，没有上限时一个几十兆的 tarball
// 会跟着每个目标节点的那一轮响应走。要分发更大的文件应该另加一条下载通道
// （服务端给 URL + Agent 主动拉取），而不是把这个数往上调——调大只会让"某台机器突然
// 上报变慢/失败"重新变成可能。
const OpsFileMaxBytes = 256 << 10

// OpsFileBlob 是随指令下发给 Agent 的文件内容。
//
// 它**只出现在下发的那一份副本里**（Store.Take 在领取时注入），绝不落任务存储：
// 任务存储是每次状态流转都整体重写的一份 JSON，把文件内容写进去会让文件以"条数 × 文件大小"
// 的量级膨胀，而其中绝大多数任务早已执行完毕。
type OpsFileBlob struct {
	// Name 是上传时的原始文件名，仅供 Agent 写回执时说明"分发的是哪个文件"。
	Name string `json:"name,omitempty"`
	// Size 是内容字节数（未 base64 的原始大小），Agent 用它做一次长度校验。
	Size int64 `json:"size,omitempty"`
	// SHA256 是内容的十六进制摘要；Agent 必须校验通过才允许替换目标文件。
	SHA256 string `json:"sha256,omitempty"`
	// Content 是文件内容的 base64（标准编码，含填充）。
	Content string `json:"content"`
}

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
	Params map[string]string `json:"params,omitempty"`
	// File 是 file.push 的文件内容，**只在真正下发给 Agent 的那一份里非空**（Store.Take 注入）。
	// 任务存储里只保留引用（Params["fileId"]），理由见 OpsFileBlob 的注释。
	File      *OpsFileBlob `json:"file,omitempty"`
	CreatedAt int64        `json:"createdAt,omitempty"`
	// ExpireAt 是过期时刻（毫秒）：超过它的指令不再下发（由 Take 与 ExpireOverdue 回收为 expired）。
	ExpireAt int64 `json:"expireAt,omitempty"`
}

// OpsResult 是 Agent 回执的执行结果。
type OpsResult struct {
	CommandID string `json:"commandId"`
	// State 取值见 OpsState*（running / succeeded / failed）。
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// Data 是动作产出的**分节文本**结果，键与值都应是可直接展示的短文本。
	// 节点操作页直接把它按分节渲染，人读的就是这一份。
	Data map[string]string `json:"data,omitempty"`
	// JSON 是同一份结果的**结构化**形态（ContainerQueryResult 的 JSON 序列化），
	// 供容器管理面渲染成表格。为空表示该动作没有结构化产出。
	//
	// 为什么要两个形态而不是只留 JSON：Data 是"给人看的分节文本"，在任务列表里一眼能读；
	// 前端表格要的是行列。刻意**不**把同一份数据塞两份——容器动作的 Data 只放一两行摘要。
	JSON string `json:"json,omitempty"`
	At   int64  `json:"at,omitempty"`
	// DurationMs 是执行耗时（毫秒），用于识别"指令发出去了但执行卡住"。
	DurationMs int64 `json:"durationMs,omitempty"`
}

// ContainerQueryResult 是一次容器只读查询的结果，也是 container.* 动作回执的结构化载荷。
//
// 放在 model 而不是 collector：它**是协议的一部分**（随 OpsResult.JSON 上行、被前端解析），
// 而 collector 只负责"怎么从 apiserver 拿"。
//
// 用「列名 + 行」这种通用表格形态而不是每种资源一个结构体：前端因此不必为每个 kind 写一份
// 渲染代码，将来加一种资源也不用改协议。
type ContainerQueryResult struct {
	Kind      string     `json:"kind"`
	Cluster   string     `json:"cluster"`
	Namespace string     `json:"namespace,omitempty"`
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	// Total 是本次查询在集群里**看到**的对象数（可能大于 len(Rows)，见 Truncated）。
	Total int `json:"total"`
	// Truncated 为 true 表示 Rows 只是前若干条。必须显式回传：
	// 静默截断会让人以为"集群里就这么多"，进而对漏掉的异常 Pod 视而不见。
	Truncated bool  `json:"truncated"`
	FetchedAt int64 `json:"fetchedAt"`
	// Notice 是给使用者的补充说明（例如"值已省略"），前端原样展示。
	Notice string `json:"notice,omitempty"`
}

// 操作任务状态机：queued → delivered → running → succeeded / failed / expired / cancelled。
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
	// OpsStateCancelled 被操作者取消（仅允许在 queued 阶段取消）。
	//
	// 刻意用「留一条 cancelled 记录」而不是把任务删掉：否则"我明明下过这条指令"会变成悬案，
	// 且与"从未下发"无法区分。删除只对**终态**记录开放，且要写审计。
	OpsStateCancelled = "cancelled"
)

// OpsStateTerminal 判断状态是否为终态（不会再变化）。
func OpsStateTerminal(state string) bool {
	switch state {
	case OpsStateSucceeded, OpsStateFailed, OpsStateExpired, OpsStateCancelled:
		return true
	}
	return false
}
