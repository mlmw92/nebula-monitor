package asset

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	// 纯 Go 的 SQLite 实现：不引入 CGO，交叉编译（linux amd64/arm64/arm）与离线包不受影响。
	// 决策记录见 docs/adr/0001-cmdb-relational-store.md。
	_ "modernc.org/sqlite"
)

// schemaVersion 是资产库结构版本，写入 PRAGMA user_version。
// 只允许递增；读到更高版本直接拒绝启动，避免新库被旧程序写坏。
const schemaVersion = 1

// schemaStatements 是幂等的建表语句。每次启动都执行：`IF NOT EXISTS` 保证重复执行无副作用，
// 同时能修复被手工删表的库。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS asset_types(
		key     TEXT PRIMARY KEY,
		title   TEXT NOT NULL DEFAULT '',
		builtin INTEGER NOT NULL DEFAULT 0,
		schema  TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE TABLE IF NOT EXISTS assets(
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		type_key    TEXT NOT NULL REFERENCES asset_types(key),
		natural_key TEXT NOT NULL,
		name        TEXT NOT NULL DEFAULT '',
		node        TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL,
		updated_at  INTEGER NOT NULL,
		UNIQUE(type_key, natural_key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_assets_node ON assets(node)`,
	`CREATE INDEX IF NOT EXISTS idx_assets_type ON assets(type_key)`,
	// 联合主键含 source：同一 key 的采集值与人工值共存，互不覆盖。
	`CREATE TABLE IF NOT EXISTS asset_attrs(
		asset_id   INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL DEFAULT '',
		source     TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		updated_by TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(asset_id, key, source)
	)`,
	`CREATE TABLE IF NOT EXISTS asset_links(
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id    INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		to_id      INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		kind       TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE(from_id, to_id, kind)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_links_from ON asset_links(from_id)`,
	`CREATE INDEX IF NOT EXISTS idx_links_to ON asset_links(to_id)`,
	`CREATE TABLE IF NOT EXISTS asset_changes(
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		asset_id  INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		field     TEXT NOT NULL,
		old_value TEXT NOT NULL DEFAULT '',
		new_value TEXT NOT NULL DEFAULT '',
		source    TEXT NOT NULL DEFAULT '',
		actor     TEXT NOT NULL DEFAULT '',
		kind      TEXT NOT NULL,
		at        INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_changes_asset ON asset_changes(asset_id, at DESC)`,
	`CREATE TABLE IF NOT EXISTS snapshots(
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		asset_id INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		taken_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_snapshots_asset ON snapshots(asset_id, taken_at DESC)`,
	`CREATE TABLE IF NOT EXISTS snapshot_fields(
		snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
		key         TEXT NOT NULL,
		value       TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(snapshot_id, key)
	)`,
}

// Store 是资产领域的 SQLite 持久化适配器。
//
// 它是 Service 的实现细节，不直接对外暴露（模块对外的接口只有 Service）。
type Store struct {
	db *sql.DB
}

// Open 打开（必要时创建）资产库并执行幂等迁移。
func Open(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("资产库路径不能为空")
	}
	// 单文件内嵌库：把并发写收成单连接，避免多写者下 SQLITE_BUSY 抖动；
	// WAL 让读不阻塞写，busy_timeout 兜住偶发争用。
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开资产库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// Close 释放数据库句柄。
func (s *Store) Close() error { return s.db.Close() }

// migrate 建表并播种内置类型。
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("读取资产库版本失败: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("资产库版本 %d 高于本程序支持的 %d，请先升级服务端", version, schemaVersion)
	}
	for _, stmt := range schemaStatements {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("资产库建表失败: %w", err)
		}
	}
	for _, t := range BuiltinTypes() {
		// 内置类型每次启动对齐标题：类型行被手工删除后也能自愈，
		// 否则会出现「资产引用不存在的类型」这种外键报错。
		if _, err := s.db.Exec(
			`INSERT INTO asset_types(key,title,builtin) VALUES(?,?,1)
			 ON CONFLICT(key) DO UPDATE SET title=excluded.title`,
			t.Key, t.Title,
		); err != nil {
			return fmt.Errorf("播种内置资产类型失败: %w", err)
		}
	}
	if version != schemaVersion {
		if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version=%d", schemaVersion)); err != nil {
			return fmt.Errorf("写入资产库版本失败: %w", err)
		}
	}
	return nil
}

// typeExists 判断资产类型是否已注册。
func (s *Store) typeExists(key string) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM asset_types WHERE key=?`, key).Scan(&n); err != nil {
		return false, fmt.Errorf("查询资产类型失败: %w", err)
	}
	return n > 0, nil
}

// assetByNatural 按（类型 + 自然键）取资产，找不到返回 ok=false。
func (s *Store) assetByNatural(typeKey, naturalKey string) (Asset, bool, error) {
	var a Asset
	err := s.db.QueryRow(
		`SELECT id,type_key,natural_key,name,node,created_at,updated_at
		 FROM assets WHERE type_key=? AND natural_key=?`, typeKey, naturalKey,
	).Scan(&a.ID, &a.TypeKey, &a.NaturalKey, &a.Name, &a.Node, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, false, nil
	}
	if err != nil {
		return Asset{}, false, fmt.Errorf("查询资产失败: %w", err)
	}
	attrs, err := s.attrsOf(a.ID)
	if err != nil {
		return Asset{}, false, err
	}
	a.Attrs = attrs
	return a, true, nil
}

// assetByID 按主键取资产。
func (s *Store) assetByID(id int64) (Asset, bool, error) {
	var typeKey, naturalKey string
	err := s.db.QueryRow(`SELECT type_key,natural_key FROM assets WHERE id=?`, id).Scan(&typeKey, &naturalKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Asset{}, false, nil
	}
	if err != nil {
		return Asset{}, false, fmt.Errorf("查询资产失败: %w", err)
	}
	return s.assetByNatural(typeKey, naturalKey)
}

// attrsOf 取资产的属性集合，返回以「属性 key + 来源」为键的 map。
func (s *Store) attrsOf(assetID int64) (map[string]Attr, error) {
	rows, err := s.db.Query(
		`SELECT key,value,source,updated_at,updated_by FROM asset_attrs WHERE asset_id=? ORDER BY key,source`, assetID)
	if err != nil {
		return nil, fmt.Errorf("查询资产属性失败: %w", err)
	}
	defer rows.Close()
	out := map[string]Attr{}
	for rows.Next() {
		var key, value, source, by string
		var at int64
		if err := rows.Scan(&key, &value, &source, &at, &by); err != nil {
			return nil, fmt.Errorf("读取资产属性失败: %w", err)
		}
		src := Source(source)
		out[attrID(key, src)] = Attr{Key: key, Value: value, Source: src, UpdatedAt: at, UpdatedBy: by}
	}
	return out, rows.Err()
}

// insertAsset 新建资产，返回自增主键。
func (s *Store) insertAsset(a Asset) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO assets(type_key,natural_key,name,node,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		a.TypeKey, a.NaturalKey, a.Name, a.Node, a.CreatedAt, a.UpdatedAt)
	if err != nil {
		return 0, fmt.Errorf("新建资产失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取资产主键失败: %w", err)
	}
	return id, nil
}

// touchAsset 更新资产的可变字段与时间戳。
func (s *Store) touchAsset(id int64, name, node string, at int64) error {
	if _, err := s.db.Exec(`UPDATE assets SET name=?,node=?,updated_at=? WHERE id=?`, name, node, at, id); err != nil {
		return fmt.Errorf("更新资产失败: %w", err)
	}
	return nil
}

// upsertAttr 写入一条属性值（同一 key + 来源覆盖写）。
func (s *Store) upsertAttr(assetID int64, a Attr) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_attrs(asset_id,key,value,source,updated_at,updated_by) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(asset_id,key,source) DO UPDATE SET
		   value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		assetID, a.Key, a.Value, string(a.Source.normalized()), a.UpdatedAt, a.UpdatedBy,
	); err != nil {
		return fmt.Errorf("写入资产属性失败: %w", err)
	}
	return nil
}

// appendChange 追加一条变更记录。
func (s *Store) appendChange(c ChangeRecord) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_changes(asset_id,field,old_value,new_value,source,actor,kind,at) VALUES(?,?,?,?,?,?,?,?)`,
		c.AssetID, c.Field, c.Old, c.New, string(c.Source), c.Actor, string(c.Kind), c.At,
	); err != nil {
		return fmt.Errorf("写入资产变更记录失败: %w", err)
	}
	return nil
}

// changesOf 取资产的变更记录（时间倒序）。
func (s *Store) changesOf(assetID int64, limit int) ([]ChangeRecord, error) {
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	rows, err := s.db.Query(
		`SELECT id,field,old_value,new_value,source,actor,kind,at FROM asset_changes
		 WHERE asset_id=? ORDER BY at DESC, id DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询资产变更记录失败: %w", err)
	}
	defer rows.Close()
	var out []ChangeRecord
	for rows.Next() {
		c := ChangeRecord{AssetID: assetID}
		var source, actor, kind string
		if err := rows.Scan(&c.ID, &c.Field, &c.Old, &c.New, &source, &actor, &kind, &c.At); err != nil {
			return nil, fmt.Errorf("读取资产变更记录失败: %w", err)
		}
		c.Source, c.Actor, c.Kind = Source(source), actor, ChangeKind(kind)
		out = append(out, c)
	}
	return out, rows.Err()
}

// assetWhere 构造资产筛选的 WHERE 子句（不含 ORDER BY / LIMIT），alias 为表别名。
//
// 列表与计数**必须共用同一条件**：页大小与总数若来自两套条件，「共 N 条」与实际能翻到的
// 条数就会对不上（此前 total 直接取当前页长度，翻页永远只有一页的量）。
func assetWhere(alias string, f ListFilter) (string, []any) {
	q := " WHERE 1=1"
	args := []any{}
	if f.TypeKey != "" {
		q += " AND " + alias + ".type_key=?"
		args = append(args, f.TypeKey)
	}
	if f.Node != "" {
		q += " AND " + alias + ".node=?"
		args = append(args, f.Node)
	}
	if kw := strings.TrimSpace(f.Keyword); kw != "" {
		q += " AND (" + alias + ".natural_key LIKE ? OR " + alias + ".name LIKE ?)"
		like := "%" + kw + "%"
		args = append(args, like, like)
	}
	if f.Nodes != nil {
		// 资源范围下推：把「可见节点集合」写进 SQL，而不是取回一页再到内存里过滤。
		// 后者有两个后果：页内被过滤掉的空位不会补人（一页 50 条可能只显示 40 条），
		// 且总数只能数到当前页——受限用户的翻页体验与「共 N 条」都会失真。
		if len(f.Nodes) == 0 {
			// 空集合（受限但无任何可见节点）：恒不匹配。绝不退化成「不过滤」——
			// 那是把资源范围校验变成越权旁路。
			q += " AND 1=0"
			return q, args
		}
		q += " AND " + alias + ".node IN (" + placeholders(len(f.Nodes)) + ")"
		for _, n := range f.Nodes {
			args = append(args, n)
		}
	}
	return q, args
}

// placeholders 生成 n 个占位符（"?,?,?"），用于 IN 子句。
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// listAssets 按条件列出资产（含属性），按「类型 + 自然键」稳定排序。
func (s *Store) listAssets(f ListFilter) ([]Asset, error) {
	sub, args := assetWhere("a2", f)
	// 分页必须作用于**资产**而不是 join 后的行：一个资产有几条属性就有几行，
	// 直接对 join 结果 LIMIT 会让「一页 50 条」变成「一页 50 行」——属性多的资产
	// 会挤占同页额度（10 条属性就吃掉一页的 1/5），OFFSET 也随之漂移。
	// 因此先用子查询选出本页的资产 ID，再取这些资产的属性。
	q := `SELECT a.id,a.type_key,a.natural_key,a.name,a.node,a.created_at,a.updated_at,
	             t.key,t.value,t.source,t.updated_at,t.updated_by
	      FROM assets a LEFT JOIN asset_attrs t ON t.asset_id=a.id
	      WHERE a.id IN (SELECT a2.id FROM assets a2` + sub + ` ORDER BY a2.type_key,a2.natural_key LIMIT ? OFFSET ?)
	      ORDER BY a.type_key,a.natural_key,t.key,t.source`
	args = append(args, f.limit(), f.offset())

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询资产列表失败: %w", err)
	}
	defer rows.Close()

	var order []int64
	byID := map[int64]*Asset{}
	for rows.Next() {
		var a Asset
		var key, value, source, by sql.NullString
		var attrAt sql.NullInt64
		if err := rows.Scan(&a.ID, &a.TypeKey, &a.NaturalKey, &a.Name, &a.Node,
			&a.CreatedAt, &a.UpdatedAt, &key, &value, &source, &attrAt, &by); err != nil {
			return nil, fmt.Errorf("读取资产列表失败: %w", err)
		}
		cur, ok := byID[a.ID]
		if !ok {
			a.Attrs = map[string]Attr{}
			cur = &a
			byID[a.ID] = cur
			order = append(order, a.ID)
		}
		if key.Valid {
			src := Source(source.String)
			cur.Attrs[attrID(key.String, src)] = Attr{
				Key: key.String, Value: value.String, Source: src,
				UpdatedAt: attrAt.Int64, UpdatedBy: by.String,
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取资产列表失败: %w", err)
	}
	out := make([]Asset, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// countAssets 统计符合条件的资产数量（忽略分页），供列表接口给出真实总数。
//
// 与 listAssets 共用 assetWhere，因此「共 N 条」与能翻到的条数必然一致；
// 走 assets 单表、不 join 属性，代价与页大小无关。
func (s *Store) countAssets(f ListFilter) (int, error) {
	where, args := assetWhere("a", f)
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM assets a`+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计资产数量失败: %w", err)
	}
	return n, nil
}

// linkAssets 建立关联；重复建立视为成功（幂等）。
func (s *Store) linkAssets(fromID, toID int64, kind LinkKind, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_links(from_id,to_id,kind,created_at) VALUES(?,?,?,?)
		 ON CONFLICT(from_id,to_id,kind) DO NOTHING`,
		fromID, toID, string(kind), at,
	); err != nil {
		return fmt.Errorf("建立资产关联失败: %w", err)
	}
	return nil
}

// unlinkAssets 解除关联；不存在时视为成功（幂等）。
func (s *Store) unlinkAssets(fromID, toID int64, kind LinkKind) error {
	if _, err := s.db.Exec(
		`DELETE FROM asset_links WHERE from_id=? AND to_id=? AND kind=?`, fromID, toID, string(kind)); err != nil {
		return fmt.Errorf("解除资产关联失败: %w", err)
	}
	return nil
}

// linksOf 取与某资产直接相关的关联（出边与入边都返回）。
func (s *Store) linksOf(assetID int64) ([]Link, error) {
	rows, err := s.db.Query(
		`SELECT l.id,l.kind,l.created_at,
		        f.type_key,f.natural_key,t.type_key,t.natural_key
		 FROM asset_links l
		 JOIN assets f ON f.id=l.from_id
		 JOIN assets t ON t.id=l.to_id
		 WHERE l.from_id=? OR l.to_id=?
		 ORDER BY l.kind,f.natural_key,t.natural_key`, assetID, assetID)
	if err != nil {
		return nil, fmt.Errorf("查询资产关联失败: %w", err)
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		var kind string
		if err := rows.Scan(&l.ID, &kind, &l.CreatedAt,
			&l.From.TypeKey, &l.From.NaturalKey, &l.To.TypeKey, &l.To.NaturalKey); err != nil {
			return nil, fmt.Errorf("读取资产关联失败: %w", err)
		}
		l.Kind = LinkKind(kind)
		out = append(out, l)
	}
	return out, rows.Err()
}

// recordSnapshot 在同一事务里写入快照头与字段，避免出现「有头无字段」的半截数据。
func (s *Store) recordSnapshot(assetID, takenAt int64, fields map[string]string) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("写入配置快照失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(`INSERT INTO snapshots(asset_id,taken_at) VALUES(?,?)`, assetID, takenAt)
	if err != nil {
		return 0, fmt.Errorf("写入配置快照失败: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取配置快照主键失败: %w", err)
	}
	for key, value := range fields {
		if _, err := tx.Exec(`INSERT INTO snapshot_fields(snapshot_id,key,value) VALUES(?,?,?)
			ON CONFLICT(snapshot_id,key) DO UPDATE SET value=excluded.value`, id, key, value); err != nil {
			return 0, fmt.Errorf("写入配置快照字段失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交配置快照失败: %w", err)
	}
	return id, nil
}

// snapshotsOf 取资产的快照头（时间倒序，不含字段）。
func (s *Store) snapshotsOf(assetID int64, limit int) ([]Snapshot, error) {
	if limit <= 0 {
		limit = defaultSnapshotLimit
	}
	rows, err := s.db.Query(
		`SELECT id,taken_at FROM snapshots WHERE asset_id=? ORDER BY taken_at DESC, id DESC LIMIT ?`, assetID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询配置快照失败: %w", err)
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		sn := Snapshot{AssetID: assetID}
		if err := rows.Scan(&sn.ID, &sn.TakenAt); err != nil {
			return nil, fmt.Errorf("读取配置快照失败: %w", err)
		}
		out = append(out, sn)
	}
	return out, rows.Err()
}

// snapshotFieldsOf 取某快照的字段集合。
func (s *Store) snapshotFieldsOf(snapshotID int64) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key,value FROM snapshot_fields WHERE snapshot_id=? ORDER BY key`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("查询配置快照字段失败: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("读取配置快照字段失败: %w", err)
		}
		out[key] = value
	}
	return out, rows.Err()
}
