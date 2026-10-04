# 容器与工作负载清单上报详细设计

> 目标：把全景表里「资产自动发现（…/容器清单）」这行从**文档说已实现**变成**真的落进台账**，
> 并解开 `member_of` 的阻塞（它按设计定在 Pod → 工作负载上，而工作负载资产此前不存在）。

## 意图与边界

**要做的**：K8s 的 Pod 与工作负载由 Agent **随上报周期上报清单**，Server 落成台账资产，
并建立 `runs_on`（Pod → 主机）与 `member_of`（Pod → 工作负载）两条边。

**不做的（各自的理由）**：

| 不做 | 理由 |
|---|---|
| Pod 日志拉取、exec 终端 | 用户已明确「后面再考虑」，本批不碰 |
| 拓扑视图 | 本批的产出正是它的前置数据；数据先真实存在，再谈画图 |
| Docker 容器迁移到新类型 | 它们**已经是资产**（见 §已知不一致），迁移只有命名收益 |
| Pod → 集群 的 `member_of` | 见 §关系设计 |
| 镜像/配置安全扫描 | P3，独立评估项 |

## 现状与依据

| # | 事实 | 证据 |
|---|---|---|
| 1 | Docker 容器**已经是资产**：`middleware-instance`，自然键 `docker:<容器ID前12位>`，且已建 `runs_on` 到上报主机 | `internal/server/receiver/assets.go:150-153,81-83` |
| 2 | K8s Pod/工作负载**只有指标、没有清单**：两个采集函数都只返回 `[]model.Metric` | `internal/agent/collector/k8s.go:186-247`（workloads）、`:249-299`（pods） |
| 3 | **`spec.nodeName` 当前根本没被读**（`k8sPodList` 只取 metadata 与 `status.phase`/`containerStatuses`）→ Pod→Node 的前提要新加 | `internal/agent/collector/k8s.go:709-717,687-695` |
| 4 | 集群**已经是资产**：`middleware-instance`，自然键 `kubernetes:<apiserver 地址>`；`K8sInstance.Instance` 是唯一键、`Name` 是可变别名 | `assets.go:158-161`、`internal/model/metric.go:556-564` |
| 5 | 上报体里容器类只有 `K8sInstances` 与 `DockerInstances` 两个清单，**没有 Pod/工作负载字段** | `internal/model/metric.go:115-129` |
| 6 | 资产类型只有 `host` 与 `middleware-instance` | `internal/server/asset/model.go:75-78,91-96` |
| 7 | 天然键形态 `<集群>/<命名空间>/<kind>/<名称>` 在注释里**已预留**（未落地） | `internal/server/asset/model.go:391-392` |
| 8 | 类型播种是**自愈**的：每次启动 `INSERT … ON CONFLICT(key) DO UPDATE SET title` | `internal/server/asset/store.go:236-246` |
| 9 | 「最近上报」与派生状态已存在：`asset_seen` + `markSeen` + `lastSeenExpr` + `online/missing/archived` | `store.go:357-366,521-530,577-584` |
| 10 | 上报体上限 ≈ **16 MiB**（`4 × logBodyLimit()`，后者默认 4 MiB），超限 413 | `internal/server/receiver/receiver.go:154` |
| 11 | 资源范围只认 `Asset.Node` → 节点分组；`Node` 空串 = 受限用户不可见 | `internal/server/api/query.go:519-537` |
| 12 | 既有约定：容器/工作负载 → upsert 资产 + `runs_on`(Pod→Node) + `member_of`(Pod→工作负载) | `2026-09-30-asset-cmdb-design.md:85` |

**一个额外结论**：资产设计件 §自动发现第 3 条引用的「容器详设中新增的**清单上报**」**在容器详设里不存在**——
`2026-09-30-container-observability-design.md` 写的是只读管理面（下行查询）与日志聚合，全文无清单上报的字段设计。
因此本设计件不是"实现一个已写好的规格"，而是**补写这个规格**。

## 数据通路

在 `ReportPayload`（`internal/model/metric.go`）新增两个平铺字段：

| 字段 | tag | 说明 |
|---|---|---|
| `K8sPods []K8sPod` | `k8sPods,omitempty` | 每个 Pod 一条，**自带所属集群** |
| `K8sWorkloads []K8sWorkload` | `k8sWorkloads,omitempty` | Deployment / StatefulSet / DaemonSet |
| `K8sPodsTruncated` / `K8sWorkloadsTruncated` | `k8sPodsTruncated,omitempty` | 是否因上限被截断（见下） |

```go
// K8sPod 是一个 Pod 的台账投影。刻意不含容器内环境变量、挂载、Secret 引用等
// 与"资产"无关的字段——清单上报是发现，不是把 apiserver 对象整个搬到中心。
type K8sPod struct {
    Cluster   string `json:"cluster"`   // 所属集群 = K8sInstance.Instance（apiserver 地址）
    Namespace string `json:"namespace"`
    Name      string `json:"name"`
    Node      string `json:"node,omitempty"`   // spec.nodeName（可能为空：未调度）
    Phase     string `json:"phase,omitempty"`  // Running / Pending / …
    Ready     int    `json:"ready"`
    Total     int    `json:"total"`
    Restarts  int    `json:"restarts"`
    Image     string `json:"image,omitempty"`  // 首个容器镜像（展示用）
    StartedAt int64  `json:"startedAt,omitempty"`
}

// K8sWorkload 是一个工作负载的台账投影；Kind 取 deployment/statefulset/daemonset。
type K8sWorkload struct {
    Cluster   string `json:"cluster"`
    Namespace string `json:"namespace"`
    Kind      string `json:"kind"`
    Name      string `json:"name"`
    Desired   int    `json:"desired"`
    Ready     int    `json:"ready"`
    Image     string `json:"image,omitempty"`
}
```

**为什么平铺、且每条自带 `Cluster`**：上报体里已有的清单全是平铺（`DockerInstances` 每条自带 `Node`/`Group`），
服务端落库与"截断计数"都简单；做成"集群 → Pod 列表"的嵌套，会让采集侧为每个集群重建一层容器，
而服务端仍要把它摊平。逐条带 `Cluster` 也让"多集群、单 Agent"的形态自然成立。

**上限与截断**：单轮 `K8sPods` ≤ 1000、`K8sWorkloads` ≤ 500。上限的理由不是 16 MiB（1000 条远够不上），
而是**每轮上报都是每 Agent 一次的固定开销**，异常大的集群不应把每轮上报体持续顶大。
截断必须**显式回传**（沿用 `container.describe` / 日志检索既有的"截断显式回传"约定）：
上报方知道"我看到 3000 个、报了 1000 个"，服务端与界面才不会把它当成"集群里只有 1000 个"。

**命名空间**：默认**排除** `kube-system` / `kube-public` / `kube-node-lease`（系统命名空间对象数量大、
几乎不由人运维），配置可覆盖为"全量"。不做白名单式（"只报某几个命名空间"）——发现语义默认全量更安全，
白名单会让新命名空间静默不出现。

## 资产模型

两个新类型（`asset/model.go` 常量 + `BuiltinTypes()` 追加）：

| 类型键 | 标题 | 承载 | 自然键 |
|---|---|---|---|
| `pod` | 容器（Pod） | K8s Pod | `<集群>/<命名空间>/pod/<名称>` |
| `workload` | 工作负载 | Deployment / StatefulSet / DaemonSet | `<集群>/<命名空间>/<kind>/<名称>` |

- `<集群>` 取 `K8sInstance.Instance`（apiserver 地址）而不是 `Name`：`Instance` 是唯一键，`Name` 是可变别名。
  别名作为属性展示（`clusterName`）。
- **逐字沿用注释里预留的形态**（`model.go:391-392`），不发明第二种。
- 类型键用 `pod` 而不是 `container`：**Pod 不是容器**（一个 Pod 可有多个容器），
  而"容器"这个词在本平台已经指 Docker 容器（它们以 `middleware-instance` 落在台账里）。
  用一个词指两个东西，以后一定会有人按"container 类型"去找 Docker 容器而找不到。
- `Asset.Name` = Pod / 工作负载名；界面按 `<命名空间>/<名称>` 组合展示（命名空间放属性，不塞进 `Name`——
  `Name` 是自由文本、不参与匹配）。
- 属性（全部 `discovery` 来源）：`namespace`、`kind`、`status`、`ready`/`total`（或 `desired`）、`restarts`、
  `image`、`k8sNode`、`clusterName`、`startedAt`。属性层是自由 `map[string]string`，无需白名单（`model.go:373-381`）。

## 关系设计

| 边 | 何时建 | 端点 |
|---|---|---|
| `runs_on` | `spec.nodeName` 能对应到一个**已存在的主机资产** | Pod → 主机 |
| `member_of` | 同集群、同命名空间，且**工作负载资产已存在** | Pod → 工作负载 |

- 复用 `LinkDiscovered`：它要求**两端资产都已存在**（`internal/server/asset/service.go:365-378,430-446`），
  不满足就跳过——**不报错、不造占位资产**。Agent 未覆盖某节点时，Pod 的 `runs_on` 自然不出现；
  这是诚实的表达（我们不知道那台机器），好过凭空建一个主机资产。
- **落库顺序是硬约束**：必须先落**工作负载**，再落 Pod 与两条边。否则 `member_of` 的对端还不存在，
  整批边建不出来。实现里要显式保证这个顺序（这不是优化，是正确性）。
- **不建 Pod → 集群 的 `member_of`**：本平台 `member_of` 的语义是"实例属于集群"，同一个 Pod 同时指向
  工作负载与集群，会变成两条含义不同的归属边，图上读成两套体系。集群维度由自然键前缀表达，
  且 Pod 的 `runs_on` 已经把它钉在一台真实机器上。

## 资源范围（需拍板）

背景：`nodeInScope` 只读 `Asset.Node`（`api/query.go:519-537`）；`Node` 为空 = 受限用户一律不可见。

| 方案 | Pod 的 `Node` | 工作负载的 `Node` | 后果 |
|---|---|---|---|
| **A（建议）** | `spec.nodeName`（它**实际所在的节点**） | 该集群的**上报主机**（与集群资产同口径） | Pod 归"它跑在哪台机器"——范围是"你能管的机器"，这是最不容易越权的口径；对不上已注册节点时落空串（仅全局可见，详情标「节点未注册」），不假装它属于某个分组 |
| B | 同 A | 空串 | 最保守，但受限用户**永远看不到工作负载**，且 `member_of` 的对端不可见会被剔除（`asset_api.go:394`）——体验是"我的 Pod 没有归属" |
| C | 都取该集群的上报主机 | 同左 | 口径一致，但 Pod 的范围会跟着集群锚点漂移（跑在 B 节点的 Pod 对只管辖 A 的人可见） |

**方案 A 不重复上一批拒绝「中间件集群资产」的错误**。当时拒绝的理由是**集群资产没有归属点**——
中间件的 `group` 只是实例上的一个属性，没有谁"上报了一个集群"。K8s 集群不同：`K8sInstances` 是某个 Agent
真实上报的，集群资产**今天就已经**按那个上报主机归属（`assets.go:158-161` 建 `runs_on` + `nodeInScope`）。
本批只是沿用既有口径，没有放宽任何既有规则。

**Pod 的对不上情形要有明确退化**：`spec.nodeName` 与已注册主机同名是同名约定（Agent 装在 K8s 节点上时成立）；
若 Agent 装在集群外，或该节点没装 Agent，则 `Node` 落空串 → 仅全局范围可见。这是"我们无法归属"的诚实表达，
比"随便挂到一个分组"安全。

## 高 churn 与治理数字（需拍板）

Pod 天生短命（滚动更新、Job、扩容缩容），而台账的语义是"长期存在、需要有人负责的配置项"。三处会被冲垮：

1. **摘要「总数」**：12 → 512，数字失去意义；
2. **摘要「失联」**：被替换掉的旧 Pod 停止上报，超过阈值后**全部计入失联**——页面顶部会出现"失联 200"，
   把真实故障埋掉；
3. **列表默认视图**：Pod 会淹没主机与中间件实例。

**建议：容器与工作负载默认不参与台账首页的默认视图与健康度计数**（显式按类型筛选时才出现）。
理由：治理数字（总数 / 失联 / 无责任人）对短命运行时对象没有意义——没人会为滚动更新掉的 Pod 指派责任人；
把它们混进同一套数字，等于让治理指标同时承担"资产普查"与"运行时快照"两种口径。
容器/工作负载仍有明确入口：资产列表按类型筛选，或从容器页跳转。

**不新造机制**：`asset_seen` + 派生状态（`store.go:521-530,577-584`）已经能回答"这个 Pod 什么时候消失的"，
且**不物理删除**资产——沿用上一批对采集资产的同一个判断（"用忽略而不是删除"）。

备选：给 Pod 单独一个更短的失联阈值；或完全不特殊处理（现状语义）。

## 权限与前端

- **权限点不新增**：容器/工作负载资产属于台账 → 复用 `assets:read` / `assets:write`（与主机、中间件实例一致）。
  容器管理面既有的 `container:read`（`api/query.go:310`）保持只读管理面用途，两者不叠加、互不放宽。
- **接口不新增**：资产列表 / 详情 / 关系 / 变更历史都是类型无关的（`asset_api.go`）。
- 前端改动很小：`AssetListView.vue` 的类型标签映射补两项；类型筛选项来自 `/api/v1/asset-types`（无需改接口）；
  「消失」沿用既有状态筛选。**不新增一级菜单**（沿用容器详设的既定决定）。

## 兼容与迁移

- **无表结构变更 → 不升 `schemaVersion`**。新增类型靠 `BuiltinTypes()` 的自愈播种（`store.go:236-246`）。
  这与上一批补 `asset_links.source` **必须**升版本 + 显式 `ALTER` 是两回事：类型是**数据行**，不是表结构。
- 存量库：启动时自动补两行类型，无人工动作；`assets.type_key` 的外键因此始终满足。
- ⚠️ **回滚的真实限制**：升上去后再滚回旧版本，旧版本**不认识**这两个类型键——资产仍会出现（类型标题来自 DB），
  但旧版前端的类型标签映射没有它们。要在发布说明里写清，不要写成"完全兼容"。

## 已知不一致（记录，本批不解决）

Docker 容器**已经是** `middleware-instance` 资产（`docker:<ID>`），而 K8s Pod 落到新的 `pod` 类型：
同一个"容器"概念两种落法。本批**不迁移**：Docker 容器已可用（有资产、有 `runs_on`），
迁移要改自然键 + 搬边 + 保证幂等，收益只有"命名整齐"。记为后续项，并入拓扑视图那批时再评估。

## 实施批次

| 子批 | 内容 |
|---|---|
| f1 | 协议与模型：`K8sPod`/`K8sWorkload`、`ReportPayload` 新字段与截断标记、`BuiltinTypes()` 两个新类型、自然键约定 |
| f2 | Agent 采集：`collectPods` 补读 `spec.nodeName` 并产出清单；`collectWorkloads` 产出清单；命名空间排除；配置项 |
| f3 | Server 落台账：**先工作负载、后 Pod** 的 upsert + `runs_on` + `member_of` |
| f4 | 范围与治理数字：`Node` 归属口径、摘要与默认视图排除 |
| f5 | 前端：类型标签与筛选 |
| f6 | 文档对账（含修正全景表 §资产自动发现那行与设计件 §未做第 1 条）+ 打包 |

## 验证方式

- **单测**：协议字段与截断标记；`applyAssets` 的 upsert 幂等；两条边的建立顺序（含"工作负载资产不存在时
  `member_of` 不建且不报错"）；范围三态（节点已注册 / 未注册 / 全局）；摘要排除（Pod 不计入失联）。
- **实机（dev-server）**：台账出现工作负载与 Pod；关系 Tab 能看到 `member_of`(Pod→工作负载) 与
  `runs_on`(Pod→主机)；删掉一个 Pod 后它转为「消失」但**不物理消失**；健康度数字不被 Pod 冲垮。

## 未做（显式清单）

1. Pod 日志拉取与 exec 终端（用户已明确「后面再考虑」）。
2. 拓扑视图——本批只把数据变成真实存在。
3. Docker 容器向 `pod` 类型的迁移（见 §已知不一致）。
4. Pod → 集群 的 `member_of`。
5. 容器资产的责任人 / 批量操作 / 导出；镜像与配置扫描（P3）。
6. 消失资产的**物理清理**策略：沿用"采集不删除"，表增长风险在 §高 churn 中记录，处理方式待定。

## 实施记录（按批次追加）

### 批次 17（2026-10-04）：K8s 容器 / 工作负载清单上报（1.30.28）

按 f1~f5 落地上报链路、资产模型、两条边、范围与治理数字、前端。

**一处设计外的必补项：Pod 的属主链**。设计时把 `member_of` 当作"Pod → 工作负载"直接建，
实现时才发现 **Pod 的直接属主是 ReplicaSet，不是 Deployment**（一切正常的话：
Deployment → ReplicaSet → Pod），而台账里的"工作负载"是 Deployment。少了这一跳，
`member_of` 在最常见的 Deployment 场景下**一条都建不出来**——那正是绝大多数线上工作负载。
因此协议里补了 `ownerKind` / `ownerName`，由**采集侧**解析这一跳
（`k8s.go:replicaSetOwners` 读一次 ReplicaSets 得到 `ns/rs → deployment` 映射，`podOwner` 查表）。
读不到映射时**返回空**而不是按名字前缀猜（`web-7d9f-x` 猜成 `web`）：猜错就是假数据，比缺失更难发现。
Job 建的 Pod 属主是 Job，本批不给 Job 建工作负载资产，属主如实留空。

**其余在实现中定下、值得记下的点**：

| 决定 | 理由 |
|---|---|
| 协议里补 `status`（有效状态）而不只有 `phase` | phase 不反映容器起不起得来（镜像拉不动时仍是 Pending、崩溃重启时仍是 Running）。判定复用采集侧同一处 `podStatus`，台账与指标页不会给出互相矛盾的结论 |
| `Ready` 的分子取 `spec.containers` 条数算总数、`containerStatuses` 算就绪数 | Pod 还没起来时 `containerStatuses` 是空的，用它当总数会显示"0/0"，看起来像这个 Pod 没有容器 |
| 上限按**整份上报**计（跨集群累计后裁剪），不是按集群 | 字段名就是 report 级的；多集群 Agent 不该因为集群多而把上报体线性顶大。截断显式置位 |
| exporter 模式（kube-state-metrics）**不产出清单** | 那份文本里没有对象级身份、也区分不出"对象已被删除"，拿它编资产等于把指标当真相。清单只走直连 |
| 关键词搜索时**不排除**容器/工作负载 | 关键词是一次定点查找（"拿 IP / 业务名 / 资产编号找资产"）。按类型排除是防"列表被淹没"，而搜索返回空会让人以为台账里没有这个对象——比列表里混进几个 Pod 糟得多。显式按类型筛选同理不排除 |
| `member_of` 的对端只在本轮清单里存在时才建边 | 上报可能被截断、工作负载也可能落在被排除的系统命名空间。这类情况不是错误，不该刷日志；已建成的边是持久的 |
| 前端类型标签对 `pod`/`workload` **显式硬编码** | 它们不能走"按自然键前缀猜产品名"那条路：自然键是 `<集群>/<命名空间>/<kind>/<名称>`，按 `:` 切会切出 apiserver 的 scheme（`https`） |
| `KIND_LABELS.member_of` 从「member_of 集群」改为「member_of 归属」 | `member_of` 的落点从"归属集群"变成"归属工作负载"，旧的标签会把 Pod → Deployment 这条边说成集群关系 |

**测试**：`internal/agent/collector/k8s_inventory_test.go`（假 apiserver 端到端：
真的从 JSON 里取到 `spec.nodeName`、经 ReplicaSet 解析到 Deployment、系统命名空间被排除但指标口径不变、
1001 个 Pod 时截断到上限并置位；另有 `podOwner` 的逐分支与 `inventoryWanted` 开关）；
`receiver/assets_test.go:TestHandleReportWritesContainerInventory`（工作负载与 Pod 落台账、
两条边的来源都是 discovery、重复上报幂等、**未注册节点上的 Pod 归属为空且不建 runs_on**）；
`asset/service_test.go:TestExcludeTypesKeepsEphemeralOutOfListAndStats`（列表/计数/摘要三者同源地排除）；
`api/asset_api_test.go:TestHandleAssetsHidesEphemeralTypesByDefault`（默认排除 / 显式类型可见 / 关键词可见）。
`go test ./...` 全绿；`npm test` 39/39；`npm run build` 通过。

**尚未在 dev-server 做实机验证**（需要有 K8s 集群接入的节点：本项目 dev-server 上是否已配 `k8sInstances`
未确认，容器只读管理面那批也留下过同样的"需一台配了 k8sInstances 的节点"的验证欠账）。

**未做（明确记录）**：

1. 设计件 §未做清单的六条全部维持（Pod 日志 / exec 终端、拓扑视图、Docker 容器迁移、
   Pod → 集群的 `member_of`、批量与导出、消失资产的物理清理）。
2. **`member_of` 的"反向"未做**：不建工作负载 → 集群的边、也不建 Pod → 集群的边（见 §关系设计）。
3. **容器资产的既有能力未跟进**：责任人指派、标签、批量操作、导出沿用既有接口即可用，
   但**没有针对高 churn 的专门处理**（例如"按工作负载批量忽略某次滚动更新产生的旧 Pod"）。
