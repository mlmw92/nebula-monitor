# 合规矩阵导出（安全基线 × 资产，CSV）设计件

- 批次：24（配置项模型页之后的下一批）
- 日期：2026-10-07
- 对应全景表：`2026-09-30-ops-platform-capability-map.md` §3.11「合规报告/合规矩阵导出」（部分实现 / P2 / S）
- 状态：**设计待评审（D1~D9）**

## 1. 起因

全景表 §3.11 那一行写得很直白：`HTML 报告可下载；无合规矩阵 CSV/PDF 导出`。

具体缺的是什么，看两份现状：

1. **报告里的安全章节只有"评分概览"，没有逐项明细**。`internal/server/report/report.go:226-238` 的
   `SecuritySection` 只有 `HasData / NodeCount / AvgScore / LowScoreNodes / CriticalEvents /
   WarningEvents / CVECount / RiskNodes / LowScores / Events`；模板 `internal/server/report/template.html:311`
   渲染的也是"基线评分均值 / 低分节点数 / 安全事件 Top N"。**没有任何一处把"哪台机器、哪一项、过没过"列出来。**
2. **界面上有明细，但拿不走**。安全中心页（`web/src/components/SecurityView.vue:213-235`「基线合规明细」）
   已经逐台逐项显示，接口 `GET /api/v1/security/baselines` 也返回完整明细——
   但**没有导出**，做合规台账的人只能手工抄。

而"资产 × 检查项"的矩阵正是合规场景里最常被要的东西（交给审计、丢进 Excel 透视、按检查项看整体覆盖率）。
`docs/superpowers/specs/2026-09-30-asset-cmdb-design.md:26/77/108` 早就写过"安全基线评分可复用为合规比对"、
"L3 合规比对"——**这条线一直没接上**。

## 2. 现状（有依据）

### 2.1 数据源：安全基线

| 事实 | 依据 |
|---|---|
| 结构：**一行一节点**，检查项内嵌在 `Items[]` | `internal/model/security.go:54-73`：`SecurityBaseline{Node, NodeIP, DisplayName, Score, Items []SecurityBaselineItem, CheckedAt}`；`SecurityBaselineItem{Key, Name, Pass, Severity, Score, Detail}` |
| 存储是 **JSON 文件 + 内存索引**（不是 SQL） | `internal/server/security/store.go:26-32`（`baseline map[string]SecurityBaseline`，落盘 `security_store.json`）；`Baselines()` 在 `:169`（按评分升序） |
| 评分口径：**每项等权**（`weight=1.0`，通过得 1、否则 0），`score = got/total*100` | `internal/agent/collector/security.go:509-524`、`mkBaselineItem` 在 `:693-712` |
| 现有接口：`GET /api/v1/security/baselines`，权限 `security:read` | 路由 `internal/server/api/query.go:323`；handler `internal/server/api/security_api.go:191` |
| **范围只在 API 层内存过滤**（store 无 `nodes` 参数） | `security_api.go:201`：`filterByNodeScope(a, Principal(r), baselines, func(b) string { return b.Node })`；函数在 `query.go:596` |
| 接口还会补 `NodeIP` / `DisplayName` | `security_api.go:202-207`（`a.nodeIP` / `a.nodeDisplayName`） |

**线上实况（2026-10-07 只读探针，dev-server 1.30.45）**：4 个节点 × 5 个检查项 = **20 格**；
评分 60 / 60 / 80 / 80；检查项并集是 `fail2ban_running`、`firewall_enabled`、`no_empty_password`、
`ssh_password_auth`、`ssh_root_login`；`nodeIP` 在这台机器上是**空**的（矩阵必须容忍空 IP）。

### 2.2 导出惯例（**有先例，不必从零新建**）

| 先例 | 位置 | 值得照抄的 | 不该照抄的 |
|---|---|---|---|
| **资产清单 CSV**（首选） | `internal/server/api/asset_batch_api.go:285` | 独立权限点 `assets:export`；与列表**同一筛选 + 同一范围下推**；绕过分页；超限**明确 400**（`:300-305`，上限常量 `maxExportAssets=20000` 在 `:32`）；写 BOM（`:320`）；文件名带时间戳（`:316`） | — |
| 指标 CSV | `internal/server/api/export.go:91` `writeMetricsCSV` | 同上（BOM、时间戳名） | — |
| 审计 CSV | `internal/server/api/security_api.go:120-149` `writeAuditCSV` | 独立权限点 `audit:export` | **无 BOM**（Excel 中文会乱码） |
| 前端下载 | `web/src/api/asset.js:220-248` `exportAssets` | 读响应 `Content-Disposition` 的文件名再 Blob 下载 | — |

权限点的登记惯例在 `internal/server/auth/model.go:362-363`：

```go
// 因此单设权限点（与 audit:export / metrics:export 同一约定）。
{"assets:export", "导出资产清单"},
```

**并且 `assets:export` 被加进了内置角色**（`model.go:419` 的管理员权限列表、`:453` 的审计员）——
新增权限点若不加进内置角色，管理员会"看不到按钮"，这是很容易漏的一步。

### 2.3 三处既有 CSV 导出**都没有公式注入防护**

全仓搜索 `encoding/csv` / `text/csv`：三处服务端导出都只用 `csv.NewWriter`（它只转义逗号与引号），
**没有处理"单元格以 `=` `+` `-` `@` 开头"**。本批导出的是"节点名 / 检查项名"——
**节点别名是管理员可改的**（`PUT /api/v1/nodes/{name}/display-name`，请求体 `{"displayName":...}`，
权限 `nodes:write`，见 `internal/server/api/query.go:172` 与 handler `:887`），
而矩阵的"显示名"列正是取它（`security_api.go:206`），所以这是**真实可触发**的路径，不是理论风险。

## 3. 范围

**做**：安全基线合规矩阵的 **CSV 导出**（后端接口 + 权限点 + 前端按钮），以及**顺带加固既有三处 CSV 导出**的注入防护。

**不做**（明确划线，避免范围膨胀）：

- **PDF 导出**：要引入排版引擎（新依赖 + 中文字体），与 P2/S 的规模不符；CSV 在 Excel 里可透视、可打印。
- **配置巡检差异矩阵**：数据源不同（`asset.InspectFindings`，`internal/server/asset/service.go:929`，
  只含**有差异**的单元格），语义是"配置漂移"而不是"合规通过与否"。它的矩阵形态另议。
- **把安全基线接成配置巡检的期望值**：那是全景表 §3.2 行 231 的 **P0**（"尚未接入安全基线清单自动生成期望值"），
  是另一批的事——本批只**导出**现有基线数据，不改变它的来源与语义。
- **未通过原因（`Detail`）清单**：见 D4。

## 4. 设计

### 4.1 接口

```
GET /api/v1/security/baselines/export
权限：security:export（新增；只读查看仍是 security:read）
响应：text/csv; charset=utf-8 + BOM，Content-Disposition: attachment; filename=compliance-matrix-<ts>.csv
```

放在 `security` 域而不是 `report` 域，理由：数据来自安全基线、界面上要点的按钮在安全中心页、
与 `GET /api/v1/security/baselines` 是同一份数据的两种形态（明细 / 落盘）。

### 4.2 矩阵形状

- **行**：节点，集合与顺序**与 `GET /api/v1/security/baselines` 完全一致**（同一 `Baselines()` + 同一
  `filterByNodeScope` + 同一补 `NodeIP/DisplayName` 的步骤）。这条是硬要求：**看得见才导得出，导出不越权**。
- **列**：所有节点出现过的检查项**并集**，按 `key` 稳定排序；列头写成 `中文名(key)`
  （例如 `SSH root 登录已禁用(ssh_root_login)`）——这样"这一列是什么"不用再翻别处。
- **身份列**（矩阵左侧固定 5 列）：`节点`、`显示名`、`IP`、`评分`、`检查时间`。
- **单元格三态**：`通过` / `未通过` / **空**。
  **空 = 该节点没上报这一项**（例如 Agent 版本旧、或该项不适用），**绝不能写成"未通过"**——
  那会把"没采到"记成"不合规"，是这类矩阵最典型的错。
- **`Detail` 不进矩阵**（见 D4）。

### 4.3 上限与拒绝

矩阵是"行 × 列"的笛卡尔积，必须定上限并**显式拒绝**（本项目多处明确反对静默截断，
如 `asset_batch_api.go:30-32`、`InspectRun.Truncated`）：

- 节点数上限 `maxMatrixHosts = 2000`；
- 单元格上限 `maxMatrixCells = 200000`（2000 × 100 项）；
- 超限 → **400**，报文里给出实际规模与建议（"请先按分组缩小范围"）。

线上规模是 4×5=20，上限对当前体量没有约束力，但它保证"将来某天 5000 台机器时不会悄悄给半张表"。

### 4.4 CSV 写出（共享工具）

新增一个共享的 CSV 写出辅助（放在 api 包内，例如 `internal/server/api/csvexport.go`）：

- `csvCell(s string) string`：**公式注入消毒**——以 `=`、`+`、`-`、`@`、Tab、CR 开头时前置 `'`
  （Excel 会把它当文本，而不是公式）；
- `writeCSVDownload(w, filename, header, rows)`：统一设置 `Content-Type` / `Content-Disposition` / BOM，
  再逐行 `csv.Write`。

**既有三处（资产 / 指标 / 审计）同步改用它**：加固注入防护 + 审计导出顺手补上 BOM
（行为变化仅限"以 `=+-@` 开头的内容多一个 `'`"与"中文不再乱码"，都属于修 bug）。
这一笔要带用例，且既有导出用例必须仍全绿。

### 4.5 无数据时的行为

没有任何基线数据（`security` 未启用、或一台都没上报）→ **400 + 明确报文**
（"还没有安全基线数据可导出：请确认 Agent 已上报基线"），**不给一个只有表头的空文件**。
理由：空 CSV 看起来像"合规全部通过"，是危险的误导。

### 4.6 前端

安全中心页「基线合规明细」卡片（`web/src/components/SecurityView.vue:213-235`）加一个"导出 CSV"按钮：

- 下载方式照 `web/src/api/asset.js:220-248`（读 `Content-Disposition` 文件名 + Blob）；
- 无 `security:export` 权限时不显示：照资产页既有判定
  （`AssetListView.vue:1563` `const canExport = computed(() => auth.can('assets:export'))` +
  `v-if="canExport"`，判定函数是 `web/src/composables/useAuth.js:102 can(perm)`）；
- 导出失败（400/403）时把报文显示出来，而不是静默什么都不发生。

## 5. 决策点（D1~D9，均给建议与理由）

| # | 问题 | 建议 | 理由 |
|---|---|---|---|
| D1 | 范围 | **只做安全基线矩阵 CSV**；不做 PDF、不做配置巡检矩阵、不碰行 231 的期望值来源 | 与 P2/S 的规模相称；PDF 要新依赖；另两项是别的批次的语义 |
| D2 | 列口径 | 检查项**并集** + 按 key 稳定排序 + 列头 `中文名(key)` | 不同 Agent 版本上报的项可能不同，固定清单会漏；稳定排序让两次导出可 diff |
| D3 | 单元格 | `通过` / `未通过` / **空**（未上报） | **空 ≠ 未通过**：把"没采到"记成"不合规"是这类矩阵最典型的错 |
| D4 | `Detail`（未通过原因） | **不进矩阵**；如需原因清单，另做一份"未通过明细 CSV"（本批不做） | 一格一值是 CSV 能被透视的前提；原因塞进单元格会毁掉这个价值 |
| D5 | 上限 | `maxMatrixHosts=2000`、`maxMatrixCells=200000`，超限 **400 明确拒绝** | 反对静默截断（项目多处已明确） |
| D6 | 权限与留痕 | 新增 `security:export`（独立权限点）+ 加进内置角色；**不写审计** | 沿用 `assets:export` 的既有约定（`auth/model.go:362`）；导出是"看"的延伸，高风险动作（写/下发）才写审计 |
| D7 | CSV 注入 | 本批新增共享消毒函数，并**同时加固既有三处** | 三处现状都无防护，而节点显示名管理员可改，是真实可触发路径 |
| D8 | 无数据 | **400 + 明确报文**，不给空表 | 空表看起来像"全部合规"，是危险误导 |
| D9 | 前端位置 | 安全中心「基线合规明细」卡片加按钮；无权限隐藏（`useAuth.js:102 can()`）；失败显示报文 | 数据在这张卡上，"在哪里看就在哪里导" |

## 6. 批次拆分、用例与实机验证

### 6.1 三笔

1. **共享 CSV 工具 + 加固既有三处**：`csvCell` / `writeCSVDownload`，资产 / 指标 / 审计导出改用它。
   用例：注入消毒（`= + - @ \t \r` 开头各一条 + 普通值不被改动）、BOM、文件名、既有导出用例仍全绿。
2. **合规矩阵导出接口**：矩阵装配（并集 / 三态 / 排序 / 身份列）、上限与 400、无数据 400、
   权限点与内置角色。用例：装配正确性、**导出集合 == `/security/baselines` 集合**（含受限身份）、
   上限拒绝、无数据拒绝、权限 403。
3. **前端按钮 + 文档对账**：按钮、下载、失败提示；设计件补实施记录、全景表行 233 更新。用例：组件用例（点击发请求、成功触发下载、失败显示报文、无权限隐藏）。

### 6.2 实机验证计划（dev-server，1.30.45 起）

线上已有 4 节点 × 5 项 = 20 格的真实基线，可**真跑**：

1. **规模与形状**：导出的 CSV 行数 = 4+1（表头）、列数 = 5（身份）+ 5（检查项）；
   每格取值 ∈ {通过, 未通过, 空}；
2. **与接口对账**：矩阵里的评分/三态与 `GET /api/v1/security/baselines` **逐格一致**（同一集合、同一口径）；
3. **范围（决定性）**：建一个受限账号只含一台节点的分组 → 它导出的 CSV **只有 1 行数据**
   （与它看到的 `/security/baselines` 一致）；且它**没有** `security:export` 时 → 403；
4. **注入消毒（决定性）**：把某节点的**别名改成 `=cmd|'/C calc'!A0`**（`PUT /api/v1/nodes/{name}/display-name`，
   权限 `nodes:write`，上限 64 字符）→ 导出后该单元格**以 `'` 开头**；验完把别名改回原值；
5. **中文与 BOM**：文件头是 `EF BB BF`，中文列名/节点名不乱码；
6. **无数据路径**：本机有数据，**该路径只能靠用例覆盖**（不得声称实机已验）。

**未覆盖边界（不得视为通过）**：Excel / WPS 里的实际打开效果（列宽、透视）；真实的大规模
（数千节点）导出耗时与内存（上限逻辑有用例，但没有压测）；PDF（本批不做）。

## 7. 待评审

D1~D9 若都按建议，则按 §6.1 的三笔开工；有异议的挑出来。
