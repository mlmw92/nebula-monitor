package alert

import (
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"gopkg.in/yaml.v3"
)

// GroupingConfig 告警分组配置：相同 groupBy 标签的告警合并为一组，
// 在 groupWait 内首次触发等待，之后每 groupInterval 汇总发送一次通知。
type GroupingConfig struct {
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	GroupBy       []string `yaml:"groupBy" json:"groupBy"`
	GroupWait     string   `yaml:"groupWait" json:"groupWait"`
	GroupInterval string   `yaml:"groupInterval" json:"groupInterval"`
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
	data, err := os.ReadFile(s.path)
	if err != nil {
		return GroupingConfig{Enabled: false, GroupBy: []string{"name"}, GroupWait: "30s", GroupInterval: "5m"}
	}
	var cfg GroupingConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("分组配置解析失败，使用默认", "err", err)
		return GroupingConfig{Enabled: false, GroupBy: []string{"name"}, GroupWait: "30s", GroupInterval: "5m"}
	}
	if len(cfg.GroupBy) == 0 {
		cfg.GroupBy = []string{"name"}
	}
	if cfg.GroupWait == "" {
		cfg.GroupWait = "30s"
	}
	if cfg.GroupInterval == "" {
		cfg.GroupInterval = "5m"
	}
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
	if len(cfg.GroupBy) == 0 {
		cfg.GroupBy = []string{"name"}
	}
	if cfg.GroupWait == "" {
		cfg.GroupWait = "30s"
	}
	if cfg.GroupInterval == "" {
		cfg.GroupInterval = "5m"
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// Grouper 将告警按 groupBy 标签分组，并在定时器触发时把一组告警汇总交给 flush 回调。
type Grouper struct {
	by       []string
	wait     time.Duration
	interval time.Duration
	mu       sync.Mutex
	groups   map[string]*groupBucket
	flush    func([]model.AlertEvent)
}

type groupBucket struct {
	pending []model.AlertEvent
	timer   *time.Timer
}

// NewGrouper 创建分组器。by 为分组标签；wait 为首次等待；interval 为后续汇总间隔。
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

// Add 将告警加入对应分组，必要时启动首次 flush 定时器（groupWait）。
func (g *Grouper) Add(ev model.AlertEvent) {
	key := g.key(ev)
	g.mu.Lock()
	b := g.groups[key]
	if b == nil {
		b = &groupBucket{}
		g.groups[key] = b
		b.timer = time.AfterFunc(g.wait, func() { g.flushKey(key) })
	}
	b.pending = append(b.pending, ev)
	g.mu.Unlock()
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
	b.timer = time.AfterFunc(g.interval, func() { g.flushKey(key) })
	g.mu.Unlock()
	if g.flush != nil {
		g.flush(pending)
	}
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
