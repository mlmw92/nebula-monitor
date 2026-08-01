package api

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/dialtest"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/notify"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/screencfg"
	"github.com/nebula/monitor/internal/server/storage"
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
	store     storage.Storage
	nodeMgr   *node.Manager
	rules     RulesProvider
	alerts    AlertStore
	hub       *Hub
	agentAuth config.AgentAuthConfig
	webDir    string
	auth      config.AuthConfig
	upgrader  *upgrade.Manager
	notifyMgr *notify.Manager
	screenMgr *screencfg.Manager
	engine    *alert.Engine
	maintenance MaintenanceProvider
	dialtest  DialtestProvider
	report    ReportProvider
}

// New 创建 API。
func New(store storage.Storage, mgr *node.Manager, rules RulesProvider, alerts AlertStore, hub *Hub, agentAuth config.AgentAuthConfig, webDir string, auth config.AuthConfig, upgrader *upgrade.Manager, notifyMgr *notify.Manager, engine *alert.Engine, maintenance MaintenanceProvider, dt DialtestProvider, rpt ReportProvider, screenMgr *screencfg.Manager) *API {
	return &API{store: store, nodeMgr: mgr, rules: rules, alerts: alerts, hub: hub, agentAuth: agentAuth, webDir: webDir, auth: auth, upgrader: upgrader, notifyMgr: notifyMgr, engine: engine, maintenance: maintenance, dialtest: dt, report: rpt, screenMgr: screenMgr}
}

// RegisterRoutes 注册所有路由到 mux。
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/nodes", a.handleNodes)
	mux.HandleFunc("GET /api/v1/nodes/latest", a.handleNodesLatest)
	mux.HandleFunc("GET /api/v1/nodes/{name}", a.handleNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{name}", a.handleNodeDelete)
	mux.HandleFunc("PUT /api/v1/nodes/{name}/group", a.handleNodeGroup)
	mux.HandleFunc("PUT /api/v1/nodes/{name}/display-name", a.handleNodeDisplayName)
	mux.HandleFunc("POST /api/v1/nodes/{name}/upgrade", a.handleNodeUpgrade)

	mux.HandleFunc("GET /api/v1/groups", a.handleGroups)
	mux.HandleFunc("POST /api/v1/groups", a.handleGroupCreate)
	mux.HandleFunc("DELETE /api/v1/groups/{name}", a.handleGroupDelete)

	mux.HandleFunc("GET /api/v1/query/range", a.handleQueryRange)
	mux.HandleFunc("GET /api/v1/query/latest", a.handleQueryLatest)
	mux.HandleFunc("GET /api/v1/processes", a.handleProcesses)

	mux.HandleFunc("GET /api/v1/middleware/redis/instances", a.handleRedisInstances)
	mux.HandleFunc("GET /api/v1/middleware/mysql/instances", a.handleMySQLInstances)
	mux.HandleFunc("GET /api/v1/middleware/postgres/instances", a.handlePostgresInstances)
	mux.HandleFunc("GET /api/v1/middleware/nginx/instances", a.handleNginxInstances)
	mux.HandleFunc("GET /api/v1/middleware/kafka/instances", a.handleKafkaInstances)
	mux.HandleFunc("GET /api/v1/middleware/docker/containers", a.handleDockerContainers)
	mux.HandleFunc("GET /api/v1/middleware/rocketmq/instances", a.handleRocketMQInstances)
	mux.HandleFunc("GET /api/v1/middleware/k8s/instances", a.handleK8sInstances)

	mux.HandleFunc("GET /api/v1/alerts", a.handleAlerts)
	mux.HandleFunc("GET /api/v1/rules", a.handleRulesList)
	mux.HandleFunc("POST /api/v1/rules", a.handleRuleCreate)
	mux.HandleFunc("PUT /api/v1/rules/{id}", a.handleRuleUpdate)
	mux.HandleFunc("POST /api/v1/rules/{id}/toggle", a.handleRuleToggle)
	mux.HandleFunc("DELETE /api/v1/rules/{id}", a.handleRuleDelete)

	mux.HandleFunc("GET /api/v1/install-info", a.handleInstallInfo)
	mux.HandleFunc("GET /api/v1/version", a.handleVersion)
	mux.HandleFunc("GET /api/v1/agent/check", a.handleAgentCheck)

	mux.HandleFunc("POST /api/v1/system/upgrade/upload", a.handleSystemUpgradeUpload)
	mux.HandleFunc("GET /api/v1/system/upgrade/current", a.handleSystemUpgradeCurrent)
	mux.HandleFunc("POST /api/v1/system/upgrade/apply", a.handleSystemUpgradeApply)
	mux.HandleFunc("POST /api/v1/system/upgrade/rollback", a.handleSystemUpgradeRollback)
	mux.HandleFunc("GET /api/v1/system/upgrade/history", a.handleSystemUpgradeHistory)

	mux.HandleFunc("POST /api/v1/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/logout", a.handleLogout)
	mux.HandleFunc("GET /api/v1/auth-info", a.handleAuthInfo)

	mux.HandleFunc("GET /api/v1/notify", a.handleNotifyGet)
	mux.HandleFunc("PUT /api/v1/notify", a.handleNotifyPut)
	mux.HandleFunc("POST /api/v1/notify/test", a.handleNotifyTest)

	mux.HandleFunc("GET /api/v1/screen/config", a.handleScreenGet)
	mux.HandleFunc("PUT /api/v1/screen/config", a.handleScreenPut)
	mux.HandleFunc("POST /api/v1/alerts/test", a.handleAlertTest)

	mux.HandleFunc("GET /api/v1/maintenance", a.handleMaintenanceGet)
	mux.HandleFunc("PUT /api/v1/maintenance", a.handleMaintenanceSet)

	mux.HandleFunc("GET /api/v1/dialtest/tasks", a.handleDialtestList)
	mux.HandleFunc("POST /api/v1/dialtest/tasks", a.handleDialtestCreate)
	mux.HandleFunc("PUT /api/v1/dialtest/tasks/{id}", a.handleDialtestUpdate)
	mux.HandleFunc("DELETE /api/v1/dialtest/tasks/{id}", a.handleDialtestDelete)
	mux.HandleFunc("GET /api/v1/dialtest/latest", a.handleDialtestLatest)

	mux.HandleFunc("POST /api/v1/report/generate", a.handleReportGenerate)
	mux.HandleFunc("GET /api/v1/report/download", a.handleReportDownload)
	mux.HandleFunc("GET /api/v1/report/history", a.handleReportHistory)
}

// handleInstallInfo 返回 Agent 一行安装命令（server 地址取自请求 Host，secret 取自配置）。
func (a *API) handleInstallInfo(w http.ResponseWriter, r *http.Request) {
	srv := "http://" + r.Host
	cmd := "curl -fsSL " + srv + "/install/agent-install.sh | bash -s -- --server " + srv
	if a.agentAuth.Enabled {
		cmd += " --secret " + a.agentAuth.Secret
	}
	// 注意：不回显明文 agentAuth.secret 字段，避免该接口成为密钥泄露点
	// （认证关闭时 /api/v1/install-info 虽需登录后方可访问，但最小暴露原则下仅下发安装命令）。
	writeJSON(w, 200, map[string]interface{}{
		"serverURL":   srv,
		"command":     cmd,
		"authEnabled": a.agentAuth.Enabled,
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
	writeJSON(w, 200, map[string]interface{}{"nodes": a.nodeMgr.ListNodes()})
}

// handleNodesLatest 一次性聚合所有节点的关键指标，供主机列表展示。
// 返回结构：metrics[node] = { cpu, mem, disk, load1, netIn, netOut, diskRead, diskWrite }。
func (a *API) handleNodesLatest(w http.ResponseWriter, r *http.Request) {
	type nodeMetric struct {
		CPU      float64 `json:"cpu"`
		Mem      float64 `json:"mem"`
		Disk     float64 `json:"disk"`
		Load1    float64 `json:"load1"`
		NetIn    float64 `json:"netIn"`
		NetOut   float64 `json:"netOut"`
		DiskRead float64 `json:"diskRead"`
		DiskWr   float64 `json:"diskWr"`
	}
	out := map[string]*nodeMetric{}

	// 通用取值（每节点一个样本）：cpu_usage / mem_used_percent / load1
	for _, name := range []string{"cpu_usage", "mem_used_percent", "load1"} {
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

	// 跨标签聚合：network_*_rate 与 disk_*_rate 取 sum
	for _, name := range []string{"network_recv_rate", "network_sent_rate", "disk_read_rate", "disk_write_rate"} {
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
			case "disk_read_rate":
				m.DiskRead += v
			case "disk_write_rate":
				m.DiskWr += v
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
	if _, ok := a.nodeMgr.GetNode(name); !ok {
		http.Error(w, "node not found", http.StatusNotFound)
		return
	}
	a.nodeMgr.RequestUpgrade(name, version.Version)
	slog.Info("收到 Agent 升级请求", "node", name, "targetVersion", version.Version)
	writeJSON(w, 200, map[string]string{
		"status":  "ok",
		"message": "升级任务已下发，等待 Agent 下次心跳时执行",
	})
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
	writeJSON(w, 200, map[string]interface{}{"groups": a.nodeMgr.ListGroups()})
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
	node := q.Get("node")
	name := q.Get("metric")
	if node == "" || name == "" {
		http.Error(w, "node and metric required", http.StatusBadRequest)
		return
	}
	start, _ := strconv.ParseInt(q.Get("start"), 10, 0)
	end, _ := strconv.ParseInt(q.Get("end"), 10, 0)
	step, _ := strconv.ParseInt(q.Get("step"), 10, 0)
	if step == 0 {
		// 默认按跨度自动降采样到约 300 点
		span := end - start
		step = span / 300
		if step < 60000 {
			step = 60000 // 最少 60s（毫秒单位）
		}
	}
	labels := parseLabelQuery(q)
	series, err := a.store.QueryRange(node, name, labels, start, end, step)
	if err != nil {
		slog.Error("查询失败", "err", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"series": series})
}

func (a *API) handleQueryLatest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	node := q.Get("node")
	name := q.Get("metric")
	if node == "" || name == "" {
		http.Error(w, "node and metric required", http.StatusBadRequest)
		return
	}
	p, err := a.store.QueryLatest(node, name, parseLabelQuery(q))
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"point": p})
}

func (a *API) handleProcesses(w http.ResponseWriter, r *http.Request) {
	node := r.URL.Query().Get("hostname")
	if node == "" {
		http.Error(w, "node required", http.StatusBadRequest)
		return
	}
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
		events = a.alerts.Active()
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
	writeJSON(w, 200, map[string]interface{}{"alerts": events})
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
	created := a.rules.Create(rule)
	writeJSON(w, 200, created)
}

func (a *API) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	var rule model.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	rule.ID = r.PathValue("id")
	if err := a.rules.Update(rule); err != nil {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *API) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	a.rules.Delete(r.PathValue("id"))
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

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

	// 3. 转为列表返回
	out := make([]redisInstanceInfo, 0, len(keys))
	for _, k := range keys {
		out = append(out, *instances[k])
	}
	writeJSON(w, 200, map[string]interface{}{"instances": out})
}
