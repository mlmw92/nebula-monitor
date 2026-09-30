package ops

import (
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

const (
	// defaultStoreFile 操作任务持久化文件名（JSON）。
	//
	// 沿用防护任务的做法（文件而非时序库/关系库）：任务量小、要能人工查看、
	// 且**不能丢**——一条已下发的重启指令丢了会让操作者以为它没执行过。
	defaultStoreFile = "ops_tasks.json"
	// taskTTL 是任务从创建到过期的最长存活时间。
	//
	// 30 分钟：足够覆盖「Agent 忙一轮、网络抖动、下一条指令排队」，
	// 又不会让一条早已无意义的指令在几小时后突然在某台机器上执行。
	taskTTL = 30 * time.Minute
	// maxTasks 是保留的任务条数上限；超出时丢弃最老的**已完成**任务。
	//
	// 保留"已完成"的任务是为了让回执里的输出能被人看到；但它是纯追加的，
	// 不设上限时文件会随时间无限增长。
	maxTasks = 500
)

// Task 是一条操作任务的全生命周期记录。
type Task struct {
	model.OpsCommand
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
	// Data 是执行产出的结构化结果（分节文本）。
	Data map[string]string `json:"data,omitempty"`
	// Operator / OperatorIP 是触发操作的管理员与来源 IP（审计用）。
	Operator   string `json:"operator,omitempty"`
	OperatorIP string `json:"operatorIP,omitempty"`
	// Reason 是操作者填写的原因（可选，但强烈建议——回看历史时"为什么重启它"往往比"谁重启的"更重要）。
	Reason      string `json:"reason,omitempty"`
	DeliveredAt int64  `json:"deliveredAt,omitempty"`
	RunningAt   int64  `json:"runningAt,omitempty"`
	DoneAt      int64  `json:"doneAt,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
}

// Store 持久化并管理操作任务。
type Store struct {
	mu    sync.RWMutex
	tasks map[string]*Task
	// caps 是 node → 该节点 Agent 声明的可执行动作集合（能力协商，第二道护栏）。
	caps      map[string]map[string]bool
	path      string
	seq       int64
	now       func() int64
	persistMu sync.Mutex
}

// NewStore 创建操作任务存储并加载既有数据（file 为空时用默认文件名）。
func NewStore(file string) *Store {
	if file == "" {
		file = defaultStoreFile
	}
	s := &Store{
		tasks: map[string]*Task{},
		caps:  map[string]map[string]bool{},
		path:  file,
		now:   func() int64 { return time.Now().UnixMilli() },
	}
	s.load()
	return s
}

// SetNow 注入时钟（测试用）。
func (s *Store) SetNow(fn func() int64) {
	if fn != nil {
		s.now = fn
	}
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var snap struct {
		Tasks []*Task              `json:"tasks"`
		Caps  map[string][]string  `json:"caps"`
		Seq   int64                `json:"seq"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		slog.Warn("操作任务存储加载失败，忽略旧数据", "path", s.path, "err", err)
		return
	}
	for _, t := range snap.Tasks {
		if t == nil || t.ID == "" {
			continue
		}
		s.tasks[t.ID] = t
	}
	for node, kinds := range snap.Caps {
		set := map[string]bool{}
		for _, k := range kinds {
			set[k] = true
		}
		s.caps[node] = set
	}
	s.seq = snap.Seq
	slog.Info("已加载操作任务存储", "tasks", len(s.tasks), "nodes", len(s.caps))
}

func (s *Store) save() {
	// 落盘串行化：save 会在 Take/回执等路径上并发调用，不加锁会让 tmp 文件互相覆盖。
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	snap := struct {
		Tasks []*Task             `json:"tasks"`
		Caps  map[string][]string `json:"caps"`
		Seq   int64               `json:"seq"`
	}{Tasks: make([]*Task, 0, len(s.tasks)), Caps: map[string][]string{}, Seq: s.seq}
	for _, t := range s.tasks {
		snap.Tasks = append(snap.Tasks, t)
	}
	for node, set := range s.caps {
		kinds := make([]string, 0, len(set))
		for k := range set {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		snap.Caps[node] = kinds
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		slog.Warn("操作任务存储序列化失败", "err", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("操作任务存储写入失败", "err", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Warn("操作任务存储落盘失败", "err", err)
	}
}

// nextID 生成任务 ID（形如 ops-7：可读、可在界面与日志里直接引用）。
func (s *Store) nextID() string {
	s.seq++
	return "ops-" + itoa(s.seq)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Create 创建一条待下发任务，初始状态 queued。
func (s *Store) Create(cmd model.OpsCommand, operator, operatorIP, reason string) *Task {
	s.mu.Lock()
	if cmd.ID == "" {
		cmd.ID = s.nextID()
	}
	if cmd.CreatedAt == 0 {
		cmd.CreatedAt = s.now()
	}
	if cmd.ExpireAt == 0 {
		cmd.ExpireAt = cmd.CreatedAt + taskTTL.Milliseconds()
	}
	t := &Task{
		OpsCommand: cmd,
		State:      model.OpsStateQueued,
		Operator:   operator,
		OperatorIP: operatorIP,
		Reason:     reason,
	}
	s.tasks[t.ID] = t
	s.mu.Unlock()
	s.prune()
	s.save()
	return t
}

// prune 在任务数超限时丢弃最老的已完成任务（queued/running 的任务永不丢弃：
// 它们还有回执要等，丢掉就等于让操作者永远不知道结果）。
func (s *Store) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) <= maxTasks {
		return
	}
	finished := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		switch t.State {
		case model.OpsStateSucceeded, model.OpsStateFailed, model.OpsStateExpired:
			finished = append(finished, t)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].CreatedAt < finished[j].CreatedAt })
	for _, t := range finished {
		if len(s.tasks) <= maxTasks {
			break
		}
		delete(s.tasks, t.ID)
	}
}

// Take 领取某节点当前可下发的任务（同一节点同时只下发一条）。
//
// kinds 是该节点 Agent 声明支持的动作集合（能力协商）：不支持的 action 继续排队，
// 而不是下发后等 Agent 静默忽略——后者会让任务在界面上一直停在 delivered，最难排查。
func (s *Store) Take(node string, kinds []string) *model.OpsCommand {
	if len(kinds) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		allowed[k] = true
	}

	s.mu.Lock()
	var picked *Task
	for _, t := range s.tasks {
		if t.Node != node || t.State != model.OpsStateQueued {
			continue
		}
		if !allowed[t.Kind] {
			continue
		}
		if picked == nil || t.CreatedAt < picked.CreatedAt {
			picked = t
		}
	}
	if picked == nil {
		s.mu.Unlock()
		return nil
	}
	now := s.now()
	if picked.ExpireAt > 0 && now > picked.ExpireAt {
		picked.State = model.OpsStateExpired
		picked.Message = "任务超时未领取"
		s.mu.Unlock()
		s.save()
		return nil
	}
	picked.State = model.OpsStateDelivered
	picked.DeliveredAt = now
	cmd := picked.OpsCommand
	s.mu.Unlock()
	s.save()
	cp := cmd
	return &cp
}

// ApplyResult 应用 Agent 回执（running / succeeded / failed）。
func (s *Store) ApplyResult(res model.OpsResult) {
	s.mu.Lock()
	t, ok := s.tasks[res.CommandID]
	if !ok {
		s.mu.Unlock()
		return
	}
	// 终态不可被后续回执改写（乱序/重放的回执不该把 succeeded 打回 running）。
	if t.State == model.OpsStateSucceeded || t.State == model.OpsStateFailed || t.State == model.OpsStateExpired {
		s.mu.Unlock()
		return
	}
	switch res.State {
	case model.OpsStateRunning:
		t.State = model.OpsStateRunning
		t.RunningAt = s.now()
	case model.OpsStateSucceeded, model.OpsStateFailed:
		t.State = res.State
		t.DoneAt = s.now()
	default:
		s.mu.Unlock()
		return
	}
	t.Message = res.Message
	if res.Data != nil {
		t.Data = res.Data
	}
	t.DurationMs = res.DurationMs
	s.mu.Unlock()
	s.save()
}

// Get 按 ID 返回任务。
func (s *Store) Get(id string) (*Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

// ListFilter 是任务列表的筛选条件。
type ListFilter struct {
	Node  string
	Kind  string
	State string
	Limit int
}

// List 返回任务列表（创建时间倒序）。
func (s *Store) List(f ListFilter) []*Task {
	s.mu.RLock()
	out := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		if f.Node != "" && t.Node != f.Node {
			continue
		}
		if f.Kind != "" && t.Kind != f.Kind {
			continue
		}
		if f.State != "" && t.State != f.State {
			continue
		}
		out = append(out, t)
	}
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// ExpireOverdue 回收超时未完成的任务（由 receiver 每轮上报调用）。
func (s *Store) ExpireOverdue() {
	now := s.now()
	s.mu.Lock()
	changed := false
	for _, t := range s.tasks {
		switch t.State {
		case model.OpsStateSucceeded, model.OpsStateFailed, model.OpsStateExpired:
			continue
		}
		if t.ExpireAt > 0 && now > t.ExpireAt {
			t.State = model.OpsStateExpired
			t.Message = "任务超时未完成（Agent 未领取或执行未回执）"
			changed = true
		}
	}
	s.mu.Unlock()
	if changed {
		s.save()
	}
}

// SaveCaps 记录某节点 Agent 声明的可执行动作集合。
func (s *Store) SaveCaps(node string, kinds []string) {
	set := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		if k != "" {
			set[k] = true
		}
	}
	s.mu.Lock()
	prev := s.caps[node]
	same := len(prev) == len(set)
	if same {
		for k := range set {
			if !prev[k] {
				same = false
				break
			}
		}
	}
	if same {
		s.mu.Unlock()
		return
	}
	s.caps[node] = set
	s.mu.Unlock()
	s.save()
}

// Caps 返回某节点声明支持的动作（副本）。
func (s *Store) Caps(node string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedKeys(s.caps[node])
}

// AllCaps 返回全量能力映射（node → 动作列表）。
func (s *Store) AllCaps() map[string][]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string][]string, len(s.caps))
	for node := range s.caps {
		out[node] = sortedKeys(s.caps[node])
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
