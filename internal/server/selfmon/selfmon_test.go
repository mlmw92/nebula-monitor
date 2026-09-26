package selfmon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/alert"
)

// fakeStorage 是 storage.Storage 的测试替身：记录写入并可按需返回错误。
type fakeStorage struct {
	mu       sync.Mutex
	writes   [][]model.Metric
	writeErr error
	queryErr error
	closed   bool
}

func (f *fakeStorage) Write(metrics []model.Metric) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	f.writes = append(f.writes, metrics)
	return nil
}

func (f *fakeStorage) QueryRange(string, string, map[string]string, int64, int64, int64) ([]model.Series, error) {
	return nil, f.queryErr
}

func (f *fakeStorage) QueryLatest(string, string, map[string]string) (*model.Point, error) {
	return nil, f.queryErr
}

func (f *fakeStorage) QueryInstant(string, string, map[string]string) ([]model.Series, error) {
	return nil, f.queryErr
}

func (f *fakeStorage) QueryInstantWithLookback(string, string, map[string]string, time.Duration) ([]model.Series, error) {
	return nil, f.queryErr
}

func (f *fakeStorage) QueryAllLatest(string, map[string]string) ([]model.Series, error) {
	return nil, f.queryErr
}

func (f *fakeStorage) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeStorage) Backend() string { return "fake" }

func (f *fakeStorage) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// fakeNotifier 是 alert.Notifier 的测试替身。
type fakeNotifier struct {
	channel string
	err     error
	calls   int
}

func (f *fakeNotifier) Notify(model.AlertEvent) error {
	f.calls++
	return f.err
}

func (f *fakeNotifier) NotifyGroup([]model.AlertEvent) error {
	f.calls++
	return f.err
}

func (f *fakeNotifier) Channel() string { return f.channel }

// TestMonitor_CountersAndSnapshot 计数与快照口径。
func TestMonitor_CountersAndSnapshot(t *testing.T) {
	m := New("1.2.3")
	m.AddHTTPRequest(200)
	m.AddHTTPRequest(404)
	m.AddHTTPRequest(500)
	m.AddWrite(nil)
	m.AddWrite(errors.New("boom"))
	m.AddQuery(nil)
	m.AddQuery(nil)
	m.AddQuery(errors.New("timeout"))
	m.AddNotify("dingtalk", nil)
	m.AddNotify("dingtalk", errors.New("token invalid"))
	m.AddNotify("email", nil)
	m.AddNotify("", nil) // 渠道缺失时归入 unknown
	m.SetWSStats(func() int { return 2 })
	m.AddEval()

	s := m.Snapshot()
	if s.Version != "1.2.3" || s.Node != m.Node() {
		t.Fatalf("版本/节点标签不符：%+v", s)
	}
	if s.HTTP.Requests != 3 || s.HTTP.Errors != 2 {
		t.Fatalf("HTTP 统计不符：%+v", s.HTTP)
	}
	if s.TSDB.WriteTotal != 2 || s.TSDB.WriteErrors != 1 {
		t.Fatalf("写入统计不符：%+v", s.TSDB)
	}
	if s.TSDB.QueryTotal != 3 || s.TSDB.QueryErrors != 1 {
		t.Fatalf("查询统计不符：%+v", s.TSDB)
	}
	if s.Notify.Sent != 3 || s.Notify.Failed != 1 {
		t.Fatalf("通知统计不符：%+v", s.Notify)
	}
	if got := s.Notify.ByChannel["dingtalk"]; got.Sent != 1 || got.Failed != 1 {
		t.Fatalf("dingtalk 渠道统计不符：%+v", got)
	}
	if got := s.Notify.ByChannel["unknown"]; got.Sent != 1 {
		t.Fatalf("空渠道应归入 unknown：%+v", s.Notify.ByChannel)
	}
	if s.WSConnections != 2 {
		t.Fatalf("WebSocket 连接数不符：%d", s.WSConnections)
	}
	if s.Alert.EvalTotal != 1 || s.Alert.EvalAgeSeconds < 0 || s.Alert.EvalAgeSeconds > 5 {
		t.Fatalf("评估统计不符：%+v", s.Alert)
	}
	if s.UptimeSeconds < 0 || s.Goroutines <= 0 || s.HeapAllocBytes == 0 {
		t.Fatalf("进程指标异常：%+v", s)
	}
}

// TestMonitor_NilAndNeverEvaluated 空接收者与「尚未评估」必须可区分（-1），
// 否则 /readyz 会把「从未评估」误判为「刚刚评估过」。
func TestMonitor_NilAndNeverEvaluated(t *testing.T) {
	var nilMon *Monitor
	if s := nilMon.Snapshot(); s.Alert.EvalAgeSeconds != -1 {
		t.Fatalf("nil 收集器的评估年龄应为 -1，got %d", s.Alert.EvalAgeSeconds)
	}
	m := New("dev")
	if s := m.Snapshot(); s.Alert.EvalAgeSeconds != -1 {
		t.Fatalf("尚未评估时评估年龄应为 -1，got %d", s.Alert.EvalAgeSeconds)
	}
	if s := m.Snapshot(); s.Alert.EvalIntervalSec != 0 {
		t.Fatalf("未注入评估周期时应为 0，got %d", s.Alert.EvalIntervalSec)
	}
}

// TestMonitor_InjectedStats 注入的告警/节点统计应进入快照。
func TestMonitor_InjectedStats(t *testing.T) {
	m := New("dev")
	m.SetAlertStats(func() (int, int) { return 7, 2 })
	m.SetNodeStats(func() (int, int) { return 9, 12 })
	m.SetEvalInterval(30 * time.Second)

	s := m.Snapshot()
	if s.Alert.Firing != 7 || s.Alert.Suppressed != 2 {
		t.Fatalf("告警统计未注入：%+v", s.Alert)
	}
	if s.Nodes.Online != 9 || s.Nodes.Total != 12 {
		t.Fatalf("节点统计未注入：%+v", s.Nodes)
	}
	if s.Alert.EvalIntervalSec != 30 {
		t.Fatalf("评估周期未注入：%+v", s.Alert)
	}
}

// TestMonitor_Metrics 指标集合：命名前缀、节点标签、低基数标签。
func TestMonitor_Metrics(t *testing.T) {
	m := New("dev")
	m.AddNotify("dingtalk", nil)
	metrics := m.Metrics(time.Now())
	if len(metrics) == 0 {
		t.Fatal("指标集合不应为空")
	}
	names := map[string]bool{}
	for _, mt := range metrics {
		if !strings.HasPrefix(mt.Name, MetricPrefix) {
			t.Fatalf("指标名必须带 %s 前缀：%s", MetricPrefix, mt.Name)
		}
		if mt.Node != m.Node() {
			t.Fatalf("节点标签不符：%s", mt.Node)
		}
		if mt.Labels["kind"] != "server" {
			t.Fatalf("缺少 kind=server 标签：%v", mt.Labels)
		}
		if mt.Timestamp == 0 {
			t.Fatal("时间戳不应为 0")
		}
		names[mt.Name] = true
	}
	for _, want := range []string{
		"self_uptime_seconds", "self_goroutines", "self_heap_alloc_bytes",
		"self_tsdb_write_errors_total", "self_tsdb_query_errors_total",
		"self_notify_failed_total", "self_ws_connections",
		"self_alert_eval_age_seconds", "self_alert_firing", "self_nodes_online",
		"self_notify_sent_channel_total", "self_notify_failed_channel_total",
	} {
		if !names[want] {
			t.Errorf("缺少指标 %s", want)
		}
	}

	// 指标标签不得共享同一 map（避免调用方改动影响其他指标）
	var first, second *model.Metric
	for i := range metrics {
		if first == nil {
			first = &metrics[i]
			continue
		}
		second = &metrics[i]
		break
	}
	first.Labels["mutated"] = "1"
	if _, ok := second.Labels["mutated"]; ok {
		t.Fatal("指标之间不应共享标签 map")
	}
}

// TestStorageWrapper 装饰器：计数 + 直通 + 错误传播。
func TestStorageWrapper(t *testing.T) {
	inner := &fakeStorage{}
	m := New("dev")
	s := NewStorage(inner, m)

	if err := s.Write([]model.Metric{{Name: "x"}}); err != nil {
		t.Fatalf("写入应直通成功：%v", err)
	}
	inner.writeErr = errors.New("vm down")
	if err := s.Write(nil); err == nil {
		t.Fatal("底层错误必须原样返回")
	}
	inner.queryErr = errors.New("query fail")
	if _, err := s.QueryRange("n", "cpu", nil, 0, 1, 1); err == nil {
		t.Fatal("查询错误必须原样返回")
	}
	if _, err := s.QueryLatest("n", "cpu", nil); err == nil {
		t.Fatal("查询错误必须原样返回")
	}
	if _, err := s.QueryInstant("n", "cpu", nil); err == nil {
		t.Fatal("查询错误必须原样返回")
	}
	if _, err := s.QueryInstantWithLookback("n", "cpu", nil, time.Minute); err == nil {
		t.Fatal("查询错误必须原样返回")
	}
	if _, err := s.QueryAllLatest("cpu", nil); err == nil {
		t.Fatal("查询错误必须原样返回")
	}
	if s.Backend() != "fake" {
		t.Fatalf("Backend 应直通：%s", s.Backend())
	}
	if err := s.Close(); err != nil || !inner.closed {
		t.Fatalf("Close 应直通：err=%v closed=%v", err, inner.closed)
	}

	snap := m.Snapshot()
	if snap.TSDB.WriteTotal != 2 || snap.TSDB.WriteErrors != 1 {
		t.Fatalf("写入计数不符：%+v", snap.TSDB)
	}
	if snap.TSDB.QueryTotal != 5 || snap.TSDB.QueryErrors != 5 {
		t.Fatalf("查询计数不符：%+v", snap.TSDB)
	}
}

// TestStorageWrapperNilMonitor 未注入收集器时仍可正常工作（零开销直通）。
func TestStorageWrapperNilMonitor(t *testing.T) {
	inner := &fakeStorage{}
	s := NewStorage(inner, nil)
	if err := s.Write(nil); err != nil {
		t.Fatalf("nil 收集器下应直通：%v", err)
	}
	if inner.writeCount() != 1 {
		t.Fatal("写入应到达底层存储")
	}
}

// TestNotifierWrapper 通知装饰器：按渠道计数、错误传播、渠道名直通。
func TestNotifierWrapper(t *testing.T) {
	ok := &fakeNotifier{channel: "dingtalk"}
	bad := &fakeNotifier{channel: "email", err: errors.New("smtp refused")}
	m := New("dev")
	wrapped := WrapNotifiers([]alert.Notifier{ok, bad}, m)
	if len(wrapped) != 2 {
		t.Fatalf("应包装 2 个通知器，got %d", len(wrapped))
	}

	if err := wrapped[0].Notify(model.AlertEvent{}); err != nil {
		t.Fatalf("通知应成功：%v", err)
	}
	if err := wrapped[0].NotifyGroup([]model.AlertEvent{{}}); err != nil {
		t.Fatalf("分组通知应成功：%v", err)
	}
	if err := wrapped[1].NotifyGroup([]model.AlertEvent{{}}); err == nil {
		t.Fatal("通知失败必须原样返回错误")
	}
	if wrapped[1].Channel() != "email" {
		t.Fatalf("渠道名应直通：%s", wrapped[1].Channel())
	}

	snap := m.Snapshot()
	if got := snap.Notify.ByChannel["dingtalk"]; got.Sent != 2 || got.Failed != 0 {
		t.Fatalf("dingtalk 统计不符：%+v", got)
	}
	if got := snap.Notify.ByChannel["email"]; got.Failed != 1 || got.Sent != 0 {
		t.Fatalf("email 统计不符：%+v", got)
	}

	// nil 收集器时原样返回，不做包装
	if got := WrapNotifiers([]alert.Notifier{ok}, nil); len(got) != 1 || got[0] != alert.Notifier(ok) {
		t.Fatal("nil 收集器时不应包装")
	}
}

// TestReporter 上报：写入 self_* 指标，且 ctx 取消后能退出。
func TestReporter(t *testing.T) {
	store := &fakeStorage{}
	m := New("dev")
	r := NewReporter(store, m, time.Hour) // 周期设长，只验证首次上报
	if err := r.Report(); err != nil {
		t.Fatalf("上报应成功：%v", err)
	}
	if store.writeCount() != 1 {
		t.Fatalf("应写入 1 批指标，got %d", store.writeCount())
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	// Run 启动时会立即上报一次
	deadline := time.Now().Add(2 * time.Second)
	for store.writeCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后上报循环应退出")
	}
}

// TestReporter_SkipsWhenUnconfigured 未注入存储/收集器时不应 panic。
func TestReporter_SkipsWhenUnconfigured(t *testing.T) {
	if err := NewReporter(nil, New("dev"), time.Second).Report(); err != nil {
		t.Fatalf("无存储时应静默跳过：%v", err)
	}
	if err := NewReporter(&fakeStorage{}, nil, time.Second).Report(); err != nil {
		t.Fatalf("无收集器时应静默跳过：%v", err)
	}
	var nilReporter *Reporter
	if err := nilReporter.Report(); err != nil {
		t.Fatalf("nil 上报器应静默跳过：%v", err)
	}
	nilReporter.Run(context.Background())
}
