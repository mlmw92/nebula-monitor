package asset

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// 存量 v1 库（asset_links 没有 source 列）必须能平滑升到 v2。
//
// 这里刻意不按版本号判断、而是查列是否存在来补列：`CREATE TABLE IF NOT EXISTS`
// 对已存在的表什么都不做，所以「新加的列」永远不会自动出现，必须显式 ALTER。
// 该用例同时覆盖补列路径，避免它只在真实升级那一刻才第一次被执行。
func TestMigrateLegacyDBWithoutLinkSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assets.db")

	// 构造一个 v1 形态的库：asset_links 无 source 列，且已有一条存量边。
	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE asset_types(key TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', builtin INTEGER NOT NULL DEFAULT 0, schema TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE assets(
			id INTEGER PRIMARY KEY AUTOINCREMENT, type_key TEXT NOT NULL, natural_key TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '', node TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, UNIQUE(type_key, natural_key))`,
		// 关键：没有 source 列，这就是 v1 的样子
		`CREATE TABLE asset_links(
			id INTEGER PRIMARY KEY AUTOINCREMENT, from_id INTEGER NOT NULL, to_id INTEGER NOT NULL,
			kind TEXT NOT NULL, created_at INTEGER NOT NULL, UNIQUE(from_id, to_id, kind))`,
		`INSERT INTO asset_links(from_id,to_id,kind,created_at) VALUES(1,2,'runs_on',123)`,
		`PRAGMA user_version=1`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("构造 v1 库失败: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭临时库失败: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开并迁移 v1 库失败: %v", err)
	}
	defer func() { _ = store.Close() }()

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("读取库版本失败: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("迁移后库版本应为 %d，实际 %d", schemaVersion, version)
	}

	// 存量边一律视为采集所得：它们确实都是采集建的，语义上没有变化。
	var source string
	if err := store.db.QueryRow(
		`SELECT source FROM asset_links WHERE from_id=1 AND to_id=2`).Scan(&source); err != nil {
		t.Fatalf("读取存量边的来源失败（source 列可能没补上）: %v", err)
	}
	if source != string(SourceDiscovery) {
		t.Fatalf("存量边的来源应为 discovery，实际 %q", source)
	}

	// 再打开一次：确认补列是幂等的（第二次不应因「列已存在」而报错）。
	second, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开同一库失败（补列不幂等）: %v", err)
	}
	defer func() { _ = second.Close() }()
}
