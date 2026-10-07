# 一体化运维平台功能全景与实现状态对照表

日期：2026-09-30
定位：**本次设计的评审卡点**。先看清全貌与缺口、排定优先级，再据首批方向展开详细设计。
配套文档：开源调研对比见 `2026-09-30-ops-platform-open-source-research.md`；总体设计见 `2026-09-30-ops-platform-evolution-design.md`；模块详设见 `2026-09-30-asset-cmdb-design.md` 与 `2026-09-30-container-observability-design.md`。

## 一、口径说明

### 1.1 状态判定规则（可核性优先）

| 状态 | 判定规则 |
|---|---|
| **已实现** | 能在代码中指出实现入口（HTTP 路由 / handler / 采集器 / 前端组件 / 配置字段） |
| **部分实现** | 有底层能力但缺完整用户路径或关键环节（如采集有、管理操作面没有；采集有、模型化没有） |
| **未实现** | 代码中无对应实现（文档提及但代码不存在者，按未实现处理并在依据列标注） |

**取证方式**：**以代码为准**，文档（`README.md`、`CONTEXT.md`、`docs/*.md`）只作线索。所有「已实现/部分实现」判定都能落到具体文件与符号；每一行的依据列即证据。

### 1.2 优先级口径（P0-P3）

| 优先级 | 含义 | 与 `README.md` §路线图 既有三档的映射 |
|---|---|---|
| **P0** | 本次首批交付范围；或当前必须持续接入的治理项 | 对应 README「P0 近期」+ 本次首批（资产与配置、容器与可观测） |
| **P1** | 下一阶段（支撑 P0 落地或既有中期项） | 对应 README「P1 中期」 |
| **P2** | 中期（可排期） | README「P2 远期」中可纳入下一阶段者 |
| **P3** | 远期 / 长尾 / 依赖外部生态（暂不做） | README「P2 远期」中的长尾项 |

> **注意**：把 README 的「P2 远期」拆成 P2/P3 已**于 2026-09-30 评审确认**，README 的档位图例已同步为四档（P0 近期 / P1 中期 / P2 中期 / P3 远期），另有「暂不考虑」项。
> **已确认的降档**：SSO/LDAP/OIDC、Windows 节点支持、值班排班、电话短信与外部平台集成、多语言 i18n、服务目录与 SLA → P3；**移动端 / 微信小程序 → 暂不考虑**（非优先级问题）。

### 1.3 复杂度口径（粗估）

S（≤3 人日）/ M（1-2 周）/ L（≥1 月）。跨模块或需新增持久化层者至少 M。

### 1.4 归属设计件代号

| 代号 | 文档 |
|---|---|
| **D0** | 本表 |
| **D1** | `2026-09-30-ops-platform-evolution-design.md`（总体设计件） |
| **D2** | `2026-09-30-asset-cmdb-design.md`（资产与配置详设） |
| **D3** | `2026-09-30-container-observability-design.md`（容器与可观测详设） |
| **—** | 不属本次设计范围（保留在 `README.md` 路线图） |

## 二、全景总览

| # | 领域 | 功能点 | 已实现 | 部分实现 | 未实现 | 本次最高优先级 |
|---|---|---|---|---|---|---|
| 1 | 监控告警 | 10 | 9 | 0 | 1 | P1 |
| 2 | 资产 / CMDB | 11 | 7 | 3 | 1 | **P0** |
| 3 | 配置管理 | 6 | 2 | 0 | 4 | P2 |
| 4 | 自动化执行 | 6 | 4 | 1 | 1 | P1 |
| 5 | 发布部署 | 4 | 1 | 0 | 3 | P2 |
| 6 | 工单流程 | 4 | 1 | 0 | 3 | P2 |
| 7 | 堡垒机 / PAM | 7 | 3 | 0 | 4 | P2 |
| 8 | 容器 / K8s | 8 | 5 | 1 | 2 | **P0** |
| 9 | 日志 | 8 | 7 | 1 | 0 | P0 |
| 10 | 链路追踪 | 3 | 0 | 0 | 3 | P3 |
| 11 | 巡检合规 | 6 | 5 | 1 | 0 | P1 |
| 12 | 报表大屏 | 8 | 6 | 0 | 2 | P2 |
| 13 | 权限审计 | 8 | 6 | 1 | 1 | P0 |
| 14 | 开放集成 | 7 | 4 | 0 | 3 | P2 |
| | **合计** | **96** | **60** | **7** | **29** | |

> 2026-10-05 对账：上表按 §三的逐项状态重新统计。旧总览 93/51/7/35 未随详细条目与状态更新；本次仅纠正计数，不将「有代码入口」等同于「通过测试」。逐项验证计划及实测结果见 `../../testing/2026-10-05-platform-validation-matrix.md`。
>
> 2026-10-06 增量：§3.8「容器/Pod 日志拉取」由**未实现**转为**已实现**（`container.logs` 只读动作 + 前端日志抽屉）；同日「容器 → 日志/资产联动」由**未实现**转为**部分实现**（资产联动双向打通，日志检索联动待日志侧标签注入）；§3.9「结构化解析」由**未实现**转为**已实现**、「字段检索」由**未实现**转为**部分实现**（字段过滤已可用，但仍是**有界扫描**而非索引）；§3.2「资产关系与拓扑」的**拓扑视图**落地、§3.2「配置项模型页 / 关系视图页」由**未实现**转为**部分实现**（关系视图已落地，配置项模型页未做）；§3.9「外部日志后端」由**未实现**转为**已实现**（`LogStore` 接口 + VictoriaLogs 适配器同时落地，ADR-0002 定稿）；§3.1「告警 ↔ 资产/变更关联闭环」、§3.9「由日志创建告警规则」、§3.11「巡检周期化调度」由**未实现/部分实现**转为**已实现**，§3.8「容器 → 日志/资产联动」补上**日志侧联动**（Server 侧 `logSource` 绑定，无需改 Agent；Pod 日志进检索仍待 Agent 协议扩展）。故合计为 **58 已实现 / 8 部分 / 30 未实现**。缺陷修复与新增用例见 `../../testing/2026-10-06-platform-review-delta.md`。**§五 的 P0 首批至此全部落地**（资产与配置、容器与可观测、横向前置三条线），§五 列出的 P1 候选已完成四项（告警↔资产↔变更、容器↔日志↔资产、由日志建规则、巡检周期化）。同日补齐外部日志后端的**离线交付与实机验证**：`deploy/install-logs.sh` 等部署脚本配套（§3.9 行内已更新），并在真实 VictoriaLogs v1.53.0 上完成八项联调；期间修复 `ProbeTSDBRetention` 按 JSON 解析 VM **纯文本** `/flags` 的既有缺陷（保留策略页 TSDB 段一直显示解析错误）。

> **2026-10-06 续：§3.8「容器 → 日志/资产联动」由部分实现转为已实现**——**Pod 日志进集中日志检索**落地（`logSources[].podLogs` + paths 通配符 + 采集侧从文件路径解析容器身份随批次上报 + 服务端落身份与 `pods=` 过滤 + `podAssets` 联动 + 前端展示与「只看」）；协议见 `../../superpowers/specs/2026-09-30-container-observability-design.md` §4.6。故合计为 **59 已实现 / 7 部分 / 30 未实现**（容器/K8s 域 5/1/2）。用例与未覆盖边界见 `../../testing/2026-10-06-platform-review-delta.md` §十二。

> **2026-10-07 增量**：不改功能点计数（仍是 **59 已实现 / 7 部分 / 30 未实现**），只把已落地能力的**界面入口补齐**：① 容器页 → 集中日志检索（§3.8 该行）；② 检索页认领 `pods=` 深链。另在 dev-server 真实 k3s 上完成 Pod 日志端到端验证并修复 **CRI 框架未剥离**的缺陷（时间改用行内时间戳、正文剥掉 `<时间> stdout F`），见 `../../testing/2026-10-06-platform-review-delta.md` §12.4 与容器可观测设计件「批次 4」。
>
> **2026-10-07 续：§3.13「资产的资源范围扩展」由未实现转为已实现**——资源范围增加**业务标签维度**（与节点分组取交集，三态与节点维度同构），服务端在资产域读+写上强制、配置入口在角色/用户表单。故合计为 **60 已实现 / 7 部分 / 29 未实现**（权限审计域 6/1/1）。设计件与实施记录见 `../../superpowers/specs/2026-10-07-asset-business-scope-design.md`。**注意**：本次顺带关掉了一条既有提权路径（创建/更新用户与更新角色此前不受"范围不得超过操作者"约束）。

**一句话结论**：采集、告警、存储、权限、审计、离线交付已有基础；短板集中在资产拓扑可视化、日志结构化与索引、告警到资产/处置的关联闭环。下行操作已有参数化动作与逐机护栏；不提供任意脚本或在线终端作为默认扩展方向。

## 三、逐领域功能点表

### 3.1 监控告警

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 主机指标采集（CPU/内存/负载/磁盘/网络/进程/硬件信息） | 已实现 | `internal/agent/collector/`（hostinfo 等），`README.md` §主机监控 | — | — | — | — |
| 中间件监控（15 类，直连 + exporter 双模式） | 已实现 | `internal/agent/collector/{redis,mysql,postgres,nginx,kafka,docker,rocketmq,k8s,mongo,fastdfs,rabbitmq,elasticsearch,clickhouse,nacos,zookeeper}.go`；类型清单唯一出处 `internal/server/mwreg/builtin.go` | — | — | — | — |
| **指标字典与告警规则模板**（可配项目录 + 阈值范本） | 已实现 | `internal/server/metrics/`（目录 + 守卫：目录名必须有产出方）、`internal/server/alert/rules.go:DefaultTemplates`（65 条，按中间件分组 + 阈值依据）、`GET /api/v1/metrics/catalog` 的 `alertGroups` 供规则表单；守卫含「模板方向必须与指标变差方向一致」 | — | — | — | D2 |
| 服务拨测（HTTP/HTTPS/TCP/ICMP + 证书到期） | 已实现 | `internal/server/dialtest/scheduler.go` | — | — | — | — |
| 规则类型（阈值/主机离线/中间件离线/主从切换/集群损坏/安全事件） | 已实现 | `internal/model/metric.go:649-661`（`RuleTypeThreshold`…`RuleTypeSecurityEvent`） | — | — | — | — |
| 告警治理（三层静默 / 抑制 / 分组 / 收敛 / 升级 / 维护窗口） | 已实现 | `internal/server/alert/`（含 `engine.go` 求值、`rules.go:DefaultTemplates`） | — | — | — | — |
| 通知渠道（邮件 / Webhook / 钉钉 / 飞书 / 企业微信） | 已实现 | `internal/server/alert/notifier.go` | — | — | — | — |
| 告警协作处置（认领 / 指派 / 关闭 / 评论） | 已实现 | `internal/server/alert/ackstore.go`、`internal/server/api/alert_collab.go` | — | — | — | — |
| **告警 ↔ 资产/变更关联闭环** | **已实现** | **2026-10-06 落地**：`GET /api/v1/alerts/impact?node=&instance=`（权限 `assets:read`）把告警的**指标标签**对到台账资产，一次给出三件事：命中资产（主机按名忽略大小写匹配 + 实例按地址后缀匹配，可能多条）、该资产的近期变更（合并排序、条数有上限）、**波及范围**（以主机为中心的 N 跳关系邻域，复用 `assetTopologyPayload`）。前端在告警详情抽屉内展示并支持跳台账。**口径说明**：「波及范围」是**关系邻域**（runs_on/member_of/人工 depends_on 展开），不是从指标推断出的依赖图——平台不猜测拓扑。**未做**：把资产/影响面写进**通知消息**（通知模板目前只有事件字段与 labels） | **P1** | M | 资产与配置（已完成） | D2 |
| 值班排班（On-Call）/ 电话短信 · 外部平台（PagerDuty 等） | 未实现 | `README.md` 路线图 P2 | P3 | M | — | — |

### 3.2 资产 / CMDB

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 资产自动发现（主机信息/中间件实例/容器与工作负载清单） | **已实现** | 上报链路：`internal/server/receiver/assets.go:applyAssets`（主机 + 15 类中间件实例，幂等）；**K8s 容器/工作负载清单已落地**（批次 17，2026-10-04，设计件 `2026-10-04-container-inventory-design.md`）：协议 `internal/model/metric.go` 的 `k8sPods`/`k8sWorkloads`（含单轮上限与截断置位）+ 采集侧 `internal/agent/collector/k8s.go`（`spec.nodeName`、ReplicaSet → Deployment 属主解析、系统命名空间默认排除）+ `receiver/assets.go:applyContainerInventory`（**先工作负载后 Pod**，建 `runs_on` 与 `member_of`）。**口径修正**：标题原写「分区/进程/容器清单」——分区与进程在采集侧是**指标**（`collector/disk.go` 等），不是台账资产；容器清单此前**并无实现**，本批才真正落地 | **P0** | M | — | D2 |
| 资产台账（资产类型 / 资产实例 / 属性/标签 / 负责人） | 已实现 | 后端读写：`internal/server/asset`（SQLite）、`GET/POST /api/v1/assets`、`PUT /api/v1/assets/{id}`（人工值，`assets:write` 并入高风险）；负责人用约定人工属性键 `owner`；**标签已落地**（独立表 `asset_labels`，支持 `key` / `key:value` 筛选，变更进历史）；**删除已落地**（`ignore/restore` 软删 + `purge` 仅纯人工资产，见批次 14）；前端台账页已落地 | **P0** | M | 关系型持久化（ADR-0001，已完成） | D2 |
| 资产关系与拓扑（依赖 / 归属 / 影响传播） | 部分实现 | `runs_on`（实例 → 主机）已随上报自动建立且幂等（`asset.Service.LinkDiscovered`，采集侧）；**关系的来源与人工维护已落地**（`asset_links.source` + `asset_link_suppressions` 表、`POST/DELETE /api/v1/assets/{id}/links` + `/links/restore`；**人工维护优先于采集**、解除走**逻辑删除**且可恢复，见设计件批次 16）；`depends_on/exposes` 可人工建立但**没有自动来源**；**`member_of` 已落地**（批次 17：Pod → 工作负载随清单上报自动建立，Pod 经 ReplicaSet 解析到 Deployment）；容器/工作负载资产默认**不计入台账首页与健康度摘要**（短命对象会把"失联"变成噪声，见 `asset.EphemeralTypes`）；**拓扑视图已落地（2026-10-06）**：`asset.Service.Topology` 逐跳 BFS 取 N 跳邻域（跳数上限 3、节点上限 500，逐层收敛并**显式标记截断**）+ `GET /api/v1/assets/{id}/topology` + 前端 ECharts 力导向图（点节点换中心）。**仍缺**：`depends_on`/`exposes` 没有自动来源（只有人工建立，这是刻意的"关系宁少而准"），以及**全库视图**（当前以单个资产为中心） | **P0** | L | 资产台账 | D2 |
| 变更历史与审计（字段级 diff / 操作留痕） | 部分实现 | 字段级 diff 已落地：`asset_changes` + `asset.Service.History` + `GET /api/v1/assets/{id}/history`（**仅真变化才记录**，首建只写一条 initial）；**前端时间线已落地**（详情抽屉「变更历史」Tab，含旧值→新值 / 来源 / 操作人）；仍缺与操作审计（`audit`）的显式交叉链接 | **P0** | M | 资产台账 | D2 |
| 采集值与人工值分离（来源标记 / 冲突可见） | **已实现** | 属性联合主键含 `source`，`Asset.Value` 取生效值（人工优先），接口返回 `values` + `attrs`（两来源并存） | P1 | M | 资产台账 | D2 |
| 资产生命周期（上线/下线/退役/成本/维保） | 未实现 | `README.md` 路线图「CMDB（P2）：资产台账、生命周期与变更记录」 | P2 | M | 资产台账 | D2 |
| 资产与资源范围的映射/迁移 | **已实现** | 接口按资产所属节点走既有范围判定（`api/nodeInScope`）：范围外资产按 404 返回、先过滤再计数；权限点 `assets:read` 已注册并授予运维/只读角色 | **P0** | M | 资产台账 | D2 |
| 配置快照与差异巡检（配置项级） | 已实现 | 快照：`snapshots` + `snapshot_fields`（同事务）、`asset.Service.Snapshot`；**巡检（L2 快照前后 diff + L3 与期望值比对）已落地**：`inspect_runs` / `inspect_findings` / `inspect_baselines` + `asset.Service.RunInspect` + `POST /api/v1/inspect/runs` + 前端「配置巡检」页；差异分级 added/changed/missing/deviation，首次无基线只建基线不报差异；现行 FIM（文件 SHA256）仍是独立的 L1 层（`internal/agent/collector/security.go`）；**未接巡检报告章节** | **P0** | M | 资产台账 | D2 |
| 资产台账页（列表/详情/关系/变更时间线） | 已实现 | 按原型 `asset-prototype.html` 对齐：健康度条（总数/失联/无责任人/冲突/近 7 天变更，前四项可下钻且下钻态可撤销）、筛选（类型/状态/来源/归属节点/关键词含属性值）、服务端分页、表格列（名称+副标题/类型/节点/状态/最近上报/来源/责任人）、抽屉三 Tab（属性对比四列+冲突高亮+恢复采集值、变更历史时间线、关联关系含方向 + 来源标记 + 人工增删 + 已人工隐藏可恢复）。证据：`web/src/components/asset/AssetListView.vue`、`internal/server/api/asset_api.go`、`internal/server/asset/{service,store,model}.go` | **P0** | M | — | D2 |
| 资产台账：批量维护 / 批量转派 / 导出 CSV | 已实现 | `POST /api/v1/assets/batch`（转派/清除责任人、打标签、忽略、恢复；逐条结论、200 条上限、范围外按「不存在」）+ `GET /api/v1/assets/export`（独立权限点 `assets:export`、服务端不分页导出当前筛选、带 BOM）。批量删除仍不做——原型自身也把它置灰，改用「批量忽略」（可恢复）与逐条「彻底删除」 | P1 | M | 台账页（已完成） | D2 |
| 资产台账：配置项模型页 / 关系视图页 | 部分实现 | **关系视图页已落地（2026-10-06）**：详情抽屉「关联关系」页签内的「关系图」——以该资产为中心的 N 跳邻域（`GET /api/v1/assets/{id}/topology`，范围与 `/links` 同一套裁剪口径），ECharts 力导向图按类型着色、按状态标红圈、人工维护的边画虚线，点节点即以它为中心重新展开。**未做**：配置项模型页（自定义类型的字段 Schema 编辑，`asset_types.schema` 目前只存不解释）；独立的全库关系视图页 | P2 | L | 关系类型扩展 | D2 |

### 3.3 配置管理

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 升级指令下发（Server → Agent） | 已实现 | `internal/server/receiver/receiver.go:HandleReport`（`resp["command"]="upgrade"`）；Agent 侧 `internal/agent/reporter/reporter.go:ReportResponse` + `cmd/agent/main.go` | — | — | — | — |
| 处置指令下发（fail2ban 托管） | 已实现 | `resp["defense"]` → `internal/agent/defense/executor.go:Executor.Execute` | — | — | — | — |
| 采集项模板解析/CRUD/下发 | **未实现（已移除）** | 移除提交 `1e25239 refactor!: 移除采集项模板功能全链路`；`internal/template` 目录不存在、无 `/api/v1/middleware/templates` 路由。**`CONTEXT.md` 仍有 7 处描述（滞后，待修订）** | P2 | L | — | — |
| 中间件 / Agent 配置远程修改 | 未实现 | `README.md` 路线图「配置下发与修复动作 P2」 | P2 | L | 资产台账 + 下行通道 | D1 |
| 配置版本化与回滚 | 未实现 | — | P2 | M | 配置远程修改 | D1 |
| 配置中心（集中配置模板/密钥引用） | 未实现 | — | P3 | L | — | — |

### 3.4 自动化执行

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 任务下发通道（Server → Agent 执行后回传） | 已实现 | **已泛化**：`internal/server/ops/`（动作目录 + 任务状态机 `queued→delivered→running→succeeded/failed/expired` + 能力协商 + 超时回收）、`internal/agent/ops/`（执行器 + 幂等落盘）、receiver 响应体搭车下发 `resp["ops"]`、回执 `payload.opsResult`；当前 8 个动作：`node.diagnostics` / `svc.status`（只读）、`svc.restart`（写）、`container.workloads|pods|describe|events`（只读）、`file.push`（写，独立护栏）。原两条专用链路（升级、fail2ban）保留 | — | — | — | D1 |
| 执行护栏（本机护栏/能力协商/中心授权+审计） | 已实现 | 四道护栏均落地：① 本机护栏 `agent.yaml guards.ops`（默认只读；写动作需 `write:true` **且** 单元在 `units` 清单）② 能力协商 `Capabilities.Ops`（只声明本机放行的动作）③ `ops:read`/`ops:exec` 权限点 + 参数校验 + 审计（`ops:exec` 已入 `auth/policy.go:HighRiskPermissions`）④ 默认只读。**护栏按"同意的性质"分设**：容器查询有 `guards.ops.container`、文件分发有 `guards.ops.file{write,dirs}`——它们各自独立于只读总开关与 `write/units`。注：`HighRiskPermissions` 本身仍是**声明式**标记（不额外拦截），二次确认按只读/写动作区分 | — | — | — | D1 |
| **参数化动作扩展**（原「批量命令 / 脚本执行」） | 已实现（持续项） | **2026-10-05 定界：操作面不提供任意命令 / 脚本执行，只做参数化动作**——中心只能填参数、不能提供命令，参数由两端按动作规格校验。机制本身已齐备（动作目录 + 逐机护栏 + 能力协商 + 参数校验 + 审计），因此本行由「待实现的大件」转为**持续项**：有新排障需求时**加一个动作**，不开 shell。理由见下方「操作面安全边界」。**批量下发是这套机制的一部分且已实现**：`POST /api/v1/ops/tasks/batch`（逐节点结论、200 台/次上限、批次号聚合、仅排队中可撤回），依据 `internal/server/ops/batch_test.go` | P1 | S/次 | 任务下发通道 + 护栏 | — |
| Web 在线终端 | 未实现 | 前端无终端组件；`grep` 无实现 | P2 | L | 批量执行通道 | — |
| 文件分发 | 已实现 | `file.push` 动作 + `POST /api/v1/ops/files`（multipart 上传，`ops:exec`）。**内容与任务分离**：内容落 `ops_files/<ref>.bin`，任务里只留 `fileId`，领取时注入下发的副本（`internal/server/ops/files.go`；不这么做 `ops_tasks.json` 会按「条数 × 文件大小」膨胀，而它每次状态流转都整体重写）。**本机护栏独立**：`guards.ops.file{write,dirs}`，默认不放行且必须列出允许目录（与「重启服务」是两个独立的同意）。Agent 侧三道约束：目录白名单（含父目录软链解析，防 `/opt/app/conf → /etc` 逃逸）、sha256 + 长度校验、**先备份再原子替换**（`internal/agent/ops/file.go`）。**单文件上限 256KiB**；大文件、目录递归、"执行分发后的文件"未做，理由见 `2026-09-30-ops-platform-evolution-design.md` 的文件分发实施记录 | P2 | M | 任务下发通道 | D1 |
| 定时计划任务 | 部分实现 | 仅拨测有调度器：`internal/server/dialtest/scheduler.go`；无通用作业调度 | P2 | M | 任务下发通道 | — |

**操作面安全边界（2026-10-05 定）**

1. **不提供任意命令 / 脚本执行**。这是唯一一个会把「逐机白名单」彻底作废的能力：能跑 `/bin/sh -c` 之后，
   「允许重启哪些单元」「允许写哪些目录」就不再是安全边界，攻破中心即等价于**全网 root RCE**
   （Agent 以 root 运行，fail2ban 托管与文件落盘都需要）。有新排障需求时**加一个参数化动作**，不开 shell。
2. **逐机本地同意优先于中心授权**（既有原则，`agent.yaml guards.ops`）：中心被攻破也不会自动获得执行权，
   只能在**本来就被本地显式授权过**的机器与动作上生效。这是唯一真正有效的结构性防线。
3. **组合护栏：可写「可执行目录」+ 可重启单元 = RCE —— 已实现（2026-10-05）**。`file.push` 的护栏是**目录前缀**、
   `svc.restart` 的护栏是**单元清单**，各自看都正常；但若某台机器同时放行了「某个服务会执行的目录」
   （如 `/etc/systemd/system/`）与「对应单元」，那么「推一个文件 + 重启」就等于任意代码执行
   ——**不需要任何"任意命令"能力**。这条依赖此前完全是隐性的，没人会意识到自己配出了一个 RCE。
   **做法（Agent 启动时检查并告警）**：`internal/agent/ops/guardcheck.go` 的 `CheckGuardRisks` + `SystemdExecutables`，
   查两条路径：① `file.push` 允许的目录覆盖了某个**可重启单元的可执行文件**所在目录
   （取 systemd 的 `ExecStart`/`ExecReload` 实际路径，含软链解析后的形态再比对）；
   ② 允许写的是**系统自己会执行的目录**（systemd / init.d / cron.* / profile.d / ld.so.conf.d / sudoers.d / root 的 `.ssh`）
   ——后者**连重启都不需要**，因此与 units 清单无关。刻意**只告警、不拒绝**：两个同意都是用户明确写下的，
   替他悄悄禁掉一个比让风险可见更难排查，也违背「机器自身的同意优先于中心授权」。
   **界面展示待做**（要把结论送到 Server 需新增上报字段，属后续小步）。
4. **若将来确实要放开任意执行**，前置条件是**双人复核 + 不可篡改审计**，而这两条现在都不具备：
   审计落在 `audit_events.json`（2000 条上限、与业务同一权限面、可被删除——**攻击者第一步就是清日志**）。
   缺了这两条，「任意执行」没有可辩护的版本。

### 3.5 发布部署

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| Server/Agent 自身升级 + 版本归档回滚 | 已实现 | `internal/server/upgrade/upgrade.go:Manager.Apply/RollbackTo/Archive`、`internal/agent/upgrader/upgrader.go:Run` | — | — | — | — |
| 应用制品管理与发布 | 未实现 | 无制品库/无应用发布模型 | P2 | L | 资产台账（应用维度） | — |
| 流水线（CI/CD） | 未实现 | — | P3 | L | 应用发布 | — |
| 应用级回滚 | 未实现 | 现有回滚仅针对 Server/Agent 二进制 | P2 | M | 应用发布 | — |

### 3.6 工单流程

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 告警处置协作（**非工单**） | 已实现 | `internal/server/alert/ackstore.go`（待处理→已认领→已关闭），无审批人/流程节点/工单号 | — | — | — | — |
| 工单 / 审批流引擎 | 未实现 | 全仓无 `ticket\|approval\|workflow` 实现 | P2 | L | 权限/审计（已有） | — |
| 变更单 / 发布审批 | 未实现 | `README.md` 路线图「单设备会话与审批流 P2」 | P2 | M | 工单引擎 | — |
| 服务目录与 SLA | 未实现 | `README.md` 路线图「服务目录与 SLA P2」 | P3 | M | 资产台账 | — |

### 3.7 堡垒机 / PAM

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| SSH 登录审计与暴力破解检测 | 已实现 | `internal/agent/collector/security.go:collectSSH`（**只读日志解析**，非会话代理） | — | — | — | — |
| sudo 提权审计 | 已实现 | `internal/agent/collector/security.go:collectSudo` | — | — | — | — |
| 入侵防御（fail2ban 专属 jail 托管封禁） | 已实现 | `internal/agent/defense/executor.go` | — | — | — | — |
| 会话录制 / 回放 | 未实现（**暂不做**） | 全仓无会话代理实现（`bastion\|replay\|pam` 仅文档命中）。**2026-10-05 用户判定价值薄**：堡垒机的价值主要来自**合规驱动**（等保/行业规范要求运维审计必须经堡垒机、会话必须录制），技术收益很薄；没有这条外部约束时，自建 PTY/协议转发/存储/回放的成本与收益严重不匹配。注意性质差别：**若将来出现合规要求，它不是"要不要做的功能"而是"必须有的证据"** | P3 | L | 会话代理 | — |
| 统一访问入口（SSH/RDP/K8s/DB） | 未实现（**暂不做**） | 同上——这是"用平台替代跳板机"的整块成本；用户 2026-10-05 判定价值薄 | P3 | L | — | — |
| 高危命令拦截 | 未实现（**保留项**） | **与会话录制无关**：拦截可落在统一下行通道 / 命令审计上，**不需要会话代理**。2026-10-05 随「砍掉堡垒机」明确为**保留项**；但落点随 3.4 的定界一起变了——**既然不提供任意命令执行，"拦住一条即将执行的任意命令"这件事就不存在**，价值转到两处：① 对**参数化动作的危险参数**做拦截 / 二次确认（如 `file.push` 覆盖系统文件、`svc.restart` 核心单元）；② 从 **SSH / sudo 日志**里发现高危命令（**事后发现**，不是事前阻断） | P2 | M | 命令审计通道（**不必**做会话代理） | — |
| 高风险操作双人复核 | 未实现（**保留项**） | `README.md` 路线图 P2。防的是误操作与单人误判，**不需要会话代理**，也不需要完整工单引擎——一小块审批状态机即可（工单/审批流引擎是 3.6 的另一件大事，别混在一起）。2026-10-05 明确为**保留项** | P2 | M | 审批状态机 + 权限（**不必**等工单引擎） | — |

### 3.8 容器 / K8s

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| K8s 集群指标采集（集群健康/Node/工作负载副本/Pod 统计） | 已实现 | `internal/agent/collector/k8s.go:collectPods/collectWorkloads`（只读 GET apiserver） | — | — | — | — |
| Docker 容器指标采集 | 已实现 | `internal/agent/collector/docker.go`（Docker Engine API） | — | — | — | — |
| 工作负载列表 / 详情 / YAML 只读视图 | 部分实现 | **列表与详情已落地**：集群清单 `GET /api/v1/container/k8s/clusters`（`internal/server/api/container_api.go`，权限点 `container:read`，按资源范围过滤）；工作负载 / Pod / 事件走统一下行通道的只读动作 `container.workloads` / `container.pods` / `container.describe` / `container.events`（`internal/server/ops/catalog.go` 目录 + `internal/agent/collector/k8s_query.go` 执行）；前端「观测监控 → 容器与工作负载」(`web/src/components/container/ContainerView.vue`)。**未做的部分**：不提供**原始 YAML** —— `container.describe` 是**白名单投影**（Pod 环境变量只给变量名、ConfigMap 只给键名、`secrets` 不在可选资源类型里），这是有意为之，不是缺失 | **P0** | L | 下行通道（ADR-0003，已完成） | D3 |
| 事件（Event）查看 | 已实现 | `container.events` 只读动作：`internal/agent/collector/k8s_query.go:QueryEvents`（服务端 `fieldSelector` 过滤 + 按时间倒序 + 200 条上限 + 截断显式回传）；前端同上页「事件」Tab | **P0** | S | 同上 | D3 |
| 容器/Pod 日志拉取 | 已实现 | **按需拉取已落地（2026-10-06）**：`container.logs` 只读动作（`internal/server/ops/catalog.go` 目录 + `internal/agent/ops/container.go` 护栏与参数复校 + `internal/agent/collector/k8s_query.go:QueryLogs` 调 apiserver `/api/v1/namespaces/{ns}/pods/{pod}/log`），前端在「容器与工作负载 → Pod」行上提供「日志」抽屉。**边界**：命名空间与 Pod 名必填；行数默认 200、最多 500，时间窗最多 24 小时；单次正文预算 96 KiB（超出即截断并显式标注）；单行超过 500 字符截断并统计；**不提供全量下载**（要看全量应上机器或走日志后端）。**仍未做**：~~把 Pod 日志接入集中日志链路~~ → **已落地（2026-10-06）**：走 `logSources[].podLogs` 采集本机 kubelet 的容器日志进入集中日志检索（协议见下一行与容器可观测设计件 §4.6）；本行这条按需拉取路径保持不变，两者分工见该设计件 §4.4；**容器页入口已打通（2026-10-07）**：Pod 行「检索日志」与日志抽屉的「在集中日志中检索」→ `/logs?pods=<命名空间>\|<Pod>`（按 `logs:read` 门控），"要看更多"不再止步于 200 行 | **P0** | M | 同上 | D3 |
| exec 终端（默认关闭、按权限开放） | 未实现 | 需三道门控 + 审计；凭据永不上行 | P2 | L | 只读管理面先落地 | D3 |
| 容器 → 日志/资产联动（Pod 打标回到资产与日志检索） | **已实现** | **资产联动（2026-10-06）**：`GET /api/v1/assets/lookup`（按类型 + 自然键，或容器身份三元组精确查一条，范围外按 404）；`assetView.container` 由服务端解出集群/命名空间/kind/名字（`asset.ParseContainerKey`，不让前端 split 含 `://` 的自然键）。双向入口：容器页 Pod/工作负载行「台账」→ 资产详情；台账里容器类资产「查看容器日志 / 查看容器工作负载」→ 容器页并按需打开日志抽屉。**文件日志侧联动（2026-10-06）**：资产上用人工属性 `logSource` 声明「这条采集来源属于我」，`GET /api/v1/logs` 按 (source, node) 反查并返回 `assets` 映射，行上显示资产标签并可「只看该资产」。**Pod 日志进检索（2026-10-06，本批）**：`logSources[].podLogs` 开启后 paths 支持通配符（`/var/log/pods/*/*/*.log`），采集侧**从文件路径**解析出容器身份随批次上报（只上报身份、不上报路径；`model.NormalizeLogOrigin` 双端同规则校验，非法即 400）；服务端落身份（VL 侧写 `k8s_*` 三个保留字段，正文不得覆盖）并支持 `pods=<命名空间>\\|<Pod>` 过滤；响应新增 `podAssets` 映射（按「节点 + 命名空间 + Pod 名」反查容器资产，类型必须是 pod），前端在行上展示「命名空间/Pod · 容器」、标到 Pod 资产并可「只看」按容器收窄。协议见 `../../superpowers/specs/2026-09-30-container-observability-design.md` §4.6 | P1 | M | 资产台账（已完成） | D2/D3 |
| 镜像/配置安全扫描 | 未实现 | 可选参考 `aquasecurity/trivy`（Apache-2.0） | P3 | M | — | — |

### 3.9 日志

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| Agent 日志采集（来源+路径+patterns、多行合并、限速、每日上限、偏移落盘） | 已实现 | `internal/agent/logship/ship.go:Shipper.send`、`internal/agent/collector/logcollect.go:mergeMultiline` | — | — | — | — |
| 日志上行接收（`X-Agent-Secret`） | 已实现 | `internal/server/receiver/logs.go:HandleLogs`（`POST /api/v1/logs`） | — | — | — | — |
| 检索（关键词/正则 + 时间范围 + 节点/来源过滤 + 游标分页 + truncated） | 已实现 | `internal/server/logstore/query.go:Store.Query`、`internal/server/api/logs_api.go:handleLogsQuery`；预算 `DefaultScanBudgetBytes=64MiB`、`DefaultScanBudgetLines=200000`、`DefaultLimit=200`/`MaxLimit=1000`；游标 `Cursor{File,Offset}` | — | — | — | — |
| 日志保留策略（默认 7 天，按天清理） | 已实现 | `internal/server/retention/`（`README.md` §数据保留） | — | — | — | — |
| 由日志创建告警规则（基于日志指标的阈值告警） | **已实现** | **2026-10-06 落地**：`GET /api/v1/logs/rule-template?source=&pattern=&node=`（权限 `logs:read`）返回一条可直接保存的阈值规则模板——指标名由服务端用 `model.LogPatternMetricName` 拼（`<来源>_log_<模式>_total`，与 Agent 产出**逐字同源**），来源名/模式名非法即 400。前端在检索结果行的「命中模式」列提供「建规则」（需 `alerts:write`），跳到告警页由那边取模板并打开表单。**不新增规则类型、不改告警引擎**（仍是既有日志指标 + `RuleTypeThreshold`） | P1 | M | 日志字段化（已完成） | D3 |
| 结构化解析（JSON/正则字段提取、标签注入） | **已实现** | **2026-10-06 落地**：`internal/server/logparse`（JSON 对象顶层标量 + 一层嵌套用点号连接；无结构日志退到 `key=value` 扫描；凭据类键名不进字段表；单行字段数/值长度封顶）。**解析在 Server 侧**：Agent 已带命中模式名，放 Agent 就必须重分发全部 Agent，放 Server 则现有 Agent 无需升级。字段与原文写在**同一条 JSON**里（`model.LogHit.fields`），因此不需要另建索引，检索仍是顺序读 + 有界扫描；字段名每来源上限 128（`MaxFieldNamesPerSource`），目录落盘 `field_names.json` 供界面做候选 | **P0** | M | — | D3 |
| 字段检索 / 全文索引 | 部分实现 | **字段过滤已落地（2026-10-06）**：`GET /api/v1/logs?field=key:value`（可重复，多条件为「与」，精确等值）在扫描时逐行判断，**仍是有界扫描而不是索引**；字段名候选 `GET /api/v1/logs/fields`。**未做**：全文倒排索引、百万行级字段筛选的性能保证（9-07 原文要求的"百万行级 + 索引一致性"需独立立项） | P1 | L | 结构化解析（已完成） | D3 |
| 外部日志后端（默认增强自研 + 可选 VictoriaLogs） | **已实现** | **2026-10-06 落地（ADR-0002 定稿为 accepted）**：接口 `logstore.LogStore`（`Append`/`Query`/`Backend`/`Sources`/`FieldNames`）与**第二个适配器同时落地**——默认自研分片落盘（`logstore.Store`），可选 `logstore.VictoriaLogs`（写入 `/insert/jsonline`、检索 `/select/logsql/query`、元数据 `field_names`/`stream_field_values`，按官方文档实现）。配置 `logBackend: local|victorialogs` + `logVictoriaLogs.addr`；**契约用例同一套断言跑两个适配器**（`backend_contract_test.go`）。**离线交付配套**：`build/fetch-packages.sh` 纳入 VL（三架构）、新增 `deploy/install-logs.sh`（二进制 + systemd + 保留期 + 双健康检查）、`deploy/install-server.sh --log-backend/--log-addr` 写入 `server.yaml`、`deploy/uninstall.sh --logs`、`install.sh logs` 子命令。**已于 2026-10-06 在真实 VictoriaLogs v1.53.0 上完成八项联调**（写入/时间/分页/方言/元数据/容量/部署/故障演练全过；发现"写入到可检索有秒级延迟"并据此补文档）。**边界**：外部后端不提供逐文件扫描诊断（三项诊断恒为 0）、单来源每日上限不适用（容量归后端）、分页按 `limit+offset` 而非文件偏移 | P2 | L | 后端抽象（已完成） | D3 |

### 3.10 链路追踪

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| Trace/Span 采集与存储 | 未实现 | 全仓 `trace\|span\|otel\|jaeger\|skywalking` 无实现 | P3 | L | 应用侧探针或 eBPF | — |
| 服务调用拓扑与延迟分解 | 未实现 | — | P3 | L | 同上 | — |
| 追踪↔告警关联（慢调用告警） | 未实现 | — | P3 | M | 同上 | — |

> 结论：**首批不做**。理由与未来路线（OTel 网关 / eBPF 零侵入）见 D1 与 D3；开源对比见调研报告 §2.3、§5。

### 3.11 巡检合规

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 巡检报告（日/周/月 HTML + 资源趋势 + SLA 可用性） | 已实现 | `internal/server/report/report.go`、`internal/server/api/middleware_api.go:handleReportGenerate/Download/History` | — | — | — | — |
| 安全基线评分（0-100 合规清单） | 已实现 | `internal/agent/collector/security.go`（`mkBaselineItem` 等）、`internal/server/security/store.go:Ingest/Baselines/Summary` | — | — | — | — |
| 文件完整性监测（FIM，文件哈希基线比对） | 已实现 | `internal/agent/collector/security.go:loadFIMBaseline/saveFIMBaseline/sha256File` | — | — | — | — |
| 配置项级差异巡检（配置快照 vs 合规模板） | 已实现 | 字段级 diff 与「期望值（标杆）」比对均已落地（见 §3.2「配置快照与差异巡检」）；期望值当前来自人工指定的**标杆资产**，尚未接入安全基线清单自动生成期望值 | **P0** | M | 资产与配置（D2） | D2 |
| 巡检周期化调度（自动生成报告） | **已实现** | **2026-10-06 落地**：`internal/server/report/schedule.go` 的 `Scheduler`（配置 + 运行状态同文件 `report_schedule.yaml`，可关/可改周期/可立即执行/上次结果可见/失败显式上报），接口 `GET/PUT /api/v1/report/schedule` + `POST /api/v1/report/schedule/run`（读 `report:read`，改与生成 `report:export`）；前端在报告页有「周期化生成」卡片。**默认关闭**；启动时**不**立即生成（报告要拉一整个周期数据，重启就生成会在频繁重启的机器上写满磁盘）。**通用定时任务仍未做且刻意不做**：平台里需要定时的只有报告与数据保留，为两条用例引入 cron 解析 + 任务注册表只增加维护面（见 schedule.go 的说明） | P2 | M | 定时计划任务（按需再评估） | — |
| 合规报告/合规矩阵导出 | 部分实现 | HTML 报告可下载；无合规矩阵 CSV/PDF 导出 | P2 | S | — | — |

### 3.12 报表大屏

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 数据大屏（`/screen`，主机/中间件/Nginx 三板块） | 已实现 | `internal/server/api/screen.go:handleScreenGet/Put`、`web/src/components/screen/ScreenView.vue` | — | — | — | — |
| 自定义仪表盘（面板编排 + 持久化） | 已实现 | `internal/server/api/dashboard_api.go`（`/api/v1/dashboards`）、`web/src/components/dashboard/DashboardView.vue` | — | — | — | — |
| 巡检报告页面（生成/下载/历史） | 已实现 | `web/src/components/ReportView.vue` | — | — | — | — |
| 指标 CSV 导出（≤7 天） | 已实现 | `internal/server/api/export.go:handleMetricsExport` | — | — | — | — |
| 审计导出 | 已实现 | `internal/server/api/audit.go:handleAuditExport` | — | — | — | — |
| 公开状态页（免登录） | 已实现 | `web/src/components/StatusView.vue`、`internal/server/api/status*` | — | — | — | — |
| 资产/容器/日志的报表化视图 | 未实现 | 依赖 D2/D3 数据源 | P2 | M | D2/D3 | D1 |
| 移动端 / 小程序（**暂不考虑**）、多语言 i18n | 未实现 | `README.md` 路线图（2026-09-30 评审：移动端不纳入，i18n 降 P3） | P3 | L | — | — |

### 3.13 权限审计

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| RBAC（用户/角色/权限点目录） | 已实现 | `internal/server/auth/model.go:PermissionCatalog()`；包装器 `internal/server/api/auth.go:permit/authz` | — | — | — | — |
| 资源范围（节点分组，**服务端强校验**） | 已实现 | `internal/server/api/auth.go:permitNode/permitHostname/checkNodeScope`、`internal/server/auth/policy.go:FilterGroups/FilterByGroup` | — | — | — | — |
| 高风险操作标记 | 已实现 | `internal/server/auth/policy.go:HighRiskPermissions` | — | — | — | — |
| 审计（管理写请求 / 登录 / 权限拒绝） | 已实现 | `internal/server/api/audit.go:AuditMiddleware/RecordLoginAudit/RecordPermissionDenied`、`internal/server/audit/store.go` | — | — | — | — |
| 前端 RBAC 界面 | 已实现 | `web/src/components/rbac/`、`internal/server/api/{users,roles,permissions}` | — | — | — | — |
| **资产的资源范围扩展**（资产分组/业务范围） | **已实现** | **2026-10-07 落地（P0 最后一项）**：资源范围增加**业务标签维度**（`auth.Scope.AssetMode` + `AssetLabels`，与节点分组维度**取交集**）。三态与节点维度**逐字同构**：空/`all` = 该维度不生效（存量账号行为逐字不变）、`limited` + 取值 = 按标签收窄、`limited` + 空 = **无权限**（绝不放大）。只认一个约定键（默认 `biz`，`users.yaml` 的 `scope_label_key` 可改）。**服务端强制**（不退化到前端隐藏）：`assetWhere` 下推 `LabelSelectors`（列表/计数/导出/摘要/巡检共用同一套条件）+ 十余处资产判定统一走 `assetVisible`（两维一起判）+ 巡检差异项按 `asset_id` 批量取标签补判。**配置入口**：角色/用户表单的「业务范围」（约定键由 `/auth/me` 下发、候选值 `GET /assets/label-values` 按可见节点收窄）。**顺带关掉一条提权路径**：四条写路径（创建/更新 用户与角色）统一过 `auth.ScopeCovers`——此前只有「创建自定义角色」一条有约束。设计件 `../../superpowers/specs/2026-10-07-asset-business-scope-design.md` | **P0** | M | 资产台账（D2） | D2 |
| 权限点与资源范围持续接入（新增接口同步纳管） | 部分实现（持续项） | `README.md` 路线图「P0 持续」；现有权限点前缀域：`dashboard/nodes/groups/alerts/notify/silence/probe/report/metrics/middleware/security/agent/system/audit/users/roles/logs` | **P0** | S/次 | — | D1 |
| SSO / LDAP / OIDC | 未实现 | `README.md` 路线图 P2 | P3 | M | — | — |

### 3.14 开放集成

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| REST API（约 140 条 `/api/v1/*`） | 已实现 | `internal/server/api/query.go:RegisterRoutes` | — | — | — | — |
| WebSocket 实时推送 | 已实现 | `internal/server/api/ws.go:RegisterWS`（`GET /ws`） | — | — | — | — |
| 出站通知（Webhook / IM 机器人） | 已实现 | `internal/server/alert/notifier.go:WebhookNotifier`、`internal/server/config/config.go:WebhookConfig` | — | — | — | — |
| Agent 二进制与安装脚本分发（内置 CDN） | 已实现 | `internal/server/agentdist/agentdist.go:Distributor.Register`（`/install/agent-install.sh`、`/bin/`） | — | — | — | — |
| OpenAPI / Swagger 文档 | 未实现 | 无生成物、无注解 | P2 | M | — | D1 |
| 第三方平台对接（ITSM / CMDB / PagerDuty） | 未实现 | — | P3 | M | 工单/资产 | — |
| 数据出口（外部数据平台 / 消息队列订阅） | 未实现 | — | P3 | L | — | — |

## 四、与 `README.md` §路线图 的对账表

> README 现有 21 条未实现项，逐条回收，**不出现两套并行说法**。

| README 路线图条目 | README 档位 | 本表归属领域 | 本表优先级 | 处置 |
|---|---|---|---|---|
| 自动化处置与自愈（D3） | P1 | 自动化执行 / 配置管理 | **暂缓** | **用户 2026-09-26 明确决定暂缓，仅在明确要求时启动** —— 它是「暂缓」不是「待办」，**不要主动推进或反复建议**；依赖任务下发通道与护栏（D1）作为前置 |
| 日志分析增强 | P1 | 日志 | **P0/P1** | **升级**：结构化解析入 P0（D3），字段索引/检索页建规则入 P1 |
| 中间件容量预测 | P2 | 监控告警 | P2 | 保留 |
| 依赖拓扑与影响传播 | P2 | 资产 / CMDB | **P0** | **升级**：并入「资产关系与拓扑」（D2） |
| 批量命令 / 脚本执行 | P1 | 自动化执行 | P1 | 保留；前置为任务下发通道（D1） |
| 定时计划任务 | P2 | 自动化执行 / 巡检合规 | P2 | 保留；`README` 中「自动生成报告」一并归此 |
| 配置下发与修复动作 | P2 | 配置管理 | P2 | 保留；前置为资产台账 + 下行通道 |
| 权限点与资源范围的持续接入 | P0 持续 | 权限审计 | P0 | 保留；扩展为「资产范围」纳管（D2） |
| Server 高可用（A1） | P1 | 平台治理 | **暂缓** | **用户 2026-09-26 明确决定「先不做」** —— 设计件与盘点结论（8 类单实例假设、方案取舍、6 子批次、5 个决策点）已存于 `docs/a1-server-ha.md`，将来评审即可实施；**不要主动推进或反复建议** |
| 代理增强（磁盘缓冲/批量合并/主备） | P1 | 平台治理 | P1 | **部分实现（2026-10-04）**：**磁盘缓冲已落地**——`internal/agent/reporter/spool.go`（JSONL + 确认偏移；送出成功才推进偏移，故崩溃只会产生重复点而时序库幂等）+ `agent.yaml` 的 `spoolFile` / `spoolMaxMB`（0=关闭）。**只缓冲时序点**（HostInfo/实例清单是指令回执补传旧值反而有害），补传**并入下一次正常上报**故 Server 零改动；单轮补传上限 512 KiB（防"补传把上报体顶爆→永远失败→缓冲排不空"）。dev-server 实测：断网 150 秒缓冲 700 KB / 0 丢弃；断网 7 小时后恢复，时序库里确实出现了时间戳落在断网窗口内的点。**批量合并与主备未做** |
| SSO / LDAP / OIDC | P2 | 权限审计 | P3 | 降档（已确认 2026-09-30） |
| 单设备会话与审批流 | P2 | 工单流程 / PAM | P2 | 保留；拆为「审批流」与「会话审计」两项 |
| 时序数据生命周期 | P2 | 平台治理 | P2 | 保留（依赖时序库能力） |
| 更多中间件 | P2 | 监控告警 | P2 | 保留 |
| 日志采集增强 | P1 | 日志 | P1 | 保留（结构化解析部分并入 D3 的 P0） |
| Windows 节点支持 | P2 | 监控告警 | P3 | 降档（已确认 2026-09-30） |
| 值班排班（On-Call） | P2 | 监控告警 | P3 | 降档（已确认 2026-09-30） |
| 电话/短信与外部平台集成 | P2 | 开放集成 | P3 | 降档（已确认 2026-09-30） |
| 数据大屏未实现项（sparkline/磁盘 await） | P2 | 报表大屏 | P2 | 保留 |
| 移动端 / 微信小程序 | P2 | 报表大屏 | **不入优先级** | **暂不考虑（已确认）** |
| 多语言（i18n） | P2 | 报表大屏 | P3 | 降档（已确认 2026-09-30） |
| **CMDB（资产台账、生命周期与变更记录）** | P2 | 资产 / CMDB | **P0** | **升级为本次首批**（D2） |
| 服务目录与 SLA | P2 | 工单流程 | P3 | 降档（已确认 2026-09-30） |

**本次新增（已同步写入 `README.md` §路线图）**：README 新增两条主线分组「资产与配置（P0 · 首批）」与「容器与可观测（P0 · 首批）」，共 12 个条目，**逐条标注来源设计件**（`2026-09-30-asset-cmdb-design.md` / `2026-09-30-container-observability-design.md` / `2026-09-30-ops-platform-evolution-design.md`）。同时把 README 中 4 个既有条目（「CMDB（P2）」「依赖拓扑与影响传播（P2）」「日志分析增强（P1）」「配置下发与修复动作（P2）」）加注「已升级/已并入/前置」，避免同一功能出现两套说法。

> 本表中**尚未写入 README** 的新增项：OpenAPI/Swagger 文档（P2，归 D1）——待评审确认后再落 README，避免与现有条目交叉。

## 五、首批范围与后续排序建议

**P0（本次首批，已确认）**

1. **资产与配置**（D2）：资产模型与台账 → 自动发现 → 关系/拓扑 → 变更历史 → 资产范围纳管 → 配置快照与差异巡检
2. **容器与可观测**（D3）：下行通道与护栏（D1 前置） → 只读管理面（工作负载/事件/日志） → 日志结构化解析与字段检索 → 日志后端抽象（可选 VictoriaLogs）
3. **横向前置**（D1）：下行操作通道（复用既有下发先例）、权限点与资源范围纳管、`LogStore` 后端抽象

**建议的 P1 候选（供排期）**：告警 ↔ 资产/变更关联、容器↔日志↔资产联动、批量命令/脚本执行（依赖 D1 通道）、日志字段索引与"由日志建规则"、巡检周期化。

**建议的 P2/P3**：见上表；其中**链路追踪明确 P3**（需应用侧侵入，与内网离线 + 轻量定位冲突，路线见 D1/D3）。

## 六、取证与复核方式

- **代码为准**：所有「已实现/部分实现」行均给出文件路径 + 符号；复核可直接跳转核对。
- **本次已发现并需修订的文档滞后项**（1 项，已列为待办）：
  - **采集项模板（Collector Template）与取数护栏（templateGuards）**：功能已在 `1e25239 refactor!: 移除采集项模板功能全链路` 移除，代码中 `internal/template` 不存在、`/api/v1/middleware/templates*` 路由未注册；但 `CONTEXT.md` 仍以现役术语描述（**7 处**），`docs/c1-collector-templates.md`、`docs/refactor-plan.md`、`docs/permission-matrix.md` 亦仍按已交付描述。处置：在术语与路线图同步时修订（见计划最后一步）。
- **未纳入本次对照的文档**：`docs/refactor-plan.md`（109 KB，历史重构计划）、`docs/system-audit/`、`docs/testing/` 作为线索使用，不作为状态依据。
- **配置模板手册**：`docs/configuration-cookbook.md`（2026-09-30 新增）——告警事件管道与集中日志的**可复制模板**（页面亦内置「示例模板 / 配置示例」入口）。本表只统计"功能有没有"，"怎么配"集中在手册里，避免两处重复维护。
- **复核清单**：①每行依据可跳转命中；②对账表覆盖 README 全部 21 条路线图项；③新增项均标注来源设计件；④P2→P3 的降档建议经确认后再写入 `README.md`。
