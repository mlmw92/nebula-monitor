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

（待实施后追加）
