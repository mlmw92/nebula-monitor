package alert

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// openAckTestDB 用**真实台账库**建表：alert_acks 的 DDL 就在 asset 的 schemaStatements 里，
// 走 asset.Open 而不是手抄建表语句，顺带验证"表真的建出来了"。
func openAckTestDB(t *testing.T) *asset.Store {
	t.Helper()
	st, err := asset.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("打开台账库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 回填既有 alert_acks.json → 入库；之后每次处置**只写一行**（不再全量重写），
// 并且内存读缓存与库保持一致。
func TestAckStoreUseSQLiteBackfillsAndUpserts(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "alert_acks.json")
	seed := []AckInfo{
		// 迁移前的老记录没有 status 字段：语义上等于「已认领」
		{Rule: "r1", Host: "h1", Instance: "cpu", StartsAt: 1, User: "ops", Time: 100},
		{Rule: "r2", Host: "h2", Instance: "mem", StartsAt: 2, Status: StatusClosed, User: "ops", Time: 200, CloseReason: "误报"},
	}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("造数据失败: %v", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	store := openAckTestDB(t)
	db := store.DB()
	s := NewAckStore(jsonPath)
	if err := s.UseSQLite(db); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatal("回填后原 JSON 应已改名")
	}
	if _, err := os.Stat(jsonPath + ".bak-migrated"); err != nil {
		t.Fatalf("应保留 .bak-migrated 作为证据: %v", err)
	}
	stats := s.Stats()
	if stats.Total != 2 || stats.Handled != 2 {
		t.Fatalf("回填后统计不符（空 status 应算已认领）：%+v", stats)
	}

	// 处置一条：库里必须**真的**落盘（用库读回来验证，而不是只看内存）
	key := ackKey("r1", "h1", "cpu", 1)
	s.Close("r1", "h1", "cpu", 1, "ops", "误报关闭")
	if _, err := s.Comment("r1", "h1", "cpu", 1, "ops", "已确认"); err != nil {
		t.Fatalf("评论失败: %v", err)
	}
	loaded, err := loadAcksFromDB(db)
	if err != nil {
		t.Fatalf("回读库失败: %v", err)
	}
	got, ok := loaded[key]
	if !ok {
		t.Fatal("库中应有该处置记录")
	}
	if got.Status != StatusClosed || got.CloseReason != "误报关闭" {
		t.Fatalf("关闭信息未落库：%+v", got)
	}
	if len(got.Comments) != 1 || got.Comments[0].Text != "已确认" {
		t.Fatalf("评论未落库：%+v", got.Comments)
	}

	// 幂等：重新加载（模拟重启）后统计不变
	reloaded := NewAckStore(jsonPath)
	if err := reloaded.UseSQLite(db); err != nil {
		t.Fatalf("重启加载失败: %v", err)
	}
	if got := reloaded.Stats().Total; got != 2 {
		t.Fatalf("重启后不应重复导入，实际 %d 条", got)
	}
}

// 保留策略：**待处理（pending）永不删**，已处置按时间删；库与内存同步。
func TestAckStorePruneHandledKeepsPending(t *testing.T) {
	store := openAckTestDB(t)
	db := store.DB()
	s := NewAckStore("")
	if err := s.UseSQLite(db); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	old := int64(1_600_000_000_000) // 很久以前
	// 一条已处置（会被清）、一条待处理（必须留下）
	s.Mark("r1", "h1", "cpu", 1, "ops")
	s.update("r2", "h2", "mem", 2, "ops", func(i *AckInfo) { i.Status = StatusPending })
	s.mu.Lock()
	for k, v := range s.acks {
		v.Time = old
		s.acks[k] = v
	}
	s.mu.Unlock()
	// 库与内存必须一致（生产路径上每次变更都会写 time_ms），否则"按时间清理"会各算各的。
	if _, err := db.Exec("UPDATE alert_acks SET time_ms=?", old); err != nil {
		t.Fatalf("回写时间失败: %v", err)
	}

	if removed := s.PruneHandled(old + 1000); removed != 1 {
		t.Fatalf("应只清掉已处置的那 1 条，实际 %d", removed)
	}
	loaded, err := loadAcksFromDB(db)
	if err != nil {
		t.Fatalf("回读库失败: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("库里应只剩待处理那 1 条，实际 %d", len(loaded))
	}
	if _, ok := loaded[ackKey("r2", "h2", "mem", 2)]; !ok {
		t.Fatalf("待处理记录被误删：%+v", loaded)
	}
}
