package alert

import (
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

type captureAlertStorage struct {
	writes []model.Metric
	active []model.Series
}

func (s *captureAlertStorage) Write(metrics []model.Metric) error {
	s.writes = append(s.writes, metrics...)
	return nil
}
func (s *captureAlertStorage) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, nil
}
func (s *captureAlertStorage) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, nil
}
func (s *captureAlertStorage) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *captureAlertStorage) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return s.active, nil
}
func (s *captureAlertStorage) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, nil
}
func (s *captureAlertStorage) Close() error    { return nil }
func (s *captureAlertStorage) Backend() string { return "test" }

func TestVMAlertStoreResolvedUsesEndsAt(t *testing.T) {
	storage := &captureAlertStorage{}
	store := NewVMAlertStore(storage)
	store.Add(model.AlertEvent{RuleID: "r1", RuleName: "r1", Node: "n1", State: model.AlertStateResolved, EndsAt: 12345})
	if len(storage.writes) != 1 || storage.writes[0].Timestamp != 12345 {
		t.Fatalf("resolved event timestamp = %d, want 12345", storage.writes[0].Timestamp)
	}
}

func TestEngineClosesActiveAlertsForDeletedRule(t *testing.T) {
	storage := &captureAlertStorage{}
	engine := &Engine{
		alerts: NewVMAlertStore(storage),
		states: map[string]*ruleState{"rule-1|node-1|": {firing: true}},
		firing: map[string]*firingEntry{},
	}
	event := model.AlertEvent{
		ID:       "event-1",
		RuleID:   "rule-1",
		RuleName: "旧规则名称",
		Node:     "node-1",
		State:    model.AlertStateFiring,
		StartsAt: 100,
	}
	engine.firing[firingKey(event)] = &firingEntry{event: event}

	engine.CloseRuleAlerts(model.AlertRule{ID: "rule-1"}, "规则已删除，告警状态已关闭")

	if len(engine.firing) != 0 {
		t.Fatalf("firing entries = %d, want 0", len(engine.firing))
	}
	if len(engine.states) != 0 {
		t.Fatalf("rule states = %d, want 0", len(engine.states))
	}
	if len(storage.writes) != 1 {
		t.Fatalf("event writes = %d, want 1", len(storage.writes))
	}
	labels := storage.writes[0].Labels
	if labels["state"] != string(model.AlertStateResolved) {
		t.Fatalf("written state = %q, want resolved", labels["state"])
	}
	if labels["message"] != "规则已删除，告警状态已关闭" {
		t.Fatalf("written message = %q", labels["message"])
	}
}

func TestVMAlertStoreActiveKeepsLongRunningAlert(t *testing.T) {
	old := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	storage := &captureAlertStorage{active: []model.Series{{
		Labels: map[string]string{"rule": "r1", "name": "r1", "host": "n1", "state": "firing", "severity": "critical"},
		Points: []model.Point{{Timestamp: old, Value: 1}},
	}}}
	active := NewVMAlertStore(storage).Active()
	if len(active) != 1 || active[0].RuleID != "r1" {
		t.Fatalf("long-running alert was dropped: %+v", active)
	}
}

func TestVMAlertStoreRecentAndActivePreferLatestResolvedState(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	resolvedAt := old + 30*60*1000
	storage := &captureAlertStorage{active: []model.Series{
		{
			Labels: map[string]string{"rule": "r1", "name": "r1", "host": "n1", "instance": "event-1", "state": "firing", "severity": "critical", "message": "firing"},
			Points: []model.Point{{Timestamp: old, Value: 1}},
		},
		{
			Labels: map[string]string{"rule": "r1", "name": "r1", "host": "n1", "instance": "event-1", "state": "resolved", "severity": "critical", "message": "resolved"},
			Points: []model.Point{{Timestamp: resolvedAt, Value: 1}},
		},
	}}
	store := NewVMAlertStore(storage)

	recent := store.Recent(10)
	if len(recent) != 1 || recent[0].State != model.AlertStateResolved || recent[0].EndsAt != resolvedAt {
		t.Fatalf("recent = %+v, want one resolved event at %d", recent, resolvedAt)
	}
	if active := store.Active(); len(active) != 0 {
		t.Fatalf("active = %+v, want no active event", active)
	}
}

func TestVMAlertStoreRecentAndActivePreferLatestFiringState(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour).UnixMilli()
	firingAt := old + 30*60*1000
	storage := &captureAlertStorage{active: []model.Series{
		{
			Labels: map[string]string{"rule": "r1", "name": "r1", "host": "n1", "instance": "event-1", "state": "resolved", "severity": "critical", "message": "resolved"},
			Points: []model.Point{{Timestamp: old, Value: 1}},
		},
		{
			Labels: map[string]string{"rule": "r1", "name": "r1", "host": "n1", "instance": "event-1", "state": "firing", "severity": "critical", "message": "firing"},
			Points: []model.Point{{Timestamp: firingAt, Value: 1}},
		},
	}}
	store := NewVMAlertStore(storage)

	recent := store.Recent(10)
	if len(recent) != 1 || recent[0].State != model.AlertStateFiring || recent[0].StartsAt != firingAt {
		t.Fatalf("recent = %+v, want one firing event at %d", recent, firingAt)
	}
	active := store.Active()
	if len(active) != 1 || active[0].State != model.AlertStateFiring || active[0].StartsAt != firingAt {
		t.Fatalf("active = %+v, want firing event at %d", active, firingAt)
	}
}
func TestEngineRestoresSpecialAlertFiringIndex(t *testing.T) {
	active := []model.Series{
		{
			Labels: map[string]string{"rule": "dialtest-task-1", "name": "拨测", "host": "target", "state": "firing", "severity": "critical"},
			Points: []model.Point{{Timestamp: 100, Value: 0}},
		},
		{
			Labels: map[string]string{"rule": "dialcert-task-2", "name": "证书", "host": "target", "state": "firing", "severity": "warning"},
			Points: []model.Point{{Timestamp: 200, Value: 3}},
		},
	}
	storage := &captureAlertStorage{active: active}
	alerts := NewVMAlertStore(storage)
	rules := &RulesStore{rules: map[string]model.AlertRule{}}
	engine := NewEngine(nil, nil, rules, alerts, nil, nil, nil, 15, nil, nil, nil)

	if len(engine.firing) != 2 {
		t.Fatalf("restored firing index size = %d, want 2", len(engine.firing))
	}
	if !engine.dialActive["dialtest:task-1"] {
		t.Fatal("dialtest active state was not restored")
	}
	if _, ok := engine.certActive["cert:task-2"]; !ok {
		t.Fatal("certificate active state was not restored")
	}
}
