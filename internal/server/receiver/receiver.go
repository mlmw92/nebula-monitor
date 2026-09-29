// Package receiver 实现 Server 的上报接收 HTTP 接口。
package receiver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/instancereg"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/nginxaccess"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/security"
	"github.com/nebula/monitor/internal/server/storage"
	"github.com/nebula/monitor/internal/template"
)

// itoa 整型转字符串。
func itoa(v int) string { return strconv.Itoa(v) }

// processCache 节点进程快照缓存（内存），供 API 实时查询。
// 进程列表是高基数快照数据，不适合写入时序库，采用最新值覆盖策略。
type processCache struct {
	mu      sync.RWMutex
	entries map[string]processEntry // key: node name
}

type processEntry struct {
	Processes []model.ProcessStat
	UpdatedAt time.Time
}

// ProcessCache 全局进程缓存实例。
var ProcessCache = &processCache{
	entries: make(map[string]processEntry),
}

// Set 更新指定节点的进程快照。
func (c *processCache) Set(node string, procs []model.ProcessStat) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]model.ProcessStat, len(procs))
	copy(copied, procs)
	c.entries[node] = processEntry{Processes: copied, UpdatedAt: time.Now()}
}

// Get 获取指定节点的最新进程快照。第二个返回值区分缓存未命中与显式空快照。
func (c *processCache) Get(node string) ([]model.ProcessStat, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[node]; ok {
		return e.Processes, true
	}
	return nil, false
}

// snapshotCache 通用快照缓存（泛型替代，用于监听端口/防火墙规则等高基数数据）。
type snapshotCache[T any] struct {
	mu      sync.RWMutex
	entries map[string]snapshotEntry[T]
}

type snapshotEntry[T any] struct {
	Data      T
	UpdatedAt time.Time
}

func newSnapshotCache[T any]() *snapshotCache[T] {
	return &snapshotCache[T]{entries: make(map[string]snapshotEntry[T])}
}

func (c *snapshotCache[T]) Set(node string, data T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[node] = snapshotEntry[T]{Data: data, UpdatedAt: time.Now()}
}

func (c *snapshotCache[T]) Get(node string) T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if e, ok := c.entries[node]; ok {
		return e.Data
	}
	var zero T
	return zero
}

// ListenerCache 监听端口快照缓存。
var ListenerCache = newSnapshotCache[[]model.ListenerStat]()

// FirewallCache 防火墙规则快照缓存。
var FirewallCache = newSnapshotCache[[]model.FirewallRule]()

// FirewallStatusCache 防火墙整体状态快照缓存（用于主机详情-防火墙监控 Tab 顶部状态展示）。
var FirewallStatusCache = newSnapshotCache[*model.FirewallStatus]()

// Receiver 接收 Agent 上报并写入存储、更新节点索引。
type Receiver struct {
	storage   storage.Storage
	nodeMgr   *node.Manager
	auth      config.AgentAuthConfig
	ngx       *nginxaccess.Window    // Nginx access log 地理聚合窗口（可空）
	sec       *security.Store        // 安全事件/基线存储（可空，传 nil 关闭安全能力）
	alerts    *alert.Engine          // 告警引擎（安全事件注入告警中心，可空）
	defense   *security.DefenseStore // 受控 fail2ban 入侵防御任务存储（可空）
	templates TemplateProvider       // 采集项模板下发数据源（可空：不注入则不下发）

	// 集中日志（C2）：logs 为 nil 表示该能力关闭（接口回 503，与不配置 logSources 的 Agent 恰好对称）
	logs       *logstore.Store
	logMaxBody int64
	logLimiter *logRateLimiter
}

// TemplateProvider 提供下发给 Agent 的采集项模板（由 templates 包实现）。
// 只依赖「取快照 + 版本号」这一最小能力，避免 receiver 依赖存储实现细节。
type TemplateProvider interface {
	// Snapshot 一次性返回模板列表与其版本号（两者必须来自同一时刻，否则可能下发到「旧内容 + 新版本号」）。
	Snapshot() ([]template.Config, uint64)
}

// SetTemplateStore 注入采集项模板下发源（C1 阶段二；未注入则不下发，行为与阶段一一致）。
func (r *Receiver) SetTemplateStore(p TemplateProvider) { r.templates = p }

// New 创建 Receiver。auth 为 Agent 接入授权配置（参考哪吒探针密钥机制）；
// ngx 为 Nginx access log 聚合窗口，可传 nil 关闭该能力；
// sec/alerts 为安全能力依赖，可传 nil 关闭安全事件落库与告警；
// defense 为受控入侵防御任务存储，可传 nil 关闭防护能力。
func New(s storage.Storage, mgr *node.Manager, auth config.AgentAuthConfig, ngx *nginxaccess.Window,
	sec *security.Store, alerts *alert.Engine, defense *security.DefenseStore) *Receiver {
	return &Receiver{storage: s, nodeMgr: mgr, auth: auth, ngx: ngx, sec: sec, alerts: alerts, defense: defense}
}

// HandleReport 处理 POST /api/v1/report。
func (r *Receiver) HandleReport(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 接入授权校验：与日志上行共用一份实现（两条路径的凭据必须永远一致）
	if !r.agentAuthorized(req) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// 上报体也加上限：原先直接 json.Decode(req.Body) 无任何限制，
	// 一个坏 Agent（或误配）就能把 Server 的内存吃满。上限取日志上限的 4 倍：
	// 上报体正常只有几十 KB，但含防护结果/安全事件时可能到 MB 级。
	body := http.MaxBytesReader(w, req.Body, 4*r.logBodyLimit())
	var payload model.ReportPayload
	if err := json.NewDecoder(body).Decode(&payload); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if payload.Node == "" {
		http.Error(w, "node is required", http.StatusBadRequest)
		return
	}

	// 更新节点索引与心跳
	r.nodeMgr.Register(&payload)

	// 补充分组标签后写入 VM
	metrics := make([]model.Metric, 0, len(payload.Metrics)+len(payload.Processes)*2+len(payload.RedisInstances))
	for _, m := range payload.Metrics {
		if m.Node == "" {
			m.Node = payload.Node
		}
		if m.Labels == nil {
			m.Labels = map[string]string{}
		}
		// 中间件实例指标（mysql/kafka/postgres 等）已自带 group 标签（实例名/集群名），
		// 仅当缺失时才回退到节点分组，避免把实例分组覆盖成默认的 "default"。
		if m.Labels["group"] == "" {
			m.Labels["group"] = payload.Group
		}
		metrics = append(metrics, m)
	}
	// 进程 TOP 榜写入 VM（带 pid/comm 标签），便于跨副本查询
	for _, p := range payload.Processes {
		labels := map[string]string{"group": payload.Group, "pid": itoa(int(p.PID)), "comm": p.Name}
		metrics = append(metrics,
			model.Metric{Node: payload.Node, Name: "proc_cpu", Labels: labels, Value: p.CPU, Timestamp: payload.ReportAt},
			model.Metric{Node: payload.Node, Name: "proc_mem", Labels: labels, Value: p.Mem, Timestamp: payload.ReportAt},
		)
	}
	// 更新进程快照缓存（供 API 实时查询完整进程列表）。新 Agent 发送 [] 时也要
	// 清空缓存；旧 Agent 未携带字段时 JSON 解码结果为 nil，保留时序回退。
	if payload.Processes != nil {
		ProcessCache.Set(payload.Node, payload.Processes)
	}
	// 更新监听端口快照缓存；显式空数组表示主机当前没有可见监听端口。
	if payload.Listeners != nil {
		ListenerCache.Set(payload.Node, payload.Listeners)
	}
	// 更新防火墙规则快照缓存；显式空数组表示未发现规则/后端。
	if payload.FirewallRules != nil {
		FirewallCache.Set(payload.Node, payload.FirewallRules)
	}
	// 更新防火墙整体状态快照缓存；nil 表示旧 Agent 不支持上报该字段。
	if payload.FirewallStatus != nil {
		payload.FirewallStatus.Node = payload.Node
		FirewallStatusCache.Set(payload.Node, payload.FirewallStatus)
	}
	// Redis 实例元信息转为 redis_instance_up 指标写入 VM，供前端聚合查询
	for _, ri := range payload.RedisInstances {
		upVal := 0.0
		if ri.Up {
			upVal = 1
		}
		// group 优先取 Redis 实例自身分组（agent 配置的 name，如集群名），
		// 为空时回退到节点分组，避免展示成默认的 "default"。
		group := ri.Group
		if group == "" {
			group = payload.Group
		}
		labels := map[string]string{
			"group":    group,
			"instance": ri.Instance,
			"name":     ri.Name,
			"role":     ri.Role,
			"topology": ri.Topology,
			"version":  ri.Version,
		}
		if ri.ReplicaOf != "" {
			labels["replica_of"] = ri.ReplicaOf
		}
		metrics = append(metrics, model.Metric{
			Node: payload.Node, Name: "redis_instance_up", Labels: labels, Value: upVal, Timestamp: payload.ReportAt,
		})
	}
	// Kubernetes 集群元信息转为 k8s_cluster_up 指标写入 VM，保证集群存活与版本可聚合查询。
	for _, ki := range payload.K8sInstances {
		upVal := 0.0
		if ki.Up {
			upVal = 1
		}
		group := ki.Group
		if group == "" {
			group = payload.Group
		}
		labels := map[string]string{
			"group":    group,
			"instance": ki.Instance,
			"name":     ki.Name,
			"version":  ki.Version,
		}
		metrics = append(metrics, model.Metric{
			Node: payload.Node, Name: "k8s_cluster_up", Labels: labels, Value: upVal, Timestamp: payload.ReportAt,
		})
	}

	// 将本次上报的中间件实例配置写入注册表：即使 Agent 离线（时序指标 stale），
	// Web 仍可从注册表枚举到"已配置但离线"的实例，避免误判为"尚未配置 Xxx 监控"。
	instancereg.Default.SetMySQL(payload.Node, payload.MySQLInstances)
	instancereg.Default.SetRedis(payload.Node, payload.RedisInstances)
	instancereg.Default.SetPostgres(payload.Node, payload.PostgresInstances)
	instancereg.Default.SetNginx(payload.Node, payload.NginxInstances)
	instancereg.Default.SetKafka(payload.Node, payload.KafkaInstances)
	instancereg.Default.SetDocker(payload.Node, payload.DockerInstances)
	instancereg.Default.SetRocketMQ(payload.Node, payload.RocketMQInstances)
	instancereg.Default.SetK8s(payload.Node, payload.K8sInstances)
	instancereg.Default.SetMongoDB(payload.Node, payload.MongoDBInstances)
	instancereg.Default.SetFastDFS(payload.Node, payload.FastDFSInstances)
	instancereg.Default.SetRabbitMQ(payload.Node, payload.RabbitMQInstances)
	instancereg.Default.SetElasticsearch(payload.Node, payload.ElasticsearchInstances)
	instancereg.Default.SetClickHouse(payload.Node, payload.ClickHouseInstances)
	instancereg.Default.SetNacos(payload.Node, payload.NacosInstances)
	instancereg.Default.SetZooKeeper(payload.Node, payload.ZooKeeperInstances)

	if err := r.storage.Write(metrics); err != nil {
		slog.Error("写入 VM 失败", "node", payload.Node, "err", err)
		http.Error(w, "write storage failed", http.StatusInternalServerError)
		return
	}

	// Nginx access log 聚合统计：写低基数指标 + 送地理窗口（高基数 IP 不进时序库）
	if len(payload.NginxAccessStats) > 0 {
		accessMets := make([]model.Metric, 0, len(payload.NginxAccessStats)*5)
		for _, st := range payload.NginxAccessStats {
			group := st.Group
			if group == "" {
				group = payload.Group
			}
			base := map[string]string{"group": group, "instance": st.Instance}
			accessMets = append(accessMets,
				model.Metric{Node: payload.Node, Name: "nginx_access_requests", Labels: cloneLabels(base), Value: st.Requests, Timestamp: payload.ReportAt},
				model.Metric{Node: payload.Node, Name: "nginx_access_bytes", Labels: cloneLabels(base), Value: st.Bytes, Timestamp: payload.ReportAt},
			)
			// 派生指标：把每周期累计的 请求数/字节数 换算成 每秒速率，
			// 使其与 network_recv_rate / network_sent_rate 同量纲，便于大屏按 Nginx 拆分网络流量。
			if st.PeriodSec > 0 {
				accessMets = append(accessMets,
					model.Metric{Node: payload.Node, Name: "nginx_access_requests_rate", Labels: cloneLabels(base), Value: st.Requests / st.PeriodSec, Timestamp: payload.ReportAt},
					model.Metric{Node: payload.Node, Name: "nginx_access_bytes_rate", Labels: cloneLabels(base), Value: st.Bytes / st.PeriodSec, Timestamp: payload.ReportAt},
				)
			}
			if st.AvgLatency > 0 {
				accessMets = append(accessMets, model.Metric{Node: payload.Node, Name: "nginx_access_avg_latency", Labels: cloneLabels(base), Value: st.AvgLatency, Timestamp: payload.ReportAt})
			}
			for code, cnt := range st.StatusCount {
				lbs := cloneLabels(base)
				lbs["status"] = code
				accessMets = append(accessMets, model.Metric{Node: payload.Node, Name: "nginx_access_requests_by_status", Labels: lbs, Value: cnt, Timestamp: payload.ReportAt})
			}
		}
		if err := r.storage.Write(accessMets); err != nil {
			slog.Error("写入 Nginx access 指标失败", "node", payload.Node, "err", err)
		}
		if r.ngx != nil {
			r.ngx.Add(payload.NginxAccessStats)
		}
	}

	// 安全事件/基线处理：先落库，再对每条事件注入告警中心（复用静默/维护窗口/通知）。
	// 回填节点 IP：事件可能未携带（旧版 Agent 或代理转发场景），统一用上报体的主机 IP 兜底，
	// 保证前端「节点」列始终展示服务器 IP。
	if r.sec != nil && (len(payload.SecurityEvents) > 0 || payload.SecurityBaseline != nil) {
		for i := range payload.SecurityEvents {
			if payload.SecurityEvents[i].Node == "" {
				payload.SecurityEvents[i].Node = payload.Node
			}
			if payload.SecurityEvents[i].NodeIP == "" {
				payload.SecurityEvents[i].NodeIP = payload.IP
			}
		}
		if payload.SecurityBaseline != nil && payload.SecurityBaseline.NodeIP == "" {
			payload.SecurityBaseline.NodeIP = payload.IP
		}
		r.sec.Ingest(payload.Node, payload.SecurityEvents, payload.SecurityBaseline)
	}
	if r.alerts != nil {
		r.alerts.IngestSecurityEvents(payload.SecurityEvents)
	}

	// 受控入侵防御：处理 Agent 上报的防护状态、指令执行结果回执，并下发待执行指令。
	if r.defense != nil {
		if payload.DefenseStatus != nil && r.sec != nil {
			r.sec.SaveDefenseStatus(payload.Node, payload.DefenseStatus)
		}
		if payload.Capabilities != nil {
			r.defense.SaveCap(payload.Node, payload.Capabilities.Defense)
		}
		if payload.DefenseResult != nil && payload.DefenseResult.CommandID != "" {
			r.defense.UpdateResult(*payload.DefenseResult)
			slog.Info("防护指令结果回执", "node", payload.Node,
				"commandId", payload.DefenseResult.CommandID,
				"state", payload.DefenseResult.State,
				"msg", payload.DefenseResult.Message)
		}
		r.defense.ExpireOverdue()
	}

	// 响应：若节点仍需升级（agent 版本未达标），持续下发 upgrade 指令
	resp := map[string]interface{}{"status": "ok"}
	if r.nodeMgr.ConsumeUpgrade(payload.Node, payload.Version, payload.BinSHA256) {
		resp["command"] = "upgrade"
		slog.Info("已下发升级指令", "node", payload.Node, "agentVersion", payload.Version)
	}

	// 受控入侵防御：仅当 Agent 声明支持 Defense 能力时，下发结构化防护指令。
	// 旧 Agent 未上报 Capabilities，不会收到 defense 字段，保持兼容。
	if r.defense != nil && payload.Capabilities != nil && payload.Capabilities.Defense {
		if cmd := r.defense.Take(payload.Node); cmd != nil {
			resp["defense"] = cmd
			slog.Info("已下发防护指令", "node", payload.Node, "type", cmd.Type, "id", cmd.ID)
		}
	}

	// 采集项模板下发（C1 阶段二）：仅当 Agent 声明支持、且其**已生效版本号**落后时携带。
	// 判定逻辑抽成纯函数，便于直接单测（否则要为此构造整套存储与 HTTP 环境）。
	attachDeliveredTemplates(resp, payload.Capabilities, payload.Group, r.templates)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// attachDeliveredTemplates 在响应中附加该节点分组适用的模板；无需下发时不改动 resp。
//
// 三条闸门，缺一不可：
//   - Agent 必须声明 Templates 能力：旧 Agent 不认该字段，发了也是白发（模板可达数 KB）；
//   - 版本号必须与 Agent 已生效版本不同：否则每轮心跳都背负整份配置，随节点数成倍放大；
//   - 内容必须按 Agent 所属分组过滤：把不属于它的模板发过去，只会让它在无关节点上产出 up=0。
//
// 注意「该分组已无模板」时下发的是**空数组**而非跳过：Agent 必须收到「清空」这个事实，
// 否则会一直跑着已被删除的模板。
func attachDeliveredTemplates(resp map[string]interface{}, caps *model.ClientCapability, group string, provider TemplateProvider) {
	if provider == nil || caps == nil || !caps.Templates {
		return
	}
	list, rev := provider.Snapshot()
	if rev == caps.TemplateRevision {
		return
	}
	scoped := templatesForGroup(list, group)
	// 再按该节点**本机放行**的取数方式过滤（阶段三：jdbc / exec / file 会以 root 触碰本机或携带库凭据，
	// 由各机器自己在 agent.yaml 里决定是否放行）。未放行的节点收到也用不了，只会多出 up=0 噪音。
	scoped = templatesForKinds(scoped, caps.TemplateKinds)
	resp["templates"] = scoped
	resp["templateRevision"] = rev
	// 用 Debug：Agent 若因模板非法而拒绝应用会持续落后（设计如此，配置修好后自愈），
	// 此处用 Info 会在该场景下按上报周期刷屏；「是否真的生效」由 Agent 日志与 up 指标体现。
	slog.Debug("已下发采集项模板", "group", group, "count", len(scoped), "revision", rev)
}

// templatesForKinds 过滤出该节点能执行的模板（见 template.KindEnabledOnNode）。
//
// 网络取数类（prometheus-exporter / http-json / http-text）任何节点都能执行；
// 护栏类（jdbc / exec / file）必须由该节点在上报能力里声明已放行。
func templatesForKinds(list []template.Config, declared []string) []template.Config {
	out := make([]template.Config, 0, len(list))
	for _, t := range list {
		if template.KindEnabledOnNode(t.Kind, declared) {
			out = append(out, t)
		}
	}
	return out
}

// templatesForGroup 过滤出对该节点分组生效的模板。
//
// 空集合也照样下发（而不是跳过）：模板被删空时，Agent 必须收到「清空」这个事实，
// 否则它会一直跑着已被删除的模板。这也是 Server 侧要求模板必须声明 groups 的原因——
// 若默认「全部节点」，一台只跑某中间件的机器配一个模板会让其余节点每轮各报一个 up=0。
func templatesForGroup(list []template.Config, group string) []template.Config {
	out := make([]template.Config, 0, len(list))
	for _, t := range list {
		for _, g := range t.Groups {
			if strings.TrimSpace(g) == group {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// cloneLabels 复制标签 map，避免多个指标共享同一 map 被并发修改。
func cloneLabels(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
