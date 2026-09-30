package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/ops"
)

// 统一下行操作通道的接口层：动作目录 / 创建任务 / 任务查询。
//
// 权限：查看用 ops:read；**创建任务用 ops:exec**（属高风险——即使是只读查询，它也是
// "在成百上千台机器上执行东西"的能力）。执行与否还取决于目标 Agent 的本机护栏与能力声明，
// 因此"服务端放行"不等于"一定会执行"：这层差别会在响应里说清楚，不让用户对着一个
// 永远停在 queued 的任务猜原因。

// SetOpsService 注入统一下行操作通道（可空：不注入时相关接口返回 503，与其它可选能力一致）。
func (a *API) SetOpsService(svc *ops.Service) { a.ops = svc }

// opsTaskView 是任务的对外形态（附带动作标题，省得前端再查一次目录）。
type opsTaskView struct {
	*ops.Task
	Title    string `json:"title,omitempty"`
	ReadOnly bool   `json:"readOnly"`
}

func (a *API) opsTaskView(t *ops.Task) opsTaskView {
	v := opsTaskView{Task: t}
	if action, ok := ops.Lookup(t.Kind); ok {
		v.Title = action.Title
		v.ReadOnly = action.ReadOnly
	}
	return v
}

// requireOpsNode 解析目标节点并做资源范围校验。
//
// 范围外的节点一律按「不存在」返回（与资产台账同一约定）：否则 403/404 的差异会变成
// 「这台机器存不存在」的探测面。
func (a *API) requireOpsNode(w http.ResponseWriter, r *http.Request, node string) (model.Node, bool) {
	if node == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少目标节点"})
		return model.Node{}, false
	}
	if a.nodeMgr == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "节点管理未启用"})
		return model.Node{}, false
	}
	if !a.nodeInScope(Principal(r), node) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "节点不存在"})
		return model.Node{}, false
	}
	nd, ok := a.nodeMgr.GetNode(node)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "节点不存在"})
		return model.Node{}, false
	}
	return nd, true
}

// 节点资源范围校验复用 query.go 的 a.nodeInScope（未启用认证或全局范围恒真，
// 受限用户对不可归属的节点一律不可见）。

// handleOpsActions 返回动作目录；带 node 参数时附带该节点的可执行清单。
//
// 带上 nodeSupport 是为了让界面能直接说清"这个动作在这台机器上是灰的，因为
// 它的 agent.yaml 没放行写操作"，而不是让用户点了才知道。
func (a *API) handleOpsActions(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	resp := map[string]interface{}{
		"actions":     ops.Catalog(),
		"permissions": map[string]string{"read": ops.PermRead, "exec": ops.PermExec},
	}
	if node := strings.TrimSpace(r.URL.Query().Get("node")); node != "" {
		if _, ok := a.requireOpsNode(w, r, node); !ok {
			return
		}
		support := a.ops.NodeSupport(node)
		resp["nodeSupport"] = support
		// 空清单有两种含义：Agent 太旧 / 本机护栏全关。区分开来才能给对提示。
		resp["nodeSupportKnown"] = len(support) > 0
	}
	writeJSON(w, http.StatusOK, resp)
}

type opsCreateBody struct {
	Node   string            `json:"node"`
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
	Reason string            `json:"reason"`
}

// handleOpsCreate 创建一条下行操作任务（等待 Agent 下一轮上报时领取）。
func (a *API) handleOpsCreate(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	var body opsCreateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	nd, ok := a.requireOpsNode(w, r, strings.TrimSpace(body.Node))
	if !ok {
		return
	}
	// 离线节点一律拒绝：指令只能随上报的响应送回，Agent 不在线时它必然等到过期，
	// 而"创建一个注定过期的任务"会让操作者以为指令已下达。
	if nd.Status != "online" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("节点 %s 当前不在线（状态 %s），指令无法下发", nd.Hostname, nd.Status),
		})
		return
	}

	operator := AuthenticatedUser(r)
	operatorIP := audit.ClientIP(r)
	task, err := a.ops.Create(nd.Hostname, body.Kind, body.Params, operator, operatorIP, body.Reason)
	if err != nil {
		// 「不支持」与「参数写错」要分开报：前者用户改参数没用，得去目标机器或升级 Agent。
		status := http.StatusBadRequest
		if errors.Is(err, ops.ErrUnsupported) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User:      operator,
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    http.StatusCreated,
			RemoteIP:  operatorIP,
			Succeeded: true,
			Category:  "ops",
			Action:    "create:" + task.Kind,
			Detail: fmt.Sprintf("节点 %s 下发动作 %s（任务 %s%s）",
				task.Node, task.Kind, task.ID, reasonSuffix(task.Reason)),
		})
	}
	slog.Info("已创建操作任务", "id", task.ID, "node", task.Node, "kind", task.Kind, "operator", operator)
	writeJSON(w, http.StatusCreated, map[string]interface{}{"task": a.opsTaskView(task)})
}

func reasonSuffix(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	return "，原因：" + strings.TrimSpace(reason)
}

// handleOpsTasks 列出任务（按资源范围过滤）。
func (a *API) handleOpsTasks(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	q := r.URL.Query()
	filter := ops.ListFilter{
		Node:  strings.TrimSpace(q.Get("node")),
		Kind:  strings.TrimSpace(q.Get("kind")),
		State: strings.TrimSpace(q.Get("state")),
		Limit: 50,
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}
	if filter.Node != "" {
		// 指定了节点就必须校验范围：否则可以用它探测别人的节点是否有任务。
		if _, ok := a.requireOpsNode(w, r, filter.Node); !ok {
			return
		}
	}
	items := a.ops.Tasks(filter)
	views := make([]opsTaskView, 0, len(items))
	for _, t := range items {
		views = append(views, a.opsTaskView(t))
	}
	views = filterByNodeScope(a, Principal(r), views, func(v opsTaskView) string { return v.Node })
	writeJSON(w, http.StatusOK, map[string]interface{}{"tasks": views})
}

// handleOpsTask 返回单条任务（含执行输出）。
func (a *API) handleOpsTask(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	id := param(r, "id")
	t, ok := a.ops.Task(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return
	}
	if !a.nodeInScope(Principal(r), t.Node) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"task": a.opsTaskView(t)})
}
