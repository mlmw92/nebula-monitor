package alert

import (
	"log/slog"
	"sort"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/storage"
)

// alertMetric 是告警事件的存储指标名。样本时间戳=事件时间(ms)，值=触发值。
const alertMetric = "monitor_alert"

// VMAlertStore 基于 VictoriaMetrics 的告警事件存储。
type VMAlertStore struct {
	store storage.Storage
}

// NewVMAlertStore 创建告警存储。
func NewVMAlertStore(store storage.Storage) *VMAlertStore {
	return &VMAlertStore{store: store}
}

// Add 写入一条告警事件到 VM（仅在状态切换时调用，避免刷屏）。
// 注意：node 标签固定为 "system"，被监控节点名放在 host 标签，避免与 writer 的 node 标签冲突。
func (s *VMAlertStore) Add(e model.AlertEvent) {
	if err := s.store.Write([]model.Metric{
		{
			Node: "system",
			Name: alertMetric,
			Labels: map[string]string{
				"rule":     e.RuleID,
				"name":     e.RuleName,
				"host":     e.Node,
				"instance": e.Instance,
				"severity": string(e.Severity),
				"state":    string(e.State),
			},
			Value:     e.Value,
			Timestamp: e.StartsAt,
		},
	}); err != nil {
		slog.Error("告警事件写入时序库失败", "err", err, "rule", e.RuleName, "node", e.Node)
	}
}

// Recent 返回最近 limit 条告警事件（按事件时间倒序）。
// 告警事件是 fire/resolve 切换时一次性写入的稀疏样本，
// 用 range query 在大窗口+固定步长下会被时序库降采样吞掉，改用 instant query
// 直接取每个序列的最新点，对每个 (rule, host, state) 都能拿到。
func (s *VMAlertStore) Recent(limit int) []model.AlertEvent {
	series, err := s.store.QueryInstant("system", alertMetric, nil)
	if err != nil {
		slog.Warn("告警事件查询失败", "err", err)
		return nil
	}
	latest := map[string]model.AlertEvent{}
	order := []string{}
	for _, ser := range series {
		if len(ser.Points) == 0 {
			continue
		}
		key := ser.Labels["rule"] + "|" + ser.Labels["host"] + "|" + ser.Labels["instance"] + "|" + ser.Labels["state"]
		p := ser.Points[len(ser.Points)-1]
		ev := buildEvent(ser.Labels, p.Timestamp, p.Value)
		if cur, ok := latest[key]; !ok || ev.StartsAt > cur.StartsAt {
			if !ok {
				order = append(order, key)
			}
			latest[key] = ev
		}
	}
	out := make([]model.AlertEvent, 0, len(latest))
	for _, k := range order {
		out = append(out, latest[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartsAt > out[j].StartsAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Active 返回当前活跃（firing）告警。对于同一规则、节点和实例，只保留最新状态仍为 firing 的事件。
func (s *VMAlertStore) Active() []model.AlertEvent {
	series, err := s.store.QueryInstant("system", alertMetric, nil)
	if err != nil {
		slog.Warn("活跃告警查询失败", "err", err)
		return nil
	}
	latest := map[string]model.AlertEvent{}
	for _, ser := range series {
		if len(ser.Points) == 0 {
			continue
		}
		p := ser.Points[len(ser.Points)-1]
		if time.Now().UnixMilli()-p.Timestamp > int64(7*24*time.Hour) {
			continue
		}
		ev := buildEvent(ser.Labels, p.Timestamp, p.Value)
		key := ev.RuleID + "|" + ev.Node + "|" + ev.Instance
		if cur, ok := latest[key]; !ok || eventTime(ev) > eventTime(cur) {
			latest[key] = ev
		}
	}
	out := make([]model.AlertEvent, 0, len(latest))
	for _, ev := range latest {
		if ev.State == model.AlertStateFiring {
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool { return eventTime(out[i]) > eventTime(out[j]) })
	return out
}

func eventTime(ev model.AlertEvent) int64 {
	if ev.EndsAt != 0 {
		return ev.EndsAt
	}
	return ev.StartsAt
}

// buildEvent 从 VM 序列标签与点构造告警事件。
func buildEvent(labels map[string]string, ts int64, value float64) model.AlertEvent {
	state := model.AlertStateFiring
	if labels["state"] == string(model.AlertStateResolved) {
		state = model.AlertStateResolved
	}
	ev := model.AlertEvent{
		RuleID:    labels["rule"],
		RuleName:  labels["name"],
		Node:      labels["host"],
		Instance:  labels["instance"],
		Severity:  model.Severity(labels["severity"]),
		State:     state,
		Value:     value,
		Threshold: 0,
	}
	if state == model.AlertStateResolved {
		ev.EndsAt = ts
	} else {
		ev.StartsAt = ts
	}
	return ev
}
