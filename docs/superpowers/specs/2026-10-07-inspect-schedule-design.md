# 配置巡检的周期化调度（设计件）

> 范围：把**配置巡检**（`inspect`）从"只能人工点"变成"可定时跑"。
> 明确**不做**通用作业调度器——理由见 §2，那是本平台一条已经写下来的决策。
>
> 状态：**待评审**（决策点 D1~D9 需要拍板）。实施记录将追加到本文末尾。

---

## 1. 背景：两行既有记录，与一个被打破的前提

能力全景表里有两行相邻但结论不同的记录：

| 行 | 原文（摘要） |
|---|---|
| `2026-09-30-ops-platform-capability-map.md:130` | `定时计划任务 \| 部分实现 \| 仅拨测有调度器：internal/server/dialtest/scheduler.go；无通用作业调度 \| **P2** \| M` |
| 同上 `:228` | `巡检周期化调度（自动生成报告） \| **已实现** \| 2026-10-06 落地：internal/server/report/schedule.go 的 Scheduler（配置+运行状态同文件、可关/可改周期/可立即执行/上次结果可见/失败显式上报）` |

而 `internal/server/report/schedule.go:19-25` 把那件事的边界写得非常清楚：

```
// 为什么不做"通用定时任务"：平台里需要定时的只有报告与数据保留（后者已有自己的循环）。
// 为两条用例引入一套 cron 表达式解析 + 任务注册表，收益只是"看起来更通用"，
// 代价是多一套需要维护与排障的调度器。这里只做报告这一件事，但把该有的做全：
// 可关、可改周期、可立即执行、上次结果可见、失败显式上报。
```

**本次要处理的问题**：那条决策的理由是"**只有两条用例**"。而**配置巡检**是第三条——
并且它和报告的性质不同：报告是"按时把数据取出来"，巡检本身就是**周期性体检**
（配置漂移、与标杆的偏差）。今天它**只能人工点**：

- 触发入口只有 `POST /api/v1/inspect/runs`（`internal/server/api/inspect_api.go:102`，权限 `inspect:run`）；
- 全仓没有任何定时器/后台 goroutine 调 `asset.Service.RunInspect`（`internal/server/asset/service.go:771`）；
- 后果：**无法回答"上周的配置是什么样、从什么时候开始偏的"**——差异项只有"人点过的那几次"可比。

所以本设计件要回答的是：**在不大动干戈的前提下，让巡检可定时**，同时把"通用调度器"该不该做这件事留一个明确结论。

---

## 2. 为什么仍然不做通用调度器

把"通用"这条路摊开看，它要解决的三个问题在本平台都还没有答案（`docs/refactor-plan.md:283/332`：A1 Server 高可用明确暂缓）：

1. **身份与权限**：所有执行入口都挂在 `a.permit(handler, "xxx:exec")` 上、靠 `*http.Request` 取 `Principal`；
   `audit.Record` 的调用点**100% 在 `internal/server/api/`**，后台 goroutine **从不写审计**，
   `audit.Event.User` 的语义就是登录用户名。做"定时下发"等于要先定义一套**系统身份**，
   否则定时器就成了绕开 `ops:exec` 高风险护栏的旁路（`ops:exec` 在 `auth/policy.go` 的 HighRiskPermissions 里）。
2. **错过窗口语义已经有三套**：拨测 `nextRun = now + interval`（停机期间的窗口静默丢弃，`dialtest/scheduler.go:113-115`）、
   报告按 `lastRunAt` 判断（**有补跑**，`report/schedule.go:227`）、保留启动即清一次（`retention.go:376`）。
   通用调度必须先统一它们，否则"报告怎么多了一份/漏了一份"无法解释。
3. **并发与重复执行**：没有分布式锁，现有调度器全靠"单进程单 goroutine + `sync.Mutex` 保护状态"；
   多副本下会重复执行。

**结论**：本次**沿用报告的范式**（那是平台里唯一被评审过、且"该有的都做全"的调度），
只把第三条用例补上。**不做**：cron 表达式、任务注册表、任意动作的定时下发、报告的外发投递。
将来真要做通用调度，本文 §2 的三条就是它的前置清单。

---

## 3. 决策点

| # | 问题 | 建议 | 理由 |
|---|---|---|---|
| **D1** | 范围：做通用调度器，还是只做巡检周期化？ | **只做巡检周期化** | §2；通用调度的三个前置（系统身份/窗口语义统一/多实例锁）都还没答案，而巡检这一件可以独立做成 |
| **D2** | 定时跑的时候**以谁的范围**巡检？ | 配置里存**范围快照**（节点清单 + 业务标签选择器 + 类型/关键词/字段），保存时校验不超保存者范围；**每次运行再与保存者当前范围取交集**；保存者已不存在 → 按空范围跑并写 `lastError`（fail-closed + 显式） | 巡检范围今天来自 `Principal`（`inspect_api.go:115-126` 的两个维度），定时器没有请求上下文；而"范围不得超过操作者"是本平台既有语义。取交集让**降权立即生效**（否则受限管理员可以先放宽范围建一个定时任务再被收窄，巡检会继续看到范围外资产）；账号没了就**明说**而不是静默放大 |
| **D3** | 错过窗口怎么办？ | 照报告：每分钟 tick + `lastRunAt` 跨重启保留 + **启动不立即跑**；被跳过的窗口在状态里如实显示（上次运行时间 + 下次预计） | 平台里"分钟 tick + lastRunAt"是已验证范式（`report/schedule.go:204-231`）；拨测那种"启动即跑一轮"用在重操作上会在频繁重启时反复触发 |
| **D4** | 上一次还没跑完就到点了 | **跳过本轮并记录**（不排队、不并发）；状态里显示"上一次尚未完成" | 巡检会遍历资产并写快照，是重操作；排队会让"卡住一次的积压"变成连续跑；周期本身就会带来下一次 |
| **D5** | 权限点 | 读 `inspect:read`；改配置与立即执行 `inspect:run`（**不新增权限点**） | 与报告页同构（`query.go:448-450`：读 `report:read`、改与跑 `report:export`）；"定时"只是"替我触发"，能力等价于 `inspect:run` |
| **D6** | 周期跑会**推进快照**，这改变了 L2 差异的含义 | **保持推进**（与手动一致），并在界面上明示"上次/下次运行"；配置变更留 `lastChangedBy/At` | 不推进就等于每次都跟同一份旧快照比 → 每次报同一批差异，那是噪音。推进后 L2 差异的含义是"距上次巡检的变化"，正是周期巡检想要的语义。（`RunInspect` 只在字段**真变化**时写快照，所以没变化时不产生新快照、成本低） |
| **D7** | 定时执行要不要写审计？ | **不写**（跟进既有惯例：`report_schedule_api.go` 与保留策略都没有审计）；留痕靠 `inspect_runs.actor = "schedule"` + 配置里的 `lastChangedBy/At` | 后台 goroutine 写审计在本平台没有先例；引入一条"系统写审计"的惯例是另一件事（与 §2 的第 1 条同源） |
| **D8** | 存储 | 配置 + 运行状态同文件 `inspect_schedule.yaml`（与 `report_schedule.yaml` 同构）；**巡检历史仍进既有表**，不新增表 | 报告的"配置与状态同文件"已证明够用；`inspect_runs`/`inspect_findings` 本来就有历史与保留（`retention.go:354` 会裁） |
| **D9** | 间隔范围与下限 | 1 小时 ~ 30 天（默认 24 小时），沿用报告的归一化口径（越界**校正**而不是报错） | `report/schedule.go:107-119`：配置是人填的，一个越界值不该让整个调度失效——那会把"巡检怎么不跑了"变成一个谜 |

---

## 4. 设计

### 4.1 配置形态

```yaml
# inspect_schedule.yaml
enabled: false          # 默认关闭（与报告调度一致：默认开启的重操作会让人意外）
intervalHours: 24       # 1..720
# 巡检筛选（与 POST /api/v1/inspect/runs 的请求体同构）
type: ""
node: ""
keyword: ""
fields: []
# 范围快照（保存时由保存者的 Principal 折算，见 D2）
scopeNodes: []           # 节点清单；空 = 不限
scopeLabels:             # 业务标签选择器
  - key: biz
    value: pay
# 运行状态（调度器维护，界面只读）
lastRunAt: 0
lastRunId: 0
lastError: ""
lastChangedBy: ""
lastChangedAt: 0
```

- 字段名与语义对齐报告调度（`Enabled`/`IntervalHours`/`LastRunAt`/`LastError` + `Status{Config,Last,NextAt}`）。
- `Normalize()` 校正越界值；`Save()` **保留运行状态字段**（那是调度器所有的，不能让前端覆盖）。

### 4.2 调度语义（照报告范式）

- `Run(ctx)`：`time.NewTicker(time.Minute)`；**启动不立即跑**，是否到点由 `lastRunAt` 判断。
- `tick()`：未启用 → 返回；`lastRunAt > 0 && now-lastRunAt < interval` → 返回；否则 `RunNow(false)`。
- `RunNow(manual bool)`：**手动与自动共用同一入口**——否则会出现"我刚手动跑过，调度却说到点该跑了"
  （报告的同一取舍，见 `report/schedule.go:24-25`）。失败**不 panic、不静默**：结果写进状态并记日志。
- 并发：`running` 标记（`sync.Mutex` 保护）；到点但上一次仍在跑 → 跳过本轮并记 `lastError="上一次巡检尚未完成"`。
- 触发者留痕：`RunInspect(sc, "schedule")` —— `actor` 字段落在 `inspect_runs.actor`，界面显示为「定时」。
  手动触发仍写登录用户名（`assetActor(r)`）。

### 4.3 范围折算（D2 的落地）

保存配置时（`PUT`）：

1. 由请求的 `Principal` 折算 `scopeNodes` / `scopeLabels`（复用 `a.assetAllowedNodes(p)` / `a.assetScopeSelectors(p)`
   的同一口径，`asset_api.go:843/995`）并写进配置；
2. **校验**：提交的范围不得超过保存者自身范围（取向与 `auth.ScopeCovers` 一致，`auth/model.go:190`）。

每次运行前：

3. 用 `auth.Store.GetPrincipal(lastChangedBy)` 取回保存者的**当前**范围（`auth/store.go:172`）；
4. 与配置快照**取交集**；取不到（账号已删/被禁）→ 按空范围跑并在 `lastError` 里说明
   （"范围来源账号已不存在，请重新保存配置"）；
5. 交集为空 → 正常跑出"0 个资产"的巡检记录，但**要能解释**：界面在记录上标注"范围已收窄为空"。

> 关键点：**不让"某个人的历史权限"成为一份永久授权**，同时避免"我什么都没改、范围却变了"这种无法解释的现象
> ——交集只会朝**收窄**方向变，且变化点由账号状态解释。

### 4.4 接口与前端

| 层 | 内容 |
|---|---|
| 接口 | `GET /api/v1/inspect/schedule`（`inspect:read`）、`PUT /api/v1/inspect/schedule`（`inspect:run`）、`POST /api/v1/inspect/schedule/run`（`inspect:run`，立即执行一次） |
| 响应 | `{config:{...}, last:{at,runId,error,manual}, nextAt}` —— 与报告调度同构 |
| 前端 | 巡检页 `web/src/components/asset/InspectView.vue` 加一张「周期化巡检」卡片（照 `ReportView.vue:29-68` 的「周期化生成」卡片：开关 + 间隔小时 + 筛选条件 + 立即执行 + 上次/下次展示 + 上次错误） |
| 记录列表 | `inspectRunView.Actor` 为 `schedule` 时显示「定时」标签；列表顶部显示该配置的启用状态与下次运行时间 |

---

## 5. 实施批次（建议拆成三笔提交）

1. **调度器与配置**：`internal/server/inspectschedule/`（或挂在 `asset` 包下的 `inspect_schedule.go`）——
   配置读写 + `Normalize` + `Save/Config/Status` + `RunNow/Run/tick` + 并发保护；`cmd/server/main.go` 接线
   （照报告的位置：`:295` 构造 `report.NewScheduler`、`:428-430` 起 `Run`）。
2. **接口与范围折算**：三个端点 + 保存时范围快照与校验 + 运行前按保存者当前范围取交集。
3. **前端**：「周期化巡检」卡片 + 记录列表的「定时」标记 + 空态文案。

权限点沿用（D5），不新增；文档对账随第 3 笔。

---

## 6. 用例与验证计划

**单测**（照 `report/schedule_test.go` 的形态，`now` 可注入）：

- 未启用时不跑；到点才跑；`lastRunAt` 跨重启保留 → 重启后不重复跑（D3）；
- 改周期后热生效；越界间隔被**校正**而不是报错（D9）；
- 上一次未完成时跳过本轮并记错误（D4）；
- 手动 `RunNow` 会更新 `lastRunAt`（同一入口，报告那条取舍的等价断言）；
- 生成失败时状态里带 `lastError` 且不 panic（D7 的可见性）；
- **范围**：保存时越权范围被拒；降权后运行**按交集收窄**；保存者被删 → 空范围 + 明确错误（D2 的三条）；
- `actor`：定时跑写 `schedule`、手动跑写登录用户名。

**实机验证（dev-server）**：该机有真实资产可巡检（46 条关系 / 44 节点，见 §3.10 记录），
步骤：① 升 Server（本批无 Agent 改动，Agent 不用升）；② 配一个**短间隔**（1 小时下限对验证太久 →
用 `POST /api/v1/inspect/schedule/run` 验"立即执行"路径 + 把 `lastRunAt` 手工回退来验"到点触发"）；
③ 判据：跑出的记录 `actor=schedule`、界面上次/下次正确、把保存者降权后**范围真的收窄**、删除保存者后
出现明确的 `lastError`、以及"上一次未完成就跳过"（用一次大范围巡检占住）。

**未覆盖边界（不得视为通过）**：多 Server 实例下的行为（沿用单实例假设，A1 暂缓）；
cron/日历语义（本批不做）；间隔的真实长期稳定性（验证会临时把间隔调短，不代表 24 小时周期的现场手感）。
