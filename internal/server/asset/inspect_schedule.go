package asset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/server/config"
)

// 配置巡检的周期化调度。
//
// 设计件：docs/superpowers/specs/2026-10-07-inspect-schedule-design.md
//
// 为什么不是"通用作业调度"：平台里那条决策写得很清楚（见 report/schedule.go 顶部）——需要定时的
// 用例只有报告与数据保留，为它们引入一套 cron 解析 + 任务注册表，收益只是"看起来更通用"，
// 代价是多一套要维护与排障的调度器。本文件补的是**第三条用例**（配置巡检：它本身就是周期性体检），
// 并沿用报告那套已被评审过的范式：配置与运行状态同文件、可关、可改周期、可立即执行、
// 上次结果可见、失败显式上报。
//
// 与手动巡检的关系：手动跑**共用同一个入口**（RunNow），因此"上次运行"的口径一致——
// 否则界面上会出现"我明明刚跑过，调度却说到点该跑了"（报告调度定过的同一条）。
// 执行端就是 Service.RunInspect，它本身接一个 actor 字符串、不做鉴权，因此定时器不需要
// 伪造一套"系统用户"：**留痕在 inspect_runs.actor 上**（定时触发写 schedule，手动触发写登录名）。

const (
	// InspectScheduleActor 是定时触发的留痕名（落在 inspect_runs.actor）。
	//
	// 刻意不编一个"系统用户"：本平台所有执行入口都靠请求上下文取 Principal，审计也只有 API 层在写，
	// 后台 goroutine 从不写审计（引入那套惯例是另一件事，见设计件 §2）。所以这里用一个**明确的触发标记**，
	// 让"这条记录是机器跑的"一眼可辨，而不是把它算到某个人头上。
	InspectScheduleActor = "schedule"

	// DefaultInspectScheduleIntervalHours / MaxInspectScheduleIntervalHours 是间隔边界（小时）。
	DefaultInspectScheduleIntervalHours = 24
	MaxInspectScheduleIntervalHours     = 720

	// inspectScheduleTick 是检查间隔：每分钟看一次是否到点，而不是 time.Ticker(interval)——
	// 后者改配置后不生效，而"保存即热生效"是本平台所有 Web 可编辑配置的既有约定。
	inspectScheduleTick = time.Minute
)

// 范围模式的取值。两个维度各自带显式模式标志，见 ScopeSnapshot 的说明。
const (
	ScopeModeAll     = "all"
	ScopeModeLimited = "limited"
)

// ScopeLabel 是范围快照里的一个业务标签选择器。
//
// 刻意不复用 LabelSelector：它要落进 YAML 配置，字段名属于配置的一部分（人可能手改它），
// 与查询结构之间在 Apply 里做一次显式映射（沿用"两边只在边界上映射一次"的取向）。
type ScopeLabel struct {
	Key   string `json:"key" yaml:"key"`
	Value string `json:"value" yaml:"value"`
}

// ScopeSnapshot 是一次巡检的范围快照（节点维度 + 业务标签维度）。
//
// 两个维度**各自带显式的模式标志**，不靠"清单空不空"猜——这是本平台在业务范围上已经定过的口径
// （见 2026-10-07-asset-business-scope-design.md）：空清单 + limited = 该维度不允许任何资产
// （fail-closed），空清单 + all = 该维度不生效。少了这个标志，YAML 的一次 round-trip 就会把
// "受限且为空"变成"不限"，那是一步越权。
type ScopeSnapshot struct {
	NodeMode  string       `json:"nodeMode" yaml:"nodeMode"`
	Nodes     []string     `json:"nodes,omitempty" yaml:"nodes,omitempty"`
	AssetMode string       `json:"assetMode" yaml:"assetMode"`
	Labels    []ScopeLabel `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// Intersect 返回两个快照的交集：任一维度受限即为受限，取值取两个清单的交集。
//
// 两条性质是 D2 的核心：
//   - **只朝收窄方向变**——保存者的范围被收窄后，定时任务不会继续拿着旧的更宽范围跑；
//   - 解析不到身份时由调用方按空范围处理（fail-closed），而不是沿用快照。
func (s ScopeSnapshot) Intersect(other ScopeSnapshot) ScopeSnapshot {
	out := ScopeSnapshot{NodeMode: normalizeScopeMode(s.NodeMode), AssetMode: normalizeScopeMode(s.AssetMode)}
	if out.NodeMode == ScopeModeLimited || normalizeScopeMode(other.NodeMode) == ScopeModeLimited {
		out.NodeMode = ScopeModeLimited
		out.Nodes = intersectStrings(s.Nodes, other.Nodes)
	}
	if out.AssetMode == ScopeModeLimited || normalizeScopeMode(other.AssetMode) == ScopeModeLimited {
		out.AssetMode = ScopeModeLimited
		out.Labels = intersectLabels(s.Labels, other.Labels)
	}
	return out
}

// IsEmpty 判断快照是否"一定看不到任何资产"。
//
// 任一维度受限且清单为空即恒空（与 ListFilter 的三态一致）。调用方据此**拒绝执行**并给出原因，
// 而不是跑出一份"0 个资产、0 条差异"的记录——那种记录会被读成"巡检过了，没问题"。
func (s ScopeSnapshot) IsEmpty() bool {
	if normalizeScopeMode(s.NodeMode) == ScopeModeLimited && len(s.Nodes) == 0 {
		return true
	}
	if normalizeScopeMode(s.AssetMode) == ScopeModeLimited && len(s.Labels) == 0 {
		return true
	}
	return false
}

// Apply 把快照折算成台账筛选的两个范围维度，**保留 nil / 非 nil 空 的三态**（见 ListFilter 说明）：
// all → nil（该维度不生效），limited → 非 nil（空即恒不匹配）。
func (s ScopeSnapshot) Apply(f ListFilter) ListFilter {
	f.Nodes = nil
	if normalizeScopeMode(s.NodeMode) == ScopeModeLimited {
		f.Nodes = append([]string{}, s.Nodes...)
	}
	f.LabelSelectors = nil
	if normalizeScopeMode(s.AssetMode) == ScopeModeLimited {
		f.LabelSelectors = make([]LabelSelector, 0, len(s.Labels))
		for _, l := range s.Labels {
			f.LabelSelectors = append(f.LabelSelectors, LabelSelector{Key: l.Key, Value: l.Value})
		}
	}
	return f
}

// InspectScheduleConfig 是周期化巡检的配置（含运行状态）。
//
// 运行状态与配置同文件：它必须跨重启保留，否则频繁重启的机器每次启动都会重新触发一轮
// （巡检是重操作：遍历资产 + 写快照）。这是报告调度已经定过的取舍。
type InspectScheduleConfig struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
	// IntervalHours 是执行间隔（小时），1..720。
	IntervalHours int `json:"intervalHours" yaml:"intervalHours"`

	// 以下四项与 POST /api/v1/inspect/runs 的请求体同构：定时巡检 = 用保存者的范围重复执行那次巡检。
	Type    string   `json:"type,omitempty" yaml:"type,omitempty"`
	Node    string   `json:"node,omitempty" yaml:"node,omitempty"`
	Keyword string   `json:"keyword,omitempty" yaml:"keyword,omitempty"`
	Fields  []string `json:"fields,omitempty" yaml:"fields,omitempty"`

	// Scope 是**保存时**由保存者身份折算出的范围快照。客户端提交的范围一律被服务端覆盖
	// （见 API 层）：范围只能由服务端按身份算，客户端说了不算。
	Scope ScopeSnapshot `json:"scope" yaml:"scope"`

	// ---- 运行状态（调度器维护，界面只读）----
	LastRunAt int64 `json:"lastRunAt,omitempty" yaml:"lastRunAt,omitempty"`
	LastRunID int64 `json:"lastRunId,omitempty" yaml:"lastRunId,omitempty"`
	// LastManual 记录上次是手动还是定时触发的：不存的话 Status 里的 "上次运行" 会把
	// 手动那次显示成定时的（报告调度没存这个字段，界面上那句话是个小谎）。
	LastManual bool `json:"lastManual,omitempty" yaml:"lastManual,omitempty"`
	// LastError 非空表示上一轮**失败**（含"范围解析不到/已收窄为空"与"上一次尚未完成"）。
	// 与"跑了但没差异"是两件事，界面必须分开显示。
	LastError     string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
	LastChangedBy string `json:"lastChangedBy,omitempty" yaml:"lastChangedBy,omitempty"`
	LastChangedAt int64  `json:"lastChangedAt,omitempty" yaml:"lastChangedAt,omitempty"`
}

func (c *InspectScheduleConfig) normalize() {
	if c.IntervalHours <= 0 {
		c.IntervalHours = DefaultInspectScheduleIntervalHours
	}
	if c.IntervalHours > MaxInspectScheduleIntervalHours {
		c.IntervalHours = MaxInspectScheduleIntervalHours
	}
	c.Fields = dedupStrings(c.Fields)
	c.Scope = normalizeScopeSnapshot(c.Scope)
}

// InspectScheduleRun 是一次执行的结果。
type InspectScheduleRun struct {
	At     int64  `json:"at"`
	RunID  int64  `json:"runId,omitempty"`
	Error  string `json:"error,omitempty"`
	Manual bool   `json:"manual,omitempty"`
	// Actor 是这次记录的触发者（定时为 schedule，手动为登录名）——即 inspect_runs.actor 的值。
	Actor string `json:"actor,omitempty"`
	// Assets / Findings / Truncated 是本次巡检的结论摘要，供界面直接显示"跑了多少、差了多少"。
	Assets    int  `json:"assets,omitempty"`
	Findings  int  `json:"findings,omitempty"`
	Truncated bool `json:"truncated,omitempty"`
}

// InspectScheduleStatus 是调度现状（供 Web 端展示）。
type InspectScheduleStatus struct {
	Config InspectScheduleConfig `json:"config"`
	Last   *InspectScheduleRun   `json:"last,omitempty"`
	// NextAt 是下次预计执行时间（未启用时为 0）。
	NextAt int64 `json:"nextAt,omitempty"`
}

// InspectScheduler 周期化执行配置巡检。
type InspectScheduler struct {
	mu   sync.Mutex
	path string
	cfg  InspectScheduleConfig
	svc  *Service
	// resolve 按用户名取回**当前**范围快照；ok=false 表示这个身份已解析不到（账号被删/被禁）。
	//
	// 由 API 层注入：范围折算要用身份仓库与节点管理器，那是 API 层的依赖——
	// asset 包刻意不依赖 auth 包（见 LabelSelector 的说明），依赖方向保持清晰。
	resolve func(user string) (ScopeSnapshot, bool)
	now     func() time.Time
	// running 标记本轮是否在执行：巡检会遍历资产，到点时上一次还没跑完就跳过本轮
	// （不排队——排队会把"卡住一次"变成连续积压，而周期本身会带来下一次）。
	running bool
}

// NewInspectScheduler 创建调度器。配置文件不存在时用 initial 初始化并落盘（与保留策略一致）。
func NewInspectScheduler(path string, initial InspectScheduleConfig, svc *Service,
	resolve func(string) (ScopeSnapshot, bool)) (*InspectScheduler, error) {
	if resolve == nil {
		return nil, errors.New("周期化巡检缺少范围解析器")
	}
	s := &InspectScheduler{path: path, svc: svc, resolve: resolve, now: time.Now}
	cfg := initial
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("解析周期化巡检配置失败: %w", err)
		}
	case errors.Is(err, os.ErrNotExist):
		cfg.normalize()
		if err := writeInspectSchedule(path, cfg); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("读取周期化巡检配置失败: %w", err)
	}
	cfg.normalize()
	s.cfg = cfg
	return s, nil
}

// Config 返回当前配置（含运行状态）。
func (s *InspectScheduler) Config() InspectScheduleConfig {
	if s == nil {
		return InspectScheduleConfig{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Save 保存配置（热生效）。运行状态字段由调度器维护，这里保留原值——
// 前端只改"要不要跑、多久跑一次、按什么筛"，改不了"上次跑了什么"。
func (s *InspectScheduler) Save(cfg InspectScheduleConfig, changedBy string) error {
	if s == nil {
		return errors.New("周期化巡检未启用")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg.LastRunAt = s.cfg.LastRunAt
	cfg.LastRunID = s.cfg.LastRunID
	cfg.LastManual = s.cfg.LastManual
	cfg.LastError = s.cfg.LastError
	// 范围只能由服务端按身份折算（API 层已覆盖过请求体里的值），这里再兜一道：
	// 调用方传了空快照就沿用旧的，避免"客户端把范围抹成不限"。
	if normalizeScopeMode(cfg.Scope.NodeMode) == "" && normalizeScopeMode(cfg.Scope.AssetMode) == "" {
		cfg.Scope = s.cfg.Scope
	}
	cfg.LastChangedBy = changedBy
	cfg.LastChangedAt = s.now().UnixMilli()
	cfg.normalize()
	if err := writeInspectSchedule(s.path, cfg); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

// Status 返回调度现状。
func (s *InspectScheduler) Status() InspectScheduleStatus {
	if s == nil {
		return InspectScheduleStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := InspectScheduleStatus{Config: s.cfg}
	if s.cfg.LastRunAt > 0 {
		out.Last = &InspectScheduleRun{
			At: s.cfg.LastRunAt, RunID: s.cfg.LastRunID,
			Error: s.cfg.LastError, Manual: s.cfg.LastManual,
		}
		if s.cfg.Enabled {
			out.NextAt = s.cfg.LastRunAt + int64(s.cfg.IntervalHours)*int64(time.Hour/time.Millisecond)
		}
	}
	return out
}

// 执行前的拒绝原因。都是"本轮不跑，但原因要说清楚"的情形——不静默、也不产出
// 一份"0 个资产、0 条差异"的记录（那种记录会被读成"巡检过了，没问题"）。
var (
	ErrInspectScheduleBusy    = errors.New("上一次巡检尚未完成，本轮跳过")
	ErrInspectScheduleNoOwner = errors.New("配置没有记录范围来源身份，请重新保存配置")
	ErrInspectScheduleNoScope = errors.New("范围来源身份已不存在或已停用，请重新保存配置")
	ErrInspectScheduleEmpty   = errors.New("范围已收窄为空，本轮不执行；请调整范围或重新保存配置")
)

// RunNow 立即执行一次巡检。
//
// by 与 manual 一起决定"以谁的身份跑"：**实际范围 = 配置快照 ∩ 本次触发身份的当前范围**。
//   - 定时触发（manual=false）：身份是**配置保存者**（LastChangedBy），by 可传空；
//   - 手动触发（manual=true）：身份是**点击者**（by 传登录名）——必须算进去，否则一个范围很窄的人
//     点一下"立即执行"，就能拿到范围外资产的差异项（配置快照可能是管理员保存的"全部"）。
//
// 失败**不 panic、不静默**：原因写进返回结果与配置状态，同时记日志——否则"巡检怎么不跑了"
// 只能靠翻文件时间猜。
func (s *InspectScheduler) RunNow(by string, manual bool) InspectScheduleRun {
	at := s.now().UnixMilli()
	if s == nil || s.svc == nil {
		return InspectScheduleRun{At: at, Manual: manual, Error: "周期化巡检未启用"}
	}
	cfg := s.Config()
	actor := InspectScheduleActor
	if manual {
		actor = strings.TrimSpace(by)
		if actor == "" {
			actor = "anonymous"
		}
	}
	run := InspectScheduleRun{At: at, Manual: manual, Actor: actor}
	if !s.begin() {
		run.Error = ErrInspectScheduleBusy.Error()
		s.record(run)
		return run
	}
	defer s.end()

	filter, err := s.effectiveFilter(cfg, by, manual)
	if err != nil {
		run.Error = err.Error()
		slog.Warn("周期化巡检本轮未执行", "reason", run.Error, "actor", actor)
		s.record(run)
		return run
	}
	res, err := s.svc.RunInspect(InspectScope{Filter: filter, Fields: cfg.Fields}, actor)
	if err != nil {
		run.Error = err.Error()
		slog.Error("周期化巡检执行失败", "err", err, "actor", actor)
		s.record(run)
		return run
	}
	run.RunID, run.Assets, run.Findings, run.Truncated = res.ID, res.Assets, res.Findings, res.Truncated
	slog.Info("周期化巡检完成", "runId", res.ID, "assets", res.Assets, "findings", res.Findings,
		"truncated", res.Truncated, "actor", actor, "manual", manual)
	s.record(run)
	return run
}

// effectiveFilter 计算本次实际巡检的台账筛选 = 配置快照 ∩ 触发身份当前范围 + 配置里的筛选条件。
//
// 取交集是"降权立即生效"的实现：交集只朝收窄方向变，不会放大；解析不到身份则**拒绝执行**
// （fail-closed），而不是沿用快照继续跑。
func (s *InspectScheduler) effectiveFilter(cfg InspectScheduleConfig, by string, manual bool) (ListFilter, error) {
	who := strings.TrimSpace(cfg.LastChangedBy)
	if manual {
		who = strings.TrimSpace(by)
	}
	if who == "" {
		return ListFilter{}, ErrInspectScheduleNoOwner
	}
	current, ok := s.resolve(who)
	if !ok {
		return ListFilter{}, fmt.Errorf("%w（身份 %s）", ErrInspectScheduleNoScope, who)
	}
	scope := cfg.Scope.Intersect(current)
	if scope.IsEmpty() {
		return ListFilter{}, ErrInspectScheduleEmpty
	}
	filter := scope.Apply(ListFilter{})
	filter.TypeKey = strings.TrimSpace(cfg.Type)
	filter.Node = strings.TrimSpace(cfg.Node)
	filter.Keyword = strings.TrimSpace(cfg.Keyword)
	return filter, nil
}

// record 记录本轮结果并落盘。
//
// **失败也推进 LastRunAt**：否则每个 tick 都会重试一次、每次都记一条同样的错误（噪声），
// 而"到点该跑"这件事本轮已经尝试过——下次再试是下一个周期。
func (s *InspectScheduler) record(run InspectScheduleRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.LastRunAt = run.At
	s.cfg.LastManual = run.Manual
	s.cfg.LastError = run.Error
	if run.RunID > 0 {
		s.cfg.LastRunID = run.RunID
	}
	if err := writeInspectSchedule(s.path, s.cfg); err != nil {
		// 落盘失败只影响"跨重启的去重"，不影响本轮结果：记日志继续。
		slog.Warn("周期化巡检状态落盘失败（下次重启可能重复执行一轮）", "err", err)
	}
}

func (s *InspectScheduler) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *InspectScheduler) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = false
}

// Run 按周期执行，直到 ctx 取消。
//
// 启动时**不**立即执行（与报告调度同一条取舍）：巡检是重操作（遍历资产 + 写快照），
// 频繁重启的机器会在每次启动都触发一轮；是否到点由 LastRunAt（跨重启保留）判断。
func (s *InspectScheduler) Run(ctx context.Context) {
	if s == nil {
		return
	}
	ticker := time.NewTicker(inspectScheduleTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

// tick 检查是否到点，到点则执行一次。
func (s *InspectScheduler) tick() {
	cfg := s.Config()
	if !cfg.Enabled {
		return
	}
	interval := time.Duration(cfg.IntervalHours) * time.Hour
	if cfg.LastRunAt > 0 && s.now().Sub(time.UnixMilli(cfg.LastRunAt)) < interval {
		return
	}
	s.RunNow("", false)
}

func writeInspectSchedule(path string, cfg InspectScheduleConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化周期化巡检配置失败: %w", err)
	}
	if err := config.AtomicWrite(path, data); err != nil {
		return fmt.Errorf("保存周期化巡检配置失败: %w", err)
	}
	return nil
}

// normalizeScopeMode 校正范围模式。**缺省即受限**（与 auth.Scope 同取向：模式为空按"受限"处理），
// 未知取值同样按受限——方向永远朝 fail-closed，不朝"不限"。
func normalizeScopeMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), ScopeModeAll) {
		return ScopeModeAll
	}
	if strings.TrimSpace(mode) == "" {
		return ""
	}
	return ScopeModeLimited
}

// normalizeScopeSnapshot 把快照校正成规范形态：all ⇒ 清单为 nil（该维度不生效），
// limited ⇒ 清单非 nil（空即恒不匹配）。这个不变量是 Intersect 与 Apply 正确性的前提。
func normalizeScopeSnapshot(s ScopeSnapshot) ScopeSnapshot {
	s.NodeMode = normalizeScopeMode(s.NodeMode)
	if s.NodeMode == "" {
		s.NodeMode = ScopeModeLimited
	}
	if s.NodeMode == ScopeModeAll {
		s.Nodes = nil
	} else {
		s.Nodes = dedupStrings(s.Nodes)
	}
	s.AssetMode = normalizeScopeMode(s.AssetMode)
	if s.AssetMode == "" {
		s.AssetMode = ScopeModeLimited
	}
	if s.AssetMode == ScopeModeAll {
		s.Labels = nil
	} else {
		out := make([]ScopeLabel, 0, len(s.Labels))
		seen := make(map[string]bool, len(s.Labels))
		for _, l := range s.Labels {
			k := strings.TrimSpace(l.Key)
			v := strings.TrimSpace(l.Value)
			if k == "" || v == "" {
				continue // 半个选择器匹配不到任何东西，留着只会让范围看起来更大
			}
			key := k + "=" + v
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ScopeLabel{Key: k, Value: v})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Key == out[j].Key {
				return out[i].Value < out[j].Value
			}
			return out[i].Key < out[j].Key
		})
		s.Labels = out
	}
	return s
}

// intersectStrings 求两个节点清单的交集；nil 表示"该维度不限"，因此与 nil 的交集就是另一侧。
// 返回**非 nil**（可能是空切片）：空是可解释的状态（受限但没有任何节点），
// 返回 nil 会被下游读成"不限"，那正是要防的那一步。
func intersectStrings(a, b []string) []string {
	if a == nil {
		return append([]string{}, b...)
	}
	if b == nil {
		return append([]string{}, a...)
	}
	if len(a) == 0 || len(b) == 0 {
		return []string{}
	}
	set := make(map[string]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if set[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func intersectLabels(a, b []ScopeLabel) []ScopeLabel {
	if a == nil {
		return append([]ScopeLabel{}, b...)
	}
	if b == nil {
		return append([]ScopeLabel{}, a...)
	}
	if len(a) == 0 || len(b) == 0 {
		return []ScopeLabel{}
	}
	set := make(map[string]bool, len(b))
	for _, l := range b {
		set[l.Key+"="+l.Value] = true
	}
	out := make([]ScopeLabel, 0, len(a))
	for _, l := range a {
		if set[l.Key+"="+l.Value] {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key == out[j].Key {
			return out[i].Value < out[j].Value
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// dedupStrings 去重 + 去空白项 + 排序（让配置文件的 diff 稳定可读）。
func dedupStrings(in []string) []string {
	if in == nil {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
