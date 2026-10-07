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

// 存量库补 request_id 列：**表已存在但没有这一列**是最容易出事的一条升级路径。
//
// 新库由 schemaStatements 直接带上新列，存量库只能靠 ALTER 补——所以依赖新列的
// 部分索引必须排在补列**之后**（见 postAlterIndexes）：放进 schemaStatements 的话，
// 存量库会在建索引那一步报 "no such column"，把整个服务打成起不动。
// 本用例用旧形态的两张表把这个顺序钉住，并且顺带验证存量行留空、新行可写可查。
func TestMigrateAddsRequestIDColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assets.db")
	legacy, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("打开临时库失败: %v", err)
	}
	for _, stmt := range []string{
		// 旧形态：两张表都没有 request_id
		`CREATE TABLE asset_changes(
			id INTEGER PRIMARY KEY AUTOINCREMENT, asset_id INTEGER NOT NULL, field TEXT NOT NULL,
			old_value TEXT NOT NULL DEFAULT '', new_value TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '', actor TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL, at INTEGER NOT NULL)`,
		`CREATE TABLE audit_events(
			id INTEGER PRIMARY KEY AUTOINCREMENT, time_ms INTEGER NOT NULL,
			user_name TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0,
			remote_ip TEXT NOT NULL DEFAULT '', source_loc TEXT NOT NULL DEFAULT '',
			succeeded INTEGER NOT NULL DEFAULT 0, category TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '', detail TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO asset_changes(asset_id,field,old_value,new_value,source,actor,kind,at)
		 VALUES(1,'cpuCores','4','8','manual','alice','update',1000)`,
		`INSERT INTO audit_events(time_ms,user_name,method,path,status,succeeded,category)
		 VALUES(1000,'alice','PUT','/api/v1/assets/1',200,1,'management')`,
	} {
		if _, err := legacy.Exec(stmt); err != nil {
			t.Fatalf("构造存量库失败: %v", err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("关闭临时库失败: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("打开并迁移存量库失败（多半是索引建在了补列之前）: %v", err)
	}
	defer func() { _ = store.Close() }()

	// 存量行留空：不按时间与操作人回填（那会造出看着像证据、实际靠猜的关联）。
	var legacyID string
	if err := store.db.QueryRow(`SELECT request_id FROM asset_changes WHERE at=1000`).Scan(&legacyID); err != nil {
		t.Fatalf("读取存量变更的关联列失败（列可能没补上）: %v", err)
	}
	if legacyID != "" {
		t.Fatalf("存量变更的关联 id 应为空，实际 %q", legacyID)
	}
	if err := store.db.QueryRow(`SELECT request_id FROM audit_events WHERE time_ms=1000`).Scan(&legacyID); err != nil {
		t.Fatalf("读取存量审计的关联列失败（列可能没补上）: %v", err)
	}

	// 依赖新列的部分索引必须真的建出来了。
	var indexes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index'
		AND name IN ('idx_changes_request','idx_audit_events_request')`).Scan(&indexes); err != nil {
		t.Fatalf("查询索引失败: %v", err)
	}
	if indexes != 2 {
		t.Fatalf("两条按关联 id 的索引都应建出来，实际 %d 条", indexes)
	}

	// 新行可写：补列不是"能打开就算过"，写入路径也要真能用。
	// 反向查询要 JOIN assets（拿资产身份），所以先建一台真实资产。
	svc := NewService(store)
	host, _, err := svc.Apply(hostObservation("migrated-01", nil))
	if err != nil {
		t.Fatalf("建档失败: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO asset_changes(asset_id,field,old_value,new_value,source,actor,kind,at,request_id)
		VALUES(?,'env','','prod','manual','alice','update',2000,'deadbeefdeadbeef')`, host.ID); err != nil {
		t.Fatalf("向补列后的表写入失败: %v", err)
	}
	items, truncated, err := store.changesByRequest("deadbeefdeadbeef", ChangeScope{}, 0)
	if err != nil {
		t.Fatalf("按关联 id 查询失败: %v", err)
	}
	if truncated || len(items) != 1 || items[0].NaturalKey != "migrated-01" {
		t.Fatalf("应查到 migrated-01 的 1 条，实际 %d 条 truncated=%v", len(items), truncated)
	}
}
