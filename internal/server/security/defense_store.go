package security

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
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
	// maxDefenseTasks 是保留的防护任务条数上限；超出时丢弃最老的**已结束**任务。
	//
	// 与下行操作任务（ops.maxTasks）同一个理由，而且这里更紧迫：任务是纯追加的，
	// 不设上限时文件会无限增长，而**每次状态变化都要全量重写整个文件**、启动时还要整个读进内存
	// ——于是"封禁次数越多、每次写越慢、启动越慢"，且没有任何地方会提示这件事。
	// 保留已结束的任务是为了让回执可回看；未结束的（queued/delivered/running）永不丢弃：
	// 它们还有回执要等，丢掉就等于让操作者永远不知道结果。
	maxDefenseTasks = 500
)

// DefenseTask 是单条防护任务的全生命周期记录。
type DefenseTask struct {
	model.DefenseCommand
	State       string `json:"state"`                 // queued/delivered/running/succeeded/failed/expired
	Message     string `json:"message,omitempty"`     // 执行摘要或失败原因
	Operator    string `json:"operator,omitempty"`    // 触发操作的管理员账户
	OperatorIP  string `json:"operatorIP,omitempty"`  // 操作人真实来源 IP（用于白名单与审计）
	DeliveredAt int64  `json:"deliveredAt,omitempty"` // 领取下发时间（毫秒）
	RunningAt   int64  `json:"runningAt,omitempty"`   // Agent 开始执行时间（毫秒）
	DoneAt      int64  `json:"doneAt,omitempty"`      // 完成（成功/失败）时间（毫秒）
}

// DefenseStore 持久化并管理防护任务。
type DefenseStore struct {
	mu    sync.RWMutex
	tasks map[string]*DefenseTask
	caps  map[string]bool // node -> 是否支持结构化防御指令（由 Agent 上报）
	path  string
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
		Tasks []*DefenseTask  `json:"tasks"`
		Caps  map[string]bool `json:"caps"`
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

	// 加载时顺手自愈：回收超时未结束的、裁掉超量的已结束任务。
	//
	// 必须在**加载时**就做，而不是等下一次防护动作：老版本没有上限，存量文件可能已经很大，
	// 而这份文件要整个读进内存；不在这里修，它就得等到下一次有人点"封禁"才缩回去。
	expired := s.expireOverdue(time.Now().UnixMilli())
	dropped := s.prune()
	if expired || dropped > 0 {
		slog.Warn("防护任务存储已自动裁剪", "expired", expired, "dropped", dropped, "kept", len(s.tasks))
		s.save()
	}
}

// expireOverdue 是"超时未完成的任务回收为 expired"的**唯一实现**（返回是否有改动，
// 由调用方决定要不要落盘）：加载与落盘前也要复用同一套判定，否则同一条超时的任务
// 会因触发路径不同而得到不同结果。
//
// 为什么 prune 之外还需要它：prune 只丢**已结束**的任务，而一条 queued 的任务若该节点
// 此后再没来领取（Agent 下线、被移除、一直忙），Take 里的过期检查永远不会被触发
// ——它会永远停在 queued：既不结束、也不可裁，白占一条量。
func (s *DefenseStore) expireOverdue(now int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for _, t := range s.tasks {
		switch t.State {
		case model.DefenseStateSucceeded, model.DefenseStateFailed, model.DefenseStateExpired:
			continue
		}
		if t.ExpiresAt > 0 && now > t.ExpiresAt {
			t.State = model.DefenseStateExpired
			t.Message = "任务超时未完成"
			changed = true
		}
	}
	return changed
}

// prune 在任务数超限时丢弃最老的已结束任务，返回丢弃条数。
//
// 与 ops.Store.prune 同一套语义：**只丢已结束的**（succeeded/failed/expired）。
// 若未结束的任务本身就超过上限（节点一直不来领取），这里是尽力而为——不会为了凑数
// 去丢一条还在等回执的任务。
func (s *DefenseStore) prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) <= maxDefenseTasks {
		return 0
	}
	finished := make([]*DefenseTask, 0, len(s.tasks))
	for _, t := range s.tasks {
		switch t.State {
		case model.DefenseStateSucceeded, model.DefenseStateFailed, model.DefenseStateExpired:
			finished = append(finished, t)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].CreatedAt < finished[j].CreatedAt })
	dropped := 0
	for _, t := range finished {
		if len(s.tasks) <= maxDefenseTasks {
			break
		}
		delete(s.tasks, t.ID)
		dropped++
	}
	return dropped
}

func (s *DefenseStore) save() {
	// 落盘前先自愈（两个函数各自加锁，此处不得持锁）：
	// 这样"回收 + 裁剪"跟着每一次状态变化发生，而不是只在重启时发生一次。
	s.expireOverdue(time.Now().UnixMilli())
	s.prune()

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
	if s.expireOverdue(time.Now().UnixMilli()) {
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
