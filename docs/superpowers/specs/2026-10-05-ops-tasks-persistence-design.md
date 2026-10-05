# 操作任务（ops_tasks）入库（设计件，批次 19）

**日期**：2026-10-05　**状态**：待实施（决策点见 §5）
**位置**：持久化顺序里的 ③（① `defense_tasks` 已修 → ② 审计与告警处置**已入库并实机验证**（批次 18）→ **③ 本文** → ④ `instancereg` 回退台账 → ⑤ `users.yaml`）。

## 1. 现状（复核结论）

- 存储：`internal/server/ops/store.go:32` 的 `ops_tasks.json`，**每次状态流转都 `json.MarshalIndent` 整体重写**
  （`save()`，`store.go:140-177`），内存里是 `tasks map[string]*Task` + `caps` + `seq`。
- 上限：`maxTasks = 500`（`store.go:38-42`），`prune()` 只丢**已完成**的最老任务
  （`store.go:329-352`，queued/running 永不丢——它们还有回执要等）。
- 与 ② 的**关键差别**：这里**没有无界增长问题**（已有 500 条上限）。痛点是
  ① 每次状态流转重写 500 条；② 不能按时间/维度检索（列表只能在内存里过滤）。

## 2. 「顺带」那条已经不需要做了（**记忆里的说法是过时的**）

原计划顺带"把容器页的「认领上次结果」从拉 50 条在客户端过滤改成一次条件查询"。复核发现
`web/src/components/container/ContainerView.vue:344-374` 的 `adoptLastResult` **早就是条件查询**：

```js
listOpsTasks({ node: cluster.node, kind: target.kind, state: 'succeeded', limit: 50 })
```

只有 `params.cluster` / `params.namespace` 的匹配留在前端 —— 那是因为服务端 `List` 没有
params 过滤（params 是 map，要按它筛得做 JSON 提取或加索引列），**不是遗漏**。
→ 本批**不含**这项改动；要做得单独评估"给 params 加可检索列"的收益。

## 3. 表结构（追加到 `asset/store.go` 的 `schemaStatements`，纯附加表、**不升 schemaVersion**）

```sql
-- 一条操作任务一行。回执载荷（payload）与参数（params）存 JSON 列：
-- 它们永远随任务整体读写，拆表只会让每次状态流转多几次 join。
CREATE TABLE IF NOT EXISTS ops_tasks(
  id           TEXT PRIMARY KEY,        -- 形如 ops-7（沿用既有可读 ID，界面与日志都直接引用）
  batch_id     TEXT NOT NULL DEFAULT '',
  node         TEXT NOT NULL DEFAULT '',
  kind         TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT '',
  operator     TEXT NOT NULL DEFAULT '',
  operator_ip  TEXT NOT NULL DEFAULT '',
  reason       TEXT NOT NULL DEFAULT '',
  message      TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL DEFAULT 0,
  delivered_at INTEGER NOT NULL DEFAULT 0,
  started_at   INTEGER NOT NULL DEFAULT 0,
  done_at      INTEGER NOT NULL DEFAULT 0,
  params       TEXT NOT NULL DEFAULT '{}',
  file_meta    TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_ops_tasks_created ON ops_tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_ops_tasks_state   ON ops_tasks(state, created_at DESC);
-- 「认领上次结果」要按 节点+动作+状态 找最近一条成功回执，这个索引就是为它建的。
CREATE INDEX IF NOT EXISTS idx_ops_tasks_pick    ON ops_tasks(node, kind, state, done_at DESC);
-- seq 与 caps 是**有界元数据**（seq 一个数、caps 每个节点一行），单独两张小表，让 JSON 彻底退休。
CREATE TABLE IF NOT EXISTS ops_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS ops_caps(node TEXT PRIMARY KEY, kinds TEXT NOT NULL DEFAULT '');
```

## 4. 改造清单（**这是本批的主要工作量：10 个写入点**）

`save()` 全量重写要拆成行级写入，逐个写入点改并回归状态机（`store_test.go` 已覆盖生命周期）：

| 写入点 | 位置 | 改成 |
|---|---|---|
| `create` | `store.go:205` | INSERT 一行 |
| `CreateMany` | `store.go:250` | 单事务批量 INSERT（"要么整批成立，要么什么都不建"必须保持） |
| `Cancel` | `store.go:287` | UPDATE state |
| `Remove` | `store.go:311` | DELETE（仅终态） |
| `prune` | `store.go:331` | DELETE 最老的已完成，直到行数 ≤ maxTasks |
| `Take` | `store.go:357` | UPDATE state=delivered（**每轮上报调用**） |
| `failTask` | `store.go:433` | UPDATE state=failed |
| `ApplyResult` | `store.go:493` | UPDATE（**每轮上报调用**） |
| `ExpireOverdue` | `store.go:577` | UPDATE 批量（**每轮上报调用**） |
| `SaveCaps` | `store.go:599` | upsert `ops_caps`（"只在变化时才写"要保留） |

**内存 map 保留为读缓存**（与批次 18 的处置存储同一取向）：`Get`/`List`/`Caps`/`FileRefInUse`
在热路径上，不该每次查库。启动时一次性 `load` 建缓存。

**回填**：`ops_tasks.json` 一次性导入（判据「表为空」），原文件改名 `.bak-migrated`；
回填失败不阻断启动、退回 JSON 降级模式（与批次 18 完全同构）。

## 5. 需要拍板的决策点

| # | 决策 | 建议 |
|---|---|---|
| **D1** | `caps` 与 `seq` 是否也入库 | **入库**（两张小表），让 `ops_tasks.json` 彻底退休 —— 否则"一半在库、一半在文件"是最难解释的形态 |
| **D2** | 回执载荷（`payload`）怎么存 | **JSON 列**。它永远随任务整体读写；拆表只在"要按回执内容检索"时才值 |
| **D3** | 保留口径 | **沿用 500 条 + 只丢已完成**（现状语义，不引入时间保留）。理由：任务与审计不同，它没有"查很久以前"的合规诉求，而 queued/running 丢了会让操作者永远不知道结果 |
| **D4** | 是否同时把"列表分页"改成 SQL 分页 | **本批不做**，只保证行为等价；分页留到有真实诉求时（前端目前按 `limit` 拉一屏） |

## 6. 验证计划

1. **行为等价**：`store_test.go` 全绿（生命周期、能力门控、批次、取消、删除、回执、过期）。
2. **回填幂等**：带旧 JSON（含 queued/running/已完成三类）启动 → 表里条数与状态一致、文件改名；重启不重复导入。
3. **热路径**：每轮上报的 `Take`/`ApplyResult`/`ExpireOverdue` 在入库模式下不产生额外全表写。
4. **实机**：dev-server 上跑一次节点操作（只读动作）与批量下发，确认状态流转与回执展示不变。

## 7. 实施记录（按批次追加）

### 批次 19（2026-10-05）：操作任务入库（未打包）

**实施中的一个关键简化（比原计划更省、更稳）**：**那十个写入点一行都没改**。原计划是"逐点改成行级写入"，
实际做法是**在 `save()` 里与「上次落盘快照」做差**：内存 map 仍是唯一读源，`save()` 比对 `flushed`
（上次落盘的任务副本）+ `capsFlushed` + `seqFlushed`，只写变化的那几行，**没有任何变化时一次写都不做**
（能力每次上报都会写，但绝大多数轮次是没变的）。好处是状态机完全不动，也就不存在"漏改一处导致某个状态
不落盘"的风险；代价是每次 save 要 `reflect.DeepEqual` 一遍内存里的任务（≤500 条，微秒级）。

**表结构也简化了**：任务**整条存 `task_json` 列**，只把真正要筛的字段（node / kind / state / 时间 / 批次）
提成索引列。逐字段建十几列会让"每加一个任务字段就要改表"，而任务永远整体读写。
索引 `idx_ops_tasks_pick(node, kind, state, done_at)` 就是给容器页「认领上次结果」那条查询建的。

**测试抓到一个真 bug（已修）**：`UseSQLite` 原先只在「任务表非空」时才从库重建内存 —— 但
**caps 与 seq 在另外两张表里**，「任务表为空」不等于「库里什么都没有」。表现是重启后节点能力凭空消失
（`Caps("web-01")` 变空），而能力是**第二道护栏**：消失的后果是该节点不再声明任何动作，
界面会显示"该节点未放行"。已改为无论是否回填都**以库为准重建内存**。

**测试**：`ops/store_sqlite_test.go` 两个用例 —— ① 回填任务 + 能力 + 序号，且**序号必须接着走**
（否则回填后新建的任务会从 1 开始，与既有 `ops-1`/`ops-2` **撞 ID**，会让整批取消或回执匹配到错的任务）、
原文件改名、重复启动不重复导入；② 行级差量写入：创建 / 取消 / 删除 / 能力都**直接查库**验证（不只看内存），
并验证重启后能力仍在。`go test ./...` 全绿。

**未做**：① 实机验证（dev-server 上真实的 `ops_tasks.json` 回填 + 一次真实下发）；
② 打包；③ 前端未改（列表接口与行为完全等价，无需改）；④ SQL 分页（决策点 D4）。
