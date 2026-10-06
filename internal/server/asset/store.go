package asset

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	// 纯 Go 的 SQLite 实现：不引入 CGO，交叉编译（linux amd64/arm64/arm）与离线包不受影响。
	// 决策记录见 docs/adr/0001-cmdb-relational-store.md。
	_ "modernc.org/sqlite"
)

// schemaVersion 是资产库结构版本，写入 PRAGMA user_version。
// 只允许递增；读到更高版本直接拒绝启动，避免新库被旧程序写坏。
//
// v2：关系加来源列（asset_links.source）与关联抑制表（asset_link_suppressions），
// 支撑「人工维护的关系优先于采集自动发现」以及关系的逻辑删除。
const schemaVersion = 2

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
	// source 记录这条边由谁认领（采集 / 人工，见 model.Source）。
	// 唯一约束里**刻意不含** source：同一 (from,to,kind) 只有一条边，
	// 人工认领之后采集侧不再把它降级回 discovery —— 这正是「人工优先」的落点。
	`CREATE TABLE IF NOT EXISTS asset_links(
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id    INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		to_id      INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		kind       TEXT NOT NULL,
		source     TEXT NOT NULL DEFAULT 'discovery',
		created_at INTEGER NOT NULL,
		UNIQUE(from_id, to_id, kind)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_links_from ON asset_links(from_id)`,
	`CREATE INDEX IF NOT EXISTS idx_links_to ON asset_links(to_id)`,
	// 关联的**逻辑删除**：人工删掉一条采集来的边 → 记在这里，采集侧不再重建。
	// 若物理删除，下一轮采集立刻把它建回来，用户的操作等于没做——这与「采集资产用忽略、
	// 而不是删除」是同一个判断：在采集驱动的系统里，"删掉"往往不是用户的真实意图。
	`CREATE TABLE IF NOT EXISTS asset_link_suppressions(
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		from_id    INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		to_id      INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		kind       TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		UNIQUE(from_id, to_id, kind)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_link_supp_from ON asset_link_suppressions(from_id)`,
	`CREATE INDEX IF NOT EXISTS idx_link_supp_to ON asset_link_suppressions(to_id)`,
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
	`CREATE TABLE IF NOT EXISTS inspect_run_manifest(
		run_id INTEGER PRIMARY KEY REFERENCES inspect_runs(id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS inspect_run_members(
		run_id INTEGER NOT NULL REFERENCES inspect_runs(id) ON DELETE CASCADE,
		asset_id INTEGER NOT NULL,
		node_at_run TEXT NOT NULL DEFAULT '',
		baselined INTEGER NOT NULL DEFAULT 0,
		findings INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY(run_id,asset_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_inspect_members_asset ON inspect_run_members(asset_id,run_id)`,
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
	`CREATE TABLE IF NOT EXISTS inspect_finding_baselines(
		finding_id INTEGER PRIMARY KEY REFERENCES inspect_findings(id) ON DELETE CASCADE,
		asset_id INTEGER NOT NULL
	)`,
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
	// asset_ignored 记「已从台账隐藏」的资产（每个资产最多一行）。
	//
	// 为什么是独立表而不是 assets 上的一个列：与 asset_seen 同理——纯附加表，
	// 老版本程序不读它，因此**不提升 schemaVersion**，保留「回滚旧版本时 assets.db 可直接沿用」。
	// 语义上也更贴切：忽略是一个**动作与理由**（谁、什么时候、为什么），不是资产本身的属性。
	`CREATE TABLE IF NOT EXISTS asset_ignored(
		asset_id   INTEGER PRIMARY KEY REFERENCES assets(id) ON DELETE CASCADE,
		reason     TEXT NOT NULL DEFAULT '',
		ignored_by TEXT NOT NULL DEFAULT '',
		ignored_at INTEGER NOT NULL
	)`,
	// asset_labels 是资产的管理标签（分类维度），与 asset_attrs 职责分开：
	// attrs 是采集值/人工值（参与变更与巡检语义），labels 只用于展示与筛选。
	// 单独建表而不是塞进 attrs：标签要能按 key/value 索引筛选，且不该混进巡检快照。
	`CREATE TABLE IF NOT EXISTS asset_labels(
		asset_id   INTEGER NOT NULL REFERENCES assets(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL,
		updated_by TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(asset_id, key)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_asset_labels_kv ON asset_labels(key, value)`,
	// audit_events / alert_acks：审计事件与告警处置从 JSON 文件搬进本库（设计件批次 18，2026-10-05）。
	//
	// 为什么放同一个库：两者的写入频率是「人工管理操作」级（每分钟几条到几十条），而本库
	// 已在承载「每轮上报 × 资产数」的写入；单连接下这点增量可忽略，换来一个备份文件、
	// 一套迁移机制。**审计放进库里不等于"防篡改"**——同机同权限面，真正的防篡改要靠
	// 外部只追加存储，见设计件 §5。
	//
	// 与 asset_seen / asset_ignored 同理：纯附加表，老版本不读它，**不提升 schemaVersion**。
	// 代价是回滚后旧版本的审计与处置列表会变空（旧版本读 JSON，而 JSON 已改名 .bak-migrated）。
	`CREATE TABLE IF NOT EXISTS audit_events(
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		time_ms    INTEGER NOT NULL,
		user_name  TEXT NOT NULL DEFAULT '',
		method     TEXT NOT NULL DEFAULT '',
		path       TEXT NOT NULL DEFAULT '',
		status     INTEGER NOT NULL DEFAULT 0,
		remote_ip  TEXT NOT NULL DEFAULT '',
		source_loc TEXT NOT NULL DEFAULT '',
		succeeded  INTEGER NOT NULL DEFAULT 0,
		category   TEXT NOT NULL DEFAULT '',
		action     TEXT NOT NULL DEFAULT '',
		detail     TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_time ON audit_events(time_ms DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_events_user ON audit_events(user_name, time_ms DESC)`,
	// ack_key 沿用既有的 rule|host|instance|startsAt 形态，语义零变化（Map/Get 的 key 不变）。
	// comments 存 JSON 数组而不是拆表：评论永远随处置记录整体读写，拆表只会让读取多一次 join。
	`CREATE TABLE IF NOT EXISTS alert_acks(
		ack_key       TEXT PRIMARY KEY,
		rule          TEXT NOT NULL DEFAULT '',
		host          TEXT NOT NULL DEFAULT '',
		instance      TEXT NOT NULL DEFAULT '',
		starts_at     INTEGER NOT NULL DEFAULT 0,
		status        TEXT NOT NULL DEFAULT 'pending',
		user_name     TEXT NOT NULL DEFAULT '',
		assignee      TEXT NOT NULL DEFAULT '',
		time_ms       INTEGER NOT NULL DEFAULT 0,
		ack_time_ms   INTEGER NOT NULL DEFAULT 0,
		close_time_ms INTEGER NOT NULL DEFAULT 0,
		close_reason  TEXT NOT NULL DEFAULT '',
		comments      TEXT NOT NULL DEFAULT '[]'
	)`,
	`CREATE INDEX IF NOT EXISTS idx_alert_acks_time ON alert_acks(time_ms DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_alert_acks_status ON alert_acks(status, time_ms DESC)`,
	// ops_tasks / ops_meta / ops_caps：操作任务从 JSON 文件搬进本库（设计件批次 19，2026-10-05）。
	//
	// **整条任务存 task_json 列**，只把真正要用来筛的字段（node/kind/state/时间/批次）提成索引列：
	// 任务永远整体读写，逐字段建十几列只会让"每加一个任务字段就要改表"。
	// 与批次 18 同理：纯附加表、**不升 schemaVersion**。
	`CREATE TABLE IF NOT EXISTS ops_tasks(
		id         TEXT PRIMARY KEY,
		batch_id   TEXT NOT NULL DEFAULT '',
		node       TEXT NOT NULL DEFAULT '',
		kind       TEXT NOT NULL DEFAULT '',
		state      TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL DEFAULT 0,
		done_at    INTEGER NOT NULL DEFAULT 0,
		task_json  TEXT NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_ops_tasks_created ON ops_tasks(created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_ops_tasks_state ON ops_tasks(state)`,
	// 「认领上次结果」按 节点+动作+状态 找最近一条成功回执，这个索引就是为它建的。
	`CREATE INDEX IF NOT EXISTS idx_ops_tasks_pick ON ops_tasks(node, kind, state, done_at DESC)`,
	// seq 与 caps 是**有界元数据**（一个序号、每个节点一行），单独两张小表，让 ops_tasks.json 彻底退休。
	`CREATE TABLE IF NOT EXISTS ops_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS ops_caps(node TEXT PRIMARY KEY, kinds TEXT NOT NULL DEFAULT '')`,
}

// DB 返回底层连接，供**同库的其它持久化**复用（审计事件与告警处置，见设计件批次 18）。
//
// 刻意只给连接、不给"绕过 Service 直接改台账表"的口子：台账的读写语义（采集值不覆盖人工值、
// 变更留痕、资产范围）都在 Service 里，绕过它写 assets_* 会破坏那些不变量。
func (s *Store) DB() *sql.DB { return s.db }

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
	// v1 存量库的 asset_links 没有 source 列，而 CREATE TABLE IF NOT EXISTS 不会补列，
	// 因此显式补齐（新库建表时已含该列，这里是 no-op）。
	if err := s.ensureLinkSourceColumn(); err != nil {
		return err
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

// ensureLinkSourceColumn 为 v1 存量库补上 asset_links.source。
//
// 用「查列是否存在」而不是「按版本号判断」：手工删表、升级到一半留下的中间态都能自愈，
// 与 schemaStatements 每次执行 IF NOT EXISTS 的取向一致。存量边一律视为采集所得——
// 它们确实都是采集建的，语义上没有任何变化。
func (s *Store) ensureLinkSourceColumn() error {
	has, err := s.linkSourceColumnExists()
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if _, err := s.db.Exec(
		`ALTER TABLE asset_links ADD COLUMN source TEXT NOT NULL DEFAULT 'discovery'`,
	); err != nil {
		return fmt.Errorf("为 asset_links 补 source 列失败: %w", err)
	}
	return nil
}

// linkSourceColumnExists 查询 asset_links 是否已有 source 列。
//
// 注意先把结果读完再返回：连接池是单连接（MaxOpenConns(1)），
// 若留着未关闭的 rows 去执行 ALTER，会等不到连接而卡死。
func (s *Store) linkSourceColumnExists() (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(asset_links)`)
	if err != nil {
		return false, fmt.Errorf("读取 asset_links 结构失败: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			cid, notNull, pk int
			name, colType    string
			dflt             sql.NullString
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("读取 asset_links 结构失败: %w", err)
		}
		if name == "source" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("读取 asset_links 结构失败: %w", err)
	}
	return found, nil
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
	return s.assetByNaturalClause(typeKey, naturalKey, false)
}

// assetByNaturalFold 同 assetByNatural，但自然键**忽略大小写**。
//
// 为什么需要单独一个入口：主机名按 DNS 约定大小写不敏感，而两侧的写法由不同系统决定——
// K8s 的节点名按 RFC 1123 一律小写（`vm-0-10-ubuntu`），而 Agent 上报的 hostname 保留系统原样
// （`VM-0-10-ubuntu`）。严格比对会把**同一台机器**判成两台：Pod 的 `runs_on` 建不出来，
// 且它的归属节点为空 —— **按节点分组授权的受限用户会看不到自己机器上的 Pod**。
// 两处失效都不报错，只是"看起来没有关系"。
//
// 只用 ASCII 折叠（SQLite 的 NOCASE 语义）：主机名本就是 ASCII；中文主机名不参与大小写。
func (s *Store) assetByNaturalFold(typeKey, naturalKey string) (Asset, bool, error) {
	return s.assetByNaturalClause(typeKey, naturalKey, true)
}

func (s *Store) assetByNaturalClause(typeKey, naturalKey string, fold bool) (Asset, bool, error) {
	var a Asset
	cond := "natural_key=?"
	if fold {
		// 折叠时可能命中多条（台账里真的并存两种大小写），取 id 最小的那条让结果稳定：
		// 否则同一份数据在两次查询之间可能给出不同的资产。
		cond = "natural_key=? COLLATE NOCASE ORDER BY id LIMIT 1"
	}
	err := s.db.QueryRow(
		`SELECT id,type_key,natural_key,name,node,created_at,updated_at
		 FROM assets WHERE type_key=? AND `+cond, typeKey, naturalKey,
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
	one := []Asset{a}
	if err := s.loadMeta(one); err != nil {
		return Asset{}, false, err
	}
	return one[0], true, nil
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
	// 排除短命运行时对象（容器 / 工作负载）：默认视图与摘要都不含它们。
	// 理由见 asset.EphemeralTypes 的注释——被滚动更新替换掉的旧 Pod 会被判失联，
	// 计入之后顶部会出现"失联 200"，把真实故障埋掉。
	if len(f.ExcludeTypes) > 0 {
		q += " AND " + alias + ".type_key NOT IN (" + strings.TrimSuffix(strings.Repeat("?,", len(f.ExcludeTypes)), ",") + ")"
		for _, t := range f.ExcludeTypes {
			args = append(args, t)
		}
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
	// 已忽略资产的可见性：默认隐藏（台账是"该关心的东西"的清单）。
	switch f.Ignored {
	case IgnoreFilterWith:
		// 不过滤
	case IgnoreFilterOnly:
		q += " AND EXISTS (SELECT 1 FROM asset_ignored ig WHERE ig.asset_id=" + alias + ".id)"
	default:
		q += " AND NOT EXISTS (SELECT 1 FROM asset_ignored ig WHERE ig.asset_id=" + alias + ".id)"
	}
	if lb := strings.TrimSpace(f.Label); lb != "" {
		// 支持 `key` 与 `key:value` 两种写法；只按 key 过滤时也走索引前缀。
		if key, value, ok := strings.Cut(lb, ":"); ok && strings.TrimSpace(value) != "" {
			q += " AND EXISTS (SELECT 1 FROM asset_labels l WHERE l.asset_id=" + alias + ".id AND l.key=? AND l.value=?)"
			args = append(args, strings.TrimSpace(key), strings.TrimSpace(value))
		} else {
			q += " AND EXISTS (SELECT 1 FROM asset_labels l WHERE l.asset_id=" + alias + ".id AND l.key=?)"
			args = append(args, strings.TrimSpace(key))
		}
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

// anyIDs 把资产 ID 切片转成切片参数（供 IN 查询使用）。
func anyIDs(ids []int64) []any {
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, id)
	}
	return out
}

// loadMeta 批量补齐资产的「忽略标记」与「标签」。
//
// 用两条 IN 查询而不是把它们拼进主查询：标签是一对多，塞进主查询会让 scanAssets 的
// 「一个资产多行合并」逻辑再复杂一层（属性行已经在合并了）。两条额外查询的代价只与
// 本页资产数相关，与台账总量无关。
func (s *Store) loadMeta(assets []Asset) error {
	if len(assets) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(assets))
	index := make(map[int64]int, len(assets))
	for i := range assets {
		ids = append(ids, assets[i].ID)
		index[assets[i].ID] = i
	}
	args := anyIDs(ids)
	ph := placeholders(len(ids))

	rows, err := s.db.Query(
		`SELECT asset_id,reason,ignored_by,ignored_at FROM asset_ignored WHERE asset_id IN (`+ph+`)`, args...)
	if err != nil {
		return fmt.Errorf("查询资产忽略标记失败: %w", err)
	}
	for rows.Next() {
		var id, at int64
		var reason, by string
		if err := rows.Scan(&id, &reason, &by, &at); err != nil {
			rows.Close()
			return fmt.Errorf("读取资产忽略标记失败: %w", err)
		}
		if i, ok := index[id]; ok {
			assets[i].Ignored = true
			assets[i].IgnoreReason = reason
			assets[i].IgnoredBy = by
			assets[i].IgnoredAt = at
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("读取资产忽略标记失败: %w", err)
	}
	rows.Close()

	lrows, err := s.db.Query(
		`SELECT asset_id,key,value FROM asset_labels WHERE asset_id IN (`+ph+`) ORDER BY key`, args...)
	if err != nil {
		return fmt.Errorf("查询资产标签失败: %w", err)
	}
	defer lrows.Close()
	for lrows.Next() {
		var id int64
		var key, value string
		if err := lrows.Scan(&id, &key, &value); err != nil {
			return fmt.Errorf("读取资产标签失败: %w", err)
		}
		i, ok := index[id]
		if !ok {
			continue
		}
		if assets[i].Labels == nil {
			assets[i].Labels = map[string]string{}
		}
		assets[i].Labels[key] = value
	}
	return lrows.Err()
}

// ignoreAsset 标记资产为已忽略（重复忽略按最后一次的理由与操作人覆盖）。
func (s *Store) ignoreAsset(assetID int64, reason, actor string, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_ignored(asset_id,reason,ignored_by,ignored_at) VALUES(?,?,?,?)
		 ON CONFLICT(asset_id) DO UPDATE SET reason=excluded.reason, ignored_by=excluded.ignored_by, ignored_at=excluded.ignored_at`,
		assetID, reason, actor, at,
	); err != nil {
		return fmt.Errorf("忽略资产失败: %w", err)
	}
	return nil
}

// restoreAsset 解除忽略；本来就没被忽略时也返回成功（幂等）。
func (s *Store) restoreAsset(assetID int64) error {
	if _, err := s.db.Exec(`DELETE FROM asset_ignored WHERE asset_id=?`, assetID); err != nil {
		return fmt.Errorf("恢复资产失败: %w", err)
	}
	return nil
}

// deleteAsset 彻底删除资产（属性 / 关系 / 变更历史 / 快照 / 忽略标记随之级联删除）。
//
// 只应由「纯人工建档」资产调用：调用方（Service.Purge）负责把关，因为采集资产删掉后
// 会被下一轮上报重建——那种"删了又回来"的行为会让人以为删除没生效。
func (s *Store) deleteAsset(assetID int64) error {
	if _, err := s.db.Exec(`DELETE FROM assets WHERE id=?`, assetID); err != nil {
		return fmt.Errorf("删除资产失败: %w", err)
	}
	return nil
}

// setLabel 写入/覆盖一条标签。
func (s *Store) setLabel(assetID int64, key, value string, at int64, actor string) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_labels(asset_id,key,value,updated_at,updated_by) VALUES(?,?,?,?,?)
		 ON CONFLICT(asset_id,key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at, updated_by=excluded.updated_by`,
		assetID, key, value, at, actor,
	); err != nil {
		return fmt.Errorf("写入资产标签失败: %w", err)
	}
	return nil
}

// removeLabel 删除一条标签；不存在也返回成功（幂等）。
func (s *Store) removeLabel(assetID int64, key string) error {
	if _, err := s.db.Exec(`DELETE FROM asset_labels WHERE asset_id=? AND key=?`, assetID, key); err != nil {
		return fmt.Errorf("删除资产标签失败: %w", err)
	}
	return nil
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
	// 忽略标记与标签单独补齐（分页只影响本条查询的代价，与台账总量无关）
	if err := s.loadMeta(out); err != nil {
		return nil, err
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
	// 忽略标记与标签单独补齐（分页只影响本条查询的代价，与台账总量无关）
	if err := s.loadMeta(out); err != nil {
		return nil, err
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
	// 已忽略：用同一套筛选但强制「只看已忽略」，因此这个数字与点进去看到的列表一致。
	// 它必须由这条查询给出——已忽略资产默认不在 Total 里，不显示这个数字，
	// 用户会以为自己忽略过的东西"找不回来了"。
	forced := f
	forced.Ignored = IgnoreFilterOnly
	forced.Limit, forced.Offset = 0, 0
	if out.Ignored, err = s.countAssets(forced); err != nil {
		return Stats{}, fmt.Errorf("统计已忽略资产失败: %w", err)
	}
	// 反向修正：Total 若在「只看已忽略」筛选下，含义应是"已忽略总数"，两者一致即可。
	// （默认筛选下 Total 不含已忽略，这正是台账的预期语义。）
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
//
// **人工优先**：同一 (from,to,kind) 只有一条边，source 一旦成为 manual 就不再降级——
// 采集侧继续上报同一条边不会把人工的认领抹掉。用一条 UPSERT 表达而不是「先查再写」：
// 既原子，也不依赖调用方记得先读一次。
func (s *Store) linkAssets(fromID, toID int64, kind LinkKind, src Source, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_links(from_id,to_id,kind,source,created_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(from_id,to_id,kind) DO UPDATE SET
		   source = CASE
		     WHEN excluded.source='manual' OR asset_links.source='manual' THEN 'manual'
		     ELSE asset_links.source
		   END`,
		fromID, toID, string(kind), string(src.normalized()), at,
	); err != nil {
		return fmt.Errorf("建立资产关联失败: %w", err)
	}
	return nil
}

// unlinkAssets 物理删除关联；不存在时视为成功（幂等）。
//
// 只应由「人工删除」路径调用——该路径同时会写一条抑制记录，
// 否则下一轮采集立刻把边建回来。采集路径不要直接调它。
func (s *Store) unlinkAssets(fromID, toID int64, kind LinkKind) error {
	if _, err := s.db.Exec(
		`DELETE FROM asset_links WHERE from_id=? AND to_id=? AND kind=?`, fromID, toID, string(kind)); err != nil {
		return fmt.Errorf("解除资产关联失败: %w", err)
	}
	return nil
}

// suppressLink 记录「这条采集来的边被人工删掉了」，采集侧据此不再重建（幂等）。
func (s *Store) suppressLink(fromID, toID int64, kind LinkKind, by string, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO asset_link_suppressions(from_id,to_id,kind,created_at,created_by) VALUES(?,?,?,?,?)
		 ON CONFLICT(from_id,to_id,kind) DO UPDATE SET created_at=excluded.created_at, created_by=excluded.created_by`,
		fromID, toID, string(kind), at, by,
	); err != nil {
		return fmt.Errorf("记录关联抑制失败: %w", err)
	}
	return nil
}

// clearLinkSuppression 取消抑制，让采集侧可以重新建立这条边（幂等）。
func (s *Store) clearLinkSuppression(fromID, toID int64, kind LinkKind) error {
	if _, err := s.db.Exec(
		`DELETE FROM asset_link_suppressions WHERE from_id=? AND to_id=? AND kind=?`,
		fromID, toID, string(kind)); err != nil {
		return fmt.Errorf("取消关联抑制失败: %w", err)
	}
	return nil
}

// linkSuppressed 判断这条边是否被人工抑制。
func (s *Store) linkSuppressed(fromID, toID int64, kind LinkKind) (bool, error) {
	var n int
	if err := s.db.QueryRow(
		`SELECT COUNT(1) FROM asset_link_suppressions WHERE from_id=? AND to_id=? AND kind=?`,
		fromID, toID, string(kind)).Scan(&n); err != nil {
		return false, fmt.Errorf("查询关联抑制失败: %w", err)
	}
	return n > 0, nil
}

// suppressedLinksOf 取与某资产相关的全部抑制记录（出边与入边都返回）。
//
// 必须能被列出来：抑制是逻辑删除，界面上看不到就无从恢复，
// 用户会以为"删掉的关系永远回不来了"。
func (s *Store) suppressedLinksOf(assetID int64) ([]SuppressedLink, error) {
	rows, err := s.db.Query(
		`SELECT sp.kind,sp.created_at,sp.created_by,
		        f.type_key,f.natural_key,t.type_key,t.natural_key
		 FROM asset_link_suppressions sp
		 JOIN assets f ON f.id=sp.from_id
		 JOIN assets t ON t.id=sp.to_id
		 WHERE sp.from_id=? OR sp.to_id=?
		 ORDER BY sp.kind,f.natural_key,t.natural_key`, assetID, assetID)
	if err != nil {
		return nil, fmt.Errorf("查询关联抑制失败: %w", err)
	}
	defer rows.Close()
	var out []SuppressedLink
	for rows.Next() {
		var sl SuppressedLink
		var kind string
		if err := rows.Scan(&kind, &sl.CreatedAt, &sl.CreatedBy,
			&sl.From.TypeKey, &sl.From.NaturalKey, &sl.To.TypeKey, &sl.To.NaturalKey); err != nil {
			return nil, fmt.Errorf("读取关联抑制失败: %w", err)
		}
		sl.Kind = LinkKind(kind)
		out = append(out, sl)
	}
	return out, rows.Err()
}

// linkRow 是邻域遍历用的一条边（带两端 id）。
//
// 与领域类型 Link 分开：Link 用 Ref 表达两端（面向读接口），而逐跳展开必须按 id
// 收敛 IN(...)，否则每一层都要把 Ref 再翻译回 id 去查一次。
type linkRow struct {
	ID        int64
	FromID    int64
	ToID      int64
	Kind      LinkKind
	Source    Source
	CreatedAt int64
}

// linkRowsTouching 取与给定资产集合相关的全部边（出边与入边都算）。
func (s *Store) linkRowsTouching(ids []int64) ([]linkRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := placeholders(len(ids))
	args := make([]any, 0, len(ids)*2)
	for _, id := range ids {
		args = append(args, id)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(
		`SELECT id,from_id,to_id,kind,source,created_at FROM asset_links
		 WHERE from_id IN (`+ph+`) OR to_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询资产关联失败: %w", err)
	}
	defer rows.Close()
	var out []linkRow
	for rows.Next() {
		var r linkRow
		var kind, source string
		if err := rows.Scan(&r.ID, &r.FromID, &r.ToID, &kind, &source, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("读取资产关联失败: %w", err)
		}
		r.Kind, r.Source = LinkKind(kind), Source(source)
		out = append(out, r)
	}
	return out, rows.Err()
}

// assetsByIDs 按主键批量取资产（含属性）。
func (s *Store) assetsByIDs(ids []int64) ([]Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.Query(`SELECT `+assetSelectColumns+`
	      FROM assets a LEFT JOIN asset_attrs t ON t.asset_id=a.id
	      WHERE a.id IN (`+placeholders(len(ids))+`)
	      ORDER BY a.id,t.key,t.source`, args...)
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

// assetsByInstanceAddr 找出「实例地址 == addr」的中间件实例资产。
//
// 为什么按**地址后缀**匹配而不是精确自然键：告警事件只带实例标签（`127.0.0.1:6379`），
// 不带中间件类型；而台账的自然键是 `<类型>:<地址>`。类型可以拿指标名前缀去猜，
// 但模板派生类型的指标名与类型 key 并不一一对应——猜错会命中**另一条**资产，
// 那比"没找到"更糟（用户会以为看的就是这个实例）。因此按地址精确匹配，
// 命中多条时全部返回，由界面区分。
func (s *Store) assetsByInstanceAddr(addr string) ([]Asset, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id FROM assets WHERE type_key=? AND (natural_key=? OR natural_key LIKE ? ESCAPE '\')
		 ORDER BY natural_key`, TypeMiddlewareInst, addr, "%:"+escapeLike(addr))
	if err != nil {
		return nil, fmt.Errorf("查询实例资产失败: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取实例资产失败: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取实例资产失败: %w", err)
	}
	return s.assetsByIDs(ids)
}

// escapeLike 转义 LIKE 模式里的通配符：实例地址里出现 `_`（主机名常见）或 `%`
// 时，不转义就会变成通配匹配，把不相干的资产也捞进来。
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// assetsByLogSource 找出「人工声明了该日志来源」且归属节点匹配的资产。
//
// 节点也参与匹配：同一个来源名（如 applog）在多台机器上都会配置，只按来源名找
// 会把所有机器上的同名来源混成一条——那比"找不到"更糟，用户会看到一台机器的日志
// 被标成另一台机器的资产。
//
// node 为空时只按来源名找（调用方拿不到节点时的退化路径）。
func (s *Store) assetsByLogSource(source, node string) ([]Asset, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil, nil
	}
	cond := "AND a.node=?"
	args := []any{LogSourceKey, SourceManual, source, node}
	if strings.TrimSpace(node) == "" {
		cond = ""
		args = args[:3]
	}
	rows, err := s.db.Query(
		`SELECT a.id FROM assets a
		 JOIN asset_attrs t ON t.asset_id=a.id
		 WHERE t.key=? AND t.source=? AND t.value=? `+cond+`
		 ORDER BY a.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("按日志来源查询资产失败: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("读取日志来源资产失败: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取日志来源资产失败: %w", err)
	}
	return s.assetsByIDs(ids)
}

// topologyAround 从 rootID 出发做 depth 跳邻域遍历，返回节点（含跳数）、边与是否被上限截断。
//
// 逐层 BFS 而不是一条递归 SQL：每层用 IN(...) 收敛，**节点上限在每层立刻生效**——
// 否则一个高度连接的资产会在展开完成之后才被截断，代价已经付过了。
// 被上限挡在门外的节点，其相关的边也一并丢弃：图里不该出现指向"不存在的节点"的边。
func (s *Store) topologyAround(rootID int64, depth, maxNodes int) ([]TopologyNode, []TopologyEdge, bool, error) {
	if depth < 1 {
		depth = 1
	}
	if maxNodes <= 0 {
		maxNodes = DefaultTopologyNodes
	}
	depthOf := map[int64]int{rootID: 0}
	frontier := []int64{rootID}
	linkRows := map[int64]linkRow{}
	truncated := false

	for level := 0; level < depth && len(frontier) > 0; level++ {
		rows, err := s.linkRowsTouching(frontier)
		if err != nil {
			return nil, nil, false, err
		}
		var next []int64
		for _, r := range rows {
			linkRows[r.ID] = r
			for _, id := range [2]int64{r.FromID, r.ToID} {
				if _, seen := depthOf[id]; seen {
					continue
				}
				if len(depthOf) >= maxNodes {
					truncated = true
					continue
				}
				depthOf[id] = level + 1
				next = append(next, id)
			}
		}
		frontier = next
	}

	ids := make([]int64, 0, len(depthOf))
	for id := range depthOf {
		ids = append(ids, id)
	}
	assets, err := s.assetsByIDs(ids)
	if err != nil {
		return nil, nil, false, err
	}
	byID := make(map[int64]Asset, len(assets))
	nodes := make([]TopologyNode, 0, len(assets))
	for _, a := range assets {
		byID[a.ID] = a
		nodes = append(nodes, TopologyNode{Asset: a, Depth: depthOf[a.ID], Root: a.ID == rootID})
	}
	// 稳定排序：跳数 → 类型 → 自然键。同一份数据两次查询结果必须一致，
	// 否则前端力导向图每次打开都会"重新洗牌"。
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Depth != nodes[j].Depth {
			return nodes[i].Depth < nodes[j].Depth
		}
		if nodes[i].Asset.TypeKey != nodes[j].Asset.TypeKey {
			return nodes[i].Asset.TypeKey < nodes[j].Asset.TypeKey
		}
		return nodes[i].Asset.NaturalKey < nodes[j].Asset.NaturalKey
	})

	edges := make([]TopologyEdge, 0, len(linkRows))
	for _, r := range linkRows {
		from, ok1 := byID[r.FromID]
		to, ok2 := byID[r.ToID]
		if !ok1 || !ok2 {
			continue // 端点被上限挡在外面：这条边在本次邻域里不成立
		}
		edges = append(edges, TopologyEdge{
			FromID: r.FromID, ToID: r.ToID,
			From: Ref{TypeKey: from.TypeKey, NaturalKey: from.NaturalKey},
			To:   Ref{TypeKey: to.TypeKey, NaturalKey: to.NaturalKey},
			Kind: r.Kind, Source: r.Source, CreatedAt: r.CreatedAt,
		})
	}
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Kind != edges[j].Kind {
			return edges[i].Kind < edges[j].Kind
		}
		if edges[i].From.NaturalKey != edges[j].From.NaturalKey {
			return edges[i].From.NaturalKey < edges[j].From.NaturalKey
		}
		return edges[i].To.NaturalKey < edges[j].To.NaturalKey
	})
	return nodes, edges, truncated, nil
}

// linksOf 取与某资产直接相关的关联（出边与入边都返回）。
func (s *Store) linksOf(assetID int64) ([]Link, error) {
	rows, err := s.db.Query(
		`SELECT l.id,l.kind,l.source,l.created_at,
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
		var kind, source string
		if err := rows.Scan(&l.ID, &kind, &source, &l.CreatedAt,
			&l.From.TypeKey, &l.From.NaturalKey, &l.To.TypeKey, &l.To.NaturalKey); err != nil {
			return nil, fmt.Errorf("读取资产关联失败: %w", err)
		}
		l.Kind = LinkKind(kind)
		l.Source = Source(source)
		out = append(out, l)
	}
	return out, rows.Err()
}

// deleteSnapshot 删除一条快照（含字段，靠外键级联）。
//
// 用途：条件写（设标杆）失败时回收刚建的那份快照。不回收的话它会成为该资产的
// 「最近一次快照」，既白占存储，又会让下一次巡检拿它当 L2 比对基准。
func (s *Store) deleteSnapshot(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM snapshots WHERE id=?`, id); err != nil {
		return fmt.Errorf("删除巡检快照失败: %w", err)
	}
	return nil
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
func (s *Store) recordInspectRun(r InspectRun, members []InspectRunMember, findings []InspectFinding) (int64, error) {
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
	if _, err := tx.Exec(`INSERT INTO inspect_run_manifest(run_id) VALUES(?)`, runID); err != nil {
		return 0, fmt.Errorf("写入巡检资产清单标记失败: %w", err)
	}
	for _, m := range members {
		baselined := 0
		if m.Baselined {
			baselined = 1
		}
		if _, err := tx.Exec(`INSERT INTO inspect_run_members(run_id,asset_id,node_at_run,baselined,findings) VALUES(?,?,?,?,?)`,
			runID, m.AssetID, m.Node, baselined, m.Findings); err != nil {
			return 0, fmt.Errorf("写入巡检资产清单失败: %w", err)
		}
	}
	for i := range findings {
		f := &findings[i]
		result, err := tx.Exec(
			`INSERT INTO inspect_findings(run_id,asset_id,asset_type,asset_key,asset_name,node,field,kind,level,expected,actual,at)
			 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			runID, f.AssetID, f.AssetType, f.AssetKey, f.AssetName, f.Node,
			f.Field, string(f.Kind), string(f.Level), f.Expected, f.Actual, f.At,
		)
		if err != nil {
			return 0, fmt.Errorf("写入差异项失败: %w", err)
		}
		if f.BaselineAssetID != 0 {
			findingID, err := result.LastInsertId()
			if err != nil {
				return 0, fmt.Errorf("读取差异项主键失败: %w", err)
			}
			if _, err := tx.Exec(`INSERT INTO inspect_finding_baselines(finding_id,asset_id) VALUES(?,?)`, findingID, f.BaselineAssetID); err != nil {
				return 0, fmt.Errorf("写入差异项标杆来源失败: %w", err)
			}
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

// inspectRunExists 判断巡检记录是否存在（供「不存在」与「无差异」区分）。
func (s *Store) inspectRunExists(runID int64) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM inspect_runs WHERE id=?`, runID).Scan(&n); err != nil {
		return false, fmt.Errorf("查询巡检记录失败: %w", err)
	}
	return n > 0, nil
}

// memberNodeExpr 给出「成员资产归属节点」的取值表达式。
//
// 优先用资产的**当前**节点（节点迁移后不再授予历史可见性），资产已被删除时回退到
// 记录时的 node_at_run——成员行与差异项都刻意不带外键级联，就是为了让历史结论在
// 资产消失后仍然可读；只看当前节点会让这些证据整体消失，与冗余存身份字段的意图相悖。
func memberNodeExpr(alias string) string {
	return `COALESCE(` + alias + `.node, m.node_at_run)`
}

// inspectRunsInNodes 先按成员资产归属过滤，再对可见成员聚合并限制记录数。
func (s *Store) inspectRunsInNodes(limit int, nodes []string) ([]InspectRun, error) {
	if limit <= 0 {
		limit = defaultInspectRunLimit
	}
	args := make([]any, 0, len(nodes)*3+1)
	for i := 0; i < 3; i++ {
		for _, n := range nodes {
			args = append(args, n)
		}
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT r.id,r.actor,r.started_at,r.truncated,COUNT(*),SUM(m.baselined),
		(SELECT COUNT(*) FROM inspect_findings f JOIN inspect_run_members fm ON fm.run_id=f.run_id AND fm.asset_id=f.asset_id
		 LEFT JOIN assets fa ON fa.id=fm.asset_id WHERE f.run_id=r.id AND `+memberNodeExpr("fa")+` IN (`+placeholders(len(nodes))+`)
		 AND (f.kind!='deviation' OR EXISTS (
		 SELECT 1 FROM inspect_finding_baselines fb JOIN assets ba ON ba.id=fb.asset_id
		 WHERE fb.finding_id=f.id AND ba.node IN (`+placeholders(len(nodes))+`))))
		FROM inspect_runs r JOIN inspect_run_manifest v ON v.run_id=r.id
		JOIN inspect_run_members m ON m.run_id=r.id LEFT JOIN assets a ON a.id=m.asset_id
		WHERE `+memberNodeExpr("a")+` IN (`+placeholders(len(nodes))+`)
		GROUP BY r.id ORDER BY r.started_at DESC,r.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询可见巡检记录失败: %w", err)
	}
	defer rows.Close()
	out := []InspectRun{}
	for rows.Next() {
		var r InspectRun
		var truncated int
		if err := rows.Scan(&r.ID, &r.Actor, &r.StartedAt, &truncated, &r.Assets, &r.Baselined, &r.Findings); err != nil {
			return nil, fmt.Errorf("读取可见巡检记录失败: %w", err)
		}
		r.Truncated = truncated != 0
		r.PartialScope = true
		r.Scope = "scope:mine"
		out = append(out, r)
	}
	return out, rows.Err()
}

// inspectFindingsInNodes 对无可见成员或旧格式运行统一返回不可见。
func (s *Store) inspectFindingsInNodes(runID int64, limit int, nodes []string) ([]InspectFinding, bool, error) {
	if limit <= 0 {
		limit = defaultInspectFindingLimit
	}
	args := make([]any, 0, len(nodes)+1)
	args = append(args, runID)
	for _, n := range nodes {
		args = append(args, n)
	}
	var visible int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM inspect_run_members m JOIN inspect_run_manifest v ON v.run_id=m.run_id
		LEFT JOIN assets a ON a.id=m.asset_id WHERE m.run_id=? AND `+memberNodeExpr("a")+` IN (`+placeholders(len(nodes))+`)`, args...).Scan(&visible)
	if err != nil {
		return nil, false, fmt.Errorf("查询巡检记录范围失败: %w", err)
	}
	if visible == 0 {
		return nil, false, nil
	}
	for _, n := range nodes {
		args = append(args, n)
	}
	args = append(args, limit)
	rows, err := s.db.Query(`SELECT f.id,f.run_id,f.asset_id,f.asset_type,f.asset_key,f.asset_name,f.node,
		f.field,f.kind,f.level,f.expected,f.actual,f.at FROM inspect_findings f
		JOIN inspect_run_members m ON m.run_id=f.run_id AND m.asset_id=f.asset_id
		LEFT JOIN assets a ON a.id=m.asset_id WHERE f.run_id=? AND `+memberNodeExpr("a")+` IN (`+placeholders(len(nodes))+`)
		AND (f.kind!='deviation' OR EXISTS (
			SELECT 1 FROM inspect_finding_baselines fb JOIN assets ba ON ba.id=fb.asset_id
			WHERE fb.finding_id=f.id AND ba.node IN (`+placeholders(len(nodes))+`)))
		ORDER BY CASE f.level WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
		f.asset_type,f.asset_key,f.field LIMIT ?`, args...)
	if err != nil {
		return nil, false, fmt.Errorf("查询可见差异项失败: %w", err)
	}
	defer rows.Close()
	out := []InspectFinding{}
	for rows.Next() {
		var f InspectFinding
		var kind, level string
		if err := rows.Scan(&f.ID, &f.RunID, &f.AssetID, &f.AssetType, &f.AssetKey, &f.AssetName,
			&f.Node, &f.Field, &kind, &level, &f.Expected, &f.Actual, &f.At); err != nil {
			return nil, false, fmt.Errorf("读取可见差异项失败: %w", err)
		}
		f.Kind, f.Level = FindingKind(kind), FindingLevel(level)
		out = append(out, f)
	}
	return out, true, rows.Err()
}

func (s *Store) baselinesInNodes(nodes []string) ([]Baseline, error) {
	args := make([]any, 0, len(nodes))
	for _, n := range nodes {
		args = append(args, n)
	}
	rows, err := s.db.Query(`SELECT b.type_key,b.asset_id,b.asset_key,b.snapshot_id,b.set_by,b.set_at
		FROM inspect_baselines b JOIN assets a ON a.id=b.asset_id
		WHERE a.node IN (`+placeholders(len(nodes))+`) ORDER BY b.type_key`, args...)
	if err != nil {
		return nil, fmt.Errorf("查询可见巡检标杆失败: %w", err)
	}
	defer rows.Close()
	out := []Baseline{}
	for rows.Next() {
		var b Baseline
		if err := rows.Scan(&b.TypeKey, &b.AssetID, &b.AssetKey, &b.SnapshotID, &b.SetBy, &b.SetAt); err != nil {
			return nil, fmt.Errorf("读取可见巡检标杆失败: %w", err)
		}
		out = append(out, b)
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

func (s *Store) setBaselineIfCurrent(b Baseline, currentAssetID int64, nodes []string) (bool, error) {
	allowed := ""
	args := []any{b.TypeKey, b.AssetID, b.AssetKey, b.SnapshotID, b.SetBy, b.SetAt}
	if nodes != nil {
		if len(nodes) == 0 {
			return false, nil
		}
		allowed = ` AND node IN (` + placeholders(len(nodes)) + `)`
		for _, n := range nodes {
			args = append(args, n)
		}
	}
	var res sql.Result
	var err error
	if currentAssetID == 0 {
		args = []any{b.TypeKey, b.AssetID, b.AssetKey, b.SnapshotID, b.SetBy, b.SetAt, b.AssetID}
		if nodes != nil {
			for _, n := range nodes {
				args = append(args, n)
			}
		}
		query := `INSERT INTO inspect_baselines(type_key,asset_id,asset_key,snapshot_id,set_by,set_at)
			SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM assets WHERE id=?` + allowed + `)
			ON CONFLICT(type_key) DO NOTHING`
		res, err = s.db.Exec(query, args...)
	} else {
		args = []any{b.AssetID, b.AssetKey, b.SnapshotID, b.SetBy, b.SetAt, b.TypeKey, currentAssetID, b.AssetID}
		if nodes != nil {
			for _, n := range nodes {
				args = append(args, n)
			}
		}
		query := `UPDATE inspect_baselines SET asset_id=?,asset_key=?,snapshot_id=?,set_by=?,set_at=?
			WHERE type_key=? AND asset_id=? AND EXISTS (SELECT 1 FROM assets WHERE id=?` + allowed + `)`
		if nodes != nil {
			query += ` AND EXISTS (SELECT 1 FROM assets WHERE id=inspect_baselines.asset_id` + allowed + `)`
			for _, n := range nodes {
				args = append(args, n)
			}
		}
		res, err = s.db.Exec(query, args...)
	}
	if err != nil {
		return false, fmt.Errorf("设置巡检标杆失败: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) clearBaselineIfCurrent(typeKey string, currentAssetID int64, nodes []string) (bool, error) {
	args := []any{typeKey, currentAssetID}
	query := `DELETE FROM inspect_baselines WHERE type_key=? AND asset_id=?`
	if nodes != nil {
		if len(nodes) == 0 {
			return false, nil
		}
		query += ` AND EXISTS (SELECT 1 FROM assets WHERE id=inspect_baselines.asset_id AND node IN (` + placeholders(len(nodes)) + `))`
		for _, n := range nodes {
			args = append(args, n)
		}
	}
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return false, fmt.Errorf("清除巡检标杆失败: %w", err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
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
