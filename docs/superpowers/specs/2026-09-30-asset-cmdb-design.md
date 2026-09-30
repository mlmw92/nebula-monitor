# 资产与配置模块详细设计

日期：2026-09-30
上位文档：`2026-09-30-ops-platform-evolution-design.md`（总体设计）、`2026-09-30-ops-platform-capability-map.md`（全景对照表 §3.2）
决策记录：`docs/adr/0001-cmdb-relational-store.md`

## 意图与边界

**意图**：把"已经在采集、但没有模型"的基础设施信息，升级为**可查询、可关联、可追溯变更、可比对合规**的资产视图，为告警关联、配置巡检、后续自动化执行提供权威数据源。

**边界**：

- 本模块只负责**资产与配置的模型、关系、变更、快照与差异**；不负责应用发布（P2/P3）、不负责工单审批（P2）、不负责配置下发执行（P2，见总体设计 §下行操作通道）。
- **不新增采集器**：资产数据全部来自既有采集产物（主机信息、中间件实例、容器清单），必要时仅扩展 Agent 的**清单上报字段**（见容器详设）。
- **不改动现有节点/分组语义**：资产是"其上的一层模型"，节点与分组仍由 `internal/server/node` 管理。

## 现状与依据

| 事实 | 依据 |
|---|---|
| 节点模型已存在，且**已有"人工值不覆盖采集值"的先例** | `internal/model/metric.go:597-614` `Node{Hostname, Mode, DisplayName, IP, OS, Arch, Group, Labels, Version, HostInfo, Status, LastSeen, CreatedAt, LogSources}`；字段注释：「DisplayName 自定义显示名/别名（**不修改 Agent 上报的真实主机名**）」 |
| 节点管理方法齐备 | `internal/server/node/manager.go`：`Register / ListNodes / ListHostNodes / GetNode / SetNodeGroup / SetNodeDisplayName / RemoveNode / ListGroups / AddGroup / RemoveGroup / persist` |
| 主机信息已在采集 | `internal/agent/collector/hostinfo.go`；`model.HostInfo{CPUModel, CPUCores, MemoryTotal, DiskTotal, DiskUsed, Disks[], BootTime, OnlineUsers[]}` |
| 中间件实例已注册聚合 | `internal/server/instancereg/`、`internal/server/mwreg/`；对外 `GET /api/v1/middleware/{type}/instances` |
| 容器/工作负载指标已采集（无清单） | `internal/agent/collector/k8s.go:collectPods/collectWorkloads`、`docker.go` |
| 安全基线评分已存在（可复用为合规比对） | `internal/agent/collector/security.go`（`mkBaselineItem` 等）、`internal/server/security/store.go:Ingest/Baselines/Summary` |
| FIM 是**文件哈希**基线，不是配置项级比对 | `internal/agent/collector/security.go:loadFIMBaseline/saveFIMBaseline/sha256File` |
| **本模块当前无任何实现** | 全仓 `asset / cmdb / inventory / AssetType / ChangeLog` 无命中；前端无 `asset/` 目录 |
| 权限与审计机制可直接复用 | `internal/server/api/auth.go`（`permit/authz/permitNode/checkNodeScope/denyScope`）、`internal/server/auth/model.go:PermissionCatalog()`、`internal/server/audit/store.go` |

## 领域模型

### 3.1 术语先定（避免同义词漂移，写入 `CONTEXT.md`）

- **资产（Asset）**：一个被管理的对象实例（一台主机、一个中间件实例、一个容器、一个服务端点）。对外统一用此词。
- **资产类型（Asset Type）**：资产的定义（字段集合、来源、是否可人工维护）。_避免_：资产模型、对象模型。
- **配置项（CI）**：资产在某一时刻的**规范化属性集合**——它是"资产 + 属性"的视图，**不是独立实体**。_避免_把 CI 与资产当两个东西。
- **资产关联（Asset Link）**：两个资产之间的有向关系（如"运行于""成员属于""依赖"）。
- **配置快照（Snapshot）**：某资产在某一时刻被抽取的**关注字段集合**（用于差异比对）。
- **差异巡检（Inspection / Diff）**：快照与前一快照、或与合规期望值比对，产出差异项与结论。
- **发现来源（Discovery Source）**：属性值的来源，`discovery`（采集）或 `manual`（人工）。
- **未知 vs 异常**：沿用既有对外状态页语义（`CONTEXT.md`），资产巡检的「未知」表示数据不足，不表示不合规。

### 3.2 实体与关系

```
AssetType 1───n Asset 1───n AssetAttr (value, source, updatedAt, updatedBy)
                    │
                    ├──n AssetLink (toAssetID, kind, direction)
                    ├──n ChangeRecord (field-level diff, actor, source, at)
                    └──n Snapshot (takenAt) 1───n SnapshotField
InspectRun 1───n InspectFinding (assetID, level, expected, actual)
```

**关键设计决策**：

1. **属性值带来源**：`AssetAttr{Key, Value, Source(discovery|manual), UpdatedAt, UpdatedBy}`。
   - 人工值**不覆盖**采集值：两者并存，展示时人工值优先，但差异在详情页可见；
   - 这正是既有 `DisplayName` 的语义（`model.Node` 注释）在资产层的推广；
   - 采集值变化不产生"人工修改"告警，人工修改必进变更历史。
2. **自然键与幂等**：`Asset` 的唯一键为 `(typeKey, naturalKey)`：
   - 主机：`hostname`（与 `model.Node.Hostname` 一致，避免第二套标识）；
   - 中间件实例：`<类型>:<addr>`（与实例列表一致）；
   - 容器/工作负载：`<集群>/<命名空间>/<kind>/<name>`；
   - 发现流程按自然键 upsert，**重复上报不会产生重复资产**。
3. **关系类型保持最小集合**（宁可少而准）：`runs_on`（服务/容器运行于主机）、`member_of`（实例属于集群）、`depends_on`（显式声明）、`exposes`（服务暴露于端点）。_避免_一开始就做通用图数据库。
4. **范围单一映射**：`Asset → 所属 Node → Group`。资源范围校验仍走既有节点分组（`auth/policy.go:FilterGroups`），**不做第二套范围判定**（见总体设计 §3.1）。

### 3.3 与现有模型的关系（不破坏现网）

| 现有 | 与资产的关系 | 处置 |
|---|---|---|
| `model.Node`（节点） | 「主机」资产的来源与标识基础 | 节点继续由 `node.Manager` 管理；资产侧**只读引用** `hostname`，不再造第二套主机标识 |
| `model.Group`（节点分组） | 资产的归属维度（映射用） | 不变；资产通过节点继承分组 |
| 中间件实例注册表 | 「中间件实例」资产的来源 | 按 `<类型>:<addr>` upsert，关联到宿主机（`runs_on`） |
| 端口/拨测结果 | 「服务端点」资产（可选，P1） | 首期可不做，避免范围膨胀 |
| 安全基线评分 | 合规期望值来源之一 | 巡检复用其"逐项结果 + 评分"的表达方式 |

## 自动发现（复用既有采集）

**流程**（事件驱动 + 周期兜底，全部幂等）：

1. **主机**：`node.Manager.Register`（`internal/server/node/manager.go`）处理上报后，资产侧消费同一份 `payload`（`HostInfo/OS/Arch/IP/Labels`）→ upsert 主机资产 + 属性（CPU 型号/核数/内存/磁盘/OS/架构/启动时间）。
2. **中间件实例**：实例注册表刷新后，按实例 upsert 资产（类型、地址、拓扑、角色），并建 `runs_on` 关系（实例 → 宿主机）。
3. **容器/工作负载**：依赖容器详设中新增的**清单上报**（当前只有指标）→ upsert 容器/工作负载资产 + `runs_on`（Pod → Node 资产）与 `member_of`（Pod → 工作负载）。
4. **不做的发现源**：不做网络扫描（SNMP/端口全网扫描）——与"内网离线 + Agent 授权"模型冲突，且会引入不可控流量。

**增量与性能约束**：

- 上报路径上仅做**批量 upsert**（按自然键），单批上限与超时受现有上报体量约束（`receiver` 侧已有体量上限）；
- 变更记录只在**值真的变化**时写入（比较规范化后的值），避免每轮上报都写一条；
- 列表查询走索引（`type_key + natural_key`、`node_hostname`、`updated_at`），关系查询走邻接表（`from_id`/`to_id` 索引）。

## 配置快照与差异巡检

**三层比对**（明确区分，避免与既有能力混淆）：

| 层 | 内容 | 与既有能力的关系 |
|---|---|---|
| L1 文件完整性 | 关键文件 SHA256 与基线比对 | **已是现状**：`security.go:loadFIMBaseline/sha256File`，不重复实现 |
| L2 配置项差异 | 关注字段集合的前后快照 diff（如 sshd 关键项、内核参数、服务列表、中间件关键参数） | **本模块新增** |
| L3 合规比对 | 快照字段 vs 合规期望值（期望值可来自安全基线清单或人工录入） | **本模块新增**，复用安全基线的"逐项结果 + 0-100 评分"表达 |

**快照采集方式**：由 Agent 在既有采集周期内**顺带产出**（不新增独立采集进程），字段集合按资产类型声明；快照按「资产 + 时间」存储，保留策略沿用本地数据保留机制（`internal/server/retention/`）。

**差异分级与结论**：`新增 / 变更 / 缺失 / 合规偏差`，每条 finding 带 `expected / actual / level`、证据字段与建议（对齐既有智能分析的"只读决策辅助"口径：**巡检只给结论，不自动修复**；修复动作属 P2 配置下发）。

**巡检结果的可视化**：列表 + 差异详情；接入既有巡检报告（`internal/server/report/report.go`）作为报告章节。

## 接口草案与权限点

```
# 资产（读写分离，读用 authz、写用 permit + 范围校验）
GET    /api/v1/asset-types                      # 类型与字段 schema
GET    /api/v1/assets?type=&group=&q=&page=     # 列表（服务端按节点分组裁剪）
GET    /api/v1/assets/{id}                      # 详情（属性 + 来源 + 关系 + 最近变更）
POST   /api/v1/assets                           # 新建（手工资产）
PUT    /api/v1/assets/{id}                      # 更新（写 manual 值，落变更记录）
DELETE /api/v1/assets/{id}
POST   /api/v1/assets/{id}/links                # 建关系
DELETE /api/v1/assets/{id}/links/{linkId}
GET    /api/v1/assets/{id}/history              # 变更历史（字段级 diff）
GET    /api/v1/assets/{id}/snapshots            # 快照列表/详情
POST   /api/v1/inspect/runs                     # 触发差异巡检（按范围）
GET    /api/v1/inspect/runs                     # 巡检记录
GET    /api/v1/inspect/runs/{id}/findings       # 差异项
```

**权限点（对齐既有前缀域命名）**：`assets:read`、`assets:write`、`inspect:read`、`inspect:run`。
**高风险**：`assets:write`（可改动资产与关系）纳入 `auth/policy.go:HighRiskPermissions` 语义范围。
**审计**：写操作经 `api/audit.go:AuditMiddleware`（管理写请求）+ `RecordChangeAudit`；资产字段变更另记 `ChangeRecord`（业务级历史，与审计互补：审计记"谁调了什么接口"，变更记"哪个字段从什么变成什么"）。

## 前端与菜单

- 新目录 `web/src/components/asset/`：`AssetListView.vue`（筛选/分页，复用既有表格与 KPI 卡）、`AssetDetailDrawer.vue`（属性/来源/关系/时间线）、`AssetTopologyView.vue`（关系视图）、`InspectView.vue`（巡检记录与差异）。
- 复用既有交互约定：抽屉式详情、`RefreshBar` 刷新条、`useAuth().can('assets:write')` 控制按钮可见性——**前端可见性只做体验，服务端仍是安全边界**（对齐 `docs/superpowers/specs/2026-09-27-*` 的既有结论）。

## 存储设计与迁移（详见 ADR-0001）

- 关系型持久化：内嵌 SQLite（`modernc.org/sqlite`，纯 Go 无 CGO，不破坏交叉编译与离线包）。
- 表（初版）：`asset_types`、`assets`、`asset_attrs`、`asset_links`、`asset_changes`、`snapshots`、`snapshot_fields`、`inspect_runs`、`inspect_findings`。
- **迁移路径**：首次启动按 `node.Manager.ListNodes()` 与实例注册表**导入一次**（来源标记 `discovery`），幂等（按自然键）。导入失败不影响主流程（可重跑）。
- **备份**：纳入既有升级流程的文件备份清单（`internal/server/upgrade` 的备份替换路径），并在 `server.yaml` 暴露数据库路径配置项。

## 文档与验证

- 术语：`资产 / 资产类型 / 配置项 / 资产关联 / 配置快照 / 差异巡检 / 发现来源` 写入 `CONTEXT.md`（含 `_避免_` 同义词）。
- 模块导航：`internal/server/asset`、`internal/server/inspect` 入「二、核心模块导航」，标注「逻辑复杂」。
- 实现阶段的测试面（本设计不写代码）：以 `asset.Service` 接口为测试接缝（fake store），覆盖：自然键幂等 upsert、人工值与采集值并存、关系双向一致性、变更记录只在真变化时写入、范围裁剪（无权限分组不可见）；差异巡检覆盖 L2 三种差异与"数据不足→未知"分支。
- 验证命令（沿用仓库约定）：`go test ./...`、`go vet ./...`、`npm --prefix web test`、`npm --prefix web run build`。

## 风险与取舍

| 风险 | 取舍 |
|---|---|
| 资产数据漂移（采集值与人工值冲突） | **不做自动覆盖**；两者并存、差异可见、人工修改留痕（对齐 `DisplayName` 既有语义） |
| 关系模型做太重（万能图） | 只保留 4 种关系；关系视图按需懒加载；不做图数据库 |
| 与节点/分组双轨造成权限混乱 | 单一映射（资产→节点→分组），范围校验复用既有包装器 |
| 首次导入产生大量变更记录 | 首次导入标记为 `initial`，不产生逐条变更噪声 |
| 巡检误报（字段波动） | 关注字段集合显式声明 + 规范化后比较（去空白/排序）+ 允许容忍列表 |
| 内嵌数据库与既有 JSON 双轨 | 只承接关系型数据；其余保持 JSON，不做无收益搬迁（ADR-0001 记录） |
| 自动发现被误解为"网络扫描" | 明确只消费 Agent 授权范围内的既有采集产物，不主动扫描网段 |
