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

## 实施记录（按批次追加）

### 批次 1（2026-09-30）：资产地基与自动发现接入

**已落地**（`internal/server/asset/`、`internal/server/receiver/assets.go`、`internal/server/api/asset_api.go`）：

| 设计项 | 实现状态 | 证据 |
|---|---|---|
| 关系型持久化（内嵌 SQLite） | ✅ | `asset.Open`（WAL + busy_timeout + 外键 + 单写连接），`PRAGMA user_version` 版本守卫，幂等建表 + 内置类型播种 |
| 资产类型 / 资产 / 属性（采集值与人工值并存） | ✅ | `asset_attrs` 联合主键含 `source`；`Asset.Value` 取生效值（人工优先）；测试 `TestApplyKeepsDiscoveryAndManualValuesApart` |
| 按（类型 + 自然键）幂等 upsert | ✅ | 唯一约束 `(type_key, natural_key)`；`TestApplyIsIdempotentAndRecordsInitialOnce` |
| 变更历史（字段级 diff，仅真变化才记录） | ✅ | `asset_changes`；`TestApplyWritesChangeOnlyOnRealValueChange` |
| 资产关联（四类，幂等） | ✅ | `asset_links`；`TestLinkIsIdempotentAndResolvesMissingAssets` |
| 配置快照（关注字段集合） | ✅（写入/读取，尚无巡检消费方） | `snapshots` + `snapshot_fields`（同一事务写入）；`TestSnapshotCapturesEffectiveValues` |
| 自动发现：主机 | ✅ | `receiver.applyAssets` → 自然键 = hostname（与 `model.Node.Hostname` 一致），属性含 os/arch/ip/group/agentVersion/cpuModel/cpuCores/memoryMB/diskMB |
| 自动发现：中间件实例（15 类） | ✅ | `instanceObservations`：显式逐类型转换（不引入反射），自然键 `<类型>:<地址>`，并建立 `runs_on` 指向主机 |
| 只读接口 | ✅ | `GET /api/v1/assets`、`/api/v1/assets/{id}`、`/api/v1/assets/{id}/history`；API 侧以 `AssetProvider` 接口依赖（不引具体实现类型） |
| 权限点与资源范围 | ✅ | 新增权限点 `assets:read`（目录「资产」域，授予运维管理员与只读角色）；范围按资产所属节点裁剪，范围外资产按 **404** 返回（不区分 403，避免状态码探测）；先过滤再计数，`total` 不泄露范围外规模 |
| 变更历史与审计的分工 | ✅（变更侧） | 字段级 diff 落在 `asset_changes`；接口级审计仍走既有 `AuditMiddleware`（写接口尚未开放） |
| 跨架构交叉编译 | ✅ | `CGO_ENABLED=0` 下 linux/amd64、arm64、arm 均编译通过（ADR-0001 已补记） |

**与设计的差异（有意为之，后续批次处理）**：

1. **暂未设 `assets:write`**：当前写入全部由 Agent 上报驱动（`source=discovery`），没有人工维护入口，所以只设读权限点。手工新建/编辑资产与 `assets:write` 随对应写接口一起加。
2. **未记录 `bootTime` 与磁盘明细**：每次重启都会产生一条"变更"，会把变更历史刷成噪声；分区表变化同理且体积大。等真的需要"重启事件/磁盘变更"这类诉求时，再以独立资产类型或专门事件承载。
3. **Elasticsearch 的 `Status`（green/yellow/red）映射到通用 `role` 属性**：它是各类型里唯一的"状态型"字段，先复用 role 位（避免为一类实例新增专用字段），后续若需要更精确的语义再引入 `status` 保留键。
4. **资产 `Name` 取采集值（主机名）**，人工展示名仍由既有的节点 `DisplayName` 承载；资产侧要覆盖展示名需走 `manual` 提交，属下一批次。
5. **`inspect`（差异巡检）尚未落地**：快照的写入/读取已具备，比对与巡检运行在下一批次。

**验证方式**：`go test ./internal/server/{asset,receiver,api,auth}/`（含 8 个资产领域用例、上报→台账的幂等与关联用例、范围裁剪与真实路由权限负例）；`go vet`、`gofmt -l` 干净；`go build ./...` 通过。

### 批次 2（2026-09-30）：人工维护写接口

**已落地**：

| 设计项 | 实现状态 | 证据 |
|---|---|---|
| 写权限点 | ✅ | `assets:write`（目录「资产」域），默认仅授予运维管理员；按设计**纳入 `auth/policy.go:HighRiskPermissions`**（二次确认 + 审计） |
| 手工新建资产 | ✅ | `POST /api/v1/assets`：以 `source=manual` 建档；对已存在的（类型 + 自然键）返回 **409**，避免"以为是新建、实际覆盖既有台账"；成功写回 `201` |
| 人工维护属性/名称 | ✅ | `PUT /api/v1/assets/{id}`：只写 `manual` 来源，**采集值原样保留**（两者并存、差异可见）；字段级 diff 与操作人进变更历史 |
| 写操作的资源范围 | ✅ | 更新：范围外资产按 **404**（复用 `assetInScope`）；新建：归属节点必须在范围内，范围外走既有 `denyScope`（403 + 拒绝审计）；**受限用户不允许创建无归属资产**（否则造出自己都看不见的脏数据） |
| 归属节点不可改 | ✅ | 更新接口显式拒绝变更 `node`（400）：节点是范围锚点，可改等于可把资产移出/移入他人可见范围 |
| 审计 | ✅ | 管理写请求由 `AuditMiddleware` 自动留痕；并把 `/assets` 并入 `enrichChangeDetail` 的路径集合，审计摘要带上 `change=update /api/v1/assets/{id}` |
| 操作人取值 | ✅ | `assetActor`：认证中间件写入的用户名优先 → Principal.Username → `anonymous`（与告警处置约定一致），避免变更历史出现空操作人 |

**与设计的差异**：

1. **未实现 `DELETE /api/v1/assets/{id}`**：删除语义（级联关系、变更历史留存、软删还是硬删）尚未定，贸然开放会让台账出现"消失的资产"且无法追溯。待明确后单独设计。
2. **未实现关联的增删接口**（`POST/DELETE /api/v1/assets/{id}/links`）：当前 `runs_on` 由采集自动维护，手工建关系需要先确定"人工关系 vs 采集关系"的优先级与冲突处理。

**验证方式**：`go test ./internal/server/api/ -run Asset`（12 个用例：新建/冲突/范围三态、更新保留采集值、归属节点拒绝、写权限负例等）；全量 `go test ./internal/...` 通过。

### 批次 3（2026-09-30）：前端资产页

**已落地**（`web/src/components/asset/AssetListView.vue`、`web/src/api/asset.js`、路由与菜单）：

| 设计项 | 实现状态 | 证据 |
|---|---|---|
| 资产台账页（列表） | ✅ | 类型 / 归属节点 / 关键词筛选 + 分页（「加载更多」，页大小 50）；筛选项为空时不拼进查询串（`?type=` 与不传语义不同） |
| 资产详情抽屉 | ✅ | 基本信息 + **属性双来源对比**（同一 key 并排显示人工值与采集值，生效值按「人工优先」与服务端 `Asset.Value` 同一规则计算）+ 变更历史（字段/旧值→新值/来源/操作人/时间） |
| 人工维护入口 | ✅ | 新建资产对话框与「维护人工值」对话框（属性 name/value 行编辑器）；编辑时**只预填人工值**，避免误以为提交会覆盖采集值 |
| 权限门控 | ✅ | 菜单与路由按 `assets:read` 隐藏/拦截（`Sidebar.vue` 新增「资产与配置」分组、`router/index.js` 的 `meta.perm`）；维护按钮按 `assets:write` 门控 |
| 高风险二次确认 | ✅ | 提交前 `ElMessageBox.confirm`（与 `assets:write` 纳入 `HighRiskPermissions` 的服务端语义对齐）；注释明确「前端隐藏仅为体验，服务端才是边界」 |
| 按需分包 | ✅ | 路由懒加载，构建产物 `AssetListView-*.js` 12.27 kB（gzip 4.52 kB）+ CSS 0.72 kB，不进入主包 |

**与设计的差异**：

1. **关系图（拓扑视图）未做**：数据侧目前只有 `runs_on` 一种自动关系（实例 → 主机），单独画一张关系图的收益不足以支撑成本；等有了人工关系与多跳依赖再做，届时可参考 `2026-09-30-ops-platform-open-source-research.md` 中 NetBox 的"对象 → 关联"呈现方式。
2. **未做「删除资产」按钮**：后端尚未提供删除接口（见批次 2 差异说明），前端不放置无效入口。

**验证方式**：`npm --prefix web test`（6 个测试文件全通过，含新增 `src/api/asset.test.js` 4 个用例：筛选参数拼装、ID 编码、limit 语义、更新不携带 node）；`npm --prefix web run build` 通过（14.67s，产物已分包）。

### 批次 4（2026-09-30）：分页与字号修复（实测反馈）

用户在 dev-server 上实测后反馈两点：**字号偏小**、**没有分页**。两者都不是表面问题：

| 反馈 | 根因 | 修法 |
|---|---|---|
| 字号偏小 | 该页把 `el-table` 等组件显式设成 `size="small"`（暗色主题下 14px），而 DialTestView / UpgradeView 等页面用 Element Plus 默认尺寸（16px） | 去掉该页的 `size="small"`，与既有页面看齐 |
| 没有分页 | ① 接口的 `total` 取的是**当前页长度**（`len(out)`），前端据此无法渲染分页器，只能做成「加载更多」；② 更隐蔽的是 `listAssets` 把 `LIMIT/OFFSET` 作用在 `assets LEFT JOIN asset_attrs` 的**结果行**上——一个资产有几条属性就有几行，于是「一页 50 条」实际是「一页 50 行」，属性多的资产挤占同页额度且 `OFFSET` 随之漂移（用例：12 属性的资产 + 3 个单属性资产，`limit=2` 的页装不满） | ① 服务端新增 `Count`，与 `List` **共用同一套 WHERE**（`assetWhere`）给出真实总数；② 分页改为「先按资产选出本页 ID，再取这些资产的属性」；③ 前端改用 `el-pagination` 做服务端分页（`total/sizes/prev/pager/next/jumper`，与其它页面同款） |

顺带修正一处同源缺陷：**资源范围从「取回一页再过滤」改为下推到 SQL**（`ListFilter.Nodes`）。
过滤发生在分页之后时，页内被剔除的空位不会补人（受限用户会看到忽多忽少的页），总数也只能数到当前页。
下推后：受限用户看到的 `total` 就是其范围内的真实条数，且与页内容自洽；`Nodes == nil` 表示不限制，
空切片（受限但无可见节点）显式翻译成 `AND 1=0`，**不**退化成「不过滤」（那是越权旁路）。
接口层仍保留一次 `nodeInScope` 兜底。

**验证方式**：`go test ./internal/server/asset/ ./internal/server/api/` 新增 5 个用例——按资产分页不被属性行挤占（含第二页不重复）、`Count` 与 `List` 条件一致且空节点集合恒空、`total` 与翻页自洽（含越界页为空）、受限用户每页装满且总数不含范围外资产、无可见节点返回空结果；前端 `npm test` 6 个测试文件与 `npm run build` 通过。

### 批次 5（2026-09-30）：对齐资产台账原型（`asset-prototype.html`）

用户提供了交互原型，本轮把台账页按原型重做，并补齐原型依赖的后端能力。原型里有 4 处语义原先没定义，本轮**明确并实现**（作为后续所有消费方的口径）：

| 待定义 | 采用口径 | 理由 |
|---|---|---|
| 状态 `online / missing / archived` | 有采集值且最近采集在阈值（`assetStaleThreshold = 30min`）内 → online；超阈值 → missing；**从无采集值**（纯人工建档）→ archived。阈值取 30 分钟（见批次 7），判定基准是 `asset_seen.last_seen_at`（见批次 9） | 归档与失联必须分开：台账里补录的资产从来不上报，算成"失联"会让运维每天追一批本就不该上报的对象。判定基准必须是「最后一次上报时刻」而非「最后一次值变化时刻」，否则值长期不变的资产会被整批误判 |
| 来源 `auto / manual / mixed` | 无人工值 → auto；有字段同时存在人工值与采集值 → mixed；其余有人工值 → manual | 与「冲突」共用同一判定，列表一个列就能说清"这台机器有没有人插手、插手得对不对" |
| 责任人 | 约定人工属性键 **`owner`**（`asset.OwnerKey`），不新增库列 | 目前只有它被列表/摘要/筛选直接消费；其余管理属性（业务系统/环境/维保到期…）保持自由键，等字典枚举能力上线再固化 |
| 关联关系 | 只用既有自动关系（`runs_on`），**范围外的对端不返回** | 一条边足以暴露范围外资产的名字；关系宁少而准，人工关系与上层依赖留待后续批次 |

**后端新增**：

| 能力 | 实现要点 |
|---|---|
| 派生字段 | `asset.Asset` 增加 `Owner / SourceMix / ConflictKeys / HasDiscovery / LastSeenAt`；"最近上报"取**采集值**的最大时间而不用 `Asset.UpdatedAt`（人工维护也会刷新后者，否则"最近上报"会显示成人工改动时间） |
| 筛选扩展 | `ListFilter` 新增 `Source / Status / StaleBefore / OwnerMissing / HasConflict`，关键词**同时匹配属性值**（拿 IP / 业务名 / 资产编号找资产是台账最常见的用法）；状态与来源全部用 SQL 子查询推导，不落库——存成一列就必须有人定期刷新，反而会写出"字段说 online、现实已失联" |
| 摘要 | `Service.Stats` + `GET /api/v1/assets/summary`，五个数字走**聚合查询**（不把资产拉进内存），与列表共用同一套 `assetWhere` 与同一个 `StaleBefore`——否则会出现"摘要说 12 个失联、列表只有 11 条" |
| 恢复采集值 | `Service.ResetManual` + `PUT` 的 `resetAttrs`：**删除**人工值而不是把采集值写回（写回会把当前采集值固化成人工值，此后采集再变反而显示成"人工覆盖"，用一个更隐蔽的错误替换原来那个）；删除与变更记录同事务 |
| 关联关系 | `GET /api/v1/assets/{id}/links`：返回出边与入边并带方向，**逐条校验对端是否在资源范围内**，范围外直接不返回 |

**前端（`AssetListView.vue` 重写）**：健康度条（总数/失联/无责任人/冲突/近 7 天变更，前四个可点击下钻，下钻态以可关闭标签显示）、筛选行（类型/状态/来源/归属节点/关键词/重置）、工具栏（新建资产 + 权限徽标）、表格列（名称+副标题、类型标签按产品名着色、归属节点、状态点、相对时间的最近上报、来源、责任人）、抽屉改为**三 Tab**（属性对比：技术属性/管理属性/归属节点分段，四列含生效值与来源 pill、冲突行高亮并给"恢复采集值"；变更历史：时间线；关联关系：方向+关系+对端）。

**有意未做**（下一批次）：批量维护人工值 / 批量转派责任人 / 批量删除（原型自身也把删除置灰：后端无删除接口）、导出清单 CSV、菜单里的「配置项模型」「关系视图」两个页面（原型标注为 D3）；台账**关系图**仍不做（当前只有一种自动关系，单独画图收益不足）。

**验证方式**：后端新增 8 个用例（来源/状态/责任人/冲突四类筛选含组合条件与关键词命中属性值、摘要与列表同源且范围下推、恢复采集值只删人工值并留变更记录、派生列三态与展示 ID、摘要与列表总数一致含冲突下钻与非法取值 400、关联关系对端范围过滤与入边方向）；前端 `npm test`（8 个用例含摘要/关联路径与 `resetAttrs` 提交）与 `npm run build` 通过。

### 批次 6（2026-09-30）：dev-server 实测反馈——样式还原度与表单错位

用户升级 1.30.0 后反馈两点：

1. **页面样式"没了"**：排查发现不是样式丢失，而是三类还原度问题——
   - 本页没按其它页面的惯例自带 `.view { padding }` 与 `h2` 字号（这些在仓库里是**每个视图各自 scoped 定义**的，不是全局类），导致标题与留白和其它页面不一致；
   - 全局 `.tag` 只提供形状，类型/状态/来源没有语义配色（`tag.host / tag.mw / tag.missing / tag.archived / tag.src-*` 在本页补齐），健康度分隔线又用了几乎不可见的 `--border`，看起来"没样式"；
   - 状态与来源列原本是裸文本+圆点，改为语义化 tag 后与原型一致。
2. **新建资产表单与列表对不上**：原表单让用户直接填"自然键"（还要求理解 `<类型>:<地址>` 的内部约定），且没有责任人字段——列表列是 名称/类型/归属节点/状态/来源/责任人，表单却对不上。重做为：
   - 资产类型用 主机 / 中间件实例 二选一；实例再选 **实例类型**（15 类产品名下拉）+ **实例地址**，自然键自动拼出并只读预览；
   - 表单项与列表列一一对应：资产名称 / 类型 / 归属节点 / **责任人** / 其它属性（折叠为高级项）；
   - 主机的归属节点留空默认为主机自身（与服务端自动发现"主机 Node 恒等于 hostname"一致）；
   - 维护表单同样加责任人：**清空并提交 = 恢复未指派**（走 `resetAttrs` 删除人工值，而不是写入空值）；预填"其它属性"时排除 owner，避免同一个字段出现两个编辑入口。

**验证方式**：`npm test` 6 个测试文件与 `npm run build` 通过；样式改动以截图对照验收（健康度分隔/配色、语义化标签、表单字段与列表列对应）。

### 批次 7（2026-09-30）：继续对齐原型视觉细节（1.30.2）

用户升级 1.30.1 后再次反馈「页面样式还是不对」，拿截图与原型的 `asset-prototype.html` 逐列对比，发现三处结构差异：

| 原型 | 1.30.1 | 修法 |
|---|---|---|
| 表格「状态」列 = 小圆点 + 文字（`dot` + `st-on/st-mis`） | 我们做成 tag pill | 列表状态改回「圆点 + 文字」；抽屉头部仍保留 tag pill（原型抽屉也这样） |
| 表格「来源」列 = 纯文字着色（`src-auto/man/mix`） | 我们做成 tag pill | 列表来源改回纯文字着色 |
| 工具栏权限徽标是小巧的橙色带边框锁（`.lock`） | 我们用灰色大 tag | 换成橙色小锁徽标 |

同时发现健康度条分隔线在深色主题下仍不清晰：虽然用了 `--border-strong`，但暗色背景下几乎不可见，改为 `rgba(255,255,255,0.12)`；标签与提示文字提亮，数值字号从 22px 回调到 20px 与原型 19px 接近。

**语义对齐**：原型「失联」提示为「超 30 分钟未上报」，而此前代码用了 5 分钟；将 `assetStaleThreshold` 从 `5 * time.Minute` 改为 `30 * time.Minute`。30 分钟对服务器资产既能覆盖一次 Agent 短暂失联/系统重启，也不会把真掉线藏太久；列表与摘要共用同一个 `StaleBefore`，口径不变形。

**验证方式**：`go build ./...` + `go test ./internal/server/asset/ ./internal/server/api/` 通过；`npm test` 6 个文件 + `npm run build` 通过。

### 批次 8（2026-09-30）：头部与筛选区重做（1.30.3）

用户升级 1.30.2 后仍反馈「这块区域很难看」，并建议借鉴其它页面。对照仓库既有页面（`AlertsView`、`SecurityView`、各中间件 Tab、`DockerTab`）后确认：**本页的健康度条是仓库里唯一的"自绘横条"**，其它页面统一使用 `KpiCard` 组件（顶部彩条 + 图标 + 数值 + 标签）。

| 区域 | 1.30.2 | 1.30.3 |
|---|---|---|
| 健康度 | 自绘 `.panel.health` 横条：靠分隔线切分、无图标、无卡片边界 | 5 张 `KpiCard`（`tone` 分别用 total/down/ops/mem/conn），网格 `minmax(150px,1fr)`——1080 宽窗口下 5 张同行不落单；前 4 张可点击下钻 |
| KpiCard | 只有 `value / label / tone` | 增加**可选** `hint` 属性（补充口径说明行，如"超 30 分钟未上报"），不传则与原来完全一致，其它页面不受影响 |
| 筛选 | 裸排列的控件，无标签、间距靠默认值 | `.filter-bar` 浅底圆角容器 + 字段标签（类型/状态/来源/归属节点）+ 关键词框前置搜索图标；宽度统一 |
| 操作 | 按钮、权限徽标与一长串说明挤在一行且未对齐 | `.action-bar`：左侧「新建资产 + `assets:write` 徽标」，右侧（`margin-left:auto`）一行 muted 说明；说明合并了两句原先分散的文案 |

**验证方式**：`npm test` 6 个文件 + `npm run build` 通过（`KpiCard` 独立分包 0.75 kB，`AssetListView` 23.55 kB / gzip 8.54 kB）。

### 批次 9（2026-09-30）：修复「资产全部显示失联」+ 补齐编辑入口（1.30.4）

**故障**：升级后用户反馈「中间件资产都显示失联」。

**根因**：`LastSeenAt` 取自资产属性行的 `updated_at`，而 `Apply` 为了不制造变更记录噪声，
**只在值真正变化时才写属性行**（`if ok && normalizeValue(prev) == normalizeValue(value) { continue }`）。
主机的 `os`/`cpuCores`/`memoryMB` 与中间件的 `version`/`topology`/`up` 长期不变，
于是这些资产的「最近上报」永久冻结在最后一次变更时刻，超过失联阈值后**整批**被判为 `missing`。
换句话说：把「值变了没有」当成了「还在不在」。

**修法**：把「最近见到」与「值变没变」拆成两个维度——新增 `asset_seen(asset_id PK, last_seen_at)`，
每次采集上报（`SourceDiscovery`）都 `UPSERT`（用 `MAX` 防止乱序上报把时间往回拨），
`LastSeenAt()` 改读它；列表/摘要/筛选的 `lastSeenExpr` 一并改读该表。

| 决策 | 理由 |
|---|---|
| 不提升 `schemaVersion` | 纯附加表，旧版本程序不读它；保留「回滚旧版本时 `assets.db` 可直接沿用」这一既定运维性质 |
| 不复用 `assets.updated_at` | 人工维护也会刷新它，「最近上报」会变成人工改动时间 |
| 不在属性行上刷新时间戳 | 那会让属性行的 `updated_at` 失去「值何时变过」的含义，且每轮对每个属性多一次写 |
| `MAX(last_seen_at, excluded)` | 多 Agent / 网络重试导致的乱序上报不应把时间往回拨（否则凭空出现失联资产） |

**升级后注意**：`asset_seen` 为空的历史资产在**下一次上报前**仍会显示失联（主机与实例都会有新行），
一轮采集周期（默认 15s）后自愈；这是预期行为，不是残留故障。

**同时补齐编辑入口**（用户反馈「缺少编辑功能」）：列表行操作与抽屉按钮由「维护人工值」改为
**「编辑」/「编辑资产」**；编辑对话框**预填当前名称**（此前名称框为空，看起来像不能改），
并只提交真正改过的名称（服务端把空名称视为不修改）；补充只读的「归属节点」字段，
明确自然键 / 归属节点是资源范围锚点、不可修改。删除资产仍无后端接口，故不放置无效入口。

**验证方式**：新增 `TestApplyRefreshesLastSeenEvenWhenValuesUnchanged`（值不变也必须刷新最近上报，
且不得产生变更记录）、`TestListStatusUsesLastSeenNotValueChange`（40 分钟未变仍为在线，
列表与摘要同为 0 条失联）；改写 `TestAssetDerivedFieldsAreComputedNotStored` 覆盖
「属性时间戳很旧但 `SeenAt` 新鲜 → online」。`go test ./internal/server/{asset,api,receiver}` 通过；
`npm test` 6 个文件 + `npm run build` 通过。

