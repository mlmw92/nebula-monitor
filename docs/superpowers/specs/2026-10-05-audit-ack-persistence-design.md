# 审计事件与告警处置入库（设计件，批次 18）

**日期**：2026-10-05　**状态**：待评审（决策点见 §7）
**前置**：2026-10-04 的持久化盘点（`project_refactor_roadmap.md`）定下的顺序
① `defense_tasks.json` 无界增长（**已修** `beab0ca`）→ **② 审计 + 告警处置入库**（本文）→ ③ `ops_tasks` 入库 →
④ `instancereg` 回退资产台账 → ⑤ `users.yaml` 入库。

## 1. 为什么做这件事

不是"顺序到了"，而是**审计现在会自我截断**，而它恰恰是全平台最该可信的一份数据：

- `internal/server/audit/store.go:19` —— `MaxEvents = 2000`，`trim` 保留最近 2000 条（`store.go:186-191`），
  加载时也 trim（`store.go:55`）。**没有时间语义**：写满 2000 条后，最老的证据被静默丢弃。
- `store.go:61-77` —— 每次 `Record` 都 `json.MarshalIndent` **全量重写**整个文件（`config.AtomicWrite`）。
- 读侧只有 `List/ListFiltered(limit, user, path, category)`（`store.go:80-114`），**无分页、无时间范围**；
  API `GET /api/v1/audit/events`（`api/security_api.go:61-75`，权限 `audit:read`）只有一个 `limit` 截断。
- 它与业务数据同权限面、是普通文件（可被删）—— 我们定「不提供任意命令 / 脚本执行」这条安全边界时，
  **依赖的就是"操作留痕"这条证据链**。一份会被自己截断的证据链，等于没有。

告警处置（ack）是同一形态的问题，只是换了名字：
- `internal/server/alert/ackstore.go:286-301` —— 同样 JSON 全量重写；`map[key]AckInfo`（key = `rule|host|instance|startsAt`）。
- 保留策略在 `internal/server/retention/retention.go:40`（`DefaultAcksDays = 90`）+ `:266-270`
  调 `PruneHandled(cutoff)`（`ackstore.go:232-250`，只删「已处置」、pending 永不删）。

**为什么现在做**：它是 ③④⑤ 的**前置样板**。三张表同一套手法，而且必须先回答 2026-10-04 提出的那个问题 ——
`assets.db` 现在是 `SetMaxOpenConns(1)` 单连接 + WAL（`asset/store.go:199-205`），**再加写入更频繁的表够不够用**。
这个问题不定，后面三批每批都要重新评估一遍。

## 2. 现状事实（改造面）

| 项 | 现状 | 证据 |
|---|---|---|
| 审计存储 | JSON `audit_events.json`（与 `server.yaml` 同目录），全量重写，2000 条截尾 | `audit/store.go:19,61-77,186-191`；`cmd/server/main.go:308` |
| 审计事件字段 | `Time/User/Method/Path/Status/RemoteIP/SourceLocation/Succeeded/Category/Action/Detail` | `audit/store.go:22-34` |
| 审计写入点 | 中间件（非 GET/HEAD、非 login、非 `/report`、非 `*/upload`）+ 若干 handler 显式 `Record` | `api/audit.go:25-73,103-121,153-185`；`ops_api.go:202/348/442/475`、`defense_api.go:145`、`asset_batch_api.go:148/347`、`permission_api.go:354` |
| 审计读入口 | `GET /api/v1/audit/events`（`audit:read`）、`GET /api/v1/audit/export`（`audit:export`） | `api/query.go:275-276`；`api/security_api.go:39-86` |
| 审计保留 | **retention 只管统计**（`Count()` vs `MaxEvents`），不裁剪 | `retention/retention.go:226-228` |
| ack 存储 | JSON `alert_acks.json`，全量重写，无条数上限；评论内嵌（≤50 条、单条 ≤2000 字） | `alert/ackstore.go:26-29,286-301`；`cmd/server/main.go:129` |
| ack 保留 | 90 天、只删已处置（`PruneHandled`） | `retention/retention.go:40,266-270`；`ackstore.go:232-250` |
| ack 读写入口 | `GET /api/v1/alerts/acks`（`alerts:read`）+ 4 个 POST（`alerts:write`）；前端 `web/src/components/AlertsView.vue` | `api/query.go:258-262,1249-1261`；`api/alert_collab.go:76-125` |
| 台账库 | `assets.db`：WAL + `busy_timeout(5000)` + `foreign_keys(1)` + `synchronous(NORMAL)`，单连接；`schemaVersion=2`；建表集中在 `schemaStatements`；`migrate()` 幂等 | `asset/store.go:15-20,24-182,192-253` |
| 补列范式 | 查列是否存在（**不按版本号判断**），单连接下须先读完 rows 再 ALTER | `asset/store.go:255-304` + `store_migrate_test.go:14-74` |
| 纯附加表 | **不提升 `schemaVersion`**（老版本不读它，保留回滚） | `asset/store.go:105-117,159-169` 注释 |
| 幂等迁移先例 | `MigrateSingleAdmin`（已存在则跳过） | `auth/migration.go:20-55` |
| 分页范式（可参照） | 资产列表：`limit/offset` + 同条件 `total`（子查询选 ID 再 join，`assetWhere` 共用） | `asset/store.go:505-593,808-874`；`api/asset_api.go:253-293` |
| 时间范围范式（可参照） | 日志检索：`from/to` 毫秒 + `limit` 夹紧 + 游标 | `api/logs_api.go:15,83-138` |

## 3. 目标形态

### 3.1 表结构（追加到 `asset/store.go` 的 `schemaStatements`）

```sql
-- 审计事件：只追加；除保留策略的过期清理外，不修改、不删除。
CREATE TABLE IF NOT EXISTS audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  time_ms    INTEGER NOT NULL,                  -- 事件时间（毫秒）
  user_name  TEXT NOT NULL DEFAULT '',
  method     TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL DEFAULT '',
  status     INTEGER NOT NULL DEFAULT 0,
  remote_ip  TEXT NOT NULL DEFAULT '',
  source_loc TEXT NOT NULL DEFAULT '',          -- 来源 IP 属地（ip2region 补全）
  succeeded  INTEGER NOT NULL DEFAULT 0,
  category   TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL DEFAULT '',
  detail     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_events_time ON audit_events(time_ms DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_user ON audit_events(user_name, time_ms DESC);

-- 告警处置：ack_key 沿用现有 key 形态（rule|host|instance|startsAt），语义零变化。
CREATE TABLE IF NOT EXISTS alert_acks (
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
  comments      TEXT NOT NULL DEFAULT '[]'      -- JSON 数组，随记录整体读写（见决策点 D3）
);
CREATE INDEX IF NOT EXISTS idx_alert_acks_time ON alert_acks(time_ms DESC);
CREATE INDEX IF NOT EXISTS idx_alert_acks_status ON alert_acks(status, time_ms DESC);
```

**不提升 `schemaVersion`**：两张都是纯附加表，老版本不读它、可回滚（沿用 `asset_seen` / `asset_ignored` 的既有规则）。
**但回滚有限制**，见 §6。

### 3.2 写入路径

调用点**一个都不改**：`audit.Store.Record(Event) error` 与 `AckStore.Mark/Assign/Close/Reopen/Comment`
的签名保持不变，只把内部实现从"改内存 + 全量重写 JSON"换成"一次 INSERT/UPDATE"。这是本批改动面最小的关键。

- 审计 `Record` → `INSERT INTO audit_events(...)`（单条，无事务）。
- ack 各动作 → `INSERT ... ON CONFLICT(ack_key) DO UPDATE`（一次写一条，不再重写全表）。

### 3.3 保留策略（retention 接管）

| 对象 | 现在 | 改为 |
|---|---|---|
| 审计 | 2000 条截尾（`MaxEvents`），retention 只统计 | **时间保留 `AuditDays`（默认 180 天）** + 兜底条数上限（默认 500000，防单机磁盘被写爆）；`cleanup` 里 `DELETE FROM audit_events WHERE time_ms < cutoff` |
| ack | 90 天 + 只删已处置 | **语义不变**，`PruneHandled` 内部改成 `DELETE ... WHERE status IN ('ack','closed') AND time_ms < ?` |

`retention.Status` 的审计一项从"`Count()` vs `MaxEvents`"改为"表行数 + 最老/最新时间"（`retention.go:226-228`）。

### 3.4 查询接口

| 接口 | 变化 |
|---|---|
| `GET /api/v1/audit/events` | 新增 `from` / `to`（毫秒）与 `offset`；返回体新增 `total`（同条件计数）。**现有 `user`/`path`/`category`/`limit` 参数语义不变** —— 前端不改也能用 |
| `GET /api/v1/audit/export` | 同样的筛选参数，**不受 limit 限制**（按条件流式查全量） |
| ack 全部接口 | **不变**（`Map()` 仍返回 `map[string]AckInfo`，前端零改动） |

分页选 `limit/offset + total`（与资产列表一致），理由见决策点 D5。

## 4. 迁移与回填

启动时执行，**幂等**（沿用 `MigrateSingleAdmin` 的"已存在则跳过"思路）：

1. `CREATE TABLE IF NOT EXISTS`（由既有 `migrate()` 自动完成）。
2. 若 `SELECT COUNT(*) FROM audit_events` = 0 **且** `audit_events.json` 存在 →
   读入全部事件 → **单事务批量 INSERT** → 原文件改名为 `audit_events.json.bak-migrated` → 日志明示条数与去向。
3. ack 同理（`alert_acks.json` → `alert_acks.json.bak-migrated`）。
4. 判据是"**表为空**"而不是"文件存在"：回填只可能发生一次，重复启动不会重复导入，也不需要对事件去重
   （审计事件没有天然唯一键，硬造一个只会引入假数据）。

**回填失败不阻断启动**：解析失败时**保留原文件不动**、日志报错、以空表启动（此时新事件照常入库）。
宁可丢历史，也不能因为一份坏 JSON 让服务起不来 —— 与既有的"采集资产删了会被重建"取向一致：**可用性优先**。

## 5. 与既有能力的边界

- **本批不做"不可篡改"**。入库解决的是"被自己截断"，**不解决"被删除/篡改"**：库文件仍在同一台机器、
  同一权限面下，攻击者第一步依旧是清日志。真正的防篡改需要**只追加的外部存储**（异地 syslog / 对象存储
  的对象锁），那是另一个量级的工程。**不要把入库说成"审计更安全了"** —— 它只是"审计不再自己丢"。
- 不做审计的实时告警联动、不做审计的按字段检索（`Detail` 是自由文本）。
- 不拆 `alert_ack_comments` 表（触发条件见 D3）。
- 不动 `ops_tasks` / `instancereg` / `users.yaml`（③④⑤ 各自成批）。

## 6. 回滚限制（**必须写进发布说明**）

新增表不升 `schemaVersion`，所以**二进制可以回滚**；但**数据不可见**：

> 滚回旧版本后，审计事件列表与告警处置列表会**变空**（旧版本读 JSON，而 JSON 已改名为
> `*.bak-migrated`）。数据仍在 `assets.db` 里，把文件名改回去即可让旧版本重新看到**迁移前**的快照。

因此发布说明里**不能写"完全兼容"**。这与批次 17「新增资产类型回滚后类型标签缺失」是同一类限制。

## 7. 需要拍板的决策点

| # | 决策 | 建议（理由） |
|---|---|---|
| **D1** | 放**同一个 `assets.db`** 还是新建独立库 | **同一个**。审计写入频率是"人工管理操作"级（每分钟几条到几十条），ack 更低；而现有 `assets.db` 已在承载"每轮上报 × 资产数"的写入（约 1 条/秒）。单连接下这点增量可忽略，换来"一个备份文件、一套迁移机制、一次事务"。独立库的隔离性收益有限（同机同权限面） |
| **D2** | 审计保留口径 | **时间 180 天 + 兜底条数 500000**。入库的意义就在"能查到更久以前"；只保留条数等于搬了个家。两个上限都进 retention 配置 |
| **D3** | ack 评论：**单表 JSON 列** vs 拆 `alert_ack_comments` 表 | **单表 JSON 列**。评论永远随处置记录整体读写（不存在跨记录查评论的需求），拆表只让 `Map()`/`Get()` 多一次 join。**触发条件**：出现"按评论内容检索/统计"或"评论条数需要独立上限"时再拆 |
| **D4** | 回填后原 JSON 文件怎么处置 | **改名为 `*.bak-migrated`**（不删，留证据 + 可回滚）。代价是回滚后列表变空，见 §6 |
| **D5** | 审计列表分页方式 | **`limit/offset + total`**（与资产列表一致）。审计是低频写入的"翻页查阅"场景，不存在日志那种"翻页途中文件增长"的漂移问题，用不上游标 |

## 8. 验证计划（本批必须做，不能只跑单测）

1. **迁移幂等**：带旧 `audit_events.json`（>2000 条，验证截尾之外的记录也被搬走）启动 → 表里有全部事件、
   文件已改名；再次启动**不重复导入**。
2. **保留策略**：把 `AuditDays` 调成 0 → 一轮 cleanup 后过期行被删、未过期行还在；ack 的 90 天语义不变
   （pending 永不删）。
3. **单连接争用实测**（2026-10-04 提出的前置问题）：在 Agent 正常上报（每 20 秒一轮）的同时，
   连续发 200 次管理写操作 → 看审计写入耗时、`SQLITE_BUSY`、上报落库是否被拖慢。
4. **接口回归**：`from/to/offset/total` 正确；**旧参数（`user`/`path`/`category`/`limit`）行为不变**；
   导出按条件出全量 CSV。
5. **回滚演练**：装回 1.30.x 旧 Server → 确认列表为空、把 `.bak-migrated` 改回名字后旧版本能读回迁移前快照。

## 9. 实施记录（按批次追加）

### 批次 18（2026-10-05）：审计与告警处置入库（未打包）

决策点 D1~D5 按建议拍板后落地：两张表进 `asset/store.go` 的 `schemaStatements`（**纯附加表、不升
`schemaVersion`**）、两个存储的入库模式 + 一次性回填、保留策略接管、审计查询的时间范围与分页。

**落地时定的实现细节（设计件之外的补充）**：

| 决定 | 理由 |
|---|---|
| 保留**降级模式**：台账库打不开时审计/处置退回 JSON | `asset.Open` 失败本来就只关掉台账能力；审计与台账是两件事，不该跟着失效。与"回填失败不阻断启动"同一取向 |
| 审计查询失败返回 **500 而不是空列表** | 返回空 = 界面显示"没有任何操作记录"。对审计来说这是一句谎话（等于说"没人干过事"），比报错糟得多 |
| 处置的**内存 map 保留为读缓存** | 告警引擎的 `IsHandled` 在热路径上，不该每次查库；库只在写入时 upsert 一行 |
| 顺带修：`retention.New` 从**默认值**起解 YAML（原来从零值） | 原写法下配置文件里**没有**的字段会解成 0 = 「不清理」——新增保留类（本批 `auditDays`、C2 的 `logsDays`）后，存量部署会静默地永不清理它，看起来像"清理跑了但什么都没做" |
| 导出上限取 `audit.MaxEvents`（2000） | 与入库前的内存上限同档，避免一次导出把库读爆；要更多就按时间范围分批 |
| `audit.MaxRows = 500000` 兜底条数 | 时间保留是主口径，条数是"单机磁盘被写爆"的安全网 |

**测试**：`audit/store_sqlite_test.go`（回填搬走**文件里的全部** 2100 条而不是内存截尾后的 2000 条、
原文件改名且重复启动幂等、分页与路径子串过滤、按时间与按条数清理、**坏文件保留现场且不阻断启动**）；
`alert/ackstore_sqlite_test.go`（回填、关闭/评论**真的落库**（用库读回验证）、重启不重复导入、
pending 永不删）；`retention_test.go` 三处断言按新口径更新。`go test ./...` 全绿。

**未做（明确记录）**：

1. ~~**前端未适配**~~ **已于同日补上**（`web/src/components/AuditView.vue`）：筛选条加时间范围
   （含「今天 / 近 7 天 / 近 30 天」快捷项）、条数选择改为每页条数、表格下加分页（总数取服务端同条件计数）。
   两处口径：**改筛选条件回到第 1 页**（否则第 3 页 + 新条件查出空列表，看起来像"没有记录"）；
   **导出不带 offset**（导出的是"符合条件的前 N 条"，与当前页码无关，服务端上限 2000）。
   留下的欠账：**新的时间范围与分页行为没有自动化用例，也没在界面实点过**——本批唯一未验的改动。
2. **实机验证未做**：需在 dev-server 上用真实的 `audit_events.json` / `alert_acks.json` 跑一遍，
   含回滚演练（滚回旧版本列表应为空、把 `.bak-migrated` 改回名字可恢复旧版本可见）。
3. ③④⑤（`ops_tasks` / `instancereg` / `users.yaml`）未动。

### 批次 20（2026-10-07）：变更 ↔ 审计的强关联

收能力全景 §3.13「变更历史与审计」那条 P0 欠账的最后一句（"仍缺与操作审计的显式交叉链接"）。
在此之前两者**只共享操作人字符串**：审计回答"谁在什么时候调了哪个接口"，变更记录回答"哪个字段
从什么变成什么"，而「**那一次调用**把哪些资产的哪些字段从什么改成了什么」——这个问题单看任一侧
都答不了，正是这个关联存在的理由。

#### 关联键用「请求 id」，而不是「审计 id」

审计行是**在 handler 返回之后**才写的（要拿状态码与成功标志），也就是说写变更记录那一刻审计 id
还不存在——"先写审计、再把它的 id 写进变更"这条路走不通（除非把审计提前进 handler，那会改掉既有
语义）。因此反过来：**由审计中间件在执行业务 handler 之前给请求发一个 id**，两侧各写一份。好处是
不依赖写入顺序，也就不需要跨包事务。

- id 形态：8 字节随机数的十六进制。**刻意不用"时间戳 + 序号"**：那类 id 看着能排序又能当索引，
  实际会让人以为它可靠——多实例、时钟回拨都对不上；它只需要够随机、不撞。随机源不可用时返回空
  （宁可没有关联，也不给出一个可能重复的 id：重复会把两次操作混成一次，那是"看着像证据"的错）。
- **发号位置在"跳过判断"之后**：只读请求、登录、Agent 上报不发号，于是
  「有 request id 的变更 ⇔ 有一次可查的审计」这个不变式成立，采集侧写的变更天然留空。
- 语义：**一次请求 = 一次操作**。批量维护一个请求改几百条资产，同一个 id 把整批串成一条线——
  这正是这个关联最该回答的问题（"这次操作到底动了谁"）。

#### 数据与接口

| 层 | 改动 |
|---|---|
| 表 | `asset_changes.request_id` / `audit_events.request_id`（纯附加列，**不升 schemaVersion**，按 `ensureColumn` 自愈补列） |
| 索引 | 两条**部分索引**（`WHERE request_id <> ''`）：采集侧写的变更没有关联 id，那片空串不必进索引 |
| 写入（资产侧） | 三条人工写路径带上：`Apply`（经 `Observation.RequestID`）、`SetLabels`、`ResetManual`（含恢复采集值那条内联 INSERT） |
| 写入（审计侧） | 中间件、`RecordChangeAudit`、以及全部显式 `Record` 调用点（权限/下发/防御/批量/导出）都带上 |
| 正向 | `GET /api/v1/assets/{id}/history` 每条记录带 `requestId`（顺带把此前查出却没返回的记录 `id` 也带上） |
| 反向 | 新增 `GET /api/v1/assets/changes?requestId=`：跨资产列出这次操作改动的字段（带资产身份），**受资产范围约束**（节点 + 业务标签两个维度都下推到 SQL） |
| 过滤 | `GET /api/v1/audit/events?requestId=` **精确匹配**（不透明句柄，子串匹配会把相邻操作一并带出来） |
| 前端 | 资产变更历史里带关联 id 的记录给「查看对应审计」→ `/audit?requestId=`（门控 `audit:read`）；审计页认领该深链（渲染成可关闭的条件标签）、每行给「本次改动」抽屉（门控 `assets:read`） |

#### 三条刻意的取舍

1. **空关联 id 一律拒绝**（接口 400、服务层也报错）：空值对应的是采集侧写的**全部**变更，
   放行等于把整张变更表当成"这一次操作"——那是错误信息，不是缺省行为。
2. **历史行不回填**：不按"时间 + 操作人"近似补一个 id。那种关联看着像证据、实际是猜的，
   比没有关联更糟。界面在空值处不给入口（不摆一个点进去查不到东西的按钮）。
3. **依赖新列的部分索引必须排在补列之后**（`postAlterIndexes`）：存量库那两张表**还没有这一列**，
   若把索引放进 `schemaStatements`，存量库启动会在建索引时报 `no such column`，把服务打成起不动。
   这个顺序由 `asset/store_migrate_test.go` 用旧形态的两张表钉住。

#### 验证（本批）

`asset/change_link_test.go`（人工四类写入都带 id、采集侧留空、按 id 反查带资产身份、
业务标签与节点两个维度的范围收窄、空选择器恒不匹配、条数上限**显式回报截断**）；
`asset/store_migrate_test.go`（**表已存在但缺列**的升级路径：能打开、存量行留空、两条部分索引
建出来、新行可写可查）；`api/asset_audit_link_test.go`（走完整链路：中间件发号 → 写接口随行 →
变更落库 → 审计列表按 id 过滤 → 反向查变更；只读请求不发号；反向端点按范围收窄、缺/空 id 一律 400）；
`audit/store_sqlite_test.go`（入库模式下按 id 过滤且**子串不算命中**）。
`go test ./...` 30 包全绿；前端 `npm test` 18 文件 106 项通过（新增 8 项）、`vite build` 通过。

#### 未覆盖边界（不得视为通过）

- **实机验证未做**：需在 dev-server 上走一条真实人工写操作，确认审计行与变更记录拿到同一个 id、
  `/api/v1/assets/changes` 按范围收窄、以及升级前的历史记录两侧都为空。
- **升级分界没有在真机上划过**：升级前的记录两侧都没有关联 id，"按 id 查不到"是预期行为
  （界面文案已说明）——这与 Pod 日志那次「已入库的行不会被重写」同类，核对时要按 Agent/Server
  换装时刻划界，不能把"旧数据查不到"判成修复失败。
- **前端入口没在浏览器里实点过**：只有组件用例（深链认领、条件可关闭、抽屉内容、截断提示、
  权限门控），没有真实点击 + 视觉确认。
