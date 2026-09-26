package retention

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/report"
	"github.com/nebula/monitor/internal/server/security"
)

// writeAcks 直接写一份处置记录文件（用于构造「很久以前」的记录）。
func writeAcks(t *testing.T, path string, list []alert.AckInfo) {
	t.Helper()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		t.Fatalf("序列化处置记录失败: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入处置记录失败: %v", err)
	}
}

func TestProbeTSDBRetention(t *testing.T) {
	withFlags := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/flags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"-retentionPeriod":"3","-maxSeries":"0"}`))
	}))
	defer withFlags.Close()
	got, err := ProbeTSDBRetention(withFlags.URL)
	if err != nil {
		t.Fatalf("应读到保留参数：%v", err)
	}
	if got != "-retentionPeriod=3" {
		t.Fatalf("保留参数不符：%q", got)
	}

	// 后端未暴露保留参数：应给出可读原因，而不是假装纳管
	noRetention := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"-maxSeries":"0"}`))
	}))
	defer noRetention.Close()
	if _, err := ProbeTSDBRetention(noRetention.URL); err == nil {
		t.Fatal("未暴露保留参数时应返回错误")
	}

	if _, err := ProbeTSDBRetention("http://127.0.0.1:1"); err == nil {
		t.Fatal("无法连接时应返回错误")
	}
	if _, err := ProbeTSDBRetention("  "); err == nil {
		t.Fatal("未配置地址时应返回错误")
	}
}

// TestManager_CleanupOnlyHandledAndOld 清理规则的核心：只清「已处置且超期」的记录。
func TestManager_CleanupOnlyHandledAndOld(t *testing.T) {
	dir := t.TempDir()
	acksPath := filepath.Join(dir, "alert_acks.json")
	old := time.Now().AddDate(0, 0, -200).UnixMilli()
	oldClosed := old + 1000
	writeAcks(t, acksPath, []alert.AckInfo{
		{Rule: "r1", Host: "web-01", Instance: "cpu", StartsAt: 1, Status: alert.StatusAck, Time: old},
		{Rule: "r2", Host: "web-02", Instance: "cpu", StartsAt: 2, Status: alert.StatusClosed, Time: oldClosed, CloseReason: "误报"},
		{Rule: "r3", Host: "web-03", Instance: "cpu", StartsAt: 3, Status: alert.StatusPending, Time: old}, // 重新打开：必须保留
		{Rule: "r4", Host: "web-04", Instance: "cpu", StartsAt: 4, Status: alert.StatusAck, Time: time.Now().UnixMilli()},
	})
	acks := alert.NewAckStore(acksPath)
	if stats := acks.Stats(); stats.Total != 4 || stats.Handled != 3 {
		t.Fatalf("初始统计不符：%+v", stats)
	}

	m, err := New(filepath.Join(dir, "retention.yaml"), DefaultConfig(), acks, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}

	// 保留 90 天：200 天前的已处置记录应被清理，待处理的与刚处置的必须保留
	res := m.CleanupNow()
	if res.AcksRemoved != 2 {
		t.Fatalf("应清理 2 条已处置记录，实际 %d（%+v）", res.AcksRemoved, res)
	}
	if res.AcksCutoff == 0 {
		t.Fatalf("应记录清理截止时间：%+v", res)
	}
	remaining := acks.Map()
	if len(remaining) != 2 {
		t.Fatalf("应剩余 2 条记录，实际 %d", len(remaining))
	}
	if _, ok := acks.Get("r3", "web-03", "cpu", 3); !ok {
		t.Fatal("待处理（重新打开）的记录不得被清理")
	}
	if _, ok := acks.Get("r4", "web-04", "cpu", 4); !ok {
		t.Fatal("未超期的记录不得被清理")
	}

	// 幂等：再次清理不再删除
	if again := m.CleanupNow(); again.AcksRemoved != 0 {
		t.Fatalf("重复清理应无删除，实际 %d", again.AcksRemoved)
	}
}

func TestManager_ReportPrune(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "daily-20260101-000000.html")
	freshFile := filepath.Join(dir, "daily-20260926-000000.html")
	for _, p := range []string{oldFile, freshFile} {
		if err := os.WriteFile(p, []byte("<html>report</html>"), 0o644); err != nil {
			t.Fatalf("写入报告失败: %v", err)
		}
	}
	oldTime := time.Now().AddDate(0, 0, -30)
	if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
		t.Fatalf("设置报告时间失败: %v", err)
	}
	history := []report.ReportMeta{
		{ID: "daily-20260101-000000", Generated: oldTime.UnixMilli()},
		{ID: "daily-20260926-000000", Generated: time.Now().UnixMilli()},
	}
	data, _ := json.Marshal(history)
	if err := os.WriteFile(filepath.Join(dir, "history.json"), data, 0o644); err != nil {
		t.Fatalf("写入历史失败: %v", err)
	}

	gen := report.NewGenerator(nil, nil, nil, dir)
	cfg := DefaultConfig()
	cfg.ReportsDays = 7
	cfg.AcksDays = 0
	m, err := New(filepath.Join(dir, "retention.yaml"), cfg, nil, gen, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}

	res := m.CleanupNow()
	if res.ReportFilesRemoved != 1 || res.ReportHistoryRemoved != 1 {
		t.Fatalf("应清理 1 个文件与 1 条历史，实际 %+v", res)
	}
	if res.FreedBytes <= 0 {
		t.Fatalf("应统计释放空间：%+v", res)
	}
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("超期报告文件应被删除")
	}
	if _, err := os.Stat(freshFile); err != nil {
		t.Fatal("未超期报告文件应保留")
	}
	if hs := gen.History(); len(hs) != 1 || hs[0].ID != "daily-20260926-000000" {
		t.Fatalf("历史应只剩最新一条：%+v", hs)
	}
}

func TestManager_ConfigInitAndPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retention.yaml")

	m, err := New(path, DefaultConfig(), nil, nil, nil, nil, "http://127.0.0.1:8428")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("首次运行应落盘默认配置：%v", err)
	}
	if cfg := m.Config(); cfg.AcksDays != DefaultAcksDays || cfg.ReportsDays != DefaultReportsDays || !cfg.Enabled {
		t.Fatalf("默认策略不符：%+v", cfg)
	}

	if err := m.Save(Config{Enabled: false, AcksDays: 30, ReportsDays: 0, IntervalHours: 0}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	reloaded, err := New(path, DefaultConfig(), nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	cfg := reloaded.Config()
	if cfg.Enabled || cfg.AcksDays != 30 || cfg.ReportsDays != 0 {
		t.Fatalf("配置应持久化：%+v", cfg)
	}
	if cfg.IntervalHours != DefaultIntervalHours {
		t.Fatalf("周期非法值应回落默认：%+v", cfg)
	}
}

func TestManager_StatusAndSkipped(t *testing.T) {
	dir := t.TempDir()
	acks := alert.NewAckStore(filepath.Join(dir, "acks.json"))
	acks.Mark("r1", "web-01", "cpu", 1, "ops")
	auditStore := audit.New("")
	_ = auditStore.Record(audit.Event{Method: "POST", Path: "/api/v1/x", User: "ops"})
	secStore := security.New(filepath.Join(dir, "security_store.json"))

	gen := report.NewGenerator(nil, nil, nil, filepath.Join(dir, "reports"))
	m, err := New(filepath.Join(dir, "retention.yaml"), DefaultConfig(), acks, gen, auditStore, secStore, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}

	st := m.Status()
	if st.Acks.Total != 1 || st.Acks.Handled != 1 {
		t.Fatalf("处置记录统计不符：%+v", st.Acks)
	}
	if st.Audit.Count != 1 || st.Audit.Cap != audit.MaxEvents {
		t.Fatalf("审计统计不符：%+v", st.Audit)
	}
	if st.Security.Cap != security.MaxEvents {
		t.Fatalf("安全事件上限不符：%+v", st.Security)
	}
	if st.TSDB.Error == "" {
		t.Fatal("未配置时序库地址时应给出可读原因")
	}

	// 两类保留天数都为 0 时不做任何清理，但应说明原因
	cfg := DefaultConfig()
	cfg.AcksDays, cfg.ReportsDays = 0, 0
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if res := m.CleanupNow(); res.Skipped == "" {
		t.Fatalf("应说明未清理原因：%+v", res)
	}
	// 上次清理结果应出现在状态里
	if m.Status().LastCleanup == nil {
		t.Fatal("状态应带上次清理结果")
	}
}

func TestManager_RunStopsOnCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Enabled = false // 关闭自动清理，只验证生命周期
	m, err := New(filepath.Join(dir, "retention.yaml"), cfg, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后应退出清理循环")
	}
}

// TestManager_NilSafe 空接收者不应 panic（接口未注入保留策略时仍可访问）。
func TestManager_NilSafe(t *testing.T) {
	var m *Manager
	if cfg := m.Config(); cfg.AcksDays != DefaultAcksDays {
		t.Fatalf("nil 应返回默认策略：%+v", cfg)
	}
	if res := m.CleanupNow(); res.Skipped == "" {
		t.Fatalf("nil 应说明未执行原因：%+v", res)
	}
	if st := m.Status(); st.Config.AcksDays != DefaultAcksDays {
		t.Fatalf("nil 状态应含默认策略：%+v", st)
	}
	m.Run(context.Background())
}
