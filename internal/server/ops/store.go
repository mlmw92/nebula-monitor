package ops

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 取消失败的两种原因刻意分开，供 API 层给出不同的状态码与提示：
//   - ErrNotCancellable：任务已被节点领取，撤不回来了（409，说明"已下发"）；
//   - ErrNotTerminal：想删的是还在跑的任务（409 或 400，提示先取消/等结束）。
var (
	// ErrNotCancellable 表示任务已过可取消阶段（只有 queued 能取消）。
	ErrNotCancellable = errors.New("任务已下发，无法取消")
	// ErrNotTerminal 表示任务尚未结束。
	ErrNotTerminal = errors.New("任务尚未结束")
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
	// Data 是执行产出的分节文本结果（人读）。
	Data map[string]string `json:"data,omitempty"`
	// JSON 是同一份结果的结构化形态（容器查询的表格载荷），见 model.OpsResult.JSON 的说明。
	JSON string `json:"json,omitempty"`
	// Operator / OperatorIP 是触发操作的管理员与来源 IP（审计用）。
	Operator   string `json:"operator,omitempty"`
	OperatorIP string `json:"operatorIP,omitempty"`
	// Reason 是操作者填写的原因（可选，但强烈建议——回看历史时"为什么重启它"往往比"谁重启的"更重要）。
	Reason      string `json:"reason,omitempty"`
	DeliveredAt int64  `json:"deliveredAt,omitempty"`
	RunningAt   int64  `json:"runningAt,omitempty"`
	DoneAt      int64  `json:"doneAt,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	// BatchID 是批量下发的批次号（单条下发为空）。用于按批次聚合结果与「整批取消」。
	BatchID string `json:"batchId,omitempty"`
	// File 是 file.push 任务的文件元信息快照（**不含内容**，内容在 FileStore 里按引用存）。
	//
	// 刻意冗余一份：任务列表要能回答"当时分发的是哪个文件、多大、摘要是什么"，
	// 而内容本身会被淘汰（FileStore.prune）——记录不能因此变成一串看不懂的引用号。
	// 它不参与投递：投递用的是 Params["fileId"]，见 Store.loadBlob。
	File *FileRecord `json:"file,omitempty"`
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
	// files 是文件分发的内容存储（可空：未启用时文件分发任务会在领取阶段明确失败）。
	files *FileStore
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
		Tasks []*Task             `json:"tasks"`
		Caps  map[string][]string `json:"caps"`
		Seq   int64               `json:"seq"`
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
	return s.create(cmd, "", operator, operatorIP, reason)
}

// create 是 Create 与批次创建共用的落库逻辑（batchID 为空表示单条下发）。
func (s *Store) create(cmd model.OpsCommand, batchID, operator, operatorIP, reason string) *Task {
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
		BatchID:    batchID,
	}
	s.tasks[t.ID] = t
	s.mu.Unlock()
	s.prune()
	s.save()
	return t
}

// NextBatchID 生成批次号。与任务 ID 共用同一个自增序列（并**消耗**一个序号），
// 避免两套编号各自漂移、也避免两个批次拿到同一个号（那会让"整批取消"误伤另一批）。
func (s *Store) NextBatchID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return "ob-" + itoa(s.seq)
}

// CreateInBatch 在指定批次下创建一条任务（批次号由调用方先用 NextBatchID 生成，
// 让同一批的所有任务共用它，前端才能按批次聚合）。
func (s *Store) CreateInBatch(cmd model.OpsCommand, batchID, operator, operatorIP, reason string) *Task {
	return s.create(cmd, batchID, operator, operatorIP, reason)
}

// CreateMany 在同一批次下一次性创建多条任务（一次加锁、一次落盘）。
//
// 与逐条 Create 的差别只在开销与一致性：200 台的任务逐个落盘会写 200 次 JSON 文件，
// 且中途出问题时留下"半批"。这里一次写完——批量下发要么整批成立，要么什么都不建。
func (s *Store) CreateMany(cmds []model.OpsCommand, batchID, operator, operatorIP, reason string) []*Task {
	if len(cmds) == 0 {
		return nil
	}
	out := make([]*Task, 0, len(cmds))
	s.mu.Lock()
	for _, cmd := range cmds {
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
			BatchID:    batchID,
		}
		s.tasks[t.ID] = t
		out = append(out, t)
	}
	s.mu.Unlock()
	s.prune()
	s.save()
	return out
}

// Cancel 取消一条**仍未被节点领取**（queued）的任务，返回取消后的任务。
//
// 只允许 queued 取消：一旦 delivered，指令已经随某轮上报发出去了，此时改状态只会
// 让界面显示"已取消"而机器照样执行——那比不提供取消更危险。
func (s *Store) Cancel(id string, actor string) (*Task, error) {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("任务不存在")
	}
	if t.State != model.OpsStateQueued {
		cur := t.State
		s.mu.Unlock()
		return nil, fmt.Errorf("%w：任务当前状态为 %s（已被节点领取或已结束），无法取消", ErrNotCancellable, cur)
	}
	t.State = model.OpsStateCancelled
	t.Message = "已由 " + actor + " 取消（下发前撤回）"
	t.DoneAt = s.now()
	s.mu.Unlock()
	s.save()
	return t, nil
}

// Remove 删除一条**终态**记录，返回是否真的删掉了。
//
// 只允许终态：删掉排队中/执行中的任务会让"这条指令去哪了"无从回答。
// 删除本身要写审计——它是"抹掉运维记录"，必须留痕（审计里仍能查到谁删了什么）。
func (s *Store) Remove(id string, actor string) error {
	s.mu.Lock()
	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("任务不存在")
	}
	if !model.OpsStateTerminal(t.State) {
		cur := t.State
		s.mu.Unlock()
		return fmt.Errorf("%w：任务当前状态为 %s，只有已结束（成功/失败/超时/已取消）的记录才能删除", ErrNotTerminal, cur)
	}
	delete(s.tasks, id)
	s.mu.Unlock()
	s.save()
	return nil
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

	// file.push 的内容在**领取时**才注入：任务存储里只有引用（理由见 files.go 的说明）。
	//
	// 注入失败必须让任务明确失败并带上原因，而不是下发一条没有内容的指令——
	// 那样 Agent 只会回一句"缺少文件内容"，用户还得自己去猜是哪一环出的问题。
	if cmd.Kind == model.OpsKindFilePush {
		blob, err := s.loadBlob(cmd.Params["fileId"])
		if err != nil {
			s.failTask(cmd.ID, err.Error())
			return nil
		}
		cmd.File = blob
	}
	return &cmd
}

// loadBlob 读取并编码要随指令下发的文件内容。
func (s *Store) loadBlob(ref string) (*model.OpsFileBlob, error) {
	if ref == "" {
		return nil, fmt.Errorf("指令缺少文件引用（fileId），无法下发")
	}
	if s.files == nil {
		return nil, fmt.Errorf("服务端未启用操作文件存储，无法下发文件分发任务")
	}
	content, rec := s.files.Content(ref)
	if rec == nil {
		return nil, fmt.Errorf("分发的文件内容已不存在（引用 %s 可能已被清理），请重新上传后再下发", ref)
	}
	return &model.OpsFileBlob{
		Name:    rec.Name,
		Size:    rec.Size,
		SHA256:  rec.SHA256,
		Content: base64.StdEncoding.EncodeToString(content),
	}, nil
}

// failTask 把一条任务直接判为失败（在领取阶段就发现无法下发时用）。
func (s *Store) failTask(id, msg string) {
	s.mu.Lock()
	if t, ok := s.tasks[id]; ok {
		t.State = model.OpsStateFailed
		t.Message = msg
		t.DoneAt = s.now()
	}
	s.mu.Unlock()
	s.save()
}

// SetFileStore 注入文件分发的内容存储。
func (s *Store) SetFileStore(f *FileStore) { s.files = f }

// AttachFileMany 给一批任务挂上同一个文件元信息快照（一次落盘）。
//
// 批量下发的 200 条任务要挂同一份快照：逐条挂会写 200 次 JSON 文件——
// 与 CreateMany 存在的理由完全相同。
func (s *Store) AttachFileMany(ids []string, rec *FileRecord) {
	if rec == nil || len(ids) == 0 {
		return
	}
	s.mu.Lock()
	for _, id := range ids {
		if t, ok := s.tasks[id]; ok {
			t.File = rec
		}
	}
	s.mu.Unlock()
	s.save()
}

// AttachFile 给单条任务挂上文件元信息快照（展示与审计用，不参与投递）。
func (s *Store) AttachFile(id string, rec *FileRecord) {
	if rec == nil {
		return
	}
	s.AttachFileMany([]string{id}, rec)
}

// FileRefInUse 报告某个上传文件是否仍被**未结束**的任务引用。
//
// 供 FileStore 淘汰时使用：把还在排队/领取/执行中的任务的内容删掉，会让那条任务在领取时
// 突然没有内容可发，用户看到的是"点了分发没反应"——最难排查的一类失败。
func (s *Store) FileRefInUse(ref string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.tasks {
		if t.Params["fileId"] != ref {
			continue
		}
		switch t.State {
		case model.OpsStateQueued, model.OpsStateDelivered, model.OpsStateRunning:
			return true
		}
	}
	return false
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
	t.JSON = res.JSON
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
	// BatchID 按批次过滤（前端点批次号即筛该批）。
	BatchID string
	Limit   int
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
		if f.BatchID != "" && t.BatchID != f.BatchID {
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
