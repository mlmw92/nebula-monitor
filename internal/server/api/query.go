package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/analysis"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/dashboard"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/mwreg"
	"github.com/nebula/monitor/internal/server/nginxaccess"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/notify"
	"github.com/nebula/monitor/internal/server/ops"
	"github.com/nebula/monitor/internal/server/receiver"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/retention"
	"github.com/nebula/monitor/internal/server/screencfg"
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/selfmon"
	"github.com/nebula/monitor/internal/server/storage"
	"github.com/nebula/monitor/internal/server/uicfg"
	"github.com/nebula/monitor/internal/server/upgrade"
	"github.com/nebula/monitor/internal/version"
)

// RulesProvider 提供告警规则读写（由 alert 包实现）。
type RulesProvider interface {
	List() []model.AlertRule
	Get(id string) (model.AlertRule, bool)
	Create(r model.AlertRule) model.AlertRule
	Update(r model.AlertRule) error
	Delete(id string) error
	ImportRules(rules []model.AlertRule, replace bool) (int, int, error)
}

// AlertStore 提供告警事件查询（由 alert 包实现）。
type AlertStore interface {
	Recent(limit int) []model.AlertEvent
	Active() []model.AlertEvent
}

// MaintenanceProvider 提供维护窗口读写（由 alert 包实现）。
type MaintenanceProvider interface {
	Get() model.MaintenanceWindow
	Set(mw model.MaintenanceWindow)
}

// DialtestProvider 提供拨测任务 CRUD（由 dialtest 包实现）。
type DialtestProvider interface {
	List() []dialtest.Task
	Get(id string) (dialtest.Task, bool)
	Create(t dialtest.Task) dialtest.Task
	Update(t dialtest.Task) error
	Delete(id string) error
	LastResults() map[string]dialtest.Result
}

// ReportProvider 提供报告生成与查询（由 report 包实现）。
type ReportProvider interface {
	Generate(rt report.ReportType) (string, error)
	GetHTML(id string) (string, error)
	History() []report.ReportMeta
}

// API 聚合所有 REST 接口依赖。
type API struct {
	store          storage.Storage
	nodeMgr        *node.Manager
	rules          RulesProvider
	alerts         AlertStore
	hub            *Hub
	agentAuth      config.AgentAuthConfig
	agentBinDir    string
	webDir         string
	auth           config.AuthConfig
	upgrader       *upgrade.Manager
	notifyMgr      *notify.Manager
	screenMgr      *screencfg.Manager
	engine         *alert.Engine
	maintenance    MaintenanceProvider
	dialtest       DialtestProvider
	report         ReportProvider
	acks           *alert.AckStore
	inhibit        *alert.InhibitStore
	grouping       *alert.GroupingStore
	ngx            *nginxaccess.Window    // Nginx access log 地理聚合窗口（可空）
	uiMgr          *uicfg.Manager         // 系统 UI 品牌配置（系统名称/Logo）
	serverProvince string                 // server 自动探测到的所在地（省级行政区）
	configPath     string                 // server.yaml 路径，用于改密码时持久化
	dashMgr        *dashboard.Manager     // 自定义仪表盘配置（可空）
	security       *security.Store        // 安全事件/基线存储（可空，关闭安全能力）
	audit          *audit.Store           // 管理操作审计存储（可空）
	defenseStore   *security.DefenseStore // 受控 fail2ban 入侵防御任务存储（可空，关闭防护能力）
	ops            *ops.Service           // 统一下行操作通道（可空；用 SetOpsService 注入）
	authStore      *auth.Store            // 多用户角色权限存储（可空：未启用登录认证时为 nil）
	analysis       *analysis.Analyzer     // 只读智能分析服务（可空）
	pipeline       *alert.PipelineStore   // 告警事件管道：relabel/enrich/消息模板（可空）
	selfmon        *selfmon.Monitor       // 自监控收集器（可空；不注入时探针仍可用，只是没有进程指标）
	retention      *retention.Manager     // 数据保留策略（可空；不注入时接口返回默认策略）
	mwRegistry     *mwreg.Registry        // 中间件类型注册表（可空；未注入时退化为内置类型）
	logs           *logstore.Store        // 集中日志存储（C2；可空，未注入时检索接口返回 503）
	assets         AssetProvider          // 资产台账（可空；未注入时资产接口返回 503）
	startedAt      time.Time              // 进程启动时间，供 /healthz、/readyz 报告运行时长
}

// SetDashboardManager 注入仪表盘配置管理器（可选，不注入则相关接口返回空列表）。
func (a *API) SetDashboardManager(m *dashboard.Manager) { a.dashMgr = m }

// SetAnalyzer 注入只读智能分析服务，并把它接到告警风暴收敛的关联结论来源上。
func (a *API) SetAnalyzer(analyzer *analysis.Analyzer) {
	a.analysis = analyzer
	if a.engine != nil && analyzer != nil {
		// alert 侧只认接口（避免 analysis ↔ alert 循环依赖），由持有 Analyzer 的 API 层反向注入。
		a.engine.SetCorrelationProvider(a)
	}
}

// SetPipelineStore 注入告警事件管道存储（可选；不注入时相关接口返回空配置）。
func (a *API) SetPipelineStore(p *alert.PipelineStore) { a.pipeline = p }

// SetMiddlewareRegistry 注入中间件类型注册表。
// 未注入时退化为只有内置类型，行为与改造前一致。
func (a *API) SetMiddlewareRegistry(r *mwreg.Registry) { a.mwRegistry = r }

// middlewareRegistry 返回中间件类型注册表（未注入时用内置类型单例兜底）。
func (a *API) middlewareRegistry() *mwreg.Registry {
	if a.mwRegistry != nil {
		return a.mwRegistry
	}
	return mwreg.BuiltinOnly()
}

// New 创建 API。
func New(store storage.Storage, mgr *node.Manager, rules RulesProvider, alerts AlertStore, hub *Hub, agentAuth config.AgentAuthConfig, agentBinDir string, webDir string, auth config.AuthConfig, upgrader *upgrade.Manager, notifyMgr *notify.Manager, engine *alert.Engine, maintenance MaintenanceProvider, dt DialtestProvider, rpt ReportProvider, screenMgr *screencfg.Manager, acks *alert.AckStore, inhibit *alert.InhibitStore, grouping *alert.GroupingStore, ngx *nginxaccess.Window, uiMgr *uicfg.Manager, configPath string, sec *security.Store, defenseStore *security.DefenseStore, auditStore *audit.Store, authStore *auth.Store) *API {
	return &API{store: store, nodeMgr: mgr, rules: rules, alerts: alerts, hub: hub, agentAuth: agentAuth, agentBinDir: agentBinDir, webDir: webDir, auth: auth, upgrader: upgrader, notifyMgr: notifyMgr, engine: engine, maintenance: maintenance, dialtest: dt, report: rpt, screenMgr: screenMgr, acks: acks, inhibit: inhibit, grouping: grouping, ngx: ngx, uiMgr: uiMgr, serverProvince: detectServerProvince(), configPath: configPath, security: sec, defenseStore: defenseStore, audit: auditStore, authStore: authStore, startedAt: time.Now()}
}

// RegisterRoutes 注册所有路由到 mux。
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	// 主机与节点：读取 nodes:read，写操作 nodes:write，Agent 升级 agent:upgrade；均含资源范围校验
	mux.HandleFunc("GET /api/v1/nodes", a.permit(a.handleNodes, "nodes:read"))
	mux.HandleFunc("GET /api/v1/nodes/latest", a.permit(a.handleNodesLatest, "nodes:read"))
	mux.HandleFunc("GET /api/v1/nodes/{name}", a.permitNode(a.handleNode, "nodes:read"))
	mux.HandleFunc("DELETE /api/v1/nodes/{name}", a.permitNode(a.handleNodeDelete, "nodes:write"))
	mux.HandleFunc("PUT /api/v1/nodes/{name}/group", a.permitNode(a.handleNodeGroup, "nodes:write"))
	mux.HandleFunc("PUT /api/v1/nodes/{name}/display-name", a.permitNode(a.handleNodeDisplayName, "nodes:write"))
	mux.HandleFunc("POST /api/v1/nodes/{name}/upgrade", a.permitNode(a.handleNodeUpgrade, "agent:upgrade"))
	mux.HandleFunc("POST /api/v1/nodes/upgrade", a.permit(a.handleNodesUpgrade, "agent:upgrade"))

	// 节点分组：groups:read / groups:write
	mux.HandleFunc("GET /api/v1/groups", a.permit(a.handleGroups, "groups:read"))
	mux.HandleFunc("POST /api/v1/groups", a.permit(a.handleGroupCreate, "groups:write"))
	mux.HandleFunc("DELETE /api/v1/groups/{name}", a.permit(a.handleGroupDelete, "groups:write"))

	// 指标跨节点查询在 handler 内统一解析 node / labels.node 并裁剪结果。
	mux.HandleFunc("GET /api/v1/query/range", a.permit(a.handleQueryRange, "nodes:read"))
	mux.HandleFunc("GET /api/v1/query/latest", a.permit(a.handleQueryLatest, "nodes:read"))
	mux.HandleFunc("GET /api/v1/analysis/summary", a.permit(a.handleAnalysisSummary, "nodes:read"))
	mux.HandleFunc("GET /api/v1/analysis/hosts/{name}", a.permitNode(a.handleAnalysisHost, "nodes:read"))
	mux.HandleFunc("GET /api/v1/processes", a.permitHostname(a.handleProcesses, "nodes:read"))
	mux.HandleFunc("GET /api/v1/query/listeners", a.permitHostname(a.handleListeners, "nodes:read"))
	mux.HandleFunc("GET /api/v1/query/firewall", a.permitHostname(a.handleFirewall, "nodes:read"))
	mux.HandleFunc("GET /api/v1/query/firewall/status", a.permitHostname(a.handleFirewallStatus, "nodes:read"))

	// 中间件监控：middleware:read + 资源范围（实例列表按所属节点分组过滤）
	mux.HandleFunc("GET /api/v1/middleware/redis/instances", a.permit(a.handleRedisInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/mysql/instances", a.permit(a.handleMySQLInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/postgres/instances", a.permit(a.handlePostgresInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/nginx/instances", a.permit(a.handleNginxInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/kafka/instances", a.permit(a.handleKafkaInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/docker/containers", a.permit(a.handleDockerContainers, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/rocketmq/instances", a.permit(a.handleRocketMQInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/k8s/instances", a.permit(a.handleK8sInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/mongodb/instances", a.permit(a.handleMongoDBInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/fastdfs/instances", a.permit(a.handleFastDFSInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/rabbitmq/instances", a.permit(a.handleRabbitMQInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/elasticsearch/instances", a.permit(a.handleElasticsearchInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/clickhouse/instances", a.permit(a.handleClickHouseInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/nacos/instances", a.permit(a.handleNacosInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/zookeeper/instances", a.permit(a.handleZooKeeperInstances, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/overview", a.permit(a.handleMiddlewareOverview, "middleware:read"))

	// 资产台账：assets:read + 资源范围（按资产所属节点裁剪；不可归属的资产对受限用户不可见，
	// 与 visibleMetricSeries / handleNodesLatest 的判定一致，避免台账成为越权旁路）。
	mux.HandleFunc("GET /api/v1/assets", a.permit(a.handleAssets, "assets:read"))
	// 摘要与列表同权限、同筛选参数：顶部健康度数字必须能点进列表看到同一个集合。
	// 字面量路径比 {id} 更具体，ServeMux 会优先匹配，两者不冲突。
	mux.HandleFunc("GET /api/v1/assets/summary", a.permit(a.handleAssetSummary, "assets:read"))
	// 按「类型 + 自然键」精确查一条：供跨页联动（如容器页的 Pod → 台账资产）使用。
	// 字面量路径比 {id} 更具体，ServeMux 会优先匹配。
	mux.HandleFunc("GET /api/v1/assets/lookup", a.permit(a.handleAssetLookup, "assets:read"))
	mux.HandleFunc("GET /api/v1/assets/{id}", a.permit(a.handleAssetDetail, "assets:read"))
	mux.HandleFunc("GET /api/v1/assets/{id}/history", a.permit(a.handleAssetHistory, "assets:read"))
	mux.HandleFunc("GET /api/v1/assets/{id}/links", a.permit(a.handleAssetLinks, "assets:read"))
	// 关系图（拓扑）：与 /links 同权限、同范围口径——它给的是 N 跳邻域，不是更多信息量。
	mux.HandleFunc("GET /api/v1/assets/{id}/topology", a.permit(a.handleAssetTopology, "assets:read"))
	// 人工维护关联：与忽略 / 标签 / 标杆同为「台账维护」，共用 assets:write。
	// 三种动作（建 / 删 / 取消抑制）共用一套寻址：toType/toKey/kind/direction。
	// POST 从 JSON 体读，DELETE 从**查询串**读（DELETE 的请求体在 HTTP 语义里没有定义，
	// 中间设备丢弃它是合法行为）——详见 asset_link_api.go 文件头。
	// 删除是逻辑删除（落抑制，采集不再重建），取消抑制走 /links/restore。
	mux.HandleFunc("POST /api/v1/assets/{id}/links", a.permit(a.handleAssetLinkCreate, "assets:write"))
	mux.HandleFunc("DELETE /api/v1/assets/{id}/links", a.permit(a.handleAssetLinkDelete, "assets:write"))
	mux.HandleFunc("POST /api/v1/assets/{id}/links/restore", a.permit(a.handleAssetLinkRestore, "assets:write"))
	// 人工维护：写接口在 handler 内自行做资源范围判定（新建时目标节点可能还不存在资产）。
	mux.HandleFunc("POST /api/v1/assets", a.permit(a.handleAssetCreate, "assets:write"))
	mux.HandleFunc("PUT /api/v1/assets/{id}", a.permit(a.handleAssetUpdate, "assets:write"))
	// 配置快照与「期望值（标杆）」：快照是巡检基线的证据，标杆来自某资产的快照 → 属台账维护
	mux.HandleFunc("GET /api/v1/assets/{id}/snapshots", a.permit(a.handleAssetSnapshots, "assets:read"))
	mux.HandleFunc("POST /api/v1/assets/{id}/baseline", a.permit(a.handleAssetBaselineSet, "assets:write"))
	mux.HandleFunc("DELETE /api/v1/assets/{id}/baseline", a.permit(a.handleAssetBaselineDelete, "assets:write"))
	// 生命周期与标签：忽略（可恢复的隐藏）/ 恢复 / 彻底删除（仅纯人工资产）/ 标签维护
	mux.HandleFunc("POST /api/v1/assets/{id}/ignore", a.permit(a.handleAssetIgnore, "assets:write"))
	mux.HandleFunc("POST /api/v1/assets/{id}/restore", a.permit(a.handleAssetRestore, "assets:write"))
	mux.HandleFunc("POST /api/v1/assets/{id}/purge", a.permit(a.handleAssetPurge, "assets:write"))
	mux.HandleFunc("PUT /api/v1/assets/{id}/labels", a.permit(a.handleAssetLabels, "assets:write"))
	// 批量维护与清单导出：批量沿用单条写的权限点（逐条结论、范围在 handler 内逐条判定）；
	// 导出单设 assets:export——它一次把整份台账落盘，与「逐页翻看」不是一个量级的动作。
	// 字面量路径比 {id} 更具体，ServeMux 会优先匹配（与 /assets/summary 同理）。
	mux.HandleFunc("POST /api/v1/assets/batch", a.permit(a.handleAssetsBatch, "assets:write"))
	mux.HandleFunc("GET /api/v1/assets/export", a.permit(a.handleAssetExport, "assets:export"))
	// 配置巡检（inspect）：只给结论、不改配置，因此「跑」与「改」分成两个权限点
	mux.HandleFunc("POST /api/v1/inspect/runs", a.permit(a.handleInspectRunCreate, "inspect:run"))
	mux.HandleFunc("GET /api/v1/inspect/runs", a.permit(a.handleInspectRuns, "inspect:read"))
	mux.HandleFunc("GET /api/v1/inspect/runs/{id}/findings", a.permit(a.handleInspectFindings, "inspect:read"))
	mux.HandleFunc("GET /api/v1/inspect/baselines", a.permit(a.handleInspectBaselines, "inspect:read"))
	// 中间件类型展示开关：读 middleware:read，写 system:config（与品牌/大屏展示配置同级）
	mux.HandleFunc("GET /api/v1/middleware/view-config", a.permit(a.handleMiddlewareViewConfigGET, "middleware:read"))
	mux.HandleFunc("PUT /api/v1/middleware/view-config", a.permit(a.handleMiddlewareViewConfigPUT, "system:config"))
	mux.HandleFunc("POST /api/v1/middleware/view-config", a.permit(a.handleMiddlewareViewConfigPUT, "system:config"))
	// 集中日志检索（C2）：内容敏感，独立权限点 logs:read + 节点分组范围
	mux.HandleFunc("GET /api/v1/logs", a.permit(a.handleLogsQuery, "logs:read"))
	// 结构化字段名候选（供检索页做筛选候选）。字面量路径比 /logs 更具体，不冲突。
	mux.HandleFunc("GET /api/v1/logs/fields", a.permit(a.handleLogFields, "logs:read"))
	// 对外状态页（C3）：**刻意不套 permit**——它是给外部人看的免登录页面。
	// 暴露范围由「拨测任务是否勾选 public」控制（见 dialtest.Task.Public），
	// 且响应只含名称/状态/延迟/可用率，不含 target 与节点。
	mux.HandleFunc("GET /api/v1/status", a.handlePublicStatus)
	mux.HandleFunc("GET /api/v1/middleware/nginx/access/summary", a.permit(a.handleNginxAccessSummary, "middleware:read"))
	mux.HandleFunc("GET /api/v1/middleware/nginx/access/geo", a.permit(a.handleNginxAccessGeo, "middleware:read"))

	// 告警事件：读 alerts:read、处置 alerts:write（列表与统计按节点资源范围过滤）
	// 协作处置（D4）：认领 / 关闭 / 重新打开 / 评论，均为 alerts:write；
	// 节点资源范围在各 handler 内经 decodeAlertAction 统一校验。
	mux.HandleFunc("GET /api/v1/alerts", a.permitNode(a.handleAlerts, "alerts:read"))
	mux.HandleFunc("GET /api/v1/alerts/acks", a.permit(a.handleAlertAcks, "alerts:read"))
	mux.HandleFunc("POST /api/v1/alerts/ack", a.permit(a.handleAlertAck, "alerts:write"))
	mux.HandleFunc("POST /api/v1/alerts/close", a.permit(a.handleAlertClose, "alerts:write"))
	mux.HandleFunc("POST /api/v1/alerts/reopen", a.permit(a.handleAlertReopen, "alerts:write"))
	mux.HandleFunc("POST /api/v1/alerts/comment", a.permit(a.handleAlertComment, "alerts:write"))
	// 告警规则：读 alerts:read、写 alerts:write、临时静默 silence:write
	mux.HandleFunc("GET /api/v1/rules", a.permit(a.handleRulesList, "alerts:read"))
	mux.HandleFunc("GET /api/v1/rules/export", a.permit(a.handleRulesExport, "alerts:read"))
	mux.HandleFunc("POST /api/v1/rules/import", a.permit(a.handleRulesImport, "alerts:write"))
	mux.HandleFunc("GET /api/v1/rules/templates", a.permit(a.handleRuleTemplates, "alerts:read"))
	mux.HandleFunc("POST /api/v1/rules", a.permit(a.handleRuleCreate, "alerts:write"))
	mux.HandleFunc("PUT /api/v1/rules/{id}", a.permit(a.handleRuleUpdate, "alerts:write"))
	mux.HandleFunc("POST /api/v1/rules/{id}/toggle", a.permit(a.handleRuleToggle, "alerts:write"))
	mux.HandleFunc("POST /api/v1/rules/{id}/toggle-silence", a.permit(a.handleRuleToggleSilence, "silence:write"))
	mux.HandleFunc("DELETE /api/v1/rules/{id}", a.permit(a.handleRuleDelete, "alerts:write"))

	// 管理操作审计：查看 audit:read；导出已拆为独立路由（audit:export，高危）
	mux.HandleFunc("GET /api/v1/audit/events", a.permit(a.handleAuditEvents, "audit:read"))
	mux.HandleFunc("GET /api/v1/audit/export", a.permit(a.handleAuditExport, "audit:export"))

	// 安全中心：查看 security:read；入侵防御指令下发 security:write（高危）+ 节点资源范围
	mux.HandleFunc("GET /api/v1/security/summary", a.permit(a.handleSecuritySummary, "security:read"))
	mux.HandleFunc("GET /api/v1/security/events", a.permit(a.handleSecurityEvents, "security:read"))
	mux.HandleFunc("GET /api/v1/security/baselines", a.permit(a.handleSecurityBaselines, "security:read"))

	// 受控 fail2ban 入侵防御（仅管理 nebula 专属 SSH jail）
	mux.HandleFunc("GET /api/v1/security/defense/status", a.permit(a.handleDefenseStatusList, "security:read"))
	mux.HandleFunc("GET /api/v1/security/defense/status/{node}", a.permitNode(a.handleDefenseStatus, "security:read"))
	mux.HandleFunc("POST /api/v1/security/defense/{node}/{action}", a.permitNode(a.handleDefenseAction, "security:write"))
	mux.HandleFunc("GET /api/v1/security/defense/tasks", a.permit(a.handleDefenseTasks, "security:read"))
	mux.HandleFunc("GET /api/v1/security/defense/tasks/{node}", a.permitNode(a.handleDefenseTasks, "security:read"))

	// 统一下行操作通道：查看 ops:read；**下发 ops:exec（高危）**——即使是只读动作，
	// 它也是"在一批机器上执行东西"的能力。节点资源范围在 handler 内按节点名校验
	// （目标节点来自请求体，不是路径参数，因此不能用 permitNode）。
	mux.HandleFunc("GET /api/v1/ops/actions", a.permit(a.handleOpsActions, "ops:read"))
	mux.HandleFunc("GET /api/v1/ops/tasks", a.permit(a.handleOpsTasks, "ops:read"))
	mux.HandleFunc("GET /api/v1/ops/tasks/{id}", a.permit(a.handleOpsTask, "ops:read"))
	mux.HandleFunc("POST /api/v1/ops/tasks", a.permit(a.handleOpsCreate, "ops:exec"))
	// 批量下发 / 取消（含整批）/ 删除记录：都改变任务状态，一律 ops:exec
	mux.HandleFunc("POST /api/v1/ops/tasks/batch", a.permit(a.handleOpsCreateBatch, "ops:exec"))
	mux.HandleFunc("POST /api/v1/ops/tasks/cancel", a.permit(a.handleOpsCancel, "ops:exec"))
	mux.HandleFunc("DELETE /api/v1/ops/tasks/{id}", a.permit(a.handleOpsDelete, "ops:exec"))
	// 文件分发的内容上传：与下发同权限（上传本身不动机器，但它决定了"能往机器上写什么"）；
	// 列举只读，供界面复用已上传的文件而不必重复上传。
	mux.HandleFunc("POST /api/v1/ops/files", a.permit(a.handleOpsFileUpload, "ops:exec"))
	mux.HandleFunc("GET /api/v1/ops/files", a.permit(a.handleOpsFiles, "ops:read"))

	// 容器管理面（只读）。集群清单与中间件的 /middleware/k8s/instances 同源（都来自
	// instancereg 的 K8sInstance 上报），但**不合并**：容器管理面的权限点是 container:read，
	// 不该要求运维为了看工作负载额外拿到 middleware:read。集群标识用上报里的 name
	// （agent.yaml 的 k8sInstances[].name），凭据始终只在 Agent 本地。
	mux.HandleFunc("GET /api/v1/container/k8s/clusters", a.permit(a.handleContainerClusters, "container:read"))

	// 安装信息含 Agent 长期密钥 → agent:secret:read（高危）；version 登录即可；agent/check 走 X-Agent-Secret（公开）
	mux.HandleFunc("GET /api/v1/install-info", a.permit(a.handleInstallInfo, "agent:secret:read"))
	mux.HandleFunc("GET /api/v1/version", a.handleVersion)
	// 健康探针：公开（见 auth.isPublicPath）——探针语义是「进程是否存活 / 依赖是否就绪」，
	// k8s、systemd 与反向代理通常无法携带登录令牌。
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	mux.HandleFunc("GET /readyz", a.handleReadyz)
	// 自监控快照属运维只读概览信息 → dashboard:read（与「查看概览」同级）
	mux.HandleFunc("GET /api/v1/self/status", a.permit(a.handleSelfStatus, "dashboard:read"))

	// 数据保留策略：系统配置类，读写均需 system:config（与品牌 / 大屏 / 地理库同级）
	mux.HandleFunc("GET /api/v1/system/retention", a.permit(a.handleRetentionGet, "system:config"))
	mux.HandleFunc("PUT /api/v1/system/retention", a.permit(a.handleRetentionPut, "system:config"))
	mux.HandleFunc("POST /api/v1/system/retention/cleanup", a.permit(a.handleRetentionCleanup, "system:config"))
	mux.HandleFunc("GET /api/v1/agent/check", a.handleAgentCheck)

	// 系统升级：system:upgrade（高危权限点）
	mux.HandleFunc("POST /api/v1/system/upgrade/upload", a.permit(a.handleSystemUpgradeUpload, "system:upgrade"))
	mux.HandleFunc("GET /api/v1/system/upgrade/current", a.permit(a.handleSystemUpgradeCurrent, "system:upgrade"))
	mux.HandleFunc("POST /api/v1/system/upgrade/apply", a.permit(a.handleSystemUpgradeApply, "system:upgrade"))
	mux.HandleFunc("GET /api/v1/system/upgrade/history", a.permit(a.handleSystemUpgradeHistory, "system:upgrade"))
	mux.HandleFunc("GET /api/v1/system/upgrade/archive", a.permit(a.handleSystemUpgradeArchive, "system:upgrade"))
	mux.HandleFunc("POST /api/v1/system/upgrade/rollback-to", a.permit(a.handleSystemUpgradeRollbackTo, "system:upgrade"))

	// IP 地理库更新（独立于系统升级：只替换 ip2region 库文件并热加载，不重启服务）→ system:config
	mux.HandleFunc("GET /api/v1/system/geoip", a.permit(a.handleGeoIPStatus, "system:config"))
	mux.HandleFunc("POST /api/v1/system/geoip/upload", a.permit(a.handleGeoIPUpload, "system:config"))
	mux.HandleFunc("POST /api/v1/system/geoip/reset", a.permit(a.handleGeoIPReset, "system:config"))
	mux.HandleFunc("GET /api/v1/system/geoip/test", a.permit(a.handleGeoIPTest, "system:config"))

	mux.HandleFunc("POST /api/v1/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", a.handleLogout)
	mux.HandleFunc("GET /api/v1/auth-info", a.handleAuthInfo)
	mux.HandleFunc("POST /api/v1/auth/change-password", a.handleChangePassword)

	// ===== 角色权限管理 API =====
	// 当前身份（登录即可）
	mux.HandleFunc("GET /api/v1/auth/me", a.handleMe)
	mux.HandleFunc("PUT /api/v1/auth/me", a.handleUpdateMe)
	// 用户管理（users:manage）
	mux.HandleFunc("GET /api/v1/users", a.authz(a.handleListUsers, "users:manage"))
	mux.HandleFunc("POST /api/v1/users", a.authz(a.handleCreateUser, "users:manage"))
	mux.HandleFunc("GET /api/v1/users/{username}", a.authz(a.handleGetUser, "users:manage"))
	mux.HandleFunc("PUT /api/v1/users/{username}", a.authz(a.handleUpdateUser, "users:manage"))
	mux.HandleFunc("POST /api/v1/users/{username}/reset-password", a.authz(a.handleResetUserPassword, "users:manage"))
	mux.HandleFunc("POST /api/v1/users/{username}/disable", a.authz(a.handleDisableUser, "users:manage"))
	mux.HandleFunc("POST /api/v1/users/{username}/enable", a.authz(a.handleEnableUser, "users:manage"))
	mux.HandleFunc("DELETE /api/v1/users/{username}", a.authz(a.handleDeleteUser, "users:manage"))
	// 角色管理（roles:manage 写；roles:read 列出详情）
	mux.HandleFunc("GET /api/v1/roles", a.authz(a.handleListRoles, "roles:read"))
	mux.HandleFunc("POST /api/v1/roles", a.authz(a.handleCreateRole, "roles:manage"))
	mux.HandleFunc("GET /api/v1/roles/{name}", a.authz(a.handleGetRole, "roles:read"))
	mux.HandleFunc("PUT /api/v1/roles/{name}", a.authz(a.handleUpdateRole, "roles:manage"))
	mux.HandleFunc("DELETE /api/v1/roles/{name}", a.authz(a.handleDeleteRole, "roles:manage"))
	// 权限目录（roles:read）
	mux.HandleFunc("GET /api/v1/permissions/catalog", a.authz(a.handlePermissionCatalog, "roles:read"))

	// 通知渠道：读 notify:read；写与测试通知 notify:write（高危权限点）
	mux.HandleFunc("GET /api/v1/notify", a.permit(a.handleNotifyGet, "notify:read"))
	mux.HandleFunc("PUT /api/v1/notify", a.permit(a.handleNotifyPut, "notify:write"))
	mux.HandleFunc("POST /api/v1/notify/test", a.permit(a.handleNotifyTest, "notify:write"))

	// 大屏与品牌配置：system:config（GET /ui/settings 为公开匿名只读，见 AuthMiddleware）
	mux.HandleFunc("GET /api/v1/screen/config", a.permit(a.handleScreenGet, "system:config"))
	mux.HandleFunc("PUT /api/v1/screen/config", a.permit(a.handleScreenPut, "system:config"))

	// 系统 UI 品牌配置（系统名称/Logo）
	mux.HandleFunc("GET /api/v1/ui/settings", a.handleUIGet)
	mux.HandleFunc("PUT /api/v1/ui/settings", a.permit(a.handleUIPut, "system:config"))

	// 测试告警会写事件并向通知渠道发消息，故复用 notify:write（避免「能管规则就能发通知」）。
	mux.HandleFunc("POST /api/v1/alerts/test", a.permit(a.handleAlertTest, "notify:write"))

	mux.HandleFunc("GET /api/v1/maintenance", a.permit(a.handleMaintenanceGet, "silence:read"))
	mux.HandleFunc("PUT /api/v1/maintenance", a.permit(a.handleMaintenanceSet, "silence:write"))

	// 告警抑制规则（P4）：属于告警引擎配置
	mux.HandleFunc("GET /api/v1/inhibit", a.permit(a.handleInhibitGet, "alerts:read"))
	mux.HandleFunc("PUT /api/v1/inhibit", a.permit(a.handleInhibitPut, "alerts:write"))

	// 告警事件管道（relabel / enrich / 消息模板）：独立配置，保存即热生效
	// preview 为只读试算（不落盘），故仅需 alerts:read，不按 HTTP 方法机械推导权限。
	mux.HandleFunc("GET /api/v1/alert-pipeline", a.permit(a.handlePipelineGet, "alerts:read"))
	mux.HandleFunc("PUT /api/v1/alert-pipeline", a.permit(a.handlePipelinePut, "alerts:write"))
	mux.HandleFunc("POST /api/v1/alert-pipeline/preview", a.permit(a.handlePipelinePreview, "alerts:read"))

	// 告警分组配置（P4）
	mux.HandleFunc("GET /api/v1/grouping", a.permit(a.handleGroupingGet, "alerts:read"))
	mux.HandleFunc("PUT /api/v1/grouping", a.permit(a.handleGroupingPut, "alerts:write"))

	// 告警统计看板（P4）
	mux.HandleFunc("GET /api/v1/alerts/stats", a.permit(a.handleAlertStats, "alerts:read"))

	// 拨测：probe:read / probe:write
	mux.HandleFunc("GET /api/v1/dialtest/tasks", a.permit(a.handleDialtestList, "probe:read"))
	mux.HandleFunc("POST /api/v1/dialtest/tasks", a.permit(a.handleDialtestCreate, "probe:write"))
	mux.HandleFunc("PUT /api/v1/dialtest/tasks/{id}", a.permit(a.handleDialtestUpdate, "probe:write"))
	mux.HandleFunc("DELETE /api/v1/dialtest/tasks/{id}", a.permit(a.handleDialtestDelete, "probe:write"))
	mux.HandleFunc("GET /api/v1/dialtest/latest", a.permit(a.handleDialtestLatest, "probe:read"))

	// 巡检报告：生成与下载为导出语义（report:export），历史列表 report:read
	mux.HandleFunc("POST /api/v1/report/generate", a.permit(a.handleReportGenerate, "report:export"))
	mux.HandleFunc("GET /api/v1/report/download", a.permit(a.handleReportDownload, "report:export"))
	mux.HandleFunc("GET /api/v1/report/history", a.permit(a.handleReportHistory, "report:read"))

	// 代理模式状态查询（网闸场景 Edge/Hub 代理连接状态）
	mux.HandleFunc("GET /api/v1/proxy/status", a.permit(a.handleProxyStatus, "agent:read"))

	// 可观测性增强：指标目录（自动发现）+ 历史数据导出
	mux.HandleFunc("GET /api/v1/metrics/catalog", a.permit(a.handleMetricsCatalog, "nodes:read"))
	// metricTarget 内部已完成节点冲突判定与资源范围校验（含未注册节点拒绝），
	// 这里改用 permit 避免 permitNode 对未注册节点的静默放行掩盖真正的授权判定。
	mux.HandleFunc("GET /api/v1/metrics/active", a.permit(a.handleMetricsActive, "nodes:read"))
	mux.HandleFunc("GET /api/v1/metrics/export", a.permitNode(a.handleMetricsExport, "metrics:export"))

	// 可观测性增强：自定义仪表盘（读 dashboard:read，增删改 dashboard:write）
	mux.HandleFunc("GET /api/v1/dashboards", a.permit(a.handleDashboardsList, "dashboard:read"))
	mux.HandleFunc("POST /api/v1/dashboards", a.permit(a.handleDashboardCreate, "dashboard:write"))
	mux.HandleFunc("GET /api/v1/dashboards/{id}", a.permit(a.handleDashboardGet, "dashboard:read"))
	mux.HandleFunc("PUT /api/v1/dashboards/{id}", a.permit(a.handleDashboardUpdate, "dashboard:write"))
	mux.HandleFunc("DELETE /api/v1/dashboards/{id}", a.permit(a.handleDashboardDelete, "dashboard:write"))
}

// handleInstallInfo 返回 Agent 一行安装命令（server 地址取自请求 Host，secret 取自配置）。
// 同时返回网闸代理场景（edge/hub）的配置模板，供前端引导页生成两侧 agent.yaml。
func (a *API) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	// Agent 密钥是长期凭据。若未开启登录认证，当前接口无法确认请求者是管理员，
	// 因此拒绝返回包含密钥的安装命令，避免匿名请求泄露 Agent 接入密钥。
	if a.agentAuth.Enabled && !a.auth.Enabled {
		http.Error(w, "Agent 接入认证已开启，请先开启 Server 登录认证后获取安装信息", http.StatusForbidden)
		return
	}
	srv := "http://" + r.Host
	cmd := "curl -fsSL " + srv + "/install/agent-install.sh | bash -s -- --server " + srv
	secretPart := ""
	if a.agentAuth.Enabled {
		secretPart = " --secret " + a.agentAuth.Secret
		cmd += secretPart
	}
	// 注意：不回显明文 agentAuth.secret 字段，避免该接口成为密钥泄露点
	// （认证关闭时 /api/v1/install-info 虽需登录后方可访问，但最小暴露原则下仅下发安装命令）。
	writeJSON(w, 200, map[string]interface{}{
		"serverURL":   srv,
		"command":     cmd,
		"authEnabled": a.agentAuth.Enabled,
		"secret":      secretPart, // 已格式化为 " --secret xxx" 便于前端拼接代理命令
		// 代理模式配置模板（前端引导页填充后生成完整命令）
		"proxyTemplates": map[string]interface{}{
			"edge": map[string]interface{}{
				// 网闸路由/端口策略由部署人员决定，不能从 Server 请求 Host 推断 Edge 可达地址。
				"command": "curl -fsSL http://<EDGE_DOWNLOAD_HOST>:8080/install/agent-install.sh | bash -s -- --mode edge --listen :18080 --hub-addr <HUB_IP>:8443 --server http://<EDGE_REPORT_HOST>:8080 --base-url http://<EDGE_DOWNLOAD_HOST>:8080/bin --tls-cert /path/edge.crt --tls-key /path/edge.key --tls-ca /path/ca.crt" + secretPart,
				"config":  "mode: \"edge\"\nserverURL: \"http://<EDGE_REPORT_HOST>:8080\"\nproxy:\n  listen: \":18080\"\n  hubAddr: \"<HUB_IP>:8443\"\n  tlsCert: \"/path/edge.crt\"\n  tlsKey: \"/path/edge.key\"\n  tlsCa: \"/path/ca.crt\"\n  bufferSize: 1000\n  poolSize: 2\n",
			},
			"agent": map[string]interface{}{
				"command": "curl -fsSL http://<EDGE_IP>:18080/install/agent-install.sh | bash -s -- --server http://<EDGE_IP>:18080 --base-url http://<EDGE_IP>:18080/bin --yes" + secretPart,
			},
			"hub": map[string]interface{}{
				"command": "curl -fsSL " + srv + "/install/agent-install.sh | bash -s -- --mode hub --listen :8443 --server " + srv + " --tls-cert /path/hub.crt --tls-key /path/hub.key --tls-ca /path/ca.crt" + secretPart,
				"config":  "mode: \"hub\"\nserverURL: \"" + srv + "\"\nproxy:\n  listen: \":8443\"\n  tlsCert: \"/path/hub.crt\"\n  tlsKey: \"/path/hub.key\"\n  tlsCa: \"/path/ca.crt\"\n",
			},
		},
	})
}

// handleAgentCheck 给 Agent 安装脚本做接入鉴权预检（agent 视角的连通性检查）。
// 与 HandleReport 同样走 X-Agent-Secret 校验，因此 200/401 能真实反映 Agent 上报能否被接受；
// 该路径在 AuthMiddleware 的 isPublicPath 中，不受登录 Bearer token 影响。
func (a *API) handleAgentCheck(w http.ResponseWriter, r *http.Request) {
	if !a.agentAuth.Enabled {
		writeJSON(w, 200, map[string]interface{}{"ok": true, "authEnabled": false})
		return
	}
	got := r.Header.Get("X-Agent-Secret")
	want := a.agentAuth.Secret
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "secret mismatch"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "authEnabled": true})
}

// handleVersion 返回 Server 版本信息（Agent/Web 版本由前端自行获取）。
func (a *API) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"server":    version.Version,
		"buildTime": version.BuildTime,
		"goVersion": version.GoVersion,
	})
}

// ---- 节点与分组 ----

func (a *API) handleNodes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"nodes": a.visibleNodes(a.nodeMgr.ListHostNodes(), Principal(r))})
}

// visibleNodes 按资源范围过滤节点列表；未启用认证（Principal 为 nil）或全局范围时原样返回。
func (a *API) visibleNodes(nodes []model.Node, p *auth.Principal) []model.Node {
	if p == nil {
		return nodes
	}
	return auth.FilterByGroup(p, nodes, func(n model.Node) string { return n.Group })
}

// nodeGroup 返回节点所属分组；nodeMgr 未注入或节点不存在时返回空串（视为不可归属）。
func (a *API) nodeGroup(node string) string {
	if a.nodeMgr == nil || node == "" {
		return ""
	}
	nd, ok := a.nodeMgr.GetNode(node)
	if !ok {
		return ""
	}
	return nd.Group
}

// nodeInScope 判断节点是否在当前用户的资源范围内。
// 未启用认证（p 为 nil）或全局范围恒为 true；节点不可归属（不存在/已移除）时受限用户不可见。
func (a *API) nodeInScope(p *auth.Principal, node string) bool {
	if p == nil || p.Scope.IsGlobal() {
		return true
	}
	return p.CanAccessGroup(a.nodeGroup(node))
}

// nodeOfKey 从 "node|instance"（或 "node|instance|…"）形式的实例键中取出 Agent 节点名。
func nodeOfKey(key string) string {
	if i := strings.IndexByte(key, '|'); i > 0 {
		return key[:i]
	}
	return ""
}

// filterByNodeScope 按资源范围过滤列表，nodeOf 返回元素所属的 Agent 节点名。
// 未启用认证（p 为 nil）或全局范围时原样返回，不产生额外开销。
func filterByNodeScope[T any](a *API, p *auth.Principal, items []T, nodeOf func(T) string) []T {
	if p == nil || p.Scope.IsGlobal() {
		return items
	}
	return auth.FilterByGroup(p, items, func(it T) string { return a.nodeGroup(nodeOf(it)) })
}

// deniedGroups 返回 names 中节点所属、但当前用户无权访问的分组（已去重）；
// 未启用认证或全局范围时返回 nil。用于批量操作的「先整体校验、再执行」。
func (a *API) deniedGroups(r *http.Request, names []string) []string {
	p := Principal(r)
	if p == nil || p.Scope.IsGlobal() || a.nodeMgr == nil {
		return nil
	}
	groups := make([]string, 0, len(names))
	for _, name := range names {
		if nd, ok := a.nodeMgr.GetNode(name); ok {
			groups = append(groups, nd.Group)
		}
	}
	seen := map[string]bool{}
	uniq := make([]string, 0, len(groups))
	for _, g := range auth.CheckBatchGroups(p, groups) {
		if !seen[g] {
			seen[g] = true
			uniq = append(uniq, g)
		}
	}
	return uniq
}

// handleNodesLatest 一次性聚合所有节点的关键指标，供主机列表展示。
// 返回结构：metrics[node] = { cpu, mem, disk, load1, netIn, netOut, diskRead, diskWrite }。
func (a *API) handleNodesLatest(w http.ResponseWriter, r *http.Request) {
	// 受限用户在没有任何可见节点时，结果必然被下方范围过滤全部剔除，
	// 因此与 catalog.go 的 skipStorage 一致，直接跳过全部 TSDB 聚合查询。
	if !a.visibleMetricNodes(Principal(r)) {
		writeJSON(w, 200, map[string]interface{}{"metrics": map[string]any{}})
		return
	}

	type nodeMetric struct {
		CPU        float64 `json:"cpu"`
		Mem        float64 `json:"mem"`
		Disk       float64 `json:"disk"`
		Load1      float64 `json:"load1"`
		Load5      float64 `json:"load5"`
		Load15     float64 `json:"load15"`
		NetIn      float64 `json:"netIn"`
		NetOut     float64 `json:"netOut"`
		DiskRead   float64 `json:"diskRead"`
		DiskWr     float64 `json:"diskWr"`
		DiskIopsR  float64 `json:"diskIopsR"`
		DiskIopsW  float64 `json:"diskIopsW"`
		NetDrop    float64 `json:"netDrop"`
		TcpRetrans float64 `json:"tcpRetrans"`
		MemTotal   float64 `json:"memTotal"`
		MemUsed    float64 `json:"memUsed"`
		ProcCount  float64 `json:"procCount"`
	}
	out := map[string]*nodeMetric{}

	// 通用取值（每节点一个样本）：cpu_usage / mem_used_percent / load1 / load5 / load15 / mem_total_bytes / mem_used_bytes / process_total / tcp_retransmit_rate
	for _, name := range []string{"cpu_usage", "mem_used_percent", "load1", "load5", "load15", "mem_total_bytes", "mem_used_bytes", "process_total", "tcp_retransmit_rate"} {
		series, err := a.store.QueryAllLatest(name, nil)
		if err != nil {
			slog.Warn("聚合指标查询失败", "metric", name, "err", err, "query", name)
			continue
		}
		slog.Debug("聚合指标查询结果", "metric", name, "series_count", len(series))
		for _, s := range series {
			node := s.Labels["node"]
			if node == "" || len(s.Points) == 0 {
				continue
			}
			v := s.Points[len(s.Points)-1].Value
			m, ok := out[node]
			if !ok {
				m = &nodeMetric{}
				out[node] = m
			}
			switch name {
			case "cpu_usage":
				m.CPU = round2(v)
			case "mem_used_percent":
				m.Mem = round2(v)
			case "load1":
				m.Load1 = round2(v)
			case "load5":
				m.Load5 = round2(v)
			case "load15":
				m.Load15 = round2(v)
			case "mem_total_bytes":
				m.MemTotal = v
			case "mem_used_bytes":
				m.MemUsed = v
			case "process_total":
				m.ProcCount = v
			case "tcp_retransmit_rate":
				m.TcpRetrans = round2(v)
			}
		}
	}

	diskUsage, err := aggregateDiskUsageByNode(a.store)
	if err != nil {
		slog.Warn("聚合磁盘使用率失败", "err", err)
	}
	for node, usage := range diskUsage {
		m, ok := out[node]
		if !ok {
			m = &nodeMetric{}
			out[node] = m
		}
		m.Disk = usage
	}

	// 跨标签聚合：network_*_rate 与 disk_*_rate 取 sum（单机多网卡/多设备求和）
	for _, name := range []string{"network_recv_rate", "network_sent_rate", "network_drop_rate", "disk_read_rate", "disk_write_rate", "disk_read_iops", "disk_write_iops"} {
		series, err := a.store.QueryAllLatest(name, nil)
		if err != nil {
			slog.Warn("聚合指标查询失败", "metric", name, "err", err)
			continue
		}
		for _, s := range series {
			node := s.Labels["node"]
			if node == "" || len(s.Points) == 0 {
				continue
			}
			v := s.Points[len(s.Points)-1].Value
			m, ok := out[node]
			if !ok {
				m = &nodeMetric{}
				out[node] = m
			}
			switch name {
			case "network_recv_rate":
				m.NetIn += v
			case "network_sent_rate":
				m.NetOut += v
			case "network_drop_rate":
				m.NetDrop += v
			case "disk_read_rate":
				m.DiskRead += v
			case "disk_write_rate":
				m.DiskWr += v
			case "disk_read_iops":
				m.DiskIopsR += v
			case "disk_write_iops":
				m.DiskIopsW += v
			}
		}
	}

	// 资源范围：受限用户既不该看到范围外已注册节点，也不该看到无法归属的未注册节点
	// （原实现只用 GetNode+group 比对剔除已注册的范围外节点，未注册节点会被 ok==false
	// 静默放过，泄露 ghost 之类未归属节点的聚合指标）。与 visibleMetricSeries 保持同一判定：
	// 显式要求 group 非空，未注册节点（group 为空）一律剔除，不依赖 CanAccessGroup("") 恰好为假。
	if p := Principal(r); p != nil && !p.Scope.IsGlobal() {
		for name := range out {
			if g := a.nodeGroup(name); g == "" || !p.CanAccessGroup(g) {
				delete(out, name)
			}
		}
	}

	writeJSON(w, 200, map[string]interface{}{"metrics": out})
}

func (a *API) handleNode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n, ok := a.nodeMgr.GetNode(name)
	if !ok {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, n)
}

func (a *API) handleNodeDelete(w http.ResponseWriter, r *http.Request) {
	a.nodeMgr.RemoveNode(r.PathValue("name"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleNodeUpgrade 标记节点待升级，Agent 下次上报时收到 upgrade 指令并自升级。
func (a *API) handleNodeUpgrade(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	node, ok := a.nodeMgr.GetNode(name)
	if !ok {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	// 目标二进制 = Server CDN 中该节点架构的 agent（由最近一次 Server 升级包放入）。
	// 以二进制 SHA256 作为升级目标，与 server 自身版本号解耦：CDN 里是什么，节点就升级到什么。
	binPath := filepath.Join(a.agentBinDir, "agent", "linux", node.Arch, "agent")
	targetSHA, err := fileSHA256(binPath)
	if err != nil {
		slog.Warn("CDN 中缺少该架构的 agent 二进制，无法下发升级", "node", name, "arch", node.Arch, "bin", binPath, "err", err)
		http.Error(w, "Server CDN 中没有该架构的 agent 二进制，请先在系统升级页上传并应用包含 agent 的升级包", http.StatusBadRequest)
		return
	}
	a.nodeMgr.RequestUpgrade(name, targetSHA, version.Version)
	slog.Info("收到 Agent 升级请求", "node", name, "arch", node.Arch, "targetSHA", targetSHA, "targetVersion", version.Version)
	writeJSON(w, 200, map[string]string{
		"status":  "ok",
		"message": "升级任务已下发，等待 Agent 下次心跳时执行",
	})
}

// handleNodesUpgrade 批量标记节点待升级（主机列表“批量升级”）。
// 请求体：{"names":["host1","host2",...]}。逐节点复用单机升级的同款逻辑（按架构取 CDN 二进制 SHA256）。
func (a *API) handleNodesUpgrade(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if len(req.Names) == 0 {
		http.Error(w, "names is empty", http.StatusBadRequest)
		return
	}
	// 资源范围：请求中若包含范围外节点，整体拒绝并回报涉及的分组。
	if denied := a.deniedGroups(r, req.Names); len(denied) > 0 {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error":  "部分节点不在您的资源范围内",
			"groups": denied,
		})
		return
	}
	details := make([]map[string]string, 0, len(req.Names))
	queued, skipped := 0, 0
	for _, name := range req.Names {
		node, ok := a.nodeMgr.GetNode(name)
		if !ok {
			details = append(details, map[string]string{"name": name, "status": "not_found"})
			skipped++
			continue
		}
		binPath := filepath.Join(a.agentBinDir, "agent", "linux", node.Arch, "agent")
		targetSHA, err := fileSHA256(binPath)
		if err != nil {
			slog.Warn("批量升级：CDN 缺少该架构 agent 二进制", "node", name, "arch", node.Arch, "err", err)
			details = append(details, map[string]string{"name": name, "status": "no_binary", "arch": node.Arch})
			skipped++
			continue
		}
		a.nodeMgr.RequestUpgrade(name, targetSHA, version.Version)
		details = append(details, map[string]string{"name": name, "status": "queued"})
		queued++
	}
	slog.Info("收到 Agent 批量升级请求", "total", len(req.Names), "queued", queued, "skipped", skipped)
	writeJSON(w, 200, map[string]any{
		"status":  "ok",
		"queued":  queued,
		"skipped": skipped,
		"details": details,
	})
}

// fileSHA256 计算文件的 SHA256（十六进制小写）。
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (a *API) handleNodeGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Group == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := a.nodeMgr.SetNodeGroup(r.PathValue("name"), body.Group); err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleNodeDisplayName 设置节点的自定义显示名（别名），不修改 Agent 上报的真实主机名。
func (a *API) handleNodeDisplayName(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if runes := []rune(body.DisplayName); len(runes) > 64 {
		http.Error(w, "displayName too long (max 64)", http.StatusBadRequest)
		return
	}
	if err := a.nodeMgr.SetNodeDisplayName(r.PathValue("name"), body.DisplayName); err != nil {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *API) handleGroups(w http.ResponseWriter, r *http.Request) {
	groups := a.nodeMgr.ListGroups()
	if p := Principal(r); p != nil {
		groups = auth.FilterByGroup(p, groups, func(g model.Group) string { return g.Name })
	}
	writeJSON(w, 200, map[string]interface{}{"groups": groups})
}

func (a *API) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	var g model.Group
	if err := json.NewDecoder(r.Body).Decode(&g); err != nil || g.Name == "" {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	a.nodeMgr.AddGroup(g.Name, g.Description)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *API) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	a.nodeMgr.RemoveGroup(r.PathValue("name"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// ---- 指标查询 ----

func aggregateDiskUsageByNode(store storage.Storage) (map[string]float64, error) {
	totals, err := store.QueryAllLatest("disk_total", nil)
	if err != nil {
		return nil, err
	}
	used, err := store.QueryAllLatest("disk_used", nil)
	if err != nil {
		return nil, err
	}
	totalByNode := sumLatestByNode(totals)
	usedByNode := sumLatestByNode(used)
	out := make(map[string]float64, len(totalByNode))
	for node, total := range totalByNode {
		if total <= 0 {
			continue
		}
		out[node] = round2(usedByNode[node] / total * 100)
	}
	return out, nil
}

func aggregateDiskUsageForNode(store storage.Storage, node string) (*model.Point, error) {
	totals, err := store.QueryInstant(node, "disk_total", nil)
	if err != nil {
		return nil, err
	}
	used, err := store.QueryInstant(node, "disk_used", nil)
	if err != nil {
		return nil, err
	}
	var totalValue, usedValue float64
	var ts int64
	for _, s := range totals {
		if len(s.Points) == 0 {
			continue
		}
		p := s.Points[len(s.Points)-1]
		totalValue += p.Value
		if p.Timestamp > ts {
			ts = p.Timestamp
		}
	}
	for _, s := range used {
		if len(s.Points) == 0 {
			continue
		}
		p := s.Points[len(s.Points)-1]
		usedValue += p.Value
		if p.Timestamp > ts {
			ts = p.Timestamp
		}
	}
	if totalValue <= 0 {
		return nil, nil
	}
	return &model.Point{Timestamp: ts, Value: round2(usedValue / totalValue * 100)}, nil
}

func sumLatestByNode(series []model.Series) map[string]float64 {
	out := make(map[string]float64)
	for _, s := range series {
		node := s.Labels["node"]
		if node == "" || len(s.Points) == 0 {
			continue
		}
		out[node] += s.Points[len(s.Points)-1].Value
	}
	return out
}

func (a *API) handleQueryRange(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("metric")
	if name == "" {
		http.Error(w, "metric required", http.StatusBadRequest)
		return
	}
	start, errStart := strconv.ParseInt(q.Get("start"), 10, 64)
	end, errEnd := strconv.ParseInt(q.Get("end"), 10, 64)
	if errStart != nil || errEnd != nil || start <= 0 || end <= start {
		http.Error(w, "start/end 必须为合法毫秒时间戳且 end>start", http.StatusBadRequest)
		return
	}
	// 普通趋势查询也必须限制跨度，避免异常参数拖垮时序库。
	const maxRangeSpan = int64(7 * 24 * 3600 * 1000)
	if end-start > maxRangeSpan {
		http.Error(w, "查询时间跨度不能超过 7 天", http.StatusBadRequest)
		return
	}
	step, errStep := strconv.ParseInt(q.Get("step"), 10, 64)
	if q.Get("step") != "" && errStep != nil {
		http.Error(w, "step 必须为合法毫秒数", http.StatusBadRequest)
		return
	}
	if step <= 0 {
		// 默认按跨度自动降采样到约 300 点
		span := end - start
		step = span / 300
		if step < 60000 {
			step = 60000 // 最少 60s（毫秒单位）
		}
	}
	if step < 1000 || step > end-start {
		http.Error(w, "step 必须在 1 秒到查询跨度之间", http.StatusBadRequest)
		return
	}
	labels := parseLabelQuery(q)
	node, ok := a.metricTarget(w, r, "nodes:read", labels)
	if !ok {
		return
	}
	if node == "" && !a.visibleMetricNodes(Principal(r)) {
		writeJSON(w, 200, map[string]interface{}{"series": []model.Series{}})
		return
	}
	series, err := a.store.QueryRange(node, name, labels, start, end, step)
	if err != nil {
		slog.Error("查询失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	series = a.visibleMetricSeries(Principal(r), node, series)
	writeJSON(w, 200, map[string]interface{}{"series": series})
}

func (a *API) handleQueryLatest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	name := q.Get("metric")
	labels := parseLabelQuery(q)
	node, ok := a.metricTarget(w, r, "nodes:read", labels)
	if !ok {
		return
	}
	if node == "" || name == "" {
		http.Error(w, "node and metric required", http.StatusBadRequest)
		return
	}
	series, err := a.store.QueryInstant(node, name, labels)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	series = a.visibleMetricSeries(Principal(r), node, series)
	var point *model.Point
	if len(series) > 0 && len(series[0].Points) > 0 {
		p := series[0].Points[len(series[0].Points)-1]
		point = &p
	}
	// 保留 point 兼容单序列调用，同时返回带 labels 的 series，供端口状态
	// 等多序列指标一次性读取所有标签维度。
	writeJSON(w, 200, map[string]interface{}{"point": point, "series": series})
}

func (a *API) handleProcesses(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
	// 优先从进程快照缓存获取完整字段（内存/IO/状态/fd 等）。
	// 缓存命中但为空时也应返回空数组，不应回退到旧时序数据。
	if procs, ok := receiver.ProcessCache.Get(node); ok {
		writeJSON(w, 200, map[string]interface{}{"processes": procs})
		return
	}
	// 回退：从时序库查询 proc_cpu + proc_mem（旧版 Agent 或缓存未命中）
	cpuSeries, err := a.store.QueryInstant(node, "proc_cpu", nil)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	memSeries, _ := a.store.QueryInstant(node, "proc_mem", nil)

	memByKey := map[string]float64{}
	for _, s := range memSeries {
		key := s.Labels["pid"] + "|" + s.Labels["comm"]
		if len(s.Points) > 0 {
			memByKey[key] = s.Points[len(s.Points)-1].Value
		}
	}
	type proc struct {
		PID  string  `json:"pid"`
		Name string  `json:"name"`
		CPU  float64 `json:"cpu"`
		Mem  float64 `json:"mem"`
	}
	var out []proc
	for _, s := range cpuSeries {
		pid := s.Labels["pid"]
		comm := s.Labels["comm"]
		cpu := 0.0
		if len(s.Points) > 0 {
			cpu = s.Points[len(s.Points)-1].Value
		}
		out = append(out, proc{PID: pid, Name: comm, CPU: cpu, Mem: memByKey[pid+"|"+comm]})
	}
	writeJSON(w, 200, map[string]interface{}{"processes": out})
}

// handleListeners 返回指定节点的监听端口列表（TCP/UDP）。
func (a *API) handleListeners(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
	listeners := receiver.ListenerCache.Get(node)
	writeJSON(w, 200, map[string]interface{}{"listeners": listeners})
}

// handleFirewall 返回指定节点的防火墙规则列表。
func (a *API) handleFirewall(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
	rules := receiver.FirewallCache.Get(node)
	writeJSON(w, 200, map[string]interface{}{"rules": rules})
}

// handleFirewallStatus 返回指定节点的防火墙整体状态（后端类型/运行/自启/版本等）。
func (a *API) handleFirewallStatus(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
	status := receiver.FirewallStatusCache.Get(node)
	writeJSON(w, 200, map[string]interface{}{"status": status})
}

// parseLabelQuery 从 URL 查询中提取 labels.<name>=<value> 形式的标签过滤。
func parseLabelQuery(q map[string][]string) map[string]string {
	labels := map[string]string{}
	for k, v := range q {
		if strings.HasPrefix(k, "labels.") && len(v) > 0 {
			labels[strings.TrimPrefix(k, "labels.")] = v[0]
		}
	}
	return labels
}

// ---- 告警 ----

func (a *API) handleAlerts(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		node = r.URL.Query().Get("node")
	}
	state := r.URL.Query().Get("state")
	instance := r.URL.Query().Get("instance")
	var events []model.AlertEvent
	if state == "active" || state == string(model.AlertStateFiring) {
		events = a.unacknowledgedAlerts(a.alerts.Active())
	} else {
		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			if v, err := strconv.Atoi(l); err == nil && v > 0 {
				limit = v
			}
		}
		events = a.alerts.Recent(limit)
	}
	if node != "" || instance != "" {
		filtered := make([]model.AlertEvent, 0, len(events))
		for _, e := range events {
			if (node == "" || e.Node == node) && (instance == "" || e.Instance == instance) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}
	// 资源范围：受限用户只能看到范围内节点产生的告警。
	events = filterByNodeScope(a, Principal(r), events, func(e model.AlertEvent) string { return e.Node })
	writeJSON(w, 200, map[string]interface{}{"alerts": events})
}

// unacknowledgedAlerts 返回尚未人工确认的告警，用于待处理告警的展示与统计。
// 确认不改变监控条件的真实 firing 状态，以免影响引擎重启后的状态恢复。
func (a *API) unacknowledgedAlerts(events []model.AlertEvent) []model.AlertEvent {
	if a.acks == nil || len(events) == 0 {
		return events
	}
	out := make([]model.AlertEvent, 0, len(events))
	for _, event := range events {
		// 处置接口使用 RuleName 作为键，保持与既有确认记录兼容。
		// 已认领/已关闭的告警从待处理列表移除；被「重新打开」的会重新出现。
		if !a.acks.IsHandled(event.RuleName, event.Node, event.Instance, event.StartsAt) {
			out = append(out, event)
		}
	}
	return out
}

// ---- 告警规则 CRUD ----

func (a *API) handleRulesList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"rules": a.rules.List()})
}

func (a *API) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	var rule model.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := alert.ValidateRule(rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rule.ID = ""
	created := a.rules.Create(rule)
	RecordChangeAudit(a.audit, r, "create_rule", nil, created)
	writeJSON(w, 200, created)
}

func (a *API) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	var rule model.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	before, _ := a.rules.Get(id)
	rule.ID = id
	if err := alert.ValidateRule(rule); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := a.rules.Update(rule); err != nil {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if before.Enabled && !rule.Enabled && a.engine != nil {
		a.engine.DisableRule(rule)
	}
	RecordChangeAudit(a.audit, r, "update_rule", before, rule)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *API) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	before, ok := a.rules.Get(id)
	if !ok {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if err := a.rules.Delete(id); err != nil {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if a.engine != nil {
		a.engine.CloseRuleAlerts(before, "规则已删除，告警状态已关闭")
	}
	RecordChangeAudit(a.audit, r, "delete_rule", before, nil)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// handleRuleTemplates 返回可复用的告警规则模板（供前端“从模板新建”）。
func (a *API) handleRuleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"templates": alert.DefaultTemplates()})
}

// handleAlertAcks 返回全部已确认告警的 key 映射（rule|host|instance|startsAt）。
func (a *API) handleAlertAcks(w http.ResponseWriter, r *http.Request) {
	acks := a.acks.Map()
	// 资源范围：受限用户只能看到范围内节点的确认记录（key 第 2 段为节点名）。
	if p := Principal(r); p != nil && !p.Scope.IsGlobal() {
		for k := range acks {
			parts := strings.SplitN(k, "|", 3)
			if len(parts) < 2 || !a.nodeInScope(p, parts[1]) {
				delete(acks, k)
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{"acks": acks})
}

// handleAlertAck 等协作处置接口见 alert_collab.go。

// handleRuleToggle 切换规则启用/停用状态。
func (a *API) handleRuleToggle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, ok := a.rules.Get(id)
	if !ok {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	rule.Enabled = !rule.Enabled
	if err := a.rules.Update(rule); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	// 规则由启用变为停用时，立即通知引擎清理该规则的 firing 与未处理安全事件队列，
	// 避免前端已显示关闭但后台仍在产生新告警。
	if !rule.Enabled && a.engine != nil {
		a.engine.DisableRule(rule)
	}
	writeJSON(w, 200, rule)
}

// handleRuleToggleSilence 通过 body 的 {silenced: bool} 设置规则静默（支持周期静默时段外的临时静默）。
func (a *API) handleRuleToggleSilence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rule, ok := a.rules.Get(id)
	if !ok {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	var body struct {
		Silenced bool  `json:"silenced"`
		Until    int64 `json:"silenceUntil"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		// 允许空 body（仅按查询参数行为处理）；解码失败也继续
	}
	rule.Silenced = body.Silenced
	if body.Until > 0 {
		rule.SilenceUntil = body.Until
	} else if !body.Silenced {
		rule.SilenceUntil = 0
	}
	if err := a.rules.Update(rule); err != nil {
		http.Error(w, "update failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, rule)
}

// writeJSON 统一 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// round2 保留两位小数。
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// slotRangeStart 返回 slot 区间的起始槽位号（"0-5460" → 0；单槽 "7000" → 7000）。
func slotRangeStart(r string) int {
	if i := strings.Index(r, "-"); i >= 0 {
		if v, err := strconv.Atoi(strings.TrimSpace(r[:i])); err == nil {
			return v
		}
		return 0
	}
	if v, err := strconv.Atoi(strings.TrimSpace(r)); err == nil {
		return v
	}
	return 0
}

// ---- 中间件监控：Redis ----

// handleRedisInstances 聚合所有 Redis 实例的最新状态与关键指标，供前端列表展示。
// 通过 QueryAllLatest 查询 redis_instance_up 获取实例清单（含 instance/role/topology/version 标签），
// 再批量查询 redis_connected_clients/redis_used_memory/redis_used_memory_percent/redis_ops_per_sec/
// redis_uptime_in_seconds/redis_hit_rate/redis_keys 等指标，按 instance 标签聚合到实例对象。
func (a *API) handleRedisInstances(w http.ResponseWriter, r *http.Request) {
	// 1. 查询 redis_instance_up 获取实例清单
	upSeries, err := a.store.QueryAllLatest("redis_instance_up", nil)
	if err != nil {
		slog.Error("查询 Redis 实例失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	type redisInstanceInfo struct {
		Node              string  `json:"node"`
		NodeIP            string  `json:"nodeIp"`
		Instance          string  `json:"instance"`
		Name              string  `json:"name"`
		Role              string  `json:"role"`
		Topology          string  `json:"topology"`
		Version           string  `json:"version"`
		Up                bool    `json:"up"`
		Clients           float64 `json:"clients"`
		Blocked           float64 `json:"blocked"`
		UsedMemory        float64 `json:"usedMemory"`
		MaxMemory         float64 `json:"maxMemory"`
		MemPercent        float64 `json:"memPercent"`
		Fragmentation     float64 `json:"fragmentation"`
		Ops               float64 `json:"ops"`
		Uptime            float64 `json:"uptime"`
		HitRate           float64 `json:"hitRate"`
		Keys              float64 `json:"keys"`
		Evicted           float64 `json:"evicted"`
		Expired           float64 `json:"expired"`
		Rejected          float64 `json:"rejected"`
		ConnectedSlaves   float64 `json:"connectedSlaves"`
		ReplicationOffset float64 `json:"replicationOffset"`
		ReplicationLag    float64 `json:"replicationLag"`
		Group             string  `json:"group"`
		// 集群指标（cluster 拓扑实例）。指针用于区分明确采集到 0 与尚未采集到数据。
		ClusterState         *float64 `json:"clusterState,omitempty"` // 1=ok, 0=fail
		ClusterSlotsAssigned *float64 `json:"clusterSlotsAssigned,omitempty"`
		ClusterSlotsOk       *float64 `json:"clusterSlotsOk,omitempty"`
		ClusterSlotsFail     *float64 `json:"clusterSlotsFail,omitempty"`
		ClusterKnownNodes    *float64 `json:"clusterKnownNodes,omitempty"`
		ClusterSize          *float64 `json:"clusterSize,omitempty"`
		// 哨兵指标（sentinel 拓扑实例）
		SentinelMasters   float64 `json:"sentinelMasters"`
		SentinelSlaves    float64 `json:"sentinelSlaves"`
		SentinelSentinels float64 `json:"sentinelSentinels"`
		SentinelTilt      float64 `json:"sentinelTilt"`
		// 哨兵→master 关联（master 实例上 labels.sentinel_master_of），用于关系图
		ReplicaOf string `json:"replicaOf,omitempty"`
		// 集群中 replica→master 关联（replica 实例上 labels.cluster_master_of），用于关系图
		// ClusterMasterOf removed: unified into ReplicaOf
		// 集群 master 的 slot 区间列表（来自 redis_cluster_slot_range 元信息指标），用于分片图
		SlotRanges []string `json:"slotRanges,omitempty"`
	}

	// 以 "node|instance" 为 key 建立实例索引。
	// 同一 node|instance 可能在 VM 中存在多条 redis_instance_up 序列（如 agent 改过
	// name/group 配置后旧 label 序列在 staleness 窗口内尚未消散）。去重时取“最新数据点
	// 时间戳最大”的那条；时间戳相同再优先取 name 非空的那条，避免被旧的空 name 序列覆盖。
	instances := map[string]*redisInstanceInfo{}
	latestTs := map[string]int64{}
	var keys []string
	for _, s := range upSeries {
		node := s.Labels["node"]
		instance := s.Labels["instance"]
		if node == "" || instance == "" || len(s.Points) == 0 {
			continue
		}
		key := node + "|" + instance
		ts := s.Points[len(s.Points)-1].Timestamp
		name := s.Labels["name"]
		if prev, exists := instances[key]; exists {
			// 已存在：仅当本条更新（时间戳更大），或时间戳相同但本条 name 非空而已存为空时才覆盖
			if ts < latestTs[key] {
				continue
			}
			if ts == latestTs[key] && !(name != "" && prev.Name == "") {
				continue
			}
		}
		instances[key] = &redisInstanceInfo{
			Node:      node,
			NodeIP:    a.nodeIP(node),
			Instance:  instance,
			Name:      name,
			Role:      s.Labels["role"],
			Topology:  s.Labels["topology"],
			Version:   s.Labels["version"],
			Group:     s.Labels["group"],
			ReplicaOf: s.Labels["replica_of"],
			Up:        s.Points[len(s.Points)-1].Value > 0,
		}
		if _, seen := latestTs[key]; !seen {
			keys = append(keys, key)
		}
		latestTs[key] = ts
	}

	// 配置清单补充：对已在注册表中但当前 up 指标因 agent 离线而缺失的实例，
	// 补列出来并标记为离线，避免误判为"尚未配置 Redis 监控"。
	for _, ri := range instancereg.Default.RedisInstances() {
		key := ri.Node + "|" + ri.Instance
		if _, ok := instances[key]; ok {
			continue
		}
		instances[key] = &redisInstanceInfo{
			Node:      ri.Node,
			NodeIP:    a.nodeIP(ri.Node),
			Instance:  ri.Instance,
			Name:      ri.Name,
			Role:      ri.Role,
			Topology:  ri.Topology,
			Version:   ri.Version,
			Group:     ri.Group,
			ReplicaOf: ri.ReplicaOf,
			Up:        false,
		}
		keys = append(keys, key)
	}

	// 2. 批量查询关键指标，按 instance 标签填充到实例
	floatPtr := func(v float64) *float64 {
		vv := round2(v)
		return &vv
	}

	metricMap := map[string]func(ri *redisInstanceInfo, v float64){
		"redis_connected_clients":          func(ri *redisInstanceInfo, v float64) { ri.Clients = round2(v) },
		"redis_blocked_clients":            func(ri *redisInstanceInfo, v float64) { ri.Blocked = round2(v) },
		"redis_used_memory":                func(ri *redisInstanceInfo, v float64) { ri.UsedMemory = round2(v) },
		"redis_maxmemory":                  func(ri *redisInstanceInfo, v float64) { ri.MaxMemory = round2(v) },
		"redis_used_memory_percent":        func(ri *redisInstanceInfo, v float64) { ri.MemPercent = round2(v) },
		"redis_memory_fragmentation_ratio": func(ri *redisInstanceInfo, v float64) { ri.Fragmentation = round2(v) },
		"redis_ops_per_sec":                func(ri *redisInstanceInfo, v float64) { ri.Ops = round2(v) },
		"redis_uptime_in_seconds":          func(ri *redisInstanceInfo, v float64) { ri.Uptime = round2(v) },
		"redis_hit_rate":                   func(ri *redisInstanceInfo, v float64) { ri.HitRate = round2(v) },
		"redis_keys":                       func(ri *redisInstanceInfo, v float64) { ri.Keys = round2(v) },
		"redis_evicted_keys":               func(ri *redisInstanceInfo, v float64) { ri.Evicted = round2(v) },
		"redis_expired_keys":               func(ri *redisInstanceInfo, v float64) { ri.Expired = round2(v) },
		"redis_rejected_connections":       func(ri *redisInstanceInfo, v float64) { ri.Rejected = round2(v) },
		"redis_connected_slaves":           func(ri *redisInstanceInfo, v float64) { ri.ConnectedSlaves = round2(v) },
		"redis_replication_offset":         func(ri *redisInstanceInfo, v float64) { ri.ReplicationOffset = round2(v) },
		"redis_replication_lag":            func(ri *redisInstanceInfo, v float64) { ri.ReplicationLag = round2(v) },
		// 集群指标
		"redis_cluster_state":          func(ri *redisInstanceInfo, v float64) { ri.ClusterState = floatPtr(v) },
		"redis_cluster_slots_assigned": func(ri *redisInstanceInfo, v float64) { ri.ClusterSlotsAssigned = floatPtr(v) },
		"redis_cluster_slots_ok":       func(ri *redisInstanceInfo, v float64) { ri.ClusterSlotsOk = floatPtr(v) },
		"redis_cluster_slots_fail":     func(ri *redisInstanceInfo, v float64) { ri.ClusterSlotsFail = floatPtr(v) },
		"redis_cluster_known_nodes":    func(ri *redisInstanceInfo, v float64) { ri.ClusterKnownNodes = floatPtr(v) },
		"redis_cluster_size":           func(ri *redisInstanceInfo, v float64) { ri.ClusterSize = floatPtr(v) },
		// 哨兵指标
		"redis_sentinel_masters":   func(ri *redisInstanceInfo, v float64) { ri.SentinelMasters = round2(v) },
		"redis_sentinel_slaves":    func(ri *redisInstanceInfo, v float64) { ri.SentinelSlaves = round2(v) },
		"redis_sentinel_sentinels": func(ri *redisInstanceInfo, v float64) { ri.SentinelSentinels = round2(v) },
		"redis_sentinel_tilt":      func(ri *redisInstanceInfo, v float64) { ri.SentinelTilt = round2(v) },
	}
	for metricName, setter := range metricMap {
		series, err := a.store.QueryAllLatest(metricName, nil)
		if err != nil {
			slog.Warn("聚合 Redis 指标查询失败", "metric", metricName, "err", err)
			continue
		}

		// 集群级指标（cluster_state/slots/size/known_nodes 等）在 agent 端只绑定到集群入口实例，
		// 但详情抽屉需要在任意节点上都能看到整体集群状态，因此按 node|group 维度做组级聚合。
		isClusterMetric := strings.HasPrefix(metricName, "redis_cluster_") && metricName != "redis_cluster_slot_range"
		if isClusterMetric {
			groupLatest := map[string]float64{}
			groupTs := map[string]int64{}
			for _, s := range series {
				node := s.Labels["node"]
				group := s.Labels["group"]
				if node == "" || group == "" || len(s.Points) == 0 {
					continue
				}
				key := node + "|" + group
				ts := s.Points[len(s.Points)-1].Timestamp
				if ts > groupTs[key] {
					groupTs[key] = ts
					groupLatest[key] = s.Points[len(s.Points)-1].Value
				}
			}
			for _, ri := range instances {
				if ri.Topology != "cluster" {
					continue
				}
				key := ri.Node + "|" + ri.Group
				if v, ok := groupLatest[key]; ok {
					setter(ri, v)
				}
			}
			continue
		}

		for _, s := range series {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			if node == "" || instance == "" || len(s.Points) == 0 {
				continue
			}
			key := node + "|" + instance
			ri, ok := instances[key]
			if !ok {
				continue
			}
			setter(ri, s.Points[len(s.Points)-1].Value)
			// 哨兵→master 关联标签透传
			if sm, ok := s.Labels["sentinel_master_of"]; ok && sm != "" && ri.ReplicaOf == "" {
				ri.ReplicaOf = "sentinel:" + sm
			}
			// 集群中 replica→master 关联标签透传
			// cluster replica 关系已由 labels.replica_of 在初始化时写入 ReplicaOf
		}
	}

	// 2.5 聚合集群 master 的 slot 区间（redis_cluster_slot_range 元信息指标）
	if slotSeries, err := a.store.QueryAllLatest("redis_cluster_slot_range", nil); err == nil {
		for _, s := range slotSeries {
			node := s.Labels["node"]
			instance := s.Labels["instance"]
			rng := s.Labels["range"]
			if node == "" || instance == "" || rng == "" {
				continue
			}
			ri, ok := instances[node+"|"+instance]
			if !ok {
				continue
			}
			// 去重
			exists := false
			for _, r2 := range ri.SlotRanges {
				if r2 == rng {
					exists = true
					break
				}
			}
			if !exists {
				ri.SlotRanges = append(ri.SlotRanges, rng)
			}
		}
		// 按区间起始槽位排序，保证分片图从左到右递增
		for _, ri := range instances {
			if len(ri.SlotRanges) > 1 {
				sort.Slice(ri.SlotRanges, func(a, b int) bool {
					return slotRangeStart(ri.SlotRanges[a]) < slotRangeStart(ri.SlotRanges[b])
				})
			}
		}
	}

	// 3. 转为列表返回（先按资源范围过滤，受限用户不可见范围外节点上的实例）
	keys = filterByNodeScope(a, Principal(r), keys, nodeOfKey)
	out := make([]redisInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}
