package alert

import (
	"context"
	"fmt"
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
	maintenance  *MaintenanceStore
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
	alerts *VMAlertStore, notifiers []Notifier, broadcaster Broadcaster, maintenance *MaintenanceStore, evalInterval int) *Engine {
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
		maintenance:  maintenance,
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
		// 静默期跳过：Silenced 为 true 且未到期则跳过评估
		if r.Silenced {
			if r.SilenceUntil == 0 || now < r.SilenceUntil {
				continue
			}
			// 到期自动解除静默
			r.Silenced = false
			_ = e.rules.Update(r)
		}
		for _, n := range nodes {
			if !matchesGroup(r.Group, n.Group) {
				continue
			}
			if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
				continue
			}
			for _, sample := range e.latestMetricSamples(n.Hostname, r.Metric) {
				key := r.ID + "|" + n.Hostname + "|" + sample.instance
				st := e.states[key]
				if st == nil {
					st = &ruleState{}
					e.states[key] = st
				}
				cond := Compare(r.Operator, sample.value, r.Threshold)
				if cond {
					if st.aboveSince == 0 {
						st.aboveSince = now
					}
					forSec := parseFor(r.For)
					if !st.firing && now-st.aboveSince >= forSec*1000 {
						st.firing = true
						e.fire(r, n.Hostname, sample.instance, sample.value, now)
					}
				} else {
					if st.firing {
						st.firing = false
						e.resolve(r, n.Hostname, sample.instance, sample.value, now)
					}
					st.aboveSince = 0
				}
			}
		}
	}
}

func (e *Engine) fire(r model.AlertRule, node, instance string, value float64, now int64) {
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    r.ID,
		RuleName:  r.Name,
		Node:      node,
		NodeIP:    e.nodeIP(node),
		Instance:  instance,
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
	slog.Info("告警触发", "rule", r.Name, "node", node, "metric", r.Metric,
		"value", value, "operator", r.Operator, "threshold", r.Threshold,
		"severity", r.Severity, "channels", r.Notify)
}

func (e *Engine) resolve(r model.AlertRule, node, instance string, value float64, now int64) {
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    r.ID,
		RuleName:  r.Name,
		Node:      node,
		NodeIP:    e.nodeIP(node),
		Instance:  instance,
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
	slog.Info("告警恢复", "rule", r.Name, "node", node, "metric", r.Metric, "value", value)
}

// nodeIP 查询节点 IP（通知渠道在邮件等中展示）；找不到返回空串。
func (e *Engine) nodeIP(node string) string {
	if n, ok := e.nodeMgr.GetNode(node); ok {
		return n.IP
	}
	return ""
}

// TestAlert 构造一条测试告警事件，绕过评估链路直接写入并通知，便于用户验证事件/通知链路。
// channel 非空时仅触发指定渠道；为空时按当前 notifiers 全渠道发送。
func (e *Engine) TestAlert(channel string) (model.AlertEvent, error) {
	now := model.NowMillis()
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    "test-rule",
		RuleName:  "测试告警",
		Node:      "manual",
		Metric:    "test",
		Value:     0,
		Operator:  ">=",
		Threshold: 0,
		Severity:  model.SeverityInfo,
		State:     model.AlertStateFiring,
		Message:   "这是一条由用户手动触发的测试告警事件，用于验证事件链路与通知渠道",
		StartsAt:  now,
	}
	e.alerts.Add(ev)
	e.mu.Lock()
	ns := append([]Notifier(nil), e.notifiers...)
	e.mu.Unlock()
	for _, n := range ns {
		if channel != "" && n.Channel() != channel {
			continue
		}
		if err := n.Notify(ev); err != nil {
			slog.Warn("测试告警通知失败", "channel", n.Channel(), "err", err)
			return ev, fmt.Errorf("渠道 %s 通知失败: %w", n.Channel(), err)
		}
	}
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
	slog.Info("手动触发测试告警", "channel", channel, "eventID", ev.ID)
	return ev, nil
}

// TestEmail 主动调用邮件渠道发送一封测试邮件，立即返回 SMTP 错误详情。
// 不写入告警事件，避免污染事件列表；仅在邮件渠道存在时返回成功。
func (e *Engine) TestEmail() error {
	now := model.NowMillis()
	ev := model.AlertEvent{
		ID:        genEventID(),
		RuleID:    "test-rule",
		RuleName:  "测试邮件",
		Node:      "manual",
		Metric:    "test",
		Value:     0,
		Operator:  ">=",
		Threshold: 0,
		Severity:  model.SeverityInfo,
		State:     model.AlertStateFiring,
		Message:   "这是一封由用户手动触发的测试邮件，用于验证 SMTP 配置与链路",
		StartsAt:  now,
	}
	e.mu.Lock()
	ns := append([]Notifier(nil), e.notifiers...)
	e.mu.Unlock()
	for _, n := range ns {
		if n.Channel() == "email" {
			return n.Notify(ev)
		}
	}
	return fmt.Errorf("邮件渠道未启用或未配置（请先在「通知配置」中开启并保存）")
}

// notify 按规则配置的渠道发送通知。维护窗口活跃时跳过通知（事件仍记录）。
func (e *Engine) notify(ev model.AlertEvent) {
	// 维护窗口检查：活跃时跳过通知发送
	if e.maintenance != nil && e.maintenance.IsActive(model.NowMillis()) {
		slog.Info("维护窗口活跃，跳过通知", "rule", ev.RuleName, "event", ev.ID)
		return
	}
	chs := e.ruleNotifyChannels(ev.RuleID)
	// 规则未指定渠道时不发送任何通知
	if len(chs) == 0 {
		return
	}
	for _, n := range e.notifiers {
		if !contains(chs, n.Channel()) {
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

// ruleNotifyChannels 返回规则指定的通知渠道；空表示不发送任何通知。
func (e *Engine) ruleNotifyChannels(ruleID string) []string {
	if r, ok := e.rules.Get(ruleID); ok {
		return r.Notify
	}
	return nil
}

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

type metricSample struct {
	instance string
	value    float64
}

// latestMetricSamples 返回节点某指标的全部最新时序样本。
// 普通主机指标通常只有一个无 instance 标签的样本；Redis 等多实例指标按 instance 分别评估。
// 对于 disk_used_percent，保持全部真实磁盘汇总的既有语义。
func (e *Engine) latestMetricSamples(node, metric string) []metricSample {
	if metric == "disk_used_percent" {
		if value, ok := aggregatedDiskUsage(e.store, node); ok {
			return []metricSample{{value: value}}
		}
		return nil
	}
	series, err := e.store.QueryInstant(node, metric, nil)
	if err != nil {
		return nil
	}
	samples := make([]metricSample, 0, len(series))
	for _, s := range series {
		if len(s.Points) == 0 {
			continue
		}
		samples = append(samples, metricSample{
			instance: s.Labels["instance"],
			value:    s.Points[len(s.Points)-1].Value,
		})
	}
	return samples
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
	return round2(used / total * 100), true
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}
