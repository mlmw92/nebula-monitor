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

// 规则类型别名（来自 model 包），便于引擎内分支判断。
const (
	RuleTypeThreshold    = model.RuleTypeThreshold
	RuleTypeNodeOffline  = model.RuleTypeNodeOffline
	RuleTypeServiceDown  = model.RuleTypeServiceDown
	RuleTypeRoleChange   = model.RuleTypeRoleChange
	RuleTypeClusterFault = model.RuleTypeClusterFault
)

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
	aboveSince  int64  // 状态型规则：超过阈值起算时间；事件型规则：上次触发时间
	firing      bool   // 当前是否处于 firing
	firedAt     int64  // 首次触发时间（用于升级计时）
	escalated   bool   // 是否已执行过升级通知
	lastRepeat  int64  // 上次重复提醒时间（用于升级后重复提醒）
	lastRole    string // role_change 场景：上次记录的 role 值
	resolvedAt  int64  // 最后恢复时间（事件型规则避免抖动用）
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
		// 周期静默时段：命中的星期+时间区间则跳过本轮评估（与维护窗口不同，事件不记录）
		if inQuietPeriod(r.QuietPeriods, now) {
			continue
		}
		switch r.Type {
		case RuleTypeNodeOffline:
			e.evalNodeOffline(r, nodes, now)
		case RuleTypeServiceDown:
			e.evalServiceDown(r, nodes, now)
		case RuleTypeRoleChange:
			e.evalRoleChange(r, nodes, now)
		case RuleTypeClusterFault:
			e.evalClusterFault(r, nodes, now)
		default:
			e.evalThreshold(r, nodes, now)
		}
	}
}

// inQuietPeriod 判断当前时间是否落在任意周期静默时段内（按本地星期与时间区间匹配，支持跨天）。
func inQuietPeriod(periods []model.QuietPeriod, nowMillis int64) bool {
	if len(periods) == 0 {
		return false
	}
	t := time.UnixMilli(nowMillis)
	cur := t.Weekday() // 0=周日 .. 6=周六
	nowMin := t.Hour()*60 + t.Minute()
	for _, p := range periods {
		// 星期匹配：空表示每天
		if len(p.Days) > 0 {
			hit := false
			for _, d := range p.Days {
				if d == int(cur) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		sm, em, ok := parseHHMM(p.Start), parseHHMM(p.End), true
		if sm < 0 || em < 0 {
			continue
		}
		_ = ok
		if sm <= em {
			if nowMin >= sm && nowMin < em {
				return true
			}
		} else {
			// 跨天，如 22:00-06:00
			if nowMin >= sm || nowMin < em {
				return true
			}
		}
	}
	return false
}

// parseHHMM 将 "HH:MM" 解析为当天分钟数，解析失败返回 -1。
func parseHHMM(s string) int {
	if len(s) != 5 || s[2] != ':' {
		return -1
	}
	h := int(s[0]-'0')*10 + int(s[3]-'0')
	m := int(s[3+1]-'0')*10 + int(s[4+1]-'0')
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return -1
	}
	return h*60 + m
}

// evalThreshold 传统阈值规则评估：逐节点 × 实例，指标与阈值比较，持续 For 后触发。
func (e *Engine) evalThreshold(r model.AlertRule, nodes []model.Node, now int64) {
	for _, n := range nodes {
		if !matchesGroup(r.Group, n.Group) {
			continue
		}
		if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
			continue
		}
		for _, sample := range e.latestMetricSamples(n.Hostname, r.Metric) {
			key := r.ID + "|" + n.Hostname + "|" + sample.instance
			st := e.getState(key)
			cond := Compare(r.Operator, sample.value, r.Threshold)
			if cond {
				if st.aboveSince == 0 {
					st.aboveSince = now
				}
				forSec := parseFor(r.For)
				if !st.firing && now-st.aboveSince >= forSec*1000 {
					st.firing = true
					st.firedAt = now
					st.escalated = false
					st.lastRepeat = 0
					e.fire(r, n.Hostname, sample.instance, sample.value, now)
				} else {
					e.maybeEscalate(r, n.Hostname, sample.instance, sample.value, st, now)
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

// getState 获取（或创建）规则状态项。调用方须持有 e.mu。
func (e *Engine) getState(key string) *ruleState {
	st := e.states[key]
	if st == nil {
		st = &ruleState{}
		e.states[key] = st
	}
	return st
}

// evalNodeOffline 主机离线检测：直接读取 nodeMgr 内存状态（离线无需指标上报）。
// 状态型：离线持续 For 后触发，恢复（online）即 resolve。
func (e *Engine) evalNodeOffline(r model.AlertRule, nodes []model.Node, now int64) {
	for _, n := range nodes {
		if !matchesGroup(r.Group, n.Group) {
			continue
		}
		if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
			continue
		}
		key := r.ID + "|" + n.Hostname + "|"
		st := e.getState(key)
		offline := n.Status == "offline"
		if offline {
			if st.aboveSince == 0 {
				st.aboveSince = now
			}
			forSec := parseFor(r.For)
			if !st.firing && now-st.aboveSince >= forSec*1000 {
				st.firing = true
				st.firedAt = now
				st.escalated = false
				st.lastRepeat = 0
				e.fire(r, n.Hostname, "", 0, now, "主机 "+n.Hostname+" 已离线（Status=offline，最近心跳 "+formatTS(n.LastSeen)+"）")
			} else {
				e.maybeEscalate(r, n.Hostname, "", 0, st, now)
			}
		} else {
			if st.firing {
				st.firing = false
				e.resolve(r, n.Hostname, "", 0, now, "主机 "+n.Hostname+" 已恢复正常在线")
			}
			st.aboveSince = 0
		}
	}
}

// serviceMetric 将 Service 类型映射为对应 *_instance_up 指标名。
func serviceMetric(svc string) string {
	switch svc {
	case "mysql":
		return "mysql_instance_up"
	case "postgres":
		return "postgres_instance_up"
	case "redis":
		return "redis_instance_up"
	case "nginx":
		return "nginx_instance_up"
	case "kafka":
		return "kafka_instance_up"
	case "rocketmq":
		return "rocketmq_instance_up"
	case "docker":
		return "docker_container_up"
	case "k8s":
		return "k8s_cluster_up"
	default:
		// 未知服务：退化为 redis_instance_up（前端限制了可选值，这里兜底）
		return "redis_instance_up"
	}
}

// evalServiceDown 中间件/服务离线检测：基于 *_instance_up 指标（值为 0 即离线）。
// 默认阈值 <= 0.5 触发（即 up=0），阈值/运算符可由规则自定义。状态型。
func (e *Engine) evalServiceDown(r model.AlertRule, nodes []model.Node, now int64) {
	metric := serviceMetric(r.Service)
	op := r.Operator
	if op == "" {
		op = "<="
	}
	thr := r.Threshold
	if op == "<=" && thr == 0 {
		thr = 0.5
	}
	for _, n := range nodes {
		if !matchesGroup(r.Group, n.Group) {
			continue
		}
		if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
			continue
		}
		for _, sample := range e.latestMetricSamples(n.Hostname, metric) {
			key := r.ID + "|" + n.Hostname + "|" + sample.instance
			st := e.getState(key)
			cond := Compare(op, sample.value, thr)
			if cond {
				if st.aboveSince == 0 {
					st.aboveSince = now
				}
				forSec := parseFor(r.For)
				if !st.firing && now-st.aboveSince >= forSec*1000 {
					st.firing = true
					st.firedAt = now
					st.escalated = false
					st.lastRepeat = 0
					msg := "节点 " + n.Hostname + " 的 " + r.Service + " 实例 " + instanceLabel(sample.instance) +
						" 服务离线（" + metric + "=" + formatFloat(sample.value) + "）"
					e.fire(r, n.Hostname, sample.instance, sample.value, now, msg)
				} else {
					e.maybeEscalate(r, n.Hostname, sample.instance, sample.value, st, now)
				}
			} else {
				if st.firing {
					st.firing = false
					msg := "节点 " + n.Hostname + " 的 " + r.Service + " 实例 " + instanceLabel(sample.instance) + " 已恢复正常"
					e.resolve(r, n.Hostname, sample.instance, sample.value, now, msg)
				}
				st.aboveSince = 0
			}
		}
	}
}

// evalRoleChange 数据库主从切换检测：对比各实例上一次评估记录的 role（来自 *_instance_up 的 role 标签），
// 发现变化即触发事件型告警（下一轮无变化则自动恢复），重复切换会重复触发。无 agent 改动（复用 TSDB 已有 role 标签）。
func (e *Engine) evalRoleChange(r model.AlertRule, nodes []model.Node, now int64) {
	metric := serviceMetric(r.Service)
	for _, n := range nodes {
		if !matchesGroup(r.Group, n.Group) {
			continue
		}
		if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
			continue
		}
		for _, sample := range e.latestRoleSamples(n.Hostname, metric) {
			key := r.ID + "|" + n.Hostname + "|" + sample.instance
			st := e.getState(key)
			// 仅在 topology 匹配时才判定为主从切换关注对象（未指定则全部）
			if r.Topology != "" && r.Topology != sample.topology {
				st.lastRole = sample.role
				continue
			}
			prev := st.lastRole
			st.lastRole = sample.role
			if prev == "" {
				// 首次采集仅记录基线，不触发
				continue
			}
			if prev != sample.role {
				// 事件型：每隔冷却窗口（默认 For，0 表示每次都触发）触发一次
				cooldownMs := parseFor(r.For) * 1000
				if now-st.aboveSince < cooldownMs {
					continue
				}
				st.aboveSince = now
				st.firing = true
				st.firedAt = now
				st.escalated = false
				st.lastRepeat = 0
				msg := "节点 " + n.Hostname + " 的 " + r.Service + " 实例 " + instanceLabel(sample.instance) +
					" 发生角色切换：" + roleText(prev) + " -> " + roleText(sample.role) + "（架构/拓扑=" + sample.topology + "）"
				e.fire(r, n.Hostname, sample.instance, 0, now, msg)
				// 事件型：下一轮无变化即视为恢复（仅当仍为 firing 且无再次切换）
				st.resolvedAt = 0
			} else {
				// 角色未变：若之前触发过（firing），则自动恢复
				if st.firing {
					st.firing = false
					msg := "节点 " + n.Hostname + " 的 " + r.Service + " 实例 " + instanceLabel(sample.instance) + " 角色已稳定为 " + roleText(sample.role)
					e.resolve(r, n.Hostname, sample.instance, 0, now, msg)
				}
			}
		}
	}
}

// evalClusterFault 集群状态损坏检测：按 group/name 聚合各实例 role，判定无主（0 个 PRIMARY/master）
// 或多主（>1 个 PRIMARY/master），并向每个相关节点触发。状态型。
func (e *Engine) evalClusterFault(r model.AlertRule, nodes []model.Node, now int64) {
	metric := serviceMetric(r.Service)
	// 收集该中间件全部实例的最新 role（按节点+实例+分组），去重取最新时间戳。
	byGroup := map[string][]instRole{}
	for _, n := range nodes {
		if !matchesGroup(r.Group, n.Group) {
			continue
		}
		if !matchesScope(r.Scope, r.Nodes, n.Hostname) {
			continue
		}
		for _, sample := range e.latestRoleSamples(n.Hostname, metric) {
			if r.Topology != "" && r.Topology != sample.topology {
				continue
			}
			grp := sample.group
			if grp == "" {
				grp = sample.instance
			}
			ir := instRole{node: n.Hostname, instance: sample.instance, group: grp, role: sample.role, topology: sample.topology, value: sample.value, ts: sample.ts}
			byGroup[grp] = append(byGroup[grp], ir)
		}
	}
	// 对每个集群分组判定是否存在故障，并触发/恢复每个成员节点上的告警。
	for grp, members := range byGroup {
		fault := e.classifyClusterFault(members)
		for _, m := range members {
			key := r.ID + "|" + m.node + "|" + m.instance + "|" + grp
			st := e.getState(key)
			if fault != "" {
				if st.aboveSince == 0 {
					st.aboveSince = now
				}
				forSec := parseFor(r.For)
				if !st.firing && now-st.aboveSince >= forSec*1000 {
					st.firing = true
					st.firedAt = now
					st.escalated = false
					st.lastRepeat = 0
					msg := "集群 " + grp + "（" + r.Service + "）状态损坏：" + fault +
						"（实例 " + instanceLabel(m.instance) + " 节点 " + m.node + "）"
					e.fire(r, m.node, m.instance, m.value, now, msg)
				} else {
					e.maybeEscalate(r, m.node, m.instance, m.value, st, now)
				}
			} else {
				if st.firing {
					st.firing = false
					msg := "集群 " + grp + "（" + r.Service + "）已恢复正常（单主且多数派存活）"
					e.resolve(r, m.node, m.instance, m.value, now, msg)
				}
				st.aboveSince = 0
			}
		}
	}
}

// classifyClusterFault 根据集群成员角色判断故障类型，返回空字符串表示健康。
func (e *Engine) classifyClusterFault(members []instRole) string {
	// 仅考虑 up 的成员（value>0.5）参与角色判定；整体不可达则交由 service_down 规则处理。
	primaries := 0
	alive := 0
	for _, m := range members {
		if m.value <= 0.5 {
			continue
		}
		alive++
		if isPrimaryRole(m.role) {
			primaries++
		}
	}
	if alive == 0 {
		return ""
	}
	if primaries == 0 {
		return "无主（缺少 PRIMARY/主库），集群无法写入"
	}
	if primaries > 1 {
		return "多主（检测到 " + strconv.Itoa(primaries) + " 个 PRIMARY/主库），疑似脑裂"
	}
	return ""
}

// maybeEscalate 对已 firing 且配置了升级策略的告警，按 AfterMinutes 执行一次升级通知，
// 并按 RepeatMinutes 周期重复提醒（仅通知，不落新事件）。调用方须持有 e.mu。
func (e *Engine) maybeEscalate(r model.AlertRule, node, instance string, value float64, st *ruleState, now int64) {
	esc := r.Escalation
	if esc == nil || !esc.Enabled {
		return
	}
	if !st.firing || st.firedAt == 0 {
		return
	}
	elapsedMin := (now - st.firedAt) / 60000
	if !st.escalated && elapsedMin >= int64(esc.AfterMinutes) {
		st.escalated = true
		st.lastRepeat = now
		// 构造升级事件：级别/渠道可被升级策略覆盖
		sev := r.Severity
		if esc.ToSeverity != "" {
			sev = esc.ToSeverity
		}
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
			Severity:  sev,
			State:     model.AlertStateFiring,
			Message:   "[告警升级] " + e.currentMessage(r, node, instance, value),
			StartsAt:  now,
		}
		slog.Info("告警升级触发", "rule", r.Name, "node", node, "toSeverity", sev, "elapsedMin", elapsedMin)
		if e.grouper != nil {
			e.grouper.Add(ev)
		} else {
			e.notifyEscalation(ev, esc)
		}
		return
	}
	// 已升级后按重复间隔提醒
	if st.escalated && esc.RepeatMinutes > 0 && now-st.lastRepeat >= int64(esc.RepeatMinutes)*60000 {
		st.lastRepeat = now
		sev := r.Severity
		if esc.ToSeverity != "" {
			sev = esc.ToSeverity
		}
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
			Severity:  sev,
			State:     model.AlertStateFiring,
			Message:   "[重复提醒] " + e.currentMessage(r, node, instance, value),
			StartsAt:  now,
		}
		if e.grouper != nil {
			e.grouper.Add(ev)
		} else {
			e.notifyEscalation(ev, esc)
		}
	}
}

// notifyEscalation 按升级策略指定的渠道（空则用规则渠道）发送通知，并遵循维护窗口约束。
func (e *Engine) notifyEscalation(ev model.AlertEvent, esc *model.Escalation) {
	if e.maintenance != nil && e.maintenance.IsActive(model.NowMillis()) {
		slog.Info("维护窗口活跃，跳过升级通知", "rule", ev.RuleName, "event", ev.ID)
		return
	}
	chs := esc.Channels
	if len(chs) == 0 {
		chs = e.ruleNotifyChannels(ev.RuleID)
	}
	if len(chs) == 0 {
		chs = e.allChannels()
	}
	for _, n := range e.notifiers {
		if !contains(chs, n.Channel()) {
			continue
		}
		if err := n.Notify(ev); err != nil {
			slog.Warn("升级通知发送失败", "channel", n.Channel(), "err", err)
		}
	}
}

// currentMessage 生成告警当前状态的描述（供升级/重复提醒复用）。
func (e *Engine) currentMessage(r model.AlertRule, node, instance string, value float64) string {
	switch r.Type {
	case RuleTypeNodeOffline:
		return "主机 " + node + " 持续离线超过升级阈值"
	case RuleTypeServiceDown:
		return "节点 " + node + " 的 " + r.Service + " 实例 " + instanceLabel(instance) + " 持续离线"
	case RuleTypeClusterFault:
		return "集群 " + r.Service + " 状态损坏持续未恢复（实例 " + instanceLabel(instance) + " 节点 " + node + "）"
	default:
		return triggerMessage(r, node, value)
	}
}

func roleText(role string) string {
	switch role {
	case "PRIMARY", "primary", "master":
		return "主库(PRIMARY)"
	case "SECONDARY", "secondary", "slave":
		return "从库(SECONDARY)"
	default:
		return role
	}
}

func isPrimaryRole(role string) bool {
	return role == "PRIMARY" || role == "primary" || role == "master"
}

func instanceLabel(s string) string {
	if s == "" {
		return "(默认)"
	}
	return s
}

func formatTS(ms int64) string {
	if ms <= 0 {
		return "未知"
	}
	return time.UnixMilli(ms).Format("2006-01-02 15:04:05")
}

func (e *Engine) fire(r model.AlertRule, node, instance string, value float64, now int64, msg ...string) {
	message := triggerMessage(r, node, value)
	if len(msg) > 0 && msg[0] != "" {
		message = msg[0]
	}
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
		Message:   message,
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

func (e *Engine) resolve(r model.AlertRule, node, instance string, value float64, now int64, msg ...string) {
	message := "节点 " + node + " 指标 " + r.Metric + " 已恢复（规则：" + r.Name + "）"
	if len(msg) > 0 && msg[0] != "" {
		message = msg[0]
	}
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
		Message:   message,
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

// roleSample 携带 role/group/topology 标签与最新时间戳的实例样本。
type roleSample struct {
	instance string
	role     string
	group    string
	topology string
	value    float64
	ts       int64
}

// instRole 集群成员角色聚合的中间结构（用于集群状态损坏判定）。
type instRole struct {
	node, instance, group, role, topology string
	value                                  float64
	ts                                     int64
}

// latestRoleSamples 返回某节点某中间件指标的最新实例状态，提取 role/group/topology 标签。
// 对同一 node|instance 若存在多条 series（如 GR 切主 staleness 期内新旧 role 并存），
// 取数据点时间戳最新者，避免旧 role 残留造成误判。
func (e *Engine) latestRoleSamples(node, metric string) []roleSample {
	series, err := e.store.QueryInstant(node, metric, nil)
	if err != nil {
		return nil
	}
	best := map[string]roleSample{} // key: node|instance
	for _, s := range series {
		inst := s.Labels["instance"]
		key := node + "|" + inst
		var ts int64
		var val float64
		if len(s.Points) > 0 {
			ts = s.Points[len(s.Points)-1].Timestamp
			val = s.Points[len(s.Points)-1].Value
		}
		rs := roleSample{
			instance: inst,
			role:     s.Labels["role"],
			group:    s.Labels["group"],
			topology: s.Labels["topology"],
			value:    val,
			ts:       ts,
		}
		prev, ok := best[key]
		if !ok || ts > prev.ts {
			best[key] = rs
		}
	}
	out := make([]roleSample, 0, len(best))
	for _, v := range best {
		out = append(out, v)
	}
	return out
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
