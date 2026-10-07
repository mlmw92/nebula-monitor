package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/server/asset"
)

// openTestDB 用**真实台账库**建表：两张新表的 DDL 就在 asset 的 schemaStatements 里，
// 拿 asset.Open 而不是手抄建表语句，才能顺带验证"DLL 真的建出来了"。
func openTestDB(t *testing.T) *asset.Store {
	t.Helper()
	st, err := asset.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("打开台账库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 回填必须搬走**文件里的全部**事件（而不是 New 加载时截尾后的最近 MaxEvents 条），
// 且只发生一次：原文件改名 .bak-migrated 作为证据，重复启动不重复导入。
func TestUseSQLiteBackfillsAllEventsOnce(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "audit_events.json")
	const total = 2100 // 刻意超过 MaxEvents，验证"不是把内存里那份搬过去"
	events := make([]Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, Event{
			Time: time.UnixMilli(1_700_000_000_000 + int64(i)), User: "ops",
			Method: "POST", Path: "/api/v1/assets", Status: 200, Succeeded: true, Category: "asset",
		})
	}
	data, err := json.Marshal(events)
	if err != nil {
		t.Fatalf("造数据失败: %v", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	store := New(jsonPath)
	db := openTestDB(t)
	if err := store.UseSQLite(db.DB()); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	if got := store.Count(); got != total {
		t.Fatalf("回填应搬走全部 %d 条，实际 %d", total, got)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatal("回填后原 JSON 应已改名")
	}
	if _, err := os.Stat(jsonPath + migratedSuffix); err != nil {
		t.Fatalf("应保留 %s 作为证据（也是回滚旧版本时恢复可见的办法）: %v", migratedSuffix, err)
	}

	// 幂等：再走一遍（判据是「表为空」，不是「文件存在」）
	again := New(jsonPath)
	if err := again.UseSQLite(db.DB()); err != nil {
		t.Fatalf("第二次切入库模式失败: %v", err)
	}
	if got := again.Count(); got != total {
		t.Fatalf("重复启动不应重复导入，实际 %d", got)
	}

	// 入库模式下的写入与分页查询
	if err := again.Record(Event{Time: time.Now(), User: "ops", Path: "/api/v1/assets/1", Category: "asset"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	page, count, err := again.Query(QueryFilter{Category: "asset", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if count != total+1 {
		t.Fatalf("同条件总数应为 %d，实际 %d", total+1, count)
	}
	if len(page) != 10 {
		t.Fatalf("分页应返回 10 条，实际 %d", len(page))
	}
	// 时间倒序：第一条应是刚写进去的那条
	if page[0].Path != "/api/v1/assets/1" {
		t.Fatalf("应按时间倒序，首条实际 %q", page[0].Path)
	}
	// 路径子串过滤沿用既有语义
	hit, hitTotal, err := again.Query(QueryFilter{Path: "/assets/1", Limit: 10})
	if err != nil || hitTotal != 1 || len(hit) != 1 {
		t.Fatalf("路径子串过滤失败: total=%d len=%d err=%v", hitTotal, len(hit), err)
	}
}

// 时间保留是主口径、条数是兜底：两者都要能真的删掉行。
func TestPruneByTimeAndRows(t *testing.T) {
	store := New("")
	db := openTestDB(t)
	if err := store.UseSQLite(db.DB()); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	base := time.Now().AddDate(0, 0, -200).UnixMilli() // 200 天前
	for i := 0; i < 5; i++ {
		if err := store.Record(Event{Time: time.UnixMilli(base + int64(i)*1000), User: "ops"}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	if err := store.Record(Event{Time: time.Now(), User: "ops"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	// 时间口径：180 天前的 5 条被删，今天那条留下
	removed, err := store.Prune(time.Now().AddDate(0, 0, -180).UnixMilli())
	if err != nil {
		t.Fatalf("按时间清理失败: %v", err)
	}
	if removed != 5 || store.Count() != 1 {
		t.Fatalf("按时间应删 5 条、剩 1 条，实际 removed=%d count=%d", removed, store.Count())
	}

	// 条数兜底：再写 3 条，裁到最多 2 行（保留最新的）
	for i := 0; i < 3; i++ {
		if err := store.Record(Event{Time: time.Now(), User: "ops"}); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	rows, err := store.PruneRows(2)
	if err != nil {
		t.Fatalf("按条数裁剪失败: %v", err)
	}
	if rows != 2 || store.Count() != 2 {
		t.Fatalf("兜底裁剪应删 2 条、剩 2 条，实际 rows=%d count=%d", rows, store.Count())
	}
}

// 按关联 id 过滤（入库模式）：这是"从一条资产变更跳到那次操作"的查询路径。
// 顺带钉住"精确匹配"这个决定——关联 id 是不透明句柄，子串匹配会把相邻操作一并带出来。
func TestQueryFilterByRequestIDOnSQLite(t *testing.T) {
	store := New("")
	db := openTestDB(t)
	if err := store.UseSQLite(db.DB()); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	for _, ev := range []Event{
		{User: "alice", Method: "PUT", Path: "/api/v1/assets/1", Category: "management", RequestID: "rid-1"},
		{User: "alice", Method: "PUT", Path: "/api/v1/assets/2", Category: "management", RequestID: "rid-2"},
		{User: "agent", Method: "POST", Path: "/api/v1/report", Category: "management"}, // 上报：无关联 id
	} {
		if err := store.Record(ev); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}

	events, total, err := store.Query(QueryFilter{RequestID: "rid-1"})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if total != 1 || len(events) != 1 || events[0].RequestID != "rid-1" {
		t.Fatalf("按关联 id 应命中 1 条，实际 total=%d len=%d %#v", total, len(events), events)
	}
	if events[0].Path != "/api/v1/assets/1" {
		t.Fatalf("命中的应是 rid-1 那条，实际 %q", events[0].Path)
	}

	// 子串不算命中，空 id 也不该被当成条件把"无关联"的行捞出来。
	if _, total, err := store.Query(QueryFilter{RequestID: "rid-"}); err != nil || total != 0 {
		t.Fatalf("关联 id 应精确匹配，实际 total=%d err=%v", total, err)
	}
	if _, total, err := store.Query(QueryFilter{RequestID: ""}); err != nil || total != 3 {
		t.Fatalf("空关联 id 表示不过滤，实际 total=%d err=%v", total, err)
	}
}

// 坏文件**不阻断启动、也不改名**：宁可丢历史，也不能因为一份坏 JSON 让服务起不来，
// 更不能把现场改没了。
func TestUseSQLiteKeepsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "audit_events.json")
	if err := os.WriteFile(jsonPath, []byte("{不是数组}"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	store := New(jsonPath)
	db := openTestDB(t)
	if err := store.UseSQLite(db.DB()); err == nil {
		t.Fatal("坏文件应返回错误（由调用方记日志）")
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatalf("坏文件必须保留现场: %v", err)
	}
	// 返回错误后仍可按降级模式工作（内存 + JSON），不影响主链路
	if err := store.Record(Event{Time: time.Now(), User: "ops"}); err != nil {
		t.Fatalf("降级模式写入失败: %v", err)
	}
	if store.Count() != 1 {
		t.Fatalf("降级模式应记 1 条，实际 %d", store.Count())
	}
}
