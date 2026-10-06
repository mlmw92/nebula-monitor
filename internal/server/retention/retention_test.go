package retention

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/server/alert"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/logstore"
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

// TestManager_CleanupFailureIsReported 清理落盘失败必须显式上报。
//
// 静默吞掉错误会让人看到一次「清理完成、删除 0 条」，而处置记录其实在无界增长；
// 同时失败时绝不能先删内存——否则重启后又会被库/文件读回来（或反之丢记录）。
func TestManager_CleanupFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "acks")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	acksPath := filepath.Join(sub, "alert_acks.json")
	old := time.Now().AddDate(0, 0, -200).UnixMilli()
	writeAcks(t, acksPath, []alert.AckInfo{
		{Rule: "r1", Host: "web-01", Instance: "cpu", StartsAt: 1, Status: alert.StatusAck, Time: old},
	})
	acks := alert.NewAckStore(acksPath)

	// 让落盘必然失败：把存放处置记录的目录换成同名文件（MkdirAll 会失败）。
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := New(filepath.Join(dir, "retention.yaml"), DefaultConfig(), acks, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	res := m.CleanupNow()
	if len(res.Errors) == 0 {
		t.Fatalf("落盘失败必须上报错误，而不是显示清理成功：%+v", res)
	}
	if res.AcksRemoved != 0 {
		t.Fatalf("失败时不得报告删除条数：%+v", res)
	}
	if _, ok := acks.Get("r1", "web-01", "cpu", 1); !ok {
		t.Fatal("落盘失败时不得删除内存记录（重启后会复活）")
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
	if _, err := acks.Mark("r1", "web-01", "cpu", 1, "ops"); err != nil {
		t.Fatal(err)
	}
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
	// 入库后审计的保留主口径是时间（AuditDays），Cap 显示的是兜底条数上限。
	if st.Audit.Count != 1 || st.Audit.Cap != audit.MaxRows {
		t.Fatalf("审计统计不符：%+v", st.Audit)
	}
	if st.Security.Cap != security.MaxEvents {
		t.Fatalf("安全事件上限不符：%+v", st.Security)
	}
	if st.TSDB.Error == "" {
		t.Fatal("未配置时序库地址时应给出可读原因")
	}

	// 各类保留天数都为 0 时不做任何清理，但应说明原因
	cfg := DefaultConfig()
	cfg.AcksDays, cfg.ReportsDays, cfg.LogsDays, cfg.AuditDays = 0, 0, 0, 0
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if res := m.CleanupNow(); res.Skipped == "" {
		t.Fatalf("应说明未清理原因：%+v", res)
	}
	// 天数 > 0 但该类数据源未接入时同样是「什么也没做」，也要给原因
	// （否则界面会显示一次「清理完成」，而实际上没有清理任何东西）
	cfg = DefaultConfig()
	cfg.AcksDays, cfg.ReportsDays, cfg.AuditDays = 0, 0, 0
	cfg.LogsDays = 7 // 本测试未给 Manager 注入日志存储 → 该类无法执行
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if res := m.CleanupNow(); res.Skipped == "" {
		t.Fatalf("天数 > 0 但未接入对应数据源时应说明原因：%+v", res)
	}
	// 上次清理结果应出现在状态里
	if m.Status().LastCleanup == nil {
		t.Fatal("状态应带上次清理结果")
	}
}

// TestManager_CleansCentralLogs 集中日志纳入保留清理（C2）：
// 按「来源/日期/节点」的日期分片**整天删除**，当天分片永不删（清理不碰正在写的数据），
// 并在状态里报告实际占用（这是运维开关日志前最想知道的事）。
func TestManager_CleansCentralLogs(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "logs")
	oldDate := time.Now().AddDate(0, 0, -10).Format("2006-01-02")
	today := time.Now().Format("2006-01-02")
	for _, d := range []string{oldDate, today} {
		p := filepath.Join(root, "applog", d)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "n1.log"), []byte(strings.Repeat("x", 256)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store := logstore.New(root, 0)

	cfg := DefaultConfig()
	cfg.AcksDays, cfg.ReportsDays = 0, 0 // 只验证日志类别
	cfg.LogsDays = 7
	m, err := New(filepath.Join(dir, "retention.yaml"), cfg, nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	m.SetLogStore(store)

	if st := m.Status(); st.Logs.Files != 2 || st.Logs.Bytes == 0 || st.Logs.Sources != 1 {
		t.Fatalf("状态应报告日志占用，got %+v", st.Logs)
	}

	res := m.CleanupNow()
	if res.LogsCutoff == 0 {
		t.Fatal("应报告日志类别的清理截止时间")
	}
	if res.LogDirsRemoved != 1 || res.LogFilesRemoved != 1 || res.FreedBytes == 0 {
		t.Fatalf("应删掉 1 个过期日期分片并报告释放量，got %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "applog", today)); err != nil {
		t.Fatalf("当天分片必须保留：%v", err)
	}
	if st := m.Status(); st.Logs.Days != 1 {
		t.Fatalf("清理后应只剩当天分片，got %+v", st.Logs)
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
// 外部日志后端接管时，"日志没有可清理内容"必须说清是**架构选择**而不是配置缺失：
// 否则界面会显示"日志未接入"，把一次有意的部署说成故障。
func TestManager_ExternalLogBackendIsReported(t *testing.T) {
	dir := t.TempDir()
	m, err := New(filepath.Join(dir, "retention.yaml"), DefaultConfig(), nil, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("创建管理器失败: %v", err)
	}
	m.SetLogStoreExternal("victorialogs")

	if st := m.Status(); st.LogsBackend != "victorialogs" {
		t.Fatalf("状态应报告日志后端标识：%+v", st)
	}
	res := m.CleanupNow()
	if !strings.Contains(res.Skipped, "victorialogs") || !strings.Contains(res.Skipped, "外部后端") {
		t.Fatalf("清理结果应说明日志由外部后端负责：%+v", res)
	}
	// 日志类不参与清理：不得报告删除量（报 0 也容易被读成"清理过了"）
	if res.LogFilesRemoved != 0 || res.LogDirsRemoved != 0 {
		t.Fatalf("外部后端不应参与本地清理：%+v", res)
	}

	// 注入本地存储后回到 local 口径
	store := logstore.New(t.TempDir(), 0)
	m.SetLogStore(store)
	if st := m.Status(); st.LogsBackend != logstore.BackendLocal {
		t.Fatalf("注入本地存储后应报告 local：%+v", st)
	}
	if res := m.CleanupNow(); strings.Contains(res.Skipped, "victorialogs") {
		t.Fatalf("本地存储不应再报外部后端：%+v", res)
	}
}

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
