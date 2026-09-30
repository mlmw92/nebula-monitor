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

	caps := s.store.Caps(node)
	if len(caps) == 0 {
		return nil, fmt.Errorf("%w：节点 %s 尚未声明支持下行操作（Agent 可能版本过低，或未在本机 agent.yaml 中启用 guards.ops）",
			ErrUnsupported, node)
	}
	if !contains(caps, action.Kind) {
		if !action.ReadOnly {
			return nil, fmt.Errorf(
				"%w：节点 %s 未放行写操作「%s」——需在该节点 agent.yaml 的 guards.ops 中把 write 设为 true，"+
					"并把单元加入 units 清单（默认只读，写操作必须由机器自己同意）", ErrUnsupported, node, action.Title)
		}
		return nil, fmt.Errorf("%w：节点 %s 的 Agent 未声明支持动作「%s」（已声明：%s）",
			ErrUnsupported, node, action.Title, strings.Join(caps, " / "))
	}

	t := s.store.Create(model.OpsCommand{Node: node, Kind: action.Kind, Params: norm}, actor, ip, reason)
	return t, nil
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
