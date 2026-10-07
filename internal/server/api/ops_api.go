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
	// 批量能力查询：?nodes=a,b,c → nodeCaps{node:[kinds]}。
	// 批量下发前必须一次拿到所有目标节点的放行情况，否则要么逐个查（N 次请求）、
	// 要么把"本机未放行"的动作下下去（用户只会在结果里看到一堆失败）。
	if nodesParam := strings.TrimSpace(r.URL.Query().Get("nodes")); nodesParam != "" {
		p := Principal(r)
		caps := map[string][]string{}
		for i, n := range strings.Split(nodesParam, ",") {
			if i >= maxBatchQueryNodes {
				break
			}
			n = strings.TrimSpace(n)
			if n == "" || !a.nodeInScope(p, n) {
				continue // 范围外不出现，与「不存在」一致
			}
			if _, ok := a.nodeMgr.GetNode(n); !ok {
				continue
			}
			caps[n] = a.ops.NodeSupport(n)
		}
		resp["nodeCaps"] = caps
	}
	writeJSON(w, http.StatusOK, resp)
}

// maxBatchQueryNodes 是一次批量能力查询的节点数上限（防止超长 URL 与无意义的遍历）。
const maxBatchQueryNodes = 200

type opsCreateBody struct {
	Node   string            `json:"node"`
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
	Reason string            `json:"reason"`
}

// opsNodeProblem 是节点维度的前置检查失败项（批量下发的 Items 用它统一表达）。
type opsNodeProblem struct {
	Node  string `json:"node"`
	Error string `json:"error"`
	OK    bool   `json:"ok"`
}

// precheckNodes 逐个做资源范围、存在性与在线判断：
// 只有范围内、存在且**在线**的节点进入可下发集合，其余按原因记入失败项。
//
// 范围外节点按「不存在」处理（与单节点下发一致）：否则 403/404 的差异会变成
// "这台机器存不存在"的探测面。
func (a *API) precheckNodes(r *http.Request, nodes []string) (eligible []string, problems []opsNodeProblem) {
	p := Principal(r)
	for _, n := range nodes {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !a.nodeInScope(p, n) {
			problems = append(problems, opsNodeProblem{Node: n, Error: "节点不存在"})
			continue
		}
		nd, ok := a.nodeMgr.GetNode(n)
		if !ok {
			problems = append(problems, opsNodeProblem{Node: n, Error: "节点不存在"})
			continue
		}
		if nd.Status != "online" {
			problems = append(problems, opsNodeProblem{Node: n, Error: fmt.Sprintf("节点不在线（状态 %s）：指令只能随上报响应送回", nd.Status)})
			continue
		}
		eligible = append(eligible, n)
	}
	return eligible, problems
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
			RequestID: RequestID(r),
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
		Node:    strings.TrimSpace(q.Get("node")),
		Kind:    strings.TrimSpace(q.Get("kind")),
		State:   strings.TrimSpace(q.Get("state")),
		BatchID: strings.TrimSpace(q.Get("batchId")),
		Limit:   50,
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

// ---- 批量下发 / 取消 / 删除 ----

type opsBatchBody struct {
	Nodes  []string          `json:"nodes"`
	Kind   string            `json:"kind"`
	Params map[string]string `json:"params"`
	Reason string            `json:"reason"`
}

// handleOpsCreateBatch 对多个节点下发同一动作。
//
// 部分成功是批量下发的常态（几台离线、几台没放行），因此**逐节点**返回结论，
// 且在没有任何节点可下发时才用 409 —— 那时"成功"是零，得让调用方明确知道整批没成。
func (a *API) handleOpsCreateBatch(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	var body opsBatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if len(body.Nodes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "必须指定至少一个目标节点"})
		return
	}

	eligible, problems := a.precheckNodes(r, body.Nodes)
	operator := AuthenticatedUser(r)
	operatorIP := audit.ClientIP(r)

	if len(eligible) == 0 {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "没有任何节点可以下发（原因见 items）",
			"batch": map[string]interface{}{
				"total": len(pickNodes(body.Nodes)), "created": 0, "failed": len(problems), "items": problems,
			},
		})
		return
	}

	result, err := a.ops.CreateBatch(eligible, body.Kind, body.Params, operator, operatorIP, body.Reason)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ops.ErrUnsupported) {
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	// 合并前置检查的失败项：调用方只需要看一份逐节点清单
	items := make([]interface{}, 0, len(problems)+len(result.Items))
	for _, p := range problems {
		items = append(items, p)
	}
	created := 0
	for _, it := range result.Items {
		items = append(items, it)
		if it.OK {
			created++
		}
	}
	total := len(problems) + len(result.Items)
	failed := total - created
	result.Created, result.Failed = created, failed

	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: operator, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusCreated, RemoteIP: operatorIP, Succeeded: true,
			RequestID: RequestID(r),
			Category: "ops", Action: "create-batch:" + result.Kind,
			Detail: fmt.Sprintf("批量下发 %s：目标 %d 台，成功 %d、失败 %d（批次 %s%s）",
				result.Kind, total, created, failed, result.BatchID, reasonSuffix(body.Reason)),
		})
	}
	slog.Info("已创建批量操作任务", "batch", result.BatchID, "kind", result.Kind,
		"total", total, "created", created, "failed", failed, "operator", operator)

	status := http.StatusCreated
	if created == 0 {
		// 一个都没下发成功（全部未放行）：明确 409，别让调用方以为"部分成功"
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]interface{}{
		"batch": map[string]interface{}{
			"batchId": result.BatchID, "kind": result.Kind,
			"total": total, "created": created, "failed": failed, "items": items,
		},
	})
}

// pickNodes 去掉空串，用于统计"用户实际给了多少个节点"（前置检查会跳过空值）。
func pickNodes(nodes []string) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if strings.TrimSpace(n) != "" {
			out = append(out, strings.TrimSpace(n))
		}
	}
	return out
}

type opsCancelBody struct {
	// IDs 是单条或若干条任务 ID；BatchID 非空时整批取消（忽略 IDs）。
	IDs     []string `json:"ids"`
	BatchID string   `json:"batchId"`
}

// handleOpsCancel 取消尚未下发的任务（单条 / 批量 / 整批）。
//
// 资源范围在这里解析而不是交给 service：整批取消必须逐条过范围校验，
// 否则一个受限用户只要猜到批次号就能撤掉别人的指令。
func (a *API) handleOpsCancel(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	var body opsCancelBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	ids := body.IDs
	if strings.TrimSpace(body.BatchID) != "" {
		ids = ids[:0]
		for _, t := range a.ops.Tasks(ops.ListFilter{BatchID: strings.TrimSpace(body.BatchID), Limit: maxBatchQueryNodes}) {
			ids = append(ids, t.ID)
		}
		if len(ids) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "批次不存在或该批没有任何任务"})
			return
		}
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请提供 ids 或 batchId"})
		return
	}

	// 范围过滤：范围外的任务对调用方就是「不存在」（保持与查询一致的不可区分性）
	p := Principal(r)
	inScope := make([]string, 0, len(ids))
	blocked := make([]ops.CancelItem, 0)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		t, ok := a.ops.Task(id)
		if !ok || !a.nodeInScope(p, t.Node) {
			blocked = append(blocked, ops.CancelItem{ID: id, Error: "任务不存在"})
			continue
		}
		inScope = append(inScope, id)
	}

	operator := AuthenticatedUser(r)
	res := a.ops.Cancel(inScope, "", operator)
	res.Items = append(blocked, res.Items...)
	res.Failed += len(blocked)

	if a.audit != nil && (res.Cancelled > 0 || len(blocked) > 0) {
		_ = a.audit.Record(audit.Event{
			User: operator, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: audit.ClientIP(r), Succeeded: res.Cancelled > 0,
			RequestID: RequestID(r),
			Category: "ops", Action: "cancel",
			Detail: fmt.Sprintf("取消操作任务：成功 %d、失败 %d（批次 %s）", res.Cancelled, res.Failed, body.BatchID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"result": res})
}

// handleOpsDelete 删除一条**已结束**的任务记录（清理列表用；审计保留"谁删了什么"）。
func (a *API) handleOpsDelete(w http.ResponseWriter, r *http.Request) {
	if a.ops == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "下行操作通道未启用"})
		return
	}
	id := param(r, "id")
	t, ok := a.ops.Task(id)
	if !ok || !a.nodeInScope(Principal(r), t.Node) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "任务不存在"})
		return
	}
	operator := AuthenticatedUser(r)
	if err := a.ops.Delete(id, operator); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ops.ErrNotTerminal) {
			// 还在跑的任务不能删：先取消或等它结束（提示里说清楚）
			status = http.StatusConflict
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	if a.audit != nil {
		_ = a.audit.Record(audit.Event{
			User: operator, Method: r.Method, Path: r.URL.Path,
			Status: http.StatusOK, RemoteIP: audit.ClientIP(r), Succeeded: true,
			RequestID: RequestID(r),
			Category: "ops", Action: "delete",
			Detail: fmt.Sprintf("删除操作任务记录 %s（节点 %s，状态 %s）", id, t.Node, t.State),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": id})
}
