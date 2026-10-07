package api

import (
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/security"
)

// DefenseStatusView 在节点防护状态基础上附带 Agent 是否支持结构化防御指令的判断，
// 前端据此区分“未启用 / 需升级 Agent”。
type DefenseStatusView struct {
	*model.DefenseStatus
	AgentSupported bool   `json:"agentSupported"`
	NodeIP         string `json:"nodeIp,omitempty"`      // 节点 IP（服务器 IP）
	DisplayName    string `json:"displayName,omitempty"` // 节点别名
}

// enrichDefenseView 为单条防护状态补充节点 IP 与别名（来自节点管理器）。
func (a *API) enrichDefenseView(st *model.DefenseStatus, caps map[string]bool) DefenseStatusView {
	v := DefenseStatusView{DefenseStatus: st, AgentSupported: caps[st.Node]}
	if a.nodeMgr != nil {
		if nd, ok := a.nodeMgr.GetNode(st.Node); ok {
			v.NodeIP = nd.IP
			v.DisplayName = nd.DisplayName
		}
	}
	return v
}

// handleDefenseStatusList 返回所有节点的入侵防护状态与 Agent 兼容情况。
func (a *API) handleDefenseStatusList(w http.ResponseWriter, r *http.Request) {
	if a.defenseStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "防护功能未启用"})
		return
	}
	caps := a.defenseStore.AllCaps()
	var out []DefenseStatusView
	if a.security != nil {
		for _, st := range a.security.ListDefenseStatus() {
			out = append(out, a.enrichDefenseView(st, caps))
		}
	}
	// 补充未上报过状态的节点（仅返回其 Agent 兼容情况）
	for n := range caps {
		found := false
		for _, v := range out {
			if v.Node == n {
				found = true
				break
			}
		}
		if !found {
			out = append(out, a.enrichDefenseView(&model.DefenseStatus{Node: n, Supported: false, Installed: false, Running: false, ManagedJail: false}, caps))
		}
	}
	// 资源范围：受限用户只能看到范围内节点的防护状态。
	out = filterByNodeScope(a, Principal(r), out, func(v DefenseStatusView) string { return v.Node })
	writeJSON(w, http.StatusOK, map[string]interface{}{"statuses": out})
}

// handleDefenseStatus 返回单节点防护状态。
func (a *API) handleDefenseStatus(w http.ResponseWriter, r *http.Request) {
	nodeName := strings.TrimPrefix(r.URL.Path, "/api/v1/security/defense/status/")
	if a.defenseStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "防护功能未启用"})
		return
	}
	var st *model.DefenseStatus
	if a.security != nil {
		st = a.security.GetDefenseStatus(nodeName)
	}
	if st == nil {
		st = &model.DefenseStatus{Node: nodeName}
	}
	writeJSON(w, http.StatusOK, a.enrichDefenseView(st, a.defenseStore.AllCaps()))
}

// handleDefenseAction 创建防护任务（enable/disable/status）。
// 仅当节点在线、Agent 支持结构化防御指令时允许；操作人真实来源 IP 进入精确白名单。
func (a *API) handleDefenseAction(w http.ResponseWriter, r *http.Request) {
	nodeName := param(r, "node")
	action := param(r, "action")
	if a.defenseStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "防护功能未启用"})
		return
	}
	if nodeName == "" || action == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "缺少节点或动作参数"})
		return
	}
	action = strings.ToLower(action)
	if action != model.DefenseActionEnable && action != model.DefenseActionDisable && action != model.DefenseActionStatus {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "不支持的动作，仅支持 enable/disable/status"})
		return
	}

	// 节点必须存在且在线
	nd, ok := a.nodeMgr.GetNode(nodeName)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "节点不存在"})
		return
	}
	if nd.Status != "online" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "节点当前不在线，无法下发防护指令"})
		return
	}
	// Agent 必须支持结构化防御指令
	if !a.defenseStore.GetCap(nodeName) {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error":            "节点 Agent 不支持防护指令，请先升级 Agent",
			"needAgentUpgrade": true,
		})
		return
	}

	// 提取操作人真实来源 IP（仅 RemoteAddr/可信代理，不信任自定义转发头）
	operatorIP := audit.ClientIP(r)
	operator := AuthenticatedUser(r)

	// 精确白名单：回环 + 操作人真实来源 IP。
	// 不在未显式授权的情况下自动放行整个内网段，避免被同网段攻击者规避封禁。
	ignoreIPs := []string{"127.0.0.1", "::1"}
	if operatorIP != "" {
		ignoreIPs = append(ignoreIPs, normalizeWhitelistIP(operatorIP))
	}

	cmd := model.DefenseCommand{
		ID:        genDefenseTaskID(nodeName),
		Type:      action,
		Node:      nodeName,
		IgnoreIPs: ignoreIPs,
		CreatedAt: time.Now().UnixMilli(),
	}
	task := a.defenseStore.Create(cmd, operator, operatorIP)

	// 操作审计
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User:      operator,
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    http.StatusAccepted,
			RemoteIP:  operatorIP,
			Succeeded: true,
			RequestID: RequestID(r),
			Category:  "security",
			Action:    "intrusion_defense:" + action,
			Detail:    fmt.Sprintf("节点 %s 防护任务已创建，任务ID %s，白名单 %s", nodeName, cmd.ID, strings.Join(ignoreIPs, ",")),
		})
	}

	slog.Info("创建防护任务", "node", nodeName, "action", action, "id", cmd.ID, "operator", operator, "ip", operatorIP)
	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"task": map[string]interface{}{
			"id":         task.ID,
			"node":       task.Node,
			"type":       task.Type,
			"state":      task.State,
			"createdAt":  task.CreatedAt,
			"expiresAt":  task.ExpiresAt,
			"operator":   task.Operator,
			"operatorIP": task.OperatorIP,
		},
	})
}

// handleDefenseTasks 返回防护任务列表，可按节点过滤。
func (a *API) handleDefenseTasks(w http.ResponseWriter, r *http.Request) {
	if a.defenseStore == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "防护功能未启用"})
		return
	}
	nodeName := param(r, "node")
	tasks := a.defenseStore.List(nodeName)
	// 资源范围：受限用户只能看到范围内节点的防护任务。
	tasks = filterByNodeScope(a, Principal(r), tasks, func(t *security.DefenseTask) string { return t.Node })
	writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": tasks})
}

// genDefenseTaskID 生成防护任务唯一 ID（不引入额外依赖）。
func genDefenseTaskID(node string) string {
	src := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("dfn-%d-%06x", time.Now().UnixNano(), src.Int31())
}

// normalizeWhitelistIP 规整白名单 IP，剔除端口与代理链，仅保留首个地址。
func normalizeWhitelistIP(ip string) string {
	ip = strings.TrimSpace(ip)
	// 去除可能的端口
	if i := strings.LastIndex(ip, ":"); i >= 0 {
		// IPv6 形如 [::1]:port 或 IPv4 1.2.3.4:port；简单按最后冒号切，IPv6 含多个冒号需谨慎
		if !strings.Contains(ip[:i], ":") {
			ip = ip[:i]
		} else if strings.HasPrefix(ip, "[") {
			// [::1]:port
			if end := strings.Index(ip, "]"); end > 0 {
				ip = ip[1:end]
			}
		}
	}
	return ip
}

// param 从 Go 1.22+ 的路由模式 /{name} 中提取路径参数。
func param(r *http.Request, key string) string {
	return r.PathValue(key)
}
