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
	// asset_seen 只记「最后一次采集上报是什么时候」，每个资产一行，每轮上报覆盖写。
	//
	// 为什么不能复用资产属性行的 updated_at：Apply 只在值发生变化时写属性行，
	// 而主机的 os/cpuCores、中间件的 version/topology 长期不变，于是"最近上报"会永久冻结在
	// 最后一次变更时刻——超过阈值后这些资产会被整批误判为失联（真实故障）。
	// 本表与「值是否变化」无关，因此必须独立存在。
	//
	// 纯附加表：老版本程序不会读它，所以 **不** 提升 schemaVersion——保留「回滚旧版本时
	// assets.db 可直接沿用」这一既定运维性质。
	`CREATE TABLE IF NOT EXISTS asset_seen(
		asset_id     INTEGER PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
		last_seen_at INTEGER NOT NULL
	)`,
	// 差异巡检（inspect）：一次运行 + 若干差异项 + 每个资产类型最多一个「标杆」。
	//
	// 差异项**不加外键级联**：巡检记录是历史证据，资产后来被删除也不该让结论无法解读，
	// 因此冗余存资产身份字段（type/key/name/node）而非只存 asset_id。
	`CREATE TABLE IF NOT EXISTS inspect_runs(
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		scope       TEXT NOT NULL DEFAULT '',
		actor       TEXT NOT NULL DEFAULT '',
		started_at  INTEGER NOT NULL,
		assets      INTEGER NOT NULL DEFAULT 0,
		baselined   INTEGER NOT NULL DEFAULT 0,
		findings    INTEGER NOT NULL DEFAULT 0,
		truncated   INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inspect_runs_at ON inspect_runs(started_at DESC)`,
	`CREATE TABLE IF NOT EXISTS inspect_findings(
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id     INTEGER NOT NULL REFERENCES inspect_runs(id) ON DELETE CASCADE,
		asset_id   INTEGER NOT NULL,
		asset_type TEXT NOT NULL DEFAULT '',
		asset_key  TEXT NOT NULL DEFAULT '',
		asset_name TEXT NOT NULL DEFAULT '',
		node       TEXT NOT NULL DEFAULT '',
		field      TEXT NOT NULL DEFAULT '',
		kind       TEXT NOT NULL,
		level      TEXT NOT NULL DEFAULT '',
		expected   TEXT NOT NULL DEFAULT '',
		actual     TEXT NOT NULL DEFAULT '',
		at         INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inspect_findings_run ON inspect_findings(run_id, level, kind)`,
	// inspect_baselines 按**资产类型**存标杆：期望值是「同类资产该长什么样」，
	// 而不是某台机器自己的历史值（后者由快照前后比对覆盖，见 L2）。
	`CREATE TABLE IF NOT EXISTS inspect_baselines(
		type_key    TEXT PRIMARY KEY,
		asset_id    INTEGER NOT NULL,
		asset_key   TEXT NOT NULL DEFAULT '',
		snapshot_id INTEGER NOT NULL,
		set_by      TEXT NOT NULL DEFAULT '',
		set_at      INTEGER NOT NULL
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
	if err := s.seenOf(a.ID, &a); err != nil {
		return Asset{}, false, err
	}
	return a, true, nil
}

// seenOf 读取资产的「最近上报」时刻并写入 a.SeenAt（无记录时保持 0）。
func (s *Store) seenOf(assetID int64, a *Asset) error {
	var seen sql.NullInt64
	err := s.db.QueryRow(`SELECT last_seen_at FROM asset_seen WHERE asset_id=?`, assetID).Scan(&seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("查询资产最近上报时间失败: %w", err)
	}
	a.SeenAt = seen.Int64
	return nil
}

// markSeen 记录一次采集上报。
//
// 用 MAX 取较大值而不是直接覆盖：乱序到达的上报（多 Agent、网络重试）不应把"最近见到"
// 往回拨，否则台账会凭空出现一批失联资产。
func (s *Store) markSeen(assetID int64, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_seen(asset_id,last_seen_at) VALUES(?,?)
		 ON CONFLICT(asset_id) DO UPDATE SET last_seen_at=MAX(last_seen_at, excluded.last_seen_at)`,
		assetID, at,
	); err != nil {
		return fmt.Errorf("记录资产最近上报时间失败: %w", err)
	}
	return nil
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
		// 关键词同时匹配属性值：台账最常见的用法是「拿 IP / 业务名 / 资产编号 找资产」，
		// 只搜名称与自然键会让"明明有这条记录却搜不到"。
		q += " AND (" + alias + ".natural_key LIKE ? OR " + alias + ".name LIKE ?" +
			" OR EXISTS (SELECT 1 FROM asset_attrs v WHERE v.asset_id=" + alias + ".id AND v.value LIKE ?))"
		like := "%" + kw + "%"
		args = append(args, like, like, like)
	}
	switch f.Status {
	case StatusArchived:
		q += " AND NOT " + hasDiscoveryCond(alias)
	case StatusMissing:
		// MAX() 在无采集值时返回 NULL，而 SQL 中 NULL < x 为假：因此这里无需再判"是否被采集过"。
		q += " AND " + lastSeenExpr(alias) + " < ?"
		args = append(args, f.StaleBefore)
	case StatusOnline:
		q += " AND " + lastSeenExpr(alias) + " >= ?"
		args = append(args, f.StaleBefore)
	}
	switch f.Source {
	case SourceFilterAuto:
		q += " AND NOT " + hasManualCond(alias)
	case SourceFilterMixed:
		q += " AND " + conflictCond(alias)
	case SourceFilterManual:
		q += " AND " + hasManualCond(alias) + " AND NOT " + conflictCond(alias)
	}
	if f.OwnerMissing {
		q += " AND " + ownerMissingCond(alias)
	}
	if f.HasConflict {
		q += " AND " + conflictCond(alias)
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

// 以下条件片段全部以「资产 alias」为基准，供列表、计数与摘要共用同一套语义。
// 用子查询而不是把状态/来源落库：它们是采集时间与两种来源共同推导出来的结论，
// 存成一列就必须有人定期刷新，反而会写出「字段说 online、现实已失联」的自相矛盾。

// hasDiscoveryCond 判断资产是否被采集过。
func hasDiscoveryCond(alias string) string {
	return `EXISTS (SELECT 1 FROM asset_attrs d WHERE d.asset_id=` + alias + `.id AND d.source='discovery')`
}

// lastSeenExpr 取最近的**采集上报**时刻（毫秒）；从未上报时为 NULL。
//
// 取自 asset_seen 而非属性行的 MAX(updated_at)：属性行只在值变化时才更新，
// 用它会把"值长期不变"的资产（主机、中间件实例都如此）算成失联。
// NULL 的语义仍与旧实现一致：从未上报 → 任何比较都为假 → 不会落进 online / missing。
func lastSeenExpr(alias string) string {
	return `(SELECT se.last_seen_at FROM asset_seen se WHERE se.asset_id=` + alias + `.id)`
}

// hasManualCond 判断资产是否存在人工值。
func hasManualCond(alias string) string {
	return `EXISTS (SELECT 1 FROM asset_attrs m WHERE m.asset_id=` + alias + `.id AND m.source='manual')`
}

// conflictCond 判断是否存在「同一字段人工值与采集值并存」。
func conflictCond(alias string) string {
	return `EXISTS (SELECT 1 FROM asset_attrs m WHERE m.asset_id=` + alias + `.id AND m.source='manual'
	         AND EXISTS (SELECT 1 FROM asset_attrs d WHERE d.asset_id=m.asset_id AND d.key=m.key AND d.source='discovery'))`
}

// ownerMissingCond 判断人工未指派责任人（空值与纯空白都算未指派）。
func ownerMissingCond(alias string) string {
	return `NOT EXISTS (SELECT 1 FROM asset_attrs o WHERE o.asset_id=` + alias + `.id AND o.source='manual'
	         AND o.key='` + OwnerKey + `' AND TRIM(o.value) <> '')`
}

// assetSelectColumns 是「资产 + 属性」查询共用的列清单。
//
// 抽成常量是为了让 listAssets（分页）与 listAssetsAll（巡检全量）不会有一天列数不一致——
// 那种错误只在 Scan 处才暴露，且报错信息很难指向真正的原因。
const assetSelectColumns = `a.id,a.type_key,a.natural_key,a.name,a.node,a.created_at,a.updated_at,
             (SELECT se.last_seen_at FROM asset_seen se WHERE se.asset_id=a.id),
             t.key,t.value,t.source,t.updated_at,t.updated_by`

// scanAssets 把「资产 + 属性」的行集合组装成 Asset 切片（同一资产的多行合并，保留查询顺序）。
func scanAssets(rows *sql.Rows) ([]Asset, error) {
	var order []int64
	byID := map[int64]*Asset{}
	for rows.Next() {
		var a Asset
		var key, value, source, by sql.NullString
		var attrAt, seenAt sql.NullInt64
		if err := rows.Scan(&a.ID, &a.TypeKey, &a.NaturalKey, &a.Name, &a.Node,
			&a.CreatedAt, &a.UpdatedAt, &seenAt, &key, &value, &source, &attrAt, &by); err != nil {
			return nil, err
		}
		a.SeenAt = seenAt.Int64
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
		return nil, err
	}
	out := make([]Asset, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out, nil
}

// listAssets 按条件分页列出资产（含属性），按「类型 + 自然键」稳定排序。
func (s *Store) listAssets(f ListFilter) ([]Asset, error) {
	sub, args := assetWhere("a2", f)
	// 分页必须作用于**资产**而不是 join 后的行：一个资产有几条属性就有几行，
	// 直接对 join 结果 LIMIT 会让「一页 50 条」变成「一页 50 行」——属性多的资产
	// 会挤占同页额度（10 条属性就吃掉一页的 1/5），OFFSET 也随之漂移。
	// 因此先用子查询选出本页的资产 ID，再取这些资产的属性。
	q := `SELECT ` + assetSelectColumns + `
	      FROM assets a LEFT JOIN asset_attrs t ON t.asset_id=a.id
	      WHERE a.id IN (SELECT a2.id FROM assets a2` + sub + ` ORDER BY a2.type_key,a2.natural_key LIMIT ? OFFSET ?)
	      ORDER BY a.type_key,a.natural_key,t.key,t.source`
	args = append(args, f.limit(), f.offset())

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询资产列表失败: %w", err)
	}
	defer rows.Close()
	out, err := scanAssets(rows)
	if err != nil {
		return nil, fmt.Errorf("读取资产列表失败: %w", err)
	}
	return out, nil
}

// listAssetsAll 按条件列出**全部**符合条件的资产（含属性），不分页。
//
// 巡检必须用它：分页版默认只取一页，用它跑巡检等于"只检了前 50 台却报告说检过了"，
// 比不检更危险（会让人以为其余资产都合规）。
func (s *Store) listAssetsAll(f ListFilter) ([]Asset, error) {
	where, args := assetWhere("a", f)
	q := `SELECT ` + assetSelectColumns + `
	      FROM assets a LEFT JOIN asset_attrs t ON t.asset_id=a.id` + where +
		` ORDER BY a.type_key,a.natural_key,t.key,t.source`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("查询资产列表失败: %w", err)
	}
	defer rows.Close()
	out, err := scanAssets(rows)
	if err != nil {
		return nil, fmt.Errorf("读取资产列表失败: %w", err)
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

// assetStats 汇总台账健康度数字。
//
// 用聚合查询而不是「把资产全查出来在内存里数」：摘要要覆盖筛选结果集里的**全部**资产，
// 内存统计会把「摘要随台账增长而变慢、变占内存」这件事埋到生产环境才发现。
// 每个数字都与列表共用同一套 assetWhere（含资源范围），因此顶部数字能直接下钻到列表。
func (s *Store) assetStats(f ListFilter, changesSince int64) (Stats, error) {
	where, args := assetWhere("a", f)
	count := func(extra string, extraArgs ...any) (int, error) {
		var n int
		query := `SELECT COUNT(*) FROM assets a` + where + extra
		if err := s.db.QueryRow(query, append(append([]any{}, args...), extraArgs...)...).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}

	var out Stats
	var err error
	if out.Total, err = count(""); err != nil {
		return Stats{}, fmt.Errorf("统计资产总数失败: %w", err)
	}
	if out.Missing, err = count(" AND "+lastSeenExpr("a")+" < ?", f.StaleBefore); err != nil {
		return Stats{}, fmt.Errorf("统计失联资产失败: %w", err)
	}
	if out.NoOwner, err = count(" AND " + ownerMissingCond("a")); err != nil {
		return Stats{}, fmt.Errorf("统计无责任人资产失败: %w", err)
	}
	if out.Conflict, err = count(" AND " + conflictCond("a")); err != nil {
		return Stats{}, fmt.Errorf("统计冲突资产失败: %w", err)
	}
	if changesSince > 0 {
		// 变更数要 join asset_changes，单独走一条查询（where 仍是同一套，含资源范围）。
		var n int
		query := `SELECT COUNT(*) FROM asset_changes c JOIN assets a ON a.id=c.asset_id` + where + ` AND c.at >= ?`
		if err := s.db.QueryRow(query, append(append([]any{}, args...), changesSince)...).Scan(&n); err != nil {
			return Stats{}, fmt.Errorf("统计变更数失败: %w", err)
		}
		out.Changes = n
	}
	return out, nil
}

// deleteManualAttrs 删除指定字段的人工值，并为每个真正删除的字段写一条变更记录。
//
// 删除与记录放在同一事务：否则会出现「界面显示已恢复、历史里查不到」这类自相矛盾。
// 只删 source=manual，不动采集值——"恢复采集值"从来不需要写采集侧。
func (s *Store) deleteManualAttrs(assetID int64, keys []string, at int64, actor string) ([]Attr, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	removed := make([]Attr, 0, len(keys))
	for _, key := range keys {
		var value string
		err := tx.QueryRow(`SELECT value FROM asset_attrs WHERE asset_id=? AND key=? AND source=?`,
			assetID, key, string(SourceManual)).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("查询人工值失败: %w", err)
		}
		if _, err := tx.Exec(`DELETE FROM asset_attrs WHERE asset_id=? AND key=? AND source=?`,
			assetID, key, string(SourceManual)); err != nil {
			return nil, fmt.Errorf("删除人工值失败: %w", err)
		}
		if _, err := tx.Exec(
			`INSERT INTO asset_changes(asset_id,field,old_value,new_value,source,actor,kind,at) VALUES(?,?,?,?,?,?,?,?)`,
			assetID, key, value, "", string(SourceManual), actor, string(ChangeUpdate), at,
		); err != nil {
			return nil, fmt.Errorf("写入资产变更记录失败: %w", err)
		}
		removed = append(removed, Attr{Key: key, Value: value, Source: SourceManual, UpdatedAt: at, UpdatedBy: actor})
	}
	if len(removed) > 0 {
		if _, err := tx.Exec(`UPDATE assets SET updated_at=? WHERE id=?`, at, assetID); err != nil {
			return nil, fmt.Errorf("更新资产时间戳失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交恢复采集值失败: %w", err)
	}
	return removed, nil
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

// latestSnapshot 取资产最近一份快照（含字段）；该资产还没有快照时 ok=false。
func (s *Store) latestSnapshot(assetID int64) (Snapshot, bool, error) {
	sn := Snapshot{AssetID: assetID}
	err := s.db.QueryRow(
		`SELECT id,taken_at FROM snapshots WHERE asset_id=? ORDER BY taken_at DESC, id DESC LIMIT 1`,
		assetID,
	).Scan(&sn.ID, &sn.TakenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, fmt.Errorf("查询资产快照失败: %w", err)
	}
	fields, err := s.snapshotFieldsOf(sn.ID)
	if err != nil {
		return Snapshot{}, false, err
	}
	sn.Fields = fields
	return sn, true, nil
}

// recordInspectRun 在同一事务里写入巡检记录与全部差异项，返回记录主键。
//
// 为什么必须原子：记录与差异项是同一份证据的两半，「有记录、没差异」会被读成
// 「本次全部合规」——那比直接报错更糟。findings 为空时不写任何差异项行。
func (s *Store) recordInspectRun(r InspectRun, findings []InspectFinding) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("写入巡检记录失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	truncated := 0
	if r.Truncated {
		truncated = 1
	}
	res, err := tx.Exec(
		`INSERT INTO inspect_runs(scope,actor,started_at,assets,baselined,findings,truncated) VALUES(?,?,?,?,?,?,?)`,
		r.Scope, r.Actor, r.StartedAt, r.Assets, r.Baselined, r.Findings, truncated)
	if err != nil {
		return 0, fmt.Errorf("写入巡检记录失败: %w", err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取巡检记录主键失败: %w", err)
	}
	for i := range findings {
		f := &findings[i]
		if _, err := tx.Exec(
			`INSERT INTO inspect_findings(run_id,asset_id,asset_type,asset_key,asset_name,node,field,kind,level,expected,actual,at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			runID, f.AssetID, f.AssetType, f.AssetKey, f.AssetName, f.Node,
			f.Field, string(f.Kind), string(f.Level), f.Expected, f.Actual, f.At,
		); err != nil {
			return 0, fmt.Errorf("写入差异项失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交巡检结果失败: %w", err)
	}
	return runID, nil
}

// inspectRunsOf 列出巡检记录（时间倒序）。
func (s *Store) inspectRunsOf(limit int) ([]InspectRun, error) {
	if limit <= 0 {
		limit = defaultInspectRunLimit
	}
	rows, err := s.db.Query(
		`SELECT id,scope,actor,started_at,assets,baselined,findings,truncated
		 FROM inspect_runs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("查询巡检记录失败: %w", err)
	}
	defer rows.Close()
	var out []InspectRun
	for rows.Next() {
		var r InspectRun
		var truncated int
		if err := rows.Scan(&r.ID, &r.Scope, &r.Actor, &r.StartedAt,
			&r.Assets, &r.Baselined, &r.Findings, &truncated); err != nil {
			return nil, fmt.Errorf("读取巡检记录失败: %w", err)
		}
		r.Truncated = truncated != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// findingsOf 取某次巡检的差异项：严重级别高的排前面，同级按资产与字段稳定排序。
func (s *Store) findingsOf(runID int64, limit int) ([]InspectFinding, error) {
	if limit <= 0 {
		limit = defaultInspectFindingLimit
	}
	rows, err := s.db.Query(
		`SELECT id,run_id,asset_id,asset_type,asset_key,asset_name,node,field,kind,level,expected,actual,at
		 FROM inspect_findings WHERE run_id=?
		 ORDER BY CASE level WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		          asset_type, asset_key, field
		 LIMIT ?`, runID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询差异项失败: %w", err)
	}
	defer rows.Close()
	var out []InspectFinding
	for rows.Next() {
		var f InspectFinding
		var kind, level string
		if err := rows.Scan(&f.ID, &f.RunID, &f.AssetID, &f.AssetType, &f.AssetKey, &f.AssetName,
			&f.Node, &f.Field, &kind, &level, &f.Expected, &f.Actual, &f.At); err != nil {
			return nil, fmt.Errorf("读取差异项失败: %w", err)
		}
		f.Kind, f.Level = FindingKind(kind), FindingLevel(level)
		out = append(out, f)
	}
	return out, rows.Err()
}

// setBaseline 把某资产的快照设为**该资产类型**的标杆（同类型只保留一个）。
func (s *Store) setBaseline(b Baseline) error {
	if _, err := s.db.Exec(
		`INSERT INTO inspect_baselines(type_key,asset_id,asset_key,snapshot_id,set_by,set_at) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(type_key) DO UPDATE SET
		   asset_id=excluded.asset_id, asset_key=excluded.asset_key,
		   snapshot_id=excluded.snapshot_id, set_by=excluded.set_by, set_at=excluded.set_at`,
		b.TypeKey, b.AssetID, b.AssetKey, b.SnapshotID, b.SetBy, b.SetAt,
	); err != nil {
		return fmt.Errorf("设置巡检标杆失败: %w", err)
	}
	return nil
}

// clearBaseline 清除某资产类型的标杆；本来没有也视为成功（幂等）。
func (s *Store) clearBaseline(typeKey string) error {
	if _, err := s.db.Exec(`DELETE FROM inspect_baselines WHERE type_key=?`, typeKey); err != nil {
		return fmt.Errorf("清除巡检标杆失败: %w", err)
	}
	return nil
}

// baselineOf 取某资产类型的标杆。
func (s *Store) baselineOf(typeKey string) (Baseline, bool, error) {
	var b Baseline
	err := s.db.QueryRow(
		`SELECT type_key,asset_id,asset_key,snapshot_id,set_by,set_at FROM inspect_baselines WHERE type_key=?`,
		typeKey,
	).Scan(&b.TypeKey, &b.AssetID, &b.AssetKey, &b.SnapshotID, &b.SetBy, &b.SetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Baseline{}, false, nil
	}
	if err != nil {
		return Baseline{}, false, fmt.Errorf("查询巡检标杆失败: %w", err)
	}
	return b, true, nil
}

// baselinesOf 列出全部标杆（界面用于显示"哪些类型已经有期望值"）。
func (s *Store) baselinesOf() ([]Baseline, error) {
	rows, err := s.db.Query(
		`SELECT type_key,asset_id,asset_key,snapshot_id,set_by,set_at FROM inspect_baselines ORDER BY type_key`)
	if err != nil {
		return nil, fmt.Errorf("查询巡检标杆失败: %w", err)
	}
	defer rows.Close()
	var out []Baseline
	for rows.Next() {
		var b Baseline
		if err := rows.Scan(&b.TypeKey, &b.AssetID, &b.AssetKey, &b.SnapshotID, &b.SetBy, &b.SetAt); err != nil {
			return nil, fmt.Errorf("读取巡检标杆失败: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
