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
func (s *VMAlertStore) Recent(limit int) []model.AlertEvent {
	now := time.Now().UnixMilli()
	start := now - int64(7*24*time.Hour)
	end := now
	step := (end - start) / 500
	if step < int64(time.Minute) {
		step = int64(time.Minute)
	}
	series, err := s.store.QueryRange("system", alertMetric, nil, start, end, step)
	if err != nil {
		return nil
	}
	latest := map[string]model.AlertEvent{}
	order := []string{}
	for _, ser := range series {
		key := ser.Labels["rule"] + "|" + ser.Labels["node"] + "|" + ser.Labels["state"]
		for _, p := range ser.Points {
			ev := buildEvent(ser.Labels, p.Timestamp, p.Value)
			if cur, ok := latest[key]; !ok || ev.StartsAt > cur.StartsAt {
				if !ok {
					order = append(order, key)
				}
				latest[key] = ev
			}
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

// Active 返回当前活跃（firing）告警。
func (s *VMAlertStore) Active() []model.AlertEvent {
	series, err := s.store.QueryInstant("system", alertMetric, map[string]string{"state": string(model.AlertStateFiring)})
	if err != nil {
		return nil
	}
	var out []model.AlertEvent
	for _, ser := range series {
		if len(ser.Points) == 0 {
			continue
		}
		p := ser.Points[len(ser.Points)-1]
		// 仅保留最近 7 天内的 active
		if time.Now().UnixMilli()-p.Timestamp > int64(7*24*time.Hour) {
			continue
		}
		out = append(out, buildEvent(ser.Labels, p.Timestamp, p.Value))
	}
	return out
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
