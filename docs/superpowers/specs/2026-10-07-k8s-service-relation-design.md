# K8s `service → pod` 关系（补采集 Service / EndpointSlice）设计件

- 批次：25
- 日期：2026-10-07
- 对应全景表：`2026-09-30-ops-platform-capability-map.md` §3.2 行 104「资产关系与拓扑」正文里的**第三条"仍缺"**
  （原文：「K8s `service → pod`（清单里没有 Service/Endpoint，**要先补采集**）」）
- 前序：`2026-10-04-container-inventory-design.md`（清单上报）、`2026-10-07-asset-relation-auto-source-design.md`（自动来源，其 D7 就是本条）
- 状态：**设计待评审（D1~D10）**

## 1. 起因

行 104 的正文自己列了三条"仍缺"，其中第三条是 K8s `service → pod`，并写明**要先补采集**：

> `exposes` 没有自动来源（端口归并有歧义，宁缺勿错）、中间件集群归属（`Group` 是 Agent 本地别名）、
> K8s `service → pod`（**清单里没有 Service/Endpoint，要先补采集**）

前序设计件也把它记成了待办（`2026-10-07-asset-relation-auto-source-design.md` 的 D7："本批不做，
且要先补采集：清单里没有 Service/Endpoint，Pod/Workload 投影还丢掉了 labels。属 Agent 侧改动，另立一批"）。

**为什么值得做**：`service → pod` 是"这台 Pod 挂了谁会受影响""这个服务背后是谁"的直接答案，
而它恰好是**唯一一条没有歧义**的 K8s 关系（Service 与 Pod 的对应由 Endpoints 权威给出，不靠猜）。
另外它会给 `exposes` 这个关系种类**第一个自动生产者**——`exposes` 定义已久却至今没有任何来源。

**与 ①（`exposes` 无自动来源）的区别**：① 讲的是**中间件**的"实例 → 端口"（端口归并有歧义，
正文明确"宁缺勿错"）；本条是 **K8s** 的"服务 → 后端 Pod"，由 Endpoints 显式给出，不存在歧义。

## 2. 现状（有依据）

| 事实 | 依据 |
|---|---|
| 清单只有 Pod / Workload，**没有 Service/Endpoint** | `internal/model/metric.go:123-130`（`K8sPods`/`K8sWorkloads`）；`internal/agent/collector/k8s.go:264-349`（工作负载）、`:413-490`（Pod） |
| 清单上报有**整份上报级**上限，超限**显式置位** | `k8s.go:78-85`；常量 `K8sPodsMaxPerReport=1000` / `K8sWorkloadsMaxPerReport=500` 在 `metric.go:583-586`；置位字段在 `metric.go:129-130` |
| 命名空间过滤是清单专属开关 | `k8s.go:190-202`（`systemNamespaces` + `inventoryWanted`，`IncludeSystemNamespaces` 控制） |
| Pod 投影**刻意不含 labels / env / Secret** | `metric.go:588-591`（注释）、`k8s.go:353-358`（只取首个镜像） |
| 建边集中在 `applyContainerInventory`，**顺序是正确性要求** | `internal/server/receiver/assets.go:294-406`：先工作负载（`:321-348`）再 Pod（`:350-405`）；`LinkDiscovered` 要求两端已存在 |
| Pod 的两条边：`runs_on`（`:386-391`）、`member_of`（`:396-404`） | 同上；归属节点用 `spec.nodeName` 的**规范主机名**（`:373`，大小写由 `hostNameOf` 折叠） |
| 幂等：`LinkDiscovered` → `INSERT … ON CONFLICT(from_id,to_id,kind)`，人工边不降级，被抑制的边不重建 | `asset/service.go:461`、`store.go:1426-1435`、唯一约束 `store.go:65` |
| **过期不物理删除**：资产靠 `asset_seen` 停更转 `missing`，已建成的边是持久的 | `store.go:968`、`service.go:131`；`assets.go:395` 的注释 |
| `exposes` 定义 = 服务 → 端点，但**全仓没有任何生产者** | `asset/model.go:52-66`（四种 kind）、`model.go:60-66`（`Valid()`）；采集与人工都没建过 |
| 资产类型：内置清单 + 播种自愈，**不需要升 schemaVersion** | `asset/model.go:118-125`（`BuiltinTypes`）、`store.go:363-367`（`INSERT … ON CONFLICT(key) DO UPDATE SET title`）、`model.go:113-117` 的注释 |
| 短命对象默认不进台账首页与健康度 | `model.go:94-101`（`EphemeralTypes` = pod / workload）；消费处 `store.go:850-853`、`:2444-2447` |
| `ParseContainerKey` **只接受 pod / workload** | `model.go:353-371`；消费方 `api/asset_api.go:155-160` 的 `assetContainerView` |
| 关系查询对 kind/type **一视同仁**，新边自动出现 | `api/asset_api.go:659-694`（link-stats）、`:697-738`（topology）；`store.go:1568`、`:1833` |

## 3. 范围

**做**：Agent 补采集 Service 与 EndpointSlice → 上报 → Server 落 `service` 资产并建 **`service --exposes--> pod`** 自动边。

**不做**（划线，避免膨胀）：

- **② 中间件集群归属**：下一批（已与你确认"先 3 再 2"）。
- **容器页加 Service Tab / `container.services` 只读动作**：本批只做台账与关系。要看 Service 详情走
  `kubectl`；真要做，那是一条新的 ops 动作，属于另一批（与 `container.describe` 的白名单投影同一套取舍）。
- **Service 指标**（如 `k8s_service_count`）：清单只做**发现**，不顺手扩指标面。
- **`selector` 解析**：见 D2——本批不用 selector 匹配，理由在那。
- **端口的端口级关系**（service:port → pod:port）：边只到 Pod，不带端口；端口级会让边数再乘一个量级。

## 4. 设计

### 4.1 采集（Agent）

`internal/agent/collector/k8s.go` 新增 `collectServices`，按集群读两个 API：

- `/api/v1/services`（core/v1）：取 `metadata.name/namespace`、`spec.type`、`spec.clusterIP`；
- `/apis/discovery.k8s.io/v1/endpointslices?labelSelector=kubernetes.io/service-name=<name>`
  （或一次列全集群再按 `kubernetes.io/service-name` 标签归并）：取 `endpoints[].targetRef.name`
  与 `targetRef.kind`，**只收 `kind == "Pod"`** 的条目。

**为什么是 EndpointSlice 而不是 `selector` 解析**：Pod 投影**刻意不含 labels**（`metric.go:588-591`），
按 selector 匹配就必须把 labels 加进上报（又一处"清单变胖"），而且**匹配不准**——selector 匹配的是
"选择器命中的 Pod"，不是"真正在服务背后的 Pod"（未就绪的 Pod 不在 Endpoints 里，这才是运维关心的那个集合）。
EndpointSlice 的 `targetRef` 直接给出**权威的 Service→Pod 对应**，也不需要拿 IP 去反查 Pod。

沿用既有约定：`inventoryWanted` 的命名空间过滤（`k8s.go:197`）、`getJSON`（`:561`）、
整份上报级截断（`:78-85`）。

### 4.2 上报结构

`internal/model/metric.go` 仿 `K8sPod` 加：

```go
// K8sService 是上报给 Server 的一个 Service 的**台账投影**。
// BackendPods 来自 EndpointSlice 的 targetRef（只含 kind=Pod 的条目），
// 即"这个服务现在真的把流量发给哪些 Pod"——不是 selector 命中的 Pod。
type K8sService struct {
	Cluster     string   `json:"cluster"`
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`        // ClusterIP / NodePort / LoadBalancer / ExternalName
	ClusterIP   string   `json:"clusterIP,omitempty"`   // 展示用
	BackendPods []string `json:"backendPods,omitempty"` // 后端 Pod 名（同命名空间）
	// BackendsTruncated：该服务的后端数超过单服务上限被截断（**显式置位**，不静默丢）
	BackendsTruncated bool `json:"backendsTruncated,omitempty"`
}
```

`ReportPayload` 加 `K8sServices []K8sService`（`json:"k8sServices,omitempty"`）与
`K8sServicesTruncated bool`（`json:"k8sServicesTruncated,omitempty"`），上限常量
`K8sServicesMaxPerReport = 500`、`K8sServiceMaxBackends = 200`。

`omitempty` 是**兼容性的关键**：旧 Agent 不报这个字段 → Server 侧一条 service 资产都不建，
既不报错也不产生半截数据；反过来，Server 先升级、Agent 后升级也不会坏。

### 4.3 资产与建边（Server）

新增内置类型 `service`（`asset/model.go`）：

- 常量 `TypeService = "service"`，`BuiltinTypes()` 加 `{Key: TypeService, Title: "K8s 服务", Builtin: true}`；
- 自然键 `ServiceNaturalKey(cluster, namespace, name)` = `<集群>/<命名空间>/service/<名称>`
  （与 `PodNaturalKey`/`WorkloadNaturalKey` 同形）；
- **不加入 `EphemeralTypes()`**：Service 是稳定对象（不是滚动更新掉的那种高 churn 对象），
  它"失联"是真信号，应当计入台账首页与健康度——这与 Pod/Workload 的取舍正好相反，是刻意的。

`applyContainerInventory` 里新增一段，**顺序**调整为：

```
1. 工作负载（资产）           ← 不变
2. Service（资产）            ← 新增：先落资产，边留到最后
3. Pod（资产 + runs_on + member_of）  ← 不变
4. service --exposes--> pod   ← 新增：对端此时一定已存在
```

- Service 的**归属节点取上报主机**（与工作负载同口径，`assets.go:338-341`）：Service 跨节点、
  没有单一归属节点，落空串会让受限用户完全看不到它；
- 后端 Pod 用 `asset.PodNaturalKey(cluster, namespace, name)` 定位；**Pod 不在本轮清单里就跳过**
  （被截断、或在被排除的命名空间）——不造占位、不猜测（`assets.go:291-293` 的既有取向）；
- 关系种类用 **`exposes`**（"服务暴露于端点"，方向 service → pod）：语义正合，且这是它**第一个自动生产者**。
  刻意**不用** `member_of`（那是"属于"，Pod 不属于 Service）。

### 4.4 无后端 / 特殊 Service

- **无 Endpoint 的 Service**（`ClusterIP` 但选择器无匹配、`Headless`、`ExternalName`）：
  **只落资产、不建边**——不猜后端是谁（宁缺勿错）。
- `ExternalName` 没有 ClusterIP、也没有 EndpointSlice，同样只落资产。

### 4.5 前端

- 台账与关系视图**自动包含**新类型与新边（查询侧对 kind/type 一视同仁，无需改）；
- 补中文标签：`AssetListView.vue` 的 `typeLabel`/`INSTANCE_LABELS`/`PEER_TYPE_LABELS`/`KIND_LABELS`
  （`exposes` 的标签已有：`:906`、`:993`，`AssetTopologyView.vue:181` 也有，确认一遍即可）；
- **要改一句会变成假话的文案**：`AssetListView.vue:577` 现在写着"目前 runs_on 与 member_of 多由采集建立，
  **depends_on 与 exposes 只能人工维护**"——本批之后 `exposes` 有了自动来源，这句必须改
  （本项目有 `uicopy_guard_test.go` 这类守卫，界面上的话是要经得起核对的）；
- **容器页不加 Service Tab**（见 §3），因此 `ParseContainerKey` **不接受** `service`——
  否则资产详情会出现"查看容器日志 / 查看容器工作负载"这种点不通的按钮。

## 5. 决策点（D1~D10）

| # | 问题 | 建议 | 理由 |
|---|---|---|---|
| D1 | 范围 | 只做 K8s `service → pod`；② 下一批 | 已与你确认"先 3 再 2" |
| D2 | Service↔Pod 口径 | **EndpointSlice 的 `targetRef`**（不用 selector 解析、不靠 IP 反查） | Pod 投影刻意不含 labels；且 selector 匹配的是"命中的"而不是"真正在服务背后的"（未就绪的不在 Endpoints 里） |
| D3 | 关系种类 | **`exposes`**（service → pod） | 语义正合；也是它第一个自动生产者。不用 `member_of` |
| D4 | 新类型 `service` | 内置类型；**不计入** `EphemeralTypes` | Service 是稳定对象，"失联"是真信号（与 Pod/Workload 相反，刻意） |
| D5 | `ParseContainerKey` | **不接受** `service` | Service 没有日志/工作负载概念，进了会让资产详情出现点不通的按钮 |
| D6 | 无后端 / ExternalName | 只落资产、不建边 | 不造占位、不猜测（既有取向） |
| D7 | 上限与截断 | 500 服务/份 + 每服务 200 后端，**两者都显式置位**截断标记 | 静默丢会让台账看起来"集群里只有这么多服务"；后端数是主要膨胀源 |
| D8 | 建边顺序 | 工作负载 → Service（资产）→ Pod（资产+边）→ service→pod 边 | `LinkDiscovered` 要求两端已存在，顺序是正确性 |
| D9 | 权限与前端 | 不新增权限点（走 `assets:read`）；容器页不加 Tab；只补中文标签 | Service 是资产，看台账的权限已经覆盖 |
| D10 | 升级面 | **两端都要升**（采集在 Agent、建边在 Server） | 与前几批"仅 Server"不同，要提前说清 |

## 6. 批次拆分、用例与实机验证

### 6.1 三笔

1. **Agent 采集 + 上报结构**：`model/metric.go`（`K8sService` + 两个上限 + 截断字段）、
   `k8s.go`（`collectServices` + EndpointSlice 归并）、`collect_all.go` / `cmd/agent/main.go` 接线。
   用例：EndpointSlice 解析（多 slice、非 Pod 的 targetRef 要跳过、无 targetRef 要跳过）、
   命名空间过滤、上限与截断置位、apiserver 读失败时只 Warn 不阻断。
2. **Server 建资产与边**：`asset/model.go`（`TypeService` + `BuiltinTypes` + `ServiceNaturalKey`）、
   `receiver/assets.go`（Service 分支 + 最后的建边段）。
   用例：资产与边都建成、**无后端不建边**、`ExternalName` 只落资产、**顺序**（Pod 缺席时不建边）、
   **幂等**（同一份清单跑两轮边不重复）、归属节点取上报主机、截断标记不影响建边。
3. **前端标签与文案 + 文档对账**：标签补齐；**改掉 `AssetListView.vue:577` 那句"exposes 只能人工维护"**；
   全景表行 104 / 194 与相关设计件的对账。用例：标签映射（service / exposes 都有中文名）、
   文案断言（不再声称 `exposes` 只能人工维护）。

### 6.2 实机验证计划（dev-server，**Server 与 Agent 都要升**）

dev-server 上有 k3s（记忆里记着只读 SA 与验证负载），Service 是真实存在的：

1. **采集**：升级后看上报体里出现 `k8sServices`，条数与 `kubectl get svc -A`（排除系统命名空间的规则要对齐）一致；
2. **资产**：台账里出现 `service` 类型资产，自然键形状正确、归属节点 = 集群上报主机；
3. **关系（决定性）**：`GET /api/v1/assets/link-stats` 里出现 **`exposes`** 边；取一条边核对两端
   （service → 它真正的后端 Pod），与 `kubectl get endpoints` 对照一致；
4. **无后端不建边（决定性）**：找一个无 Endpoint 的 Service（如 `ExternalName` 或选择器无匹配的）
   → 它**只有资产、没有边**；
5. **幂等**：等下一轮上报后边数不增长（`ON CONFLICT` 生效）；
6. **范围**：受限用户（按分组）能看到 service 资产，且看不到范围外集群的；
7. **旧 Agent 兼容**：本机若有未升级的 Agent，其上报不含 `k8sServices` → **不报错、不产生半截数据**。

**未覆盖边界（不得视为通过）**：大规模集群（数千 Service / 数万后端）的采集耗时与上报体体积
（上限逻辑有用例，但没有压测）；`NodePort`/`LoadBalancer` 的端口语义（本批不带端口级边）；
跨命名空间的 Service（不存在，但 EndpointSlice 的 `targetRef` 命名空间要与 Service 一致才建边）。

## 7. 待评审

D1~D10 若都按建议，则按 §6.1 的三笔开工；有异议的挑出来。

## 8. 实施记录（2026-10-07，Server 与 Agent 1.30.47）

三笔都已落地（D1~D10 全部按建议）：

**① Agent 采集 + 上报结构**：`internal/model/metric.go` 新增 `K8sService`（含 `BackendPods` /
`BackendsTruncated`）、`ReportPayload.K8sServices` + `K8sServicesTruncated`、常量
`K8sServicesMaxPerReport=500` 与 `K8sServiceMaxBackends=200`；`internal/agent/collector/k8s.go`
新增 `collectServices`（读 `/api/v1/services`，Headless 的字面量 `"None"` 不当地址带出）与
`serviceBackends`（读 `/apis/discovery.k8s.io/v1/endpointslices`，按 `kubernetes.io/service-name`
标签归并，只收 `targetRef.kind=Pod`、跨命名空间跳过、同一后端跨切片去重、结果排序让上报体稳定），
接进 `CollectCtx` 的整份上报级截断；`collect_all.go` 与 `cmd/agent/main.go` 接线。
**拿不到 EndpointSlice 不是致命错误**：Service 资产照落，只是这次不建关系。

**② Server 建资产与边**：`internal/server/asset/model.go` 新增 `TypeService`、内置类型「K8s 服务」、
`ServiceNaturalKey`，并在 `EphemeralTypes` 的注释里写明 **Service 刻意不在该列表内**；
`internal/server/receiver/assets.go` 的 `applyContainerInventory` 增第 3 步（落 Service 资产，
归属节点 = 集群上报主机）与第 4 步（`exposes` 边），并用 `podKeys` / `serviceKeys` 两个集合把关——
**只对本轮真的落过的两端建边**：后端缺席时不建边、不造占位 Pod、也不刷日志。

**③ 前端与文档**：`AssetListView.vue` 的 `PEER_TYPE_LABELS` 与 `AssetTopologyView.vue` 的
`TYPE_TITLES` 补 `service`；**改掉 `AssetListView.vue:577` 那句"depends_on 与 exposes 只能人工维护"**
（本批之后 `exposes` 有自动来源了）。

**测试**：`go test ./...` 全绿；新增 6 条（Agent 侧 4 条：Service 与后端归并/去重/非 Pod 跳过 +
Headless 与 ExternalName、单服务后端上限**显式置位**、整份上报上限**显式置位**、拿不到切片仍落资产；
Server 侧 2 条：资产 + 边 + **幂等** + 缺席后端**不造占位** + 无后端不建边、Service **不进短命对象**）。
前端 `npm test` **23 文件 136 项**全绿（新增 1 条：关系总览里 `service` 与 `exposes` 都显示中文名）。

**未覆盖（不得视为通过）**：浏览器实点；`AssetListView.vue:577` 那句新文案只做了人工核对
（它在"手工加边"对话框里，写用例要先把对话框打开，收益与成本不成比例）；大规模集群的采集耗时与上报体体积。

**待办**：出包（**Server 与 Agent 同升**）后在 dev-server 按 §6.2 跑实机核对。
