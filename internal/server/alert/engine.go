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
	"github.com/nebula/monitor/internal/server/dialtest"
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
	dialActive   map[string]bool           // 拨测任务是否有未恢复故障事件，按 "dialtest:<taskID>" 记录
	firing       map[string]*firingEntry   // 活跃 firing 事件（用于抑制匹配），按 rule|node|instance 记录
	inhibit      *InhibitStore             // 抑制规则（可选）
	grouping     *GroupingStore            // 分组配置（可选）
	grouper      *Grouper                  // 分组器（分组启用时非空）
}

type ruleState struct {
	aboveSince int64
	firing     bool
}

// firingEntry 记录一个活跃 firing 事件及其是否已被抑制。
type firingEntry struct {
	event     model.AlertEvent
	suppressed bool
}

// NewEngine 创建引擎。inhibit/grouping 为可选的高级能力（抑制/分组），为空则关闭。
func NewEngine(store storage.Storage, mgr *node.Manager, rules *RulesStore,
	alerts *VMAlertStore, notifiers []Notifier, broadcaster Broadcaster, maintenance *MaintenanceStore,
	evalInterval int, inhibit *InhibitStore, grouping *GroupingStore) *Engine {
	if evalInterval <= 0 {
		evalInterval = 15
	}
	e := &Engine{
		store:        store,
		nodeMgr:      mgr,
		rules:        rules,
		alerts:       alerts,
		notifiers:    notifiers,
		broadcaster:  broadcaster,
		maintenance:  maintenance,
		evalInterval: evalInterval,
		states:       map[string]*ruleState{},
		dialActive:   map[string]bool{},
		firing:       map[string]*firingEntry{},
		inhibit:      inhibit,
		grouping:     grouping,
	}
	// 分组启用时构建分组器：相同 groupBy 的告警合并为一组，按 groupWait/groupInterval 汇总发送。
	if grouping != nil && grouping.Get().Enabled {
		cfg := grouping.Get()
		wait, err1 := time.ParseDuration(cfg.GroupWait)
		interval, err2 := time.ParseDuration(cfg.GroupInterval)
		if err1 != nil || err2 != nil {
			slog.Warn("分组配置时间解析失败，使用默认", "groupWait", cfg.GroupWait, "groupInterval", cfg.GroupInterval)
			wait, interval = 30*time.Second, 5*time.Minute
		}
		g := NewGrouper(cfg.GroupBy, wait, interval, nil)
		g.flush = func(events []model.AlertEvent) { e.flushGroup(events) }
		e.grouper = g
	}
	return e
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
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
	// 抑制：被更高优先级活跃告警压制时不发送通知（事件仍记录并在前端标记）。
	suppressed, by := e.computeSuppressedLocked(ev)
	ev.Suppressed = suppressed
	ev.SuppressedBy = by
	e.trackFiringLocked(ev, suppressed)
	if !suppressed {
		if e.grouper != nil {
			e.grouper.Add(ev)
		} else {
			e.notify(ev)
		}
	}
	slog.Info("告警触发", "rule", r.Name, "node", node, "metric", r.Metric,
		"value", value, "operator", r.Operator, "threshold", r.Threshold,
		"severity", r.Severity, "suppressed", suppressed, "channels", r.Notify)
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
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
	// 仅当原 firing 未被抑制时才发送恢复通知；随后重新评估被其抑制的其它告警。
	wasSuppressed := e.untrackFiringLocked(ev)
	if !wasSuppressed {
		if e.grouper != nil {
			e.grouper.Add(ev)
		} else {
			e.notify(ev)
		}
	}
	e.recheckSuppressedLocked()
	slog.Info("告警恢复", "rule", r.Name, "node", node, "metric", r.Metric, "value", value)
}

// EmitDialtestAlert 由拨测调度器在状态跃迁时调用，产生拨测类告警事件并联动通知。
// up=false 表示拨测故障触发，up=true 表示恢复。事件进入统一告警中心（与阈值告警共用存储与广播）。
func (e *Engine) EmitDialtestAlert(task dialtest.Task, result dialtest.Result, up bool) {
	sev := model.Severity(task.Severity)
	if sev != model.SeverityCritical && sev != model.SeverityWarning && sev != model.SeverityInfo {
		sev = model.SeverityWarning
	}
	key := "dialtest:" + task.ID
	now := model.NowMillis()

	if !up {
		// 故障触发
		e.mu.Lock()
		active, wasActive := e.dialActive[key]
		// 已有未恢复事件则忽略重复故障，避免刷屏。
		if wasActive && active {
			e.mu.Unlock()
			return
		}
		e.dialActive[key] = true
		ev := model.AlertEvent{
			ID:        "dial-" + task.ID + "-" + strconv.FormatInt(now, 36),
			RuleID:    "dialtest-" + task.ID,
			RuleName:  "拨测 - " + task.Name,
			Node:      task.Target,
			Metric:    "dial_test_up",
			Value:     0,
			Operator:  "==",
			Threshold: 1,
			Severity:  sev,
			State:     model.AlertStateFiring,
			Message:   dialMessage(task, result),
			StartsAt:  now,
		}
		// 抑制：被更高优先级活跃告警压制时不发送通知。
		suppressed, by := e.computeSuppressedLocked(ev)
		ev.Suppressed = suppressed
		ev.SuppressedBy = by
		e.trackFiringLocked(ev, suppressed)
		e.mu.Unlock()

		e.alerts.Add(ev)
		if e.broadcaster != nil {
			e.broadcaster.BroadcastAlert(ev)
		}
		if !ev.Suppressed {
			e.notifyDialtest(task, ev)
		}
		slog.Info("拨测告警触发", "task", task.Name, "target", task.Target, "severity", sev, "suppressed", ev.Suppressed, "event", ev.ID)
		return
	}

	// 恢复
	e.mu.Lock()
	_, wasActive := e.dialActive[key]
	// 没有未恢复事件（如启动后首次检测即正常）则忽略恢复。
	if !wasActive {
		e.mu.Unlock()
		return
	}
	delete(e.dialActive, key)
	ev := model.AlertEvent{
		ID:        "dialr-" + task.ID + "-" + strconv.FormatInt(now, 36),
		RuleID:    "dialtest-" + task.ID,
		RuleName:  "拨测 - " + task.Name,
		Node:      task.Target,
		Metric:    "dial_test_up",
		Value:     1,
		Operator:  "==",
		Threshold: 1,
		Severity:  sev,
		State:     model.AlertStateResolved,
		Message:   fmt.Sprintf("拨测恢复：%s (%s %s) 已恢复正常", task.Name, task.Type, task.Target),
		EndsAt:    now,
	}
	wasSuppressed := e.untrackFiringLocked(ev)
	e.mu.Unlock()

	e.alerts.Add(ev)
	if e.broadcaster != nil {
		e.broadcaster.BroadcastAlert(ev)
	}
	if !wasSuppressed {
		e.notifyDialtest(task, ev)
	}
	e.recheckSuppressed()
	slog.Info("拨测告警恢复", "task", task.Name, "target", task.Target, "severity", sev, "event", ev.ID)
}

// notifyDialtest 发送拨测告警通知：维护窗口活跃时跳过；task.Notify 为空表示仅平台展示，不推送任何外部渠道。
func (e *Engine) notifyDialtest(task dialtest.Task, ev model.AlertEvent) {
	if e.maintenance != nil && e.maintenance.IsActive(model.NowMillis()) {
		slog.Info("维护窗口活跃，跳过拨测告警通知", "task", task.Name, "event", ev.ID)
		return
	}
	// 未选择任何通知渠道：事件仅记录在平台，不推送。
	if len(task.Notify) == 0 {
		slog.Info("拨测未配置通知渠道，仅平台展示", "task", task.Name, "event", ev.ID)
		return
	}
	e.mu.Lock()
	ns := append([]Notifier(nil), e.notifiers...)
	e.mu.Unlock()
	for _, n := range ns {
		if !contains(task.Notify, n.Channel()) {
			continue
		}
		if err := n.Notify(ev); err != nil {
			slog.Warn("拨测告警通知失败", "channel", n.Channel(), "err", err)
		}
	}
}

// dialMessage 构造拨测故障的描述信息。
func dialMessage(task dialtest.Task, result dialtest.Result) string {
	base := fmt.Sprintf("拨测失败：%s (%s %s)", task.Name, task.Type, task.Target)
	if result.Error != "" {
		return base + " - " + result.Error
	}
	return base
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
// 规则未指定渠道（Notify 为空）时发给全部已启用渠道（与 model 文档语义一致）。
func (e *Engine) notify(ev model.AlertEvent) {
	// 维护窗口检查：活跃时跳过通知发送
	if e.maintenance != nil && e.maintenance.IsActive(model.NowMillis()) {
		slog.Info("维护窗口活跃，跳过通知", "rule", ev.RuleName, "event", ev.ID)
		return
	}
	chs := e.ruleNotifyChannels(ev.RuleID)
	if len(chs) == 0 {
		// 规则未指定渠道：发给全部已启用渠道
		chs = e.allChannels()
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

// allChannels 返回当前所有已启用渠道名。
func (e *Engine) allChannels() []string {
	chs := make([]string, 0, len(e.notifiers))
	for _, n := range e.notifiers {
		chs = append(chs, n.Channel())
	}
	return chs
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

// firingKey 活跃告警的标识键（规则 + 节点 + 实例），用于抑制跟踪与去重。
func firingKey(ev model.AlertEvent) string {
	return ev.RuleID + "|" + ev.Node + "|" + ev.Instance
}

// alertLabels 提取用于抑制匹配的标签集合。
func alertLabels(ev model.AlertEvent) map[string]string {
	return map[string]string{
		"name":     ev.RuleName,
		"rule":     ev.RuleID,
		"host":     ev.Node,
		"node":     ev.Node,
		"instance": ev.Instance,
		"severity": string(ev.Severity),
		"metric":   ev.Metric,
	}
}

// equalLabels 校验两标签集在 keys 指定的键上取值相等；keys 为空则视为满足。
func equalLabels(a map[string]string, keys []string, b map[string]string) bool {
	for _, k := range keys {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

// computeSuppressedLocked 在持有 e.mu 时判断 ev 是否被某条活跃 source 告警抑制。调用方须持有锁。
func (e *Engine) computeSuppressedLocked(ev model.AlertEvent) (bool, string) {
	if e.inhibit == nil {
		return false, ""
	}
	rules := e.inhibit.List()
	if len(rules) == 0 {
		return false, ""
	}
	evLabels := alertLabels(ev)
	selfKey := firingKey(ev)
	for _, r := range rules {
		if !r.Target.matches(evLabels) {
			continue
		}
		for key, fe := range e.firing {
			if key == selfKey {
				continue
			}
			if fe.event.State != model.AlertStateFiring {
				continue
			}
			srcLabels := alertLabels(fe.event)
			if !r.Source.matches(srcLabels) {
				continue
			}
			if !equalLabels(evLabels, r.Equal, srcLabels) {
				continue
			}
			return true, fe.event.RuleName
		}
	}
	return false, ""
}

func (e *Engine) trackFiringLocked(ev model.AlertEvent, suppressed bool) {
	e.firing[firingKey(ev)] = &firingEntry{event: ev, suppressed: suppressed}
}

func (e *Engine) untrackFiringLocked(ev model.AlertEvent) bool {
	key := firingKey(ev)
	fe, ok := e.firing[key]
	if !ok {
		return false
	}
	delete(e.firing, key)
	return fe.suppressed
}

// recheckSuppressed 在 source 告警离开后，重新评估此前被其抑制的告警；若不再被抑制则补发通知。
// 本函数自行加锁，适用于已释放 e.mu 的调用方（如拨测恢复路径）。
func (e *Engine) recheckSuppressed() {
	e.mu.Lock()
	e.recheckSuppressedLocked()
	e.mu.Unlock()
}

// recheckSuppressedLocked 与 recheckSuppressed 同义，但要求调用方已持有 e.mu（evaluate 评估循环内调用）。
func (e *Engine) recheckSuppressedLocked() {
	var toSend []model.AlertEvent
	for _, fe := range e.firing {
		if fe.event.State != model.AlertStateFiring || !fe.suppressed {
			continue
		}
		supp, _ := e.computeSuppressedLocked(fe.event)
		if !supp {
			fe.suppressed = false
			toSend = append(toSend, fe.event)
		}
	}
	for _, ev := range toSend {
		if e.grouper != nil {
			e.grouper.Add(ev)
		} else {
			e.notify(ev)
		}
	}
}

// flushGroup 分组器回调：将一组告警汇总发送给所有已启用渠道。维护窗口活跃时跳过。
func (e *Engine) flushGroup(events []model.AlertEvent) {
	if e.maintenance != nil && e.maintenance.IsActive(model.NowMillis()) {
		slog.Info("维护窗口活跃，跳过分组告警通知", "count", len(events))
		return
	}
	e.mu.Lock()
	ns := append([]Notifier(nil), e.notifiers...)
	e.mu.Unlock()
	for _, n := range ns {
		if err := n.NotifyGroup(events); err != nil {
			slog.Warn("分组告警通知失败", "channel", n.Channel(), "err", err)
		}
	}
}

// SetGrouping 热更新分组配置并重建分组器（无需重启 Server）。
func (e *Engine) SetGrouping(cfg GroupingConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.grouper != nil {
		e.grouper.Stop()
		e.grouper = nil
	}
	e.grouping = &GroupingStore{cfg: cfg}
	if cfg.Enabled {
		wait, err1 := time.ParseDuration(cfg.GroupWait)
		interval, err2 := time.ParseDuration(cfg.GroupInterval)
		if err1 != nil || err2 != nil {
			slog.Warn("分组配置时间解析失败，使用默认", "groupWait", cfg.GroupWait, "groupInterval", cfg.GroupInterval)
			wait, interval = 30*time.Second, 5*time.Minute
		}
		g := NewGrouper(cfg.GroupBy, wait, interval, nil)
		g.flush = func(events []model.AlertEvent) { e.flushGroup(events) }
		e.grouper = g
	}
	slog.Info("分组配置已更新", "enabled", cfg.Enabled, "groupBy", cfg.GroupBy)
}
