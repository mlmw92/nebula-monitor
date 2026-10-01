package ops

import (
	"errors"
	"fmt"
	"strings"

	"github.com/nebula/monitor/internal/model"
)

// ErrUnsupported 表示「目标节点当前不能执行该动作」——与参数写错区分开：
// 前者改参数没用（要去目标机器开护栏或升级 Agent），后者改参数即可。
// API 层据此把状态码分成 409 与 400，让界面能给出不同的下一步指引。
var ErrUnsupported = errors.New("目标节点不支持该动作")

// Service 把「校验 + 能力协商」的判断收在一处，让 API 层只关心权限与资源范围。
type Service struct {
	store *Store
}

// NewService 创建服务。
func NewService(store *Store) *Service { return &Service{store: store} }

// Store 返回底层存储（receiver 需要用它做领取与回执）。
func (s *Service) Store() *Store { return s.store }

// Create 校验动作与参数、确认目标节点确实能执行，然后创建任务。
//
// 这里刻意对「不能执行」的两种情况给出**不同的、可操作的**错误：
//   - 只读动作没被声明 → 多半是 Agent 版本低，提示升级；
//   - 写动作没被放行 → 需要去目标机器的 agent.yaml 里开开关并列出单元。
//
// 如果只回一句"节点不支持"，用户会以为平台坏了；而这条链路上"机器自身的同意优先于中心的授权"
// 是本设计最核心的取舍，必须让操作者看见它、知道去哪改。
func (s *Service) Create(node, kind string, params map[string]string, actor, ip, reason string) (*Task, error) {
	action, norm, err := Validate(kind, params)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(node) == "" {
		return nil, fmt.Errorf("必须指定目标节点")
	}
	if err := s.checkSupport(node, action); err != nil {
		return nil, err
	}
	t := s.store.Create(model.OpsCommand{Node: node, Kind: action.Kind, Params: norm}, actor, ip, reason)
	return t, nil
}

// checkSupport 判断目标节点是否确实能执行该动作（第二道护栏：能力协商）。
//
// 这里刻意对「不能执行」的两种情况给出**不同的、可操作的**错误：
//   - 只读动作没被声明 → 多半是 Agent 版本低，提示升级；
//   - 写动作没被放行 → 需要去目标机器的 agent.yaml 里开开关并列出单元。
//
// 如果只回一句"节点不支持"，用户会以为平台坏了；而这条链路上"机器自身的同意优先于中心的授权"
// 是本设计最核心的取舍，必须让操作者看见它、知道去哪改。
func (s *Service) checkSupport(node string, action Action) error {
	caps := s.store.Caps(node)
	if len(caps) == 0 {
		return fmt.Errorf("%w：节点 %s 尚未声明支持下行操作（Agent 可能版本过低，或未在本机 agent.yaml 中启用 guards.ops）",
			ErrUnsupported, node)
	}
	if contains(caps, action.Kind) {
		return nil
	}
	if !action.ReadOnly {
		return fmt.Errorf(
			"%w：节点 %s 未放行写操作「%s」——需在该节点 agent.yaml 的 guards.ops 中把 write 设为 true，"+
				"并把单元加入 units 清单（默认只读，写操作必须由机器自己同意）", ErrUnsupported, node, action.Title)
	}
	return fmt.Errorf("%w：节点 %s 的 Agent 未声明支持动作「%s」（已声明：%s）",
		ErrUnsupported, node, action.Title, strings.Join(caps, " / "))
}

// maxBatchNodes 是单次批量下发的节点上限。
//
// 200：既够日常使用，也挡住"一次点下去几千台"这种误操作；超过 200 台应当按分组分批下发。
// 超限是**显式错误**，不静默截断——静默截断会让人以为"全都下发了"。
const maxBatchNodes = 200

// BatchItem 是批量下发中单个节点的结果。
//
// 逐节点给结论而不是"整批成功/失败"：批量下发最常见的形态就是**部分成功**
// （几台离线、几台没放行），只说"失败"等于让人自己去比对哪几台。
type BatchItem struct {
	Node   string `json:"node"`
	TaskID string `json:"taskId,omitempty"`
	State  string `json:"state,omitempty"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// BatchResult 是一次批量下发的结果汇总。
type BatchResult struct {
	BatchID string      `json:"batchId"`
	Kind    string      `json:"kind"`
	Total   int         `json:"total"`
	Created int         `json:"created"`
	Failed  int         `json:"failed"`
	Items   []BatchItem `json:"items"`
}

// CreateBatch 对多个节点下发同一个动作（同一批共用一个 batchId，便于聚合与整批取消）。
//
// 返回 error 只在**整批都发不出去**时（参数不合法、节点为空、超过上限）；
// 单个节点不可执行（未放行/离线由 API 层先过滤）记录在 Items 里，其余节点照常下发。
func (s *Service) CreateBatch(nodes []string, kind string, params map[string]string, actor, ip, reason string) (BatchResult, error) {
	action, norm, err := Validate(kind, params)
	if err != nil {
		return BatchResult{}, err
	}
	nodes = dedupeNodes(nodes)
	if len(nodes) == 0 {
		return BatchResult{}, fmt.Errorf("必须指定至少一个目标节点")
	}
	if len(nodes) > maxBatchNodes {
		return BatchResult{}, fmt.Errorf("单次批量下发最多 %d 个节点（当前 %d 个）：请按分组分批下发", maxBatchNodes, len(nodes))
	}

	batchID := s.store.NextBatchID()
	items := make([]BatchItem, len(nodes))
	cmds := make([]model.OpsCommand, 0, len(nodes))
	for i, n := range nodes {
		if err := s.checkSupport(n, action); err != nil {
			items[i] = BatchItem{Node: n, Error: err.Error()}
			continue
		}
		cmds = append(cmds, model.OpsCommand{Node: n, Kind: action.Kind, Params: norm})
		items[i] = BatchItem{Node: n, OK: true}
	}

	// 一次性创建 + 一次落盘：逐个 Create 会让 200 台的任务写 200 次文件。
	tasks := s.store.CreateMany(cmds, batchID, actor, ip, reason)
	ti := 0
	for i := range items {
		if !items[i].OK {
			continue
		}
		items[i].TaskID = tasks[ti].ID
		items[i].State = tasks[ti].State
		ti++
	}

	out := BatchResult{BatchID: batchID, Kind: action.Kind, Total: len(nodes), Items: items}
	for _, it := range items {
		if it.OK {
			out.Created++
		} else {
			out.Failed++
		}
	}
	return out, nil
}

// CancelItem 是逐条取消的结果。
type CancelItem struct {
	ID    string `json:"id"`
	Node  string `json:"node,omitempty"`
	OK    bool   `json:"ok"`
	State string `json:"state,omitempty"`
	Error string `json:"error,omitempty"`
}

// CancelResult 是一次（可能批量的）取消汇总。
type CancelResult struct {
	Cancelled int          `json:"cancelled"`
	Failed    int          `json:"failed"`
	Items     []CancelItem `json:"items"`
}

// Cancel 取消任务：可传任务 ID 列表，或传 batchId 整批取消（batchId 优先）。
//
// 「整批取消」不是一句"已取消"就完事：批次里可能已经有任务被节点领走了，
// 那些必须**逐条**给出"已下发、无法撤回"——否则用户会以为全撤了，而机器照样会执行。
func (s *Service) Cancel(ids []string, batchID, actor string) CancelResult {
	targets := make([]*Task, 0, len(ids))
	if batchID != "" {
		targets = s.store.List(ListFilter{BatchID: batchID, Limit: maxBatchNodes})
	} else {
		for _, id := range ids {
			if t, ok := s.store.Get(strings.TrimSpace(id)); ok {
				targets = append(targets, t)
				continue
			}
			targets = append(targets, nil) // 占位：走下面"任务不存在"分支
		}
	}

	out := CancelResult{Items: make([]CancelItem, 0, len(targets))}
	for i, t := range targets {
		if t == nil {
			id := ""
			if i < len(ids) {
				id = strings.TrimSpace(ids[i])
			}
			out.Items = append(out.Items, CancelItem{ID: id, Error: "任务不存在"})
			out.Failed++
			continue
		}
		item := CancelItem{ID: t.ID, Node: t.Node}
		if _, err := s.store.Cancel(t.ID, actor); err != nil {
			item.Error = err.Error()
			out.Failed++
		} else {
			item.OK = true
			item.State = model.OpsStateCancelled
			out.Cancelled++
		}
		out.Items = append(out.Items, item)
	}
	return out
}

// Delete 删除一条已结束的任务记录（用于清理列表；审计仍留有"谁删了什么"）。
func (s *Service) Delete(id, actor string) error { return s.store.Remove(id, actor) }

// dedupeNodes 去掉重复节点并保持输入顺序（重复下发同一条指令没有意义，
// 但静默保留两条会让"取消"看起来没生效）。
func dedupeNodes(nodes []string) []string {
	seen := make(map[string]bool, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// NodeSupport 返回某节点可执行的动作清单（供界面说明"为什么这个动作是灰的"）。
func (s *Service) NodeSupport(node string) []string { return s.store.Caps(node) }

// AllCaps 返回全量能力映射（node → 可执行动作）。
func (s *Service) AllCaps() map[string][]string { return s.store.AllCaps() }

// Task / List / Get 是给 API 层的透传入口。

// Task 按 ID 取任务。
func (s *Service) Task(id string) (*Task, bool) { return s.store.Get(id) }

// Tasks 列出任务。
func (s *Service) Tasks(f ListFilter) []*Task { return s.store.List(f) }

// ExpireOverdue 回收超时任务（receiver 每轮调用）。
func (s *Service) ExpireOverdue() { s.store.ExpireOverdue() }

// Take 领取可下发指令（receiver 调用）。
func (s *Service) Take(node string, kinds []string) *model.OpsCommand { return s.store.Take(node, kinds) }

// ApplyResult 应用回执（receiver 调用）。
func (s *Service) ApplyResult(res model.OpsResult) { s.store.ApplyResult(res) }

// SaveCaps 记录 Agent 声明的能力（receiver 调用）。
func (s *Service) SaveCaps(node string, kinds []string) { s.store.SaveCaps(node, kinds) }

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
