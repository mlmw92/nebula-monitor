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

1. ~~**未实现 `DELETE /api/v1/assets/{id}`**~~ —— **已按批次 14 落地**，但语义与设计草案不同：删除拆成
   **忽略（隐藏，可恢复）** 与 **彻底删除（仅纯人工资产）** 两个动作。原因：采集资产由上报驱动，
   删了会被下一轮重建，"删除"对它们是没有意义的动作；把两种意图塞进一个 `DELETE` 只会让人困惑。
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

### 批次 10（2026-09-30）：区分「上报状态」与「实例可用性」（1.30.5）

**反馈**：截图里某行副标题写「离线」、状态列却是「在线」，看起来自相矛盾。

**澄清**：两者本来就是不同维度，问题出在**用词撞车**：

| | 来源 | 含义 |
|---|---|---|
| 副标题的「在线 / 离线」 | 采集属性 `up`（Docker 容器 = `status == running`；其它实例 = 探活结果） | **采集那一刻，实例/容器自己是否可用** |
| 状态列的「在线」 | 派生自 `asset_seen.last_seen_at`（批次 9） | **Agent 最近 30 分钟内是否还在上报这条资产** |

容器退出后并不会从实例清单里消失（`up=false` 但仍逐轮上报），所以两句话同时为真：
「容器已退出」+「上报正常」。

**改法**（措辞 + 补信息，不改判定逻辑）：

1. 列名「状态」→**「上报状态」**，值 `online` 的文案由「在线」改为**「上报正常」**；
   悬停给出解释（"只表示 Agent 还在上报这条资产，不表示实例可用"）+ 最近上报时间。
2. 副标题的 `up` 文案改为**「实例可达 / 实例不可达」**，与状态列不再共用「在线」。
3. **补采集字段**：给实例观测加 `status` / `image`（Docker：容器状态与镜像），
   副标题对容器改显示「已退出 / 运行中 / 已暂停…」，让"为什么不可达"一目了然；
   抽屉的采集值 / 人工值 / 生效值统一走 `attrText()` 中文化（`up` → 可达/不可达，`status` → 中文）。

**验证方式**：receiver 用例扩展为「redis + kubernetes + 已退出容器」，断言容器资产的
`status=exited`、`image` 与 `up=false` 均落库；`go test ./internal/server/{receiver,asset,api}` 通过；
`npm test` 6 个文件 + `npm run build` 通过。

### 批次 11（2026-09-30）：去掉按钮旁的权限徽标（1.30.6）

**反馈**：「新建资产旁边这个图标是干啥的」。

那是批次 6 从原型照搬的权限标注 `.lock`（`＋ 新建资产 [assets:write]`）。原型里的这行标注是
**给开发看的**（说明该动作需要哪个权限点），搬到线上界面就成了噪声：普通运维不需要知道权限点名字，
而真正缺权限的人本来就不显示这个按钮。

**改法**：删掉徽标与对应样式，改为一句话的语义 + 悬停提示：

- 有权限：按钮悬停提示「需要 `assets:write` 权限；属高风险操作，提交前会二次确认」（native title，不占视觉）
- 无权限：按钮与徽标都不出现，只留一行 muted 文案「当前账号只读：缺 assets:write 权限，可在「权限模型 → 资产」中授予」

**验证方式**：`npm test` 6 个文件 + `npm run build` 通过；全文检索确认无 `.lock` 残留。

### 批次 13（2026-09-30）：差异巡检（inspect）落地

补上「资产与配置」P0 的最后一块。设计件 §「配置快照与差异巡检」定的是三层比对，
本次落地 **L2（快照前后 diff）+ L3（与期望值比对）**，L1（文件 SHA256）仍是既有 FIM 的职责。

**表**（纯附加，不提升 `schemaVersion`，保留「回滚旧版本可沿用 assets.db」）：

| 表 | 用途 |
|---|---|
| `inspect_runs` | 一次巡检：范围 / 操作人 / 覆盖资产数 / 首次建基线数 / 差异数 / `truncated` |
| `inspect_findings` | 差异项：冗余存资产身份（type/key/name/node），**不加外键级联**——巡检记录是证据，资产被删后结论仍须可读 |
| `inspect_baselines` | 期望值（标杆）：**按资产类型**只保留一个，指向某资产的某次快照 |

**四个关键决策**（都写进代码注释）：

1. **巡检即快照推进**：设计里快照由 Agent 顺带产出，但 Agent 侧尚无该能力；若不就地抽快照，
   就永远没有可比对的基线。因此 `RunInspect` = 「取最近快照 → 与当前生效值比对 → 落 finding →
   推进快照」，且**只在本资产字段真的变了或还没有快照时才推进**（否则巡检比配置变化频繁时会把快照表刷爆）。
2. **首次无基线不产出差异**，只计入 `baselined` 并让界面解释："数据不足 ≠ 不合规"（沿用对外状态页的既有语义）。
3. **排除运行态字段**（`up` / `status` / `uptime`）：巡检问的是"配置有没有变"，
   把每次探活都可能翻转的字段放进来会把真正的配置变更淹掉。
4. **级别规则**：新增 `added` = info（多数只是采集到新项）、变更 `changed` = warning、
   缺失 `missing` = **critical**（字段消失通常意味着采集退化或配置被删，比"值变了"更该被看见）、
   合规偏差 `deviation` = warning。

**接口与权限**：`POST /api/v1/inspect/runs`（`inspect:run`）、`GET /api/v1/inspect/runs`、
`GET /api/v1/inspect/runs/{id}/findings`、`GET /api/v1/inspect/baselines`（`inspect:read`）、
`POST|DELETE /api/v1/assets/{id}/baseline`、`GET /api/v1/assets/{id}/snapshots`（`assets:read`/`assets:write`）。
**「跑巡检」与「改台账」刻意分成两个权限点**：巡检只给结论、不改任何配置。
范围复用台账筛选（含资源范围下推）：受限用户只检得到自己范围内的资产——
否则会出现"巡检说某台有问题、列表里却找不到它"。单次巡检资产上限 5000，超限**显式标注 `truncated`**。

**前端**：新增「资产与配置 → 配置巡检」（`web/src/components/asset/InspectView.vue`，路由 `/inspect`，
`inspect:read` 门控）：范围筛选 + 执行巡检（`inspect:run` 门控）+ 巡检记录表 + 差异项表（级别/差异类型/
期望值→实际值）+ 期望值（标杆）列表与清除。资产详情抽屉新增「设为期望值（标杆）」/「清除期望值」。

**与设计的差异**：① 快照列表接口已提供（`GET /assets/{id}/snapshots`），但界面暂不展示快照时间线——
差异项里已有 expected/actual，单独列快照收益不足；② **巡检结果尚未接入巡检报告**（设计 §「巡检结果的可视化」
提到的 report 章节）——列为后续。

**验证方式**：服务层 5 个用例（首次只建基线 + 值不变/`up` 翻转不产差异、`added`/`missing` 两种分级、
标杆偏差与清除标杆、资源范围下推与空范围恒空、记录可检索 + 非法记录 ID 报错）；
接口层 5 个用例（`inspect:run` 权限负例 + 首次巡检 baselined 计数、受限巡检只覆盖范围内资产、
标杆受范围约束且范围外 404、读取权限负例 + 非法 ID 400、快照列表范围外 404）。
`go test ./internal/server/{asset,api,auth,receiver}` 通过；`npm test` + `npm run build` 通过
（`InspectView` 按需分包 6.96 kB / gzip 3.47 kB）。

### 批次 14（2026-09-30）：忽略（软删）与标签（1.30.11）

用户拍板：删除采用**软删/忽略**语义（采集资产删了会被下一轮上报重建），标签采用**独立结构**。

**忽略（hidden，不是删除）**

| 决策 | 理由 |
|---|---|
| 新增独立表 `asset_ignored(asset_id PK, reason, ignored_by, ignored_at)` | 与 `asset_seen` 同理：纯附加，**不提升 schemaVersion**，保留「回滚旧版本可沿用 assets.db」；语义上也更贴切——忽略是一个**动作与理由**，不是资产自身的属性 |
| 忽略**不停止采集**，属性继续刷新 | 恢复后看到的是最新状态；"隐藏"与"停止采集"是两件事，混在一起就没法既隐藏又保数据 |
| 采集上报不会把忽略冲掉 | 忽略是用户的决定，不该被下一轮上报重置（测试断言） |
| 列表 / 摘要 / **巡检**默认都不计入 | 台账是"该关心的东西"的清单；巡检若还检已忽略的资产，差异项会指向列表里看不到的行 |
| 摘要新增 `ignored` 计数 | 不显示这个数字，用户会以为自己忽略过的东西"找不回来了" |
| 忽略/恢复不写字段级变更历史 | 那里只记属性值的变化；混进来会让"这个字段从什么变成什么"失真。管理动作由操作审计留痕（谁在何时隐藏了什么） |

**彻底删除**：只对**纯人工建档**资产开放（`HasDiscovery() == false`）。采集资产返回 **409** 并提示改用忽略——
删了会被重建，那种"删了又回来"会让人以为删除没生效。删除按外键级联清掉属性/关系/变更/快照；
若该资产正是某资产类型的巡检标杆，**一并清除**并在响应里回执 `baselineCleared`（否则标杆会指向不存在的资产）。

**标签（`asset_labels`，独立结构）**：与 `asset_attrs` 职责分开——attrs 是采集值/人工值（含 `owner`，
参与变更与巡检语义），labels 是分类维度（业务系统/环境/机房…），只用于展示与筛选。
支持 `key` 与 `key:value` 两种筛选写法（走 `(key,value)` 索引）；标签变更进**字段级变更历史**，
字段名带 `label:` 前缀，与属性共用一条时间线（"谁把 env 从 test 改成 prod"是最常被追问的一句）；
**巡检不比对标签**（标签不是配置项，测试有断言）。

**接口**：`POST /api/v1/assets/{id}/ignore|restore|purge`、`PUT /api/v1/assets/{id}/labels`（均 `assets:write`）；
列表新增 `ignored=with|only`、`label=` 查询参数，非法 `ignored` 取值明确 400（不静默按默认处理）。
视图新增 `hasDiscovery`（前端据此决定是否提供「彻底删除」）、`ignored/ignoreReason/ignoredBy/ignoredAt`、`labels`。

**前端**：筛选栏新增「标签」输入与「含已忽略」开关；健康度新增**「已忽略」**卡片（可下钻，下钻态可撤销）；
列表名称下展示标签 chips、已忽略行带「已忽略」标记（悬停显示理由与操作人）；
行内低频破坏性动作收进「更多」下拉（忽略 / 恢复 / 彻底删除，且彻底删除只对纯人工资产出现）；
抽屉顶部对已忽略资产给出说明横幅 + 「恢复」按钮；编辑对话框新增标签编辑区（删行即删标签）。

**验证方式**：服务层 3 例（忽略只隐藏不停采、不被上报复活、摘要与列表同口径、恢复幂等；
彻底删除仅人工资产 + 标杆随之清除；标签写入/筛选/变更历史/非法输入且不进巡检）
+ 接口层 3 例（权限负例、隐藏与 `ignored=only` 下钻、409 提示改用忽略、按标签筛选、空键 400、非法参数 400）。
`go test ./internal/server/{asset,api,receiver}` 通过；`npm test` + `npm run build` 通过
（`AssetListView` 30.43 kB / gzip 11.15 kB）。

### 批次 12（2026-09-30）：左侧菜单无法滚动（1.30.7）

**反馈**：截图里菜单底部被裁掉（「系统设置 → 个人中心」以下看不见），**无法上下滚动**。

**根因**：`.sidebar` 是 `overflow: hidden` 的定高 flex 列（`top:0; bottom:0`），而 `.nav`
只写了 `flex: 1`、既没有 `overflow-y: auto`，也没写 `min-height: 0`。分组默认收起时高度够用，
一旦展开几个分组（或分辨率偏小），超出视口的部分就被直接裁掉、且没有任何滚动入口。

**修法**：`.nav` 加 `min-height: 0; overflow-y: auto; overflow-x: hidden`。
`min-height: 0` 不是可选项——flex 子项默认 `min-height: auto` 不会收缩，只加 `overflow`
仍然不会出现滚动条。品牌区、版本信息与底部按钮保持固定，只有菜单区滚动。

**验证方式**：`npm test` 6 个文件 + `npm run build` 通过；产物 CSS 中 `.nav` 含 `overflow-y:auto`。

### 批次 15（2026-10-01）：批量维护与清单导出（1.30.17）

批次 5 对齐原型时列在「未做」里的一项：原型工具栏的批量按钮与「导出清单 CSV」。

**接口**：`POST /api/v1/assets/batch`（`assets:write`）+ `GET /api/v1/assets/export`（`assets:export`）。

| 决策 | 理由 |
|---|---|
| 批量沿用**单条写的服务方法**（`Apply` / `ResetManual` / `SetLabels` / `Ignore` / `Restore`），API 层只做循环与汇总 | 批量不该有"另一条写路径"：否则字段级变更历史、审计与校验迟早出现两套口径 |
| 语义照 **ops 批量下发**：逐条结论、部分成功是常态、单批 200 条上限**显式报错**、整批一条审计 | 仓库里已有一套批量语义，再发明一套只会让两个页面用不同的话说同一件事 |
| 范围外按**「资产不存在」**逐条计入 items（不整批 403） | 与单条接口的既有约定一致（不给范围探测留信息）；整批拒绝会让"一条越界"废掉整批操作 |
| **一条都没成功才 409** | 把"全失败"报成"部分成功"会让人以为改动了一部分 |
| 「清除责任人」用独立 op，且**本来没有责任人的按成功计** | 清空与转派是两件事，不靠"输入框留空"区分（留空会静默清掉一整批）；批量里把"本来就是对的"报成失败会让结果面板全是失败 |
| 导出**单设权限点** `assets:export`，并入高风险 | 一次把整份台账（IP / 责任人 / 标签）落盘，与「逐页翻看」不是一个量级的动作（与 `audit:export` 同类） |
| 导出走 `ListAll`（不分页），命中超 20000 条**明确拒绝** | 分页版默认只取一页，导出的表会"看起来像全量、其实只有 50 条"；静默截断同样不可接受 |
| 导出带 BOM、状态与来源翻成中文、只导**人工值** | 表是给人（与 Excel）看的；采集值属于逐台几十项的时序侧数据，混进来会让清单无法阅读 |
| 不做**批量彻底删除** | 原型自身就把「批量删除」置灰；忽略（可恢复）才是采集资产对应的动作，彻底删除仍限逐条且仅纯人工资产 |
| 前端把批量条抽成共享组件 `BatchBar.vue` | 节点操作页刚做过同一条，抄第二遍必然出现两页间距/文案的差异 |

**顺带修**：批量清除责任人时，`ResetManual` 对"没有人工值可恢复"会报错——在批量路径里按
**已达目标状态**处理（幂等），否则一整批里本来就没责任人的那些会被报成失败。

**验证方式**：接口层 4 个用例（范围外的按「不存在」计入且只改范围内那一条、全失败 409、
批量标签进字段级历史、批量忽略后默认列表隐藏 / `ignored=only` 可见、清除责任人、参数校验与
200 条上限、导出需独立权限点、导出只含范围内且带 BOM 与中文取值、`type=host` 筛选生效）；
`npm test` 7 文件 38 例通过；浏览器实测（真实 Agent + 假时序库）五个批量动作逐一走通
（转派 5/5、打标签 5/5 且服务端 `labels={"env":"prod"}`、忽略 5/5 且默认列表 0 条 /
`ignored=only` 5 条、恢复 5/5），导出下载得到 `assets-20261001-181953.csv`（BOM + 6 行 +
责任人/标签/人工值齐全）。

**已知取舍**：批量条里没有「导出选中」——导出接口按**筛选条件**取集合（与列表同一套参数），
要导一个子集请用类型 / 节点 / 标签筛选（原型也只画了「导出清单 CSV」一项）。

### 批次 16（2026-10-02）：关系统一「人工优先」+ 逻辑删除（1.30.24）

批次 1 起，`asset_links` 只有「采集发现」一种来源，且解除关系是物理删除。本批把**关系**当成一等公民补齐三件事：
来源可区分、人工可维护、删除可撤回。批次 14 对采集资产定的规矩是「用忽略而不是删除」，本批把同一个判断延伸到关系上。

**语义（用户拍板）**：① 人工维护的关系**优先于**采集自动发现；② 人工解除关系走**逻辑删除**。

**接口**（三个都走 `assets:write`，与忽略 / 标签 / 标杆同级的「台账维护」权限点）：

| 接口 | 说明 |
|---|---|
| `POST /api/v1/assets/{id}/links` | 人工建边；这条边若已由采集建立，则**升级为人工认领** |
| `DELETE /api/v1/assets/{id}/links` | **逻辑删除**：删边 + 落抑制，采集不再重建 |
| `POST /api/v1/assets/{id}/links/restore` | 取消抑制，交还给采集 |

| 决策 | 理由 |
|---|---|
| `asset_links` 加 `source`（复用既有 `Source`），唯一约束**不含**它 | 同一 `(from,to,kind)` 只有一条边，`source` 是这条边的属性而不是身份的一部分。人工认领后采集继续上报同一条边不降级——「优先」就落在一条 UPSERT 的 CASE 上 |
| 新增 `asset_link_suppressions` 表承载逻辑删除 | 物理删除的话下一轮采集立刻把边建回来，用户的操作等于没做；抑制记录保留的正是「人说过这条关系不存在」这个判断 |
| Service 拆成四个入口：`LinkDiscovered` / `LinkManual` / `UnlinkManual` / `RestoreDiscovered` | 采集与人工是两条不同的**意图**。合成一个带参数的 `Link()` 会让"是不是采集"变成每个调用方每次都要想的事；拆开之后 receiver 只能调 `LinkDiscovered`，人工路径也不可能绕过抑制检查 |
| 三种动作**共用一套寻址**：URL 里的资产为基准 + `toType/toKey/kind/direction` | 读接口本来就返回 `direction`，前端原样回传即可——不用自己算方向，也不会"以为是出边、实际删了入边"；同时省掉按 `linkId` 删所需的额外存储查询 |
| `DELETE` 只从**查询串**读寻址，`POST` 只从 JSON 体读 | DELETE 的请求体在 HTTP 语义里没有定义，中间设备丢弃它是**合法**行为——放进体里等于埋一个"某些环境下删不掉"的坑。也不做「哪个有值用哪个」的兜底猜测：来源不明确，调用方写错了看不出来 |
| 读载荷新增 `suppressed` 数组，写接口返回**同一个载荷** | 抑制是逻辑删除，界面上看不到就无从恢复，用户会以为删掉就永远回不来；写操作直接回填最新载荷，避免"表格已更新、抽屉还是旧的" |
| 关联视图补 `id` 与 `source` | `source` 是关系表里最需要一眼看清的属性（人工 vs 采集），`id` 给前端一个稳定的行标识 |
| 对端做资源范围判定，且**统一 404**（不区分"不存在"与"不在范围内"） | 否则受限用户可以用「建边是否成功」探测范围外资产是否存在——读接口剔除范围外对端的约束会被写接口绕过 |
| 前端新增「已人工隐藏」区并可恢复 | 同上：看不见的删除等于不可逆 |

**存储迁移**：`schemaVersion` 1→2，并**显式为存量库补 `source` 列**。`CREATE TABLE IF NOT EXISTS` 对已存在的表
什么都不做，新加的列必须 `ALTER`。用「查列是否存在」而不是「按版本号判断」——手工删表、升级到一半的中间态都能自愈。
补列前必须把 `PRAGMA table_info` 的结果**读完并关闭**：连接池是单连接，留着未关闭的 `rows` 去 `ALTER` 会直接卡死。

**验证方式**：`TestManualLinkWinsOverDiscovery` 逐条钉死五条规则（人工建边升级来源、采集不许降级、抑制期间不许重建、
取消抑制后恢复、解除人工边同样落抑制）；`TestMigrateLegacyDBWithoutLinkSource` 构造真正的 v1 形态库验证补列与幂等；
`TestHandleAssetLinkWriteLifecycle` 走完整生命周期（认领 → 逻辑删除 → 采集不复活 → 取消抑制后重建 → 按 `direction=in` 删入边），
并覆盖未知类型 / 缺对端 / 非法方向 / 自关联 / 范围外对端 404，以及**反向钉住**「DELETE 不发查询串必须 400」。
`npm test` 39/39（新增一条钉住"POST 走体 / DELETE 走查询串"）。**尚未在 dev-server 实机验证。**

**未做（明确记录）**：

1. **`member_of` 仍无目标端，本批刻意不动它。** 本文件 §自动发现第 3 条把 `member_of` 定在 **Pod → 工作负载**上，
   而那条依赖「容器详设中新增的**清单上报**」——容器那一批（D3）做的是**下发查询**（随上报响应下发、Agent 现拉现回），
   台账里没有容器/工作负载资产（资产类型仍只有 `host` 与 `middleware-instance`）。因此 `member_of` 目前只可能人工建立，
   等容器清单上报到位再并入那一批。
   **不要**退化成「中间件实例 member_of 中间件集群」：那要给中间件引入「集群」资产类型，会进台账列表与健康度计数，
   且集群资产没有归属节点、与「按节点裁剪资源范围」的规则冲突（`nodeInScope` 会把它整条剔除，等于对受限用户隐藏）。
   采集侧确实有 `group` / `topology`（`internal/server/receiver/assets.go`），但它们**已经是实例上的属性**，
   支撑的是"按 group 聚合展示"，不是"指向一个集群资产"；且 `standalone` 拓扑下 `group` 就是实例名本身。
2. **关系图（拓扑视图）未做**，理由仍见 §与设计的差异第 1 条：人工关系本批刚落地，但依赖数据仍几乎为空
   （`depends_on` / `exposes` 目前只能人工建、没有自动来源），现在画图会是一棵两层树。
3. 关系的**批量维护**（批量解除 / 恢复）与导出未做。

