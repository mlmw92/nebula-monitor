package alert

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/storage"
)

// Broadcaster 告警广播接口（由 api.Hub 实现，避免循环依赖）。
type Broadcaster interface {
	BroadcastAlert(e model.AlertEvent)
}

// Engine 阈值告警评估引擎。
type Engine struct {
	store        storage.Storage
	nodeMgr      *node.Manager
	rules        *RulesStore
	alerts       *VMAlertStore
	notifiers    []Notifier
	broadcaster  Broadcaster
	evalInterval int
	mu           sync.Mutex
	states       map[string]*ruleState
}

type ruleState struct {
	aboveSince int64
	firing     bool
}

// NewEngine 创建引擎。
func NewEngine(store storage.Storage, mgr *node.Manager, rules *RulesStore,
	alerts *VMAlertStore, notifiers []Notifier, broadcaster Broadcaster, evalInterval int) *Engine {
	if evalInterval <= 0 {
		evalInterval = 15
	}
	return &Engine{
		store:        store,
		nodeMgr:      mgr,
		rules:        rules,
		alerts:       alerts,
		notifiers:    notifiers,
		broadcaster:  broadcaster,
		evalInterval: evalInterval,
		states:       map[string]*ruleState{},
	}
}

// Start 启动评估循环。
func (e *Engine) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Duration(e.evalInterval) * time.Second)
		defer ticker.Stop()
		e.evaluate()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.evaluate()
			}
		}
	}()
}

func (e *Engine) evaluate() {
	rules := e.rules.List()
	if len(rules) == 0 {
		return
	}
	nodes := e.nodeMgr.ListNodes()
	now := model.NowMillis()

	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, n := range nodes {
			if !matchesGroup(r.Group, n.Group) {
				continue
			}
			p, ok := e.latestMetricValue(n.Hostname, r.Metric)
			if !ok {
				continue
			}
			key := r.ID + "|" + n.Hostname
			st := e.states[key]
			if st == nil {
				st = &ruleState{}
				e.states[key] = st
			}
			cond := Compare(r.Operator, p, r.Threshold)
			if cond {
				if st.aboveSince == 0 {
					st.aboveSince = now
				}
				forSec := parseFor(r.For)
				if !st.firing && now-st.aboveSince >= forSec*1000 {
					st.firing = true
					e.fire(r, n.Hostname, p, now)
				}
			} else {
				if st.firing {
					st.firing = false
					e.resolve(r, n.Hostname, p, now)
				}
				st.aboveSince = 0
			}
		}
	}
}

func (e *Engine) fire(r model.AlertRule, node string, value float64, now int64) {
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    r.ID,
		RuleName:  r.Name,
		Node:      node,
		Metric:    r.Metric,
		Value:     value,
		Operator:  r.Operator,
		Threshold: r.Threshold,
		Severity:  r.Severity,
		State:     model.AlertStateFiring,
		Message:   triggerMessage(r, node, value),
		StartsAt:  now,
	}
	e.alerts.Add(ev)
	e.notify(ev)
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
}

func (e *Engine) resolve(r model.AlertRule, node string, value float64, now int64) {
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    r.ID,
		RuleName:  r.Name,
		Node:      node,
		Metric:    r.Metric,
		Value:     value,
		Operator:  r.Operator,
		Threshold: r.Threshold,
		Severity:  r.Severity,
		State:     model.AlertStateResolved,
		Message:   "节点 " + node + " 指标 " + r.Metric + " 已恢复（规则：" + r.Name + "）",
		EndsAt:    now,
	}
	e.alerts.Add(ev)
	e.notify(ev)
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
}

// notify 按规则配置的渠道发送通知。
func (e *Engine) notify(ev model.AlertEvent) {
	for _, n := range e.notifiers {
		// 规则未指定渠道时默认全部发送；指定时仅发送给对应渠道
		if len(ruleNotifyChannels(ev.RuleID)) > 0 && !contains(ruleNotifyChannels(ev.RuleID), n.Channel()) {
			continue
		}
		if err := n.Notify(ev); err != nil {
			slog.Warn("通知发送失败", "channel", n.Channel(), "err", err)
		}
	}
}

// SetNotifiers 热加载通知器列表。在 e.mu 锁内替换，与 evaluate/notify 共用同一把锁，
// 避免并发读写 notifiers 切片导致竞态；保存配置后调用，无需重启 Server。
func (e *Engine) SetNotifiers(ns []Notifier) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifiers = ns
}

// ruleNotifyChannels 缓存规则渠道（避免每次查规则）。简单实现：返回空（全发）。
func ruleNotifyChannels(ruleID string) []string { return nil }

func contains(s []string, v string) bool {
	sort.Strings(s)
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func genEventID() string {
	return "e-" + strconv.FormatUint(uint64(time.Now().UnixNano()), 36) + "-" + strconv.FormatInt(rand.Int63(), 36)
}

// latestMetricValue 返回节点某指标的最新值。
// 对于 disk_used_percent，使用全部真实磁盘的汇总使用率（sum(disk_used)/sum(disk_total)），
// 避免只取单个挂载点（可能是随机或虚拟挂载）导致的漏报/误报，与前端展示保持一致。
func (e *Engine) latestMetricValue(node, metric string) (float64, bool) {
	if metric == "disk_used_percent" {
		return aggregatedDiskUsage(e.store, node)
	}
	p, err := e.store.QueryLatest(node, metric, nil)
	if err != nil || p == nil {
		return 0, false
	}
	return p.Value, true
}

// aggregatedDiskUsage 汇总节点全部真实磁盘的总使用率（百分比）。
func aggregatedDiskUsage(store storage.Storage, node string) (float64, bool) {
	totalSeries, err := store.QueryInstant(node, "disk_total", nil)
	if err != nil || len(totalSeries) == 0 {
		return 0, false
	}
	usedSeries, err := store.QueryInstant(node, "disk_used", nil)
	if err != nil || len(usedSeries) == 0 {
		return 0, false
	}
	var total, used float64
	for _, s := range totalSeries {
		if len(s.Points) > 0 {
			total += s.Points[len(s.Points)-1].Value
		}
	}
	for _, s := range usedSeries {
		if len(s.Points) > 0 {
			used += s.Points[len(s.Points)-1].Value
		}
	}
	if total <= 0 {
		return 0, false
	}
	return round2(used/total*100), true
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
