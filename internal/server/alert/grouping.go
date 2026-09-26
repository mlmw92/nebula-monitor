package alert

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"gopkg.in/yaml.v3"
)

// GroupingConfig 告警分组配置：相同 groupBy 标签的告警合并为一组，
// 在 groupWait 内首次触发等待，之后每 groupInterval 汇总发送一次通知。
//
// D1 风暴收敛（converge 系列字段）：分组解决「什么时候发、发给谁」，
// 收敛解决「一条通知里放什么」——同规则多节点同时告警时，把上百条明细
// 收敛为「头部告警 + 摘要 + Top N 明细」，避免通知正文被淹没。
type GroupingConfig struct {
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	GroupBy       []string `yaml:"groupBy" json:"groupBy"`
	GroupWait     string   `yaml:"groupWait" json:"groupWait"`
	GroupInterval string   `yaml:"groupInterval" json:"groupInterval"`

	// Converge 开启风暴收敛。**默认开启**（开启分组即默认收敛）。
	//
	// 用指针而非 bool 表达三态：字段缺失（nil）= 默认开启，显式写 `converge: false` 才关闭。
	// 普通 bool 无法区分「未配置」与「显式关闭」，会把用户写下的 false 悄悄翻回默认值。
	Converge *bool `yaml:"converge" json:"converge"`
	// ConvergeBy 收敛维度，默认 ["rule","severity"]——刻意不含 node/instance，
	// 这正是「同规则多节点风暴」能合并成一条的原因。
	ConvergeBy []string `yaml:"convergeBy" json:"convergeBy"`
	// ConvergeWindow 聚类时间窗：同一「代」内的告警合并，超出则开启新一代。
	ConvergeWindow string `yaml:"convergeWindow" json:"convergeWindow"`
	// HeadCount 通知内列举的明细条数上限，其余折叠为计数。
	HeadCount int `yaml:"headCount" json:"headCount"`
}

// 收敛默认值与上限。
const (
	defaultConvergeWindow = "10m"
	defaultHeadCount      = 5
	maxHeadCount          = 20
)

// defaultConvergeBy 收敛维度默认值（同规则 + 同级别）。
var defaultConvergeBy = []string{"rule", "severity"}

// ConvergeEnabled 返回收敛是否生效：未配置视为开启（开启分组即默认收敛）。
// 调用方一律使用本方法，不要直接读指针，以免把 nil 当成关闭。
func (c GroupingConfig) ConvergeEnabled() bool {
	return c.Converge == nil || *c.Converge
}

// DefaultGroupingConfig 返回规整后的默认分组配置（分组本身默认关闭，但字段齐全）。
func DefaultGroupingConfig() GroupingConfig {
	var cfg GroupingConfig
	cfg.normalize()
	return cfg
}

// normalize 补齐默认值并收敛到合法区间。load 与 Save 共用，
// 保证「热更新生效的配置」与「落盘内容」始终一致。
func (c *GroupingConfig) normalize() {
	if len(c.GroupBy) == 0 {
		c.GroupBy = []string{"name"}
	}
	if c.GroupWait == "" {
		c.GroupWait = "30s"
	}
	if c.GroupInterval == "" {
		c.GroupInterval = "5m"
	}
	if c.Converge == nil {
		// 物化为显式值：接口返回与落盘文件都能看到真实生效的开关
		v := true
		c.Converge = &v
	}
	if len(c.ConvergeBy) == 0 {
		c.ConvergeBy = append([]string(nil), defaultConvergeBy...)
	}
	if c.ConvergeWindow == "" {
		c.ConvergeWindow = defaultConvergeWindow
	}
	if c.HeadCount <= 0 {
		c.HeadCount = defaultHeadCount
	}
	if c.HeadCount > maxHeadCount {
		c.HeadCount = maxHeadCount
	}
}

// GroupingStore 分组配置存储，支持热更新。
type GroupingStore struct {
	mu   sync.RWMutex
	cfg  GroupingConfig
	path string
}

// NewGroupingStore 创建分组配置存储并加载已有配置。
func NewGroupingStore(path string) *GroupingStore {
	s := &GroupingStore{path: path}
	s.cfg = s.load()
	return s
}

func (s *GroupingStore) load() GroupingConfig {
	var cfg GroupingConfig
	data, err := os.ReadFile(s.path)
	if err != nil {
		cfg.normalize()
		return cfg
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("分组配置解析失败，使用默认", "err", err)
		cfg = GroupingConfig{}
	}
	cfg.normalize()
	return cfg
}

// Get 返回当前分组配置。
func (s *GroupingStore) Get() GroupingConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Save 持久化并热更新分组配置。
func (s *GroupingStore) Save(cfg GroupingConfig) error {
	cfg.normalize()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(s.path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// newGrouper 按分组配置构建分组器：解析时间参数、设置收敛参数、绑定 flush 回调。
// NewEngine 与 SetGrouping 共用，避免两处默认值与收敛参数漂移。
func (e *Engine) newGrouper(cfg GroupingConfig) *Grouper {
	wait, err1 := time.ParseDuration(cfg.GroupWait)
	interval, err2 := time.ParseDuration(cfg.GroupInterval)
	if err1 != nil || err2 != nil {
		slog.Warn("分组配置时间解析失败，使用默认", "groupWait", cfg.GroupWait, "groupInterval", cfg.GroupInterval)
		wait, interval = 30*time.Second, 5*time.Minute
	}
	g := NewGrouper(cfg.GroupBy, wait, interval, nil)
	g.SetConverge(cfg.ConvergeEnabled(), cfg.ConvergeBy, parseConvergeWindow(cfg.ConvergeWindow))
	g.flush = func(events []model.AlertEvent) { e.flushGroup(events) }
	return g
}

// parseConvergeWindow 解析收敛时间窗；非法值回落默认并告警。
func parseConvergeWindow(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		if s != "" {
			slog.Warn("收敛时间窗解析失败，使用默认", "convergeWindow", s, "fallback", defaultConvergeWindow)
		}
		return 10 * time.Minute
	}
	return d
}

// Grouper 将告警按 groupBy 标签分组，并在定时器触发时把一组告警汇总交给 flush 回调。
//
// D1 收敛（converge）：在 groupBy 之上再按 convergeBy 聚类，使「同规则、不同节点」的
// 风暴合并进同一组；再用 window 限制单组的存活时长（代），保证一次通知的内容有界。
type Grouper struct {
	by         []string
	converge   bool
	convergeBy []string
	window     time.Duration
	wait       time.Duration
	interval   time.Duration
	mu         sync.Mutex
	groups     map[string]*groupBucket
	flush      func([]model.AlertEvent)
}

type groupBucket struct {
	pending []model.AlertEvent
	timer   *time.Timer
	firstAt time.Time // 本代首条告警到达时间，用于判定收敛时间窗是否已过
}

// NewGrouper 创建分组器。by 为分组标签；wait 为首次等待；interval 为后续汇总间隔。
// 收敛参数经 SetConverge 设置；未设置即不收敛，行为与改造前完全一致。
func NewGrouper(by []string, wait, interval time.Duration, flush func([]model.AlertEvent)) *Grouper {
	if len(by) == 0 {
		by = []string{"name"}
	}
	if wait <= 0 {
		wait = 30 * time.Second
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &Grouper{by: by, wait: wait, interval: interval, groups: map[string]*groupBucket{}, flush: flush}
}

// SetConverge 设置收敛参数。by 为空时回落默认收敛维度。
func (g *Grouper) SetConverge(enabled bool, by []string, window time.Duration) {
	if len(by) == 0 {
		by = defaultConvergeBy
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.converge = enabled
	g.convergeBy = append([]string(nil), by...)
	g.window = window
}

func groupLabel(ev model.AlertEvent, key string) string {
	switch key {
	case "name":
		return ev.RuleName
	case "rule":
		return ev.RuleID
	case "host", "node":
		return ev.Node
	case "instance":
		return ev.Instance
	case "severity":
		return string(ev.Severity)
	case "metric":
		return ev.Metric
	default:
		return ""
	}
}

func (g *Grouper) key(ev model.AlertEvent) string {
	var b strings.Builder
	for _, k := range g.by {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(groupLabel(ev, k))
		b.WriteString("|")
	}
	return b.String()
}

// clusterKey 返回聚类键。未开启收敛时与 key 完全一致（零行为变化）。
//
// 关键语义：**收敛开启时以 convergeBy 为准，而不是叠加在 groupBy 之上**。
// 若叠加，groupBy 含 host/instance（按主机分别通知的常见配置）时键只会更细，
// 收敛永远无法跨节点合并——正好与「风暴收敛」的目标相反。
// 因此开启收敛意味着「收敛维度取代分组维度」，这一点在配置说明与前端均已写明。
func (g *Grouper) clusterKey(ev model.AlertEvent) string {
	if !g.converge || len(g.convergeBy) == 0 {
		return g.key(ev)
	}
	var b strings.Builder
	b.WriteString("~converge~")
	for _, k := range g.convergeBy {
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(groupLabel(ev, k))
		b.WriteString("|")
	}
	return b.String()
}

// Add 将告警加入对应分组，必要时启动首次 flush 定时器（groupWait）。
// 收敛开启且该组当前「代」已超出时间窗时，先把旧代发出（异步）再开启新一代，
// 从而限制单条通知的规模，也避免把几小时前的告警与新风暴混在一起。
func (g *Grouper) Add(ev model.AlertEvent) {
	key := g.clusterKey(ev)
	now := time.Now()

	g.mu.Lock()
	b := g.groups[key]
	if b != nil && g.window > 0 && now.Sub(b.firstAt) > g.window {
		stale := b.pending
		if b.timer != nil {
			b.timer.Stop()
		}
		b.pending = nil
		b.firstAt = now
		b.timer = time.AfterFunc(g.wait, func() { g.flushKey(key) })
		g.mu.Unlock()
		// Add 由 fire() 在持有 engine 锁的调用栈内调用，同步回调 flushGroup 会自锁，故异步派发。
		go g.emit(stale)
		return
	}
	if b == nil {
		b = &groupBucket{firstAt: now}
		g.groups[key] = b
		b.timer = time.AfterFunc(g.wait, func() { g.flushKey(key) })
	}
	b.pending = append(b.pending, ev)
	g.mu.Unlock()
}

// emit 把一组告警交给 flush 回调；空集合与未设置回调时直接返回。
func (g *Grouper) emit(events []model.AlertEvent) {
	if len(events) == 0 || g.flush == nil {
		return
	}
	g.flush(events)
}

func (g *Grouper) flushKey(key string) {
	g.mu.Lock()
	b := g.groups[key]
	if b == nil {
		g.mu.Unlock()
		return
	}
	pending := b.pending
	b.pending = nil
	if len(pending) == 0 {
		delete(g.groups, key)
		g.mu.Unlock()
		return
	}
	// 保持分组活跃：安排下一次汇总
	b.firstAt = time.Now()
	b.timer = time.AfterFunc(g.interval, func() { g.flushKey(key) })
	g.mu.Unlock()
	g.emit(pending)
}

// Stop 停止分组器并释放定时器。
func (g *Grouper) Stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, b := range g.groups {
		if b.timer != nil {
			b.timer.Stop()
		}
	}
	g.groups = map[string]*groupBucket{}
}
