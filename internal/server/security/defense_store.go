package security

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 受控 fail2ban 入侵防御任务持久化。
// 任务状态机：queued -> delivered -> running -> succeeded / failed / expired。
// 持久化到 JSON 文件，server 重启不丢失请求；agent 按 Command.ID 幂等执行。

const (
	// defaultDefenseStoreFile 防护任务持久化文件名（JSON）。
	defaultDefenseStoreFile = "defense_tasks.json"
	// defenseTaskTTL 任务从创建到过期的最长存活时间（毫秒）。超过则回收为 expired。
	defenseTaskTTL = 30 * time.Minute
)

// DefenseTask 是单条防护任务的全生命周期记录。
type DefenseTask struct {
	model.DefenseCommand
	State       string `json:"state"`                 // queued/delivered/running/succeeded/failed/expired
	Message     string `json:"message,omitempty"`     // 执行摘要或失败原因
	Operator    string `json:"operator,omitempty"`    // 触发操作的管理员账户
	OperatorIP  string `json:"operatorIP,omitempty"`  // 操作人真实来源 IP（用于白名单与审计）
	DeliveredAt int64  `json:"deliveredAt,omitempty"` // 领取下发时间（毫秒）
	RunningAt   int64  `json:"runningAt,omitempty"`    // Agent 开始执行时间（毫秒）
	DoneAt      int64  `json:"doneAt,omitempty"`       // 完成（成功/失败）时间（毫秒）
}

// DefenseStore 持久化并管理防护任务。
type DefenseStore struct {
	mu     sync.RWMutex
	tasks  map[string]*DefenseTask
	caps   map[string]bool // node -> 是否支持结构化防御指令（由 Agent 上报）
	path   string
}

// NewDefenseStore 创建防护任务存储并加载既有数据。
func NewDefenseStore(file string) *DefenseStore {
	if file == "" {
		file = defaultDefenseStoreFile
	}
	s := &DefenseStore{
		tasks: map[string]*DefenseTask{},
		caps:  map[string]bool{},
		path:  file,
	}
	s.load()
	return s
}

func (s *DefenseStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var snap struct {
		Tasks []*DefenseTask   `json:"tasks"`
		Caps  map[string]bool  `json:"caps"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("防护任务存储加载失败，忽略旧数据", "path", s.path, "err", err)
		return
	}
	for _, t := range snap.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		s.tasks[t.ID] = t
	}
	if snap.Caps != nil {
		s.caps = snap.Caps
	}
	slog.Info("已加载防护任务存储", "tasks", len(s.tasks), "caps", len(s.caps))
}

func (s *DefenseStore) save() {
	s.mu.RLock()
	snap := struct {
		Tasks []*DefenseTask  `json:"tasks"`
		Caps  map[string]bool `json:"caps"`
	}{
		Tasks: make([]*DefenseTask, 0, len(s.tasks)),
		Caps:  s.caps,
	}
	for _, t := range s.tasks {
		snap.Tasks = append(snap.Tasks, t)
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Warn("防护任务存储序列化失败", "err", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Warn("防护任务存储写入失败", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("防护任务存储落盘失败", "err", err)
	}
}

// Create 创建一条防护任务，初始状态 queued。operator/operatorIP 用于白名单与审计。
func (s *DefenseStore) Create(cmd model.DefenseCommand, operator, operatorIP string) *DefenseTask {
	if cmd.CreatedAt == 0 {
		cmd.CreatedAt = time.Now().UnixMilli()
	}
	if cmd.ExpiresAt == 0 {
		cmd.ExpiresAt = cmd.CreatedAt + defenseTaskTTL.Milliseconds()
	}
	t := &DefenseTask{
		DefenseCommand: cmd,
		State:          model.DefenseStateQueued,
		Operator:       operator,
		OperatorIP:     operatorIP,
	}
	s.mu.Lock()
	s.tasks[cmd.ID] = t
	s.mu.Unlock()
	s.save()
	return t
}

// Take 领取某节点当前 queued 的防护任务，置为 delivered 并装载白名单参数。
// 同一节点同时只下发一条；无则返 nil。
func (s *DefenseStore) Take(node string) *model.DefenseCommand {
	s.mu.Lock()
	defer s.mu.Unlock()
	var picked *DefenseTask
	for _, t := range s.tasks {
		if t.Node == node && t.State == model.DefenseStateQueued {
			picked = t
			break
		}
	}
	if picked == nil {
		return nil
	}
	// 过期检查：创建后超过 TTL 不再下发，标记为 expired。
	if picked.ExpiresAt > 0 && time.Now().UnixMilli() > picked.ExpiresAt {
		picked.State = model.DefenseStateExpired
		go s.save()
		return nil
	}
	picked.State = model.DefenseStateDelivered
	picked.DeliveredAt = time.Now().UnixMilli()
	go s.save()
	cmd := picked.DefenseCommand
	cp := cmd
	return &cp
}

// MarkRunning 由 Agent 回执（state=running）驱动，置为 running。
// 仅在已 delivered 或 running 时生效，保证幂等。
func (s *DefenseStore) MarkRunning(id string) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok || (t.State != model.DefenseStateDelivered && t.State != model.DefenseStateRunning) {
		s.mu.Unlock()
		return
	}
	t.State = model.DefenseStateRunning
	t.RunningAt = time.Now().UnixMilli()
	s.mu.Unlock()
	s.save()
}

// UpdateResult 应用 Agent 执行结果回执（running/succeeded/failed）。
// 仅在已 running（或 delivered）时生效；succeeded/failed 标记完成时间。
func (s *DefenseStore) UpdateResult(res model.DefenseCommandResult) {
	s.mu.Lock()
	t, ok := s.tasks[res.CommandID]
	if !ok {
		s.mu.Unlock()
		return
	}
	switch res.State {
	case model.DefenseStateRunning:
		if t.State == model.DefenseStateDelivered || t.State == model.DefenseStateRunning {
			t.State = model.DefenseStateRunning
		}
	case model.DefenseStateSucceeded, model.DefenseStateFailed:
		if t.State == model.DefenseStateDelivered || t.State == model.DefenseStateRunning {
			t.State = res.State
			t.DoneAt = time.Now().UnixMilli()
		}
	default:
		s.mu.Unlock()
		return
	}
	t.Message = res.Message
	s.mu.Unlock()
	s.save()
}

// Get 按 ID 返回任务。
func (s *DefenseStore) Get(id string) (*DefenseTask, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

// List 返回任务列表（倒序），可按 node 过滤。
func (s *DefenseStore) List(node string) []*DefenseTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*DefenseTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		if node != "" && t.Node != node {
			continue
		}
		out = append(out, t)
	}
	// 按创建时间倒序
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt > out[i].CreatedAt {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Latest 返回某节点最近一条任务（不论状态）。
func (s *DefenseStore) Latest(node string) *DefenseTask {
	var latest *DefenseTask
	for _, t := range s.List(node) {
		if latest == nil || t.CreatedAt > latest.CreatedAt {
			latest = t
		}
	}
	return latest
}

// ExpireOverdue 将超时未完成的 queued/delivered/running 任务回收为 expired。
// 由 receiver 在处理 report 时周期调用。
func (s *DefenseStore) ExpireOverdue() {
	now := time.Now().UnixMilli()
	s.mu.Lock()
	changed := false
	for _, t := range s.tasks {
		if t.State == model.DefenseStateSucceeded || t.State == model.DefenseStateFailed || t.State == model.DefenseStateExpired {
			continue
		}
		if t.ExpiresAt > 0 && now > t.ExpiresAt {
			t.State = model.DefenseStateExpired
			t.Message = "任务超时未完成"
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.save()
	}
}

// SaveCap 记录某节点 Agent 是否支持结构化防御指令。
// 由 Receiver 在收到 Agent 上报的 Capabilities 时调用。
func (s *DefenseStore) SaveCap(node string, supported bool) {
	s.mu.Lock()
	if s.caps[node] == supported {
		s.mu.Unlock()
		return
	}
	s.caps[node] = supported
	s.mu.Unlock()
	s.save()
}

// GetCap 返回某节点是否支持结构化防御指令。
func (s *DefenseStore) GetCap(node string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.caps[node]
}

// AllCaps 返回全量 capability 映射（node -> supported）。
func (s *DefenseStore) AllCaps() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.caps))
	for k, v := range s.caps {
		out[k] = v
	}
	return out
}
