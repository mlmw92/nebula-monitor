package ops

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/asset"
)

// openOpsTestDB 用**真实台账库**建表：三张 ops 表的 DDL 就在 asset 的 schemaStatements 里，
// 走 asset.Open 而不是手抄建表语句，顺带验证"表真的建出来了"。
func openOpsTestDB(t *testing.T) *asset.Store {
	t.Helper()
	st, err := asset.Open(filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatalf("打开台账库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// 回填既有 ops_tasks.json（任务 + 能力 + 序号）→ 入库；原文件改名 .bak-migrated；重复启动不重复导入。
//
// 序号（seq）必须一起搬：否则回填后新建的任务会从 1 开始，与既有 ops-1、ops-2 **撞 ID**——
// 那会让"整批取消"或回执匹配到错的任务，是这批改动最危险的一处。
func TestStoreUseSQLiteBackfillsTasksCapsSeq(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "ops_tasks.json")
	snap := map[string]any{
		"seq":  7,
		"caps": map[string][]string{"web-01": {KindSvcStatus, KindNodeDiagnostics}},
		"tasks": []map[string]any{
			{"id": "ops-1", "node": "web-01", "kind": KindSvcStatus, "state": "queued", "createdAt": 100},
			{"id": "ops-2", "node": "web-02", "kind": KindSvcStatus, "state": "succeeded", "createdAt": 200, "doneAt": 300},
		},
	}
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("造数据失败: %v", err)
	}
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	s := NewStore(jsonPath)
	db := openOpsTestDB(t)
	if err := s.UseSQLite(db.DB()); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	if got := len(s.List(ListFilter{})); got != 2 {
		t.Fatalf("回填后应有 2 条任务，实际 %d", got)
	}
	if got := s.Caps("web-01"); len(got) != 2 {
		t.Fatalf("回填后能力应为 2 项，实际 %v", got)
	}
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Fatal("回填后原 JSON 应已改名")
	}
	if _, err := os.Stat(jsonPath + opsMigratedSuffix); err != nil {
		t.Fatalf("应保留 %s 作为证据（也是回滚旧版本时恢复可见的办法）: %v", opsMigratedSuffix, err)
	}

	// 序号接着走
	if created := s.Create(model.OpsCommand{Node: "web-03", Kind: KindSvcStatus}, "ops", "127.0.0.1", ""); created.ID != "ops-8" {
		t.Fatalf("序号应从 7 接着走（避免与既有 ID 撞车），实际 %q", created.ID)
	}

	// 重启：以库为准重建内存，且不重复导入
	reloaded := NewStore(jsonPath)
	if err := reloaded.UseSQLite(db.DB()); err != nil {
		t.Fatalf("重启加载失败: %v", err)
	}
	if got := len(reloaded.List(ListFilter{})); got != 3 {
		t.Fatalf("重启后应有 3 条任务（2 条回填 + 1 条新建），实际 %d", got)
	}
	if got := reloaded.Caps("web-01"); len(got) != 2 {
		t.Fatalf("重启后能力应仍在，实际 %v", got)
	}
}

// 写入是**行级差量**：状态流转只更新那一行、删除只删那一行。
// 断言一律直接查库（绕过内存缓存），否则测的只是内存。
func TestStoreSQLiteIncrementalWrites(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "ops_tasks.json"))
	db := openOpsTestDB(t)
	if err := s.UseSQLite(db.DB()); err != nil {
		t.Fatalf("切入库模式失败: %v", err)
	}
	countRows := func() int {
		t.Helper()
		var n int
		if err := db.DB().QueryRow("SELECT COUNT(*) FROM ops_tasks").Scan(&n); err != nil {
			t.Fatalf("查库失败: %v", err)
		}
		return n
	}

	created := s.Create(model.OpsCommand{Node: "web-01", Kind: KindSvcStatus}, "ops", "127.0.0.1", "验证")
	if countRows() != 1 {
		t.Fatalf("创建应落库 1 行，实际 %d", countRows())
	}

	// 状态流转：库里那一行的 state 要跟着变（不查状态常量，直接与返回的 Task 对齐）
	cancelled, err := s.Cancel(created.ID, "ops")
	if err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	var state string
	if err := db.DB().QueryRow("SELECT state FROM ops_tasks WHERE id=?", created.ID).Scan(&state); err != nil {
		t.Fatalf("查库失败: %v", err)
	}
	if state != cancelled.State {
		t.Fatalf("库里状态应与内存一致（%q），实际 %q", cancelled.State, state)
	}

	// 删除：行要消失
	if err := s.Remove(created.ID, "ops"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if countRows() != 0 {
		t.Fatalf("删除后应为 0 行，实际 %d", countRows())
	}

	// 能力：落库后可读回
	s.SaveCaps("web-01", []string{KindSvcStatus})
	if got := s.Caps("web-01"); len(got) != 1 || got[0] != KindSvcStatus {
		t.Fatalf("能力应落库并可读回，实际 %v", got)
	}
	// 重启后能力仍在（证明真的写进了库，而不是只改了内存）
	reloaded := NewStore(filepath.Join(t.TempDir(), "unused.json"))
	if err := reloaded.UseSQLite(db.DB()); err != nil {
		t.Fatalf("重启加载失败: %v", err)
	}
	if got := reloaded.Caps("web-01"); len(got) != 1 {
		t.Fatalf("重启后能力应仍在，实际 %v", got)
	}
}
