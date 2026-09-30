# 容器与可观测模块详细设计

日期：2026-09-30
上位文档：`2026-09-30-ops-platform-evolution-design.md`（总体设计）、`2026-09-30-ops-platform-capability-map.md`（全景对照表 §3.8、§3.9、§3.10）
决策记录：`docs/adr/0002-log-backend-abstraction.md`、`docs/adr/0003-container-management-downlink.md`

## 意图与边界

**意图**（两条线）：

1. **容器/K8s**：把"只有指标"的容器监控，升级为**可查看、可排障**的只读管理面（工作负载、事件、日志、描述信息），并为将来受控的写操作（exec、下发）留出通道。
2. **日志与可观测**：把"能采能搜"的集中日志，升级为**结构化、可字段检索、后端可替换**的日志能力；明确链路追踪的取舍。

**边界**：

- **不集成重量级平台**：不引入 Rancher（25942★，需独立集群）/ KubeSphere（17059★，NOASSERTION 许可）；理由是它们与"内网离线 + 轻量单二进制"定位冲突（调研报告 §2.2、§4.2）。
- **默认只读**：首批只做读取类操作；`exec` 等写操作后置 P2 且需显式开启。
- **不引入 AGPL/重量级日志后端**：默认沿用自研落盘；仅抽出接口，第二个适配器随需引入（ADR-0002）。
- **不做链路追踪**（P3，见 §链路追踪取舍）。

## 现状与依据

| 事实 | 依据 |
|---|---|
| **K8s 凭据只存 Agent 本地，不上报** | `internal/model/metric.go:543-553` `K8sInstanceConfig{Name, APIServer, Kubeconfig, Token, InsecureTLS, MetricsServer, ExporterURL}`，注释：「Kubeconfig/Token 等凭据仅存 Agent 本地，不上报 Server（K8sInstance 结构本身不含凭据字段）」；`K8sInstance:556-561` 仅含 `Instance(=apiserver 地址)/Name/Node/Group/Version` |
| K8s 采集是**只读 GET** | `internal/agent/collector/k8s.go:collectPods/collectWorkloads` |
| Docker 采集经 Engine API | `internal/agent/collector/docker.go`（`/var/run/docker.sock`） |
| **Server 侧无 K8s 管理接口** | 全仓无 `*k8s*` handler；仅指标读取 `GET /api/v1/middleware/k8s/instances` |
| 下行指令先例（协议/协商/回执/过期俱全） | `internal/server/receiver/receiver.go:HandleReport`（`resp["command"]="upgrade"`、`resp["defense"]=cmd`，仅当 `payload.Capabilities.Defense` 才下发）；`internal/agent/reporter/reporter.go:ReportResponse{Status, Command, Defense}`；`payload.DefenseResult` → `defense.UpdateResult`；`internal/server/security.DefenseStore` 的 `Take/ExpireOverdue` |
| 日志检索现有语义 | `internal/server/logstore/query.go:Store.Query`；`internal/model/log.go:69-93` `LogQuery{From, To, Keyword, Regex, Nodes[], Sources[], Limit}`、`LogQueryResult{Lines, Truncated, Cursor, ScannedBytes, ScannedLines, Files}`；预算常量 `DefaultScanBudgetBytes=64MiB`、`DefaultScanBudgetLines=200000`、`DefaultLimit=200/MaxLimit=1000`；游标 `Cursor{File,Offset}`（文件内绝对字节偏移） |
| 日志采集已有（含多行合并） | `internal/agent/logship/ship.go:Shipper.send`、`internal/agent/collector/logcollect.go:mergeMultiline` |
| 日志指标已可用于告警 | `model.LogPatternNamePattern` 注释说明模式名拼进指标 `<来源>_log_<模式>_total`；规则类型 `RuleTypeThreshold`（`internal/model/metric.go:649-661`） |
| **链路追踪完全无实现** | 全仓 `trace/span/otel/jaeger/skywalking` 0 命中 |
| 官方 Dashboard 已归档、国产面板停维护/不可访问 | 调研报告 §2.2：`kubernetes-retired/dashboard` ARCHIVED、`KubeOperator` ARCHIVED、`eipwork/kuboard(-v3)` 404 |

## 容器只读管理面设计

### 3.1 数据通路（为什么是"异步任务"而不是"同步代理"）

```mermaid
sequenceDiagram
  participant W as 前端
  participant S as Server（container.Service + channel）
  participant A as Agent（指令执行器）
  participant K as apiserver / 容器运行时
  W->>S: POST /api/v1/ops/tasks {kind:container.events, cluster, ns, name}
  S-->>W: 202 {taskId, state:pending}   # 不在请求内等待
  A->>S: POST /api/v1/report（携带 Capabilities.Ops）
  S-->>A: {"ops":{id,kind,params,expireAt}}
  A->>K: 只读查询（GET）
  K-->>A: 结果
  A->>S: POST /api/v1/report（opsResult{id,state,message,data}）
  S->>S: channel.Result → 缓存结果
  W->>S: GET /api/v1/ops/tasks/{id}      # 轮询；或经 GET /ws 推送
  S-->>W: {state:done, data}
```

**关键取舍（必须写清，避免体验预期错位）**：指令是**随 Agent 上报响应下发**的，单次往返至少一个上报周期（默认 15s）。因此接口语义是**异步任务**（提交 → 任务 ID → 轮询/WS 推送），**不是**"点开即返回"的同步代理。

**被拒的两个替代方案**：

| 方案 | 拒绝理由 |
|---|---|
| Server 直连 apiserver | 需要把 Kubeconfig/Token 上报到 Server，**直接破坏** `K8sInstanceConfig` 注释所声明并已被遵守的凭据边界 |
| Server↔Agent 新建长连接 | 新增协议、鉴权与穿透复杂度；网闸（edge/hub）场景下更复杂；而既有"响应内下发"链路已被两条业务验证可用 |

### 3.2 指令类型（kind）

| kind | 语义 | 首批 | 说明 |
|---|---|---|---|
| `container.workloads` | 列集群/命名空间下的工作负载（Deployment/StatefulSet/DaemonSet/Job）与副本状态 | ✅ | 复用 `k8s.go:collectWorkloads` 的只读 GET |
| `container.pods` | 列 Pod（含重启次数、状态、所在节点） | ✅ | 复用 `collectPods` |
| `container.describe` | 单对象详情（含 YAML/描述信息，凭据相关字段**必须脱敏**） | ✅ | 需定义脱敏字段清单 |
| `container.events` | 命名空间/对象的事件列表（按时间倒序、分页） | ✅ | 事件量大，必须有上限与缓存 |
| `container.logs` | 拉取 Pod/容器日志（tail 行数、since、上一实例） | ✅ | 与集中日志的边界见 §4.4 |
| `container.exec` | 交互式终端 | ❌（P2） | 需显式开启 + 独立权限点 + 审计 + 会话时长限制 |

### 3.3 安全边界（四道护栏）

沿用既有"本机护栏 → 能力协商 → 中心授权 + 审计"思想，落地为四道（与总体设计 §下行操作通道 一致）：

1. **本机护栏**：`agent.yaml` 开关（如 `guards.ops.containerLogs`、`guards.ops.exec`），默认只开只读类。
2. **能力协商**：仅当 Agent 上报 `Capabilities.Ops` 含该 kind 才下发（对齐 `Capabilities.Defense` 现做法）；不支持的节点在前端显示"需升级 Agent/未开启"。
3. **中心授权 + 审计**：权限点 `container:read` / `container:exec`；`container:exec` 纳入高风险语义；任务提交与结果均写审计。
4. **默认只读 + 过期作废**：复用 `Take/ExpireOverdue` 语义，未领取/超时任务自动作废，避免堆积。

**脱敏规则**：`container.describe` 返回的 YAML 中，`Secret`、`ServiceAccount` token、`dockerconfigjson` 等敏感字段一律以占位符替换；日志拉取不返回环境变量全文。

### 3.4 前端信息架构（借 headlamp，不抄代码）

参考 `kubernetes-sigs/headlamp`（7352★，Apache-2.0）的信息组织：**集群 → 命名空间 → 资源类型 → 对象 → （详情/事件/日志/终端）**；其 README 明确「UI controls reflecting user roles (no deletion/update if not allowed)」——与现有 RBAC + 资源范围理念一致，因此**前端按权限隐藏操作按钮**，服务端仍为安全边界。

新增 `web/src/components/container/`：`K8sClusterView.vue`、`WorkloadListView.vue`、`WorkloadDetailDrawer.vue`、`EventListView.vue`、`ContainerLogViewer.vue`。复用既有抽屉/表格/`RefreshBar` 约定；**不新建独立前端框架**。

## 日志聚合设计

### 4.1 采集侧增强（Agent）

| 能力 | 现状 | 本次增强 |
|---|---|---|
| 采集（来源 + 路径 + patterns） | 已实现 | 不变 |
| 多行合并 | 已实现（`logcollect.go:mergeMultiline`） | 不变 |
| **结构化解析** | 无 | 按来源声明 `format: json \| regex`（regex 支持命名捕获）→ 产出字段 map |
| **标签注入** | 无 | 注入 `node`（已有）、`source`（已有）、容器场景追加 `namespace/pod/container/workload` |
| 限速 / 每日上限 / 偏移落盘 | 已实现 | 不变 |

**解析失败不丢数据**：解析失败时**保留原始行**并标记 `parseFailed=true`，只跳过字段化——避免"因为格式变了就看不见日志"。

### 4.2 模型扩展（明确标注为新增，不臆造既有字段）

- `model.LogQuery`（`internal/model/log.go:70-78`）新增可选 `Fields map[string]string`（字段等值过滤）。
- `model.LogHit` 新增可选 `Fields map[string]string`（解析结果）。
- `model.LogQueryResult` **不新增字段**（`Truncated/Cursor/ScannedBytes/ScannedLines/Files` 语义已完整，且是"为什么没结果"的诊断依据，必须原样保留并回传）。

### 4.3 存储抽象（`LogStore`）与第二适配器

- 接口沿用既有方法名与既有模型类型（**不得改名**，接口是调用方与测试的共同接缝）：

```
// LogStore 是日志存储抽象；现有分片落盘实现作为默认适配器，外部后端作为可选适配器。
type LogStore interface {
	Write(entries []model.LogEntry) (dropped int, reason string, err error)
	Query(q model.LogQuery, cursor logstore.Cursor) (model.LogQueryResult, error)
	Backend() string
}
```

- **随第二适配器一起引入**（ADR-0002）：目前只有一个适配器，提前抽象是假想接缝。首选候选 **VictoriaLogs**（2331★，Apache-2.0，单二进制，与现用 VictoriaMetrics 同厂；官方称相比 Elasticsearch/Loki 最多省 30x 内存、15x 磁盘——调研报告 §2.3）。
- **保留的检索语义**（不得改动）：游标不透明（前端原样回传）、`Truncated` 必须回传（静默截断会让人以为"日志就这么多"）、扫描预算与三项诊断。

### 4.4 与集中日志既有能力的边界

| 场景 | 走哪条 | 理由 |
|---|---|---|
| 已采集上行的历史日志检索 | **集中日志**（`GET /api/v1/logs`） | 有索引化扫描预算与游标语义，按天保留 |
| 某 Pod 的实时/即时日志（未配置采集） | **`container.logs` 指令** | 按需拉取，不进 Server 存储，避免"任意 Pod 日志全量落盘"的容量与合规风险 |
| 需要长期保留的容器日志 | 引导用户**配置 `logSources`** | 让"要不要留"由采集配置显式决定 |

### 4.5 由日志创建告警规则

在检索页支持"用当前查询条件创建规则"：底层**复用既有日志指标** `<来源>_log_<模式>_total` 与 `RuleTypeThreshold`，**不新增规则类型**、不改告警引擎。这等价于把 README 路线图「日志分析增强（P1）」中"在检索页直接由日志内容创建规则"落地为"由检索条件生成阈值规则"，避免引擎侧扩张。

## 链路追踪取舍（明确结论）

**结论：本批不做（P3）**。

- 理由：链路追踪需要**应用侧侵入**（各语言 SDK/探针；即便 Java 可 `-javaagent` 也需运维介入），与"内网离线 + 轻量单二进制 + 不改客户应用"的定位冲突。
- 备选路线（按侵入度排序，供未来决策）：
  1. **eBPF 零侵入**：如 `deepflowio/deepflow`（4286★，Apache-2.0，官方定位 "eBPF Observability - Distributed Tracing and Profiling"），代价是内核版本要求与部署复杂度；
  2. **OTel 网关**：应用侧接入 OTel SDK/Collector，Server 侧只做接收与展示；需客户应用配合改造；
  3. **APM 自成体系**：`apache/skywalking`（24967★，Apache-2.0）——功能强但需应用探针与独立 JVM 组件，与本定位冲突最大。
- **前置条件**（写入本设计的门槛）：明确要追踪的协议与语言矩阵、可接受的应用侧改造范围、存储与保留预算。

## 接口草案与权限点

```
# 容器管理面（新增；注意既有中间件指标路由 /api/v1/middleware/k8s/instances 保持不动）
GET    /api/v1/container/k8s/clusters                 # 集群清单（来自 K8sInstance，无凭据）
POST   /api/v1/ops/tasks                              # 提交任务 {kind, cluster, namespace, name, params}
GET    /api/v1/ops/tasks/{id}                         # 查询任务状态与结果（轮询；或经 /ws 推送）
DELETE /api/v1/ops/tasks/{id}                         # 取消/清理

# 日志（扩展现有路由，不新建）
GET    /api/v1/logs?from=&to=&q=&regex=&nodes=&sources=&fields=key:value&limit=&cursor=
```

**权限点**：`container:read`（集群/工作负载/事件/日志）、`container:exec`（P2，高风险）；日志沿用既有 `logs` 前缀。
**审计**：任务提交、结果、被拒（`RecordPermissionDenied`）均落审计；`container:exec` 记录会话起止与长度。

## 前端与菜单

- 新增 `web/src/components/container/`（见 §3.4）。
- `web/src/components/LogsView.vue` 增强：字段过滤条、命中行展开（字段 + 上下文行）、"由本次查询创建规则"入口。
- 菜单挂载遵循现有 `Sidebar` 分组方式；**不新增一级菜单**，容器归入「中间件/容器」，资产归入「资产」（与 D2 的 `asset/` 一致）。

## 文档与验证

- 术语新增并写入 `CONTEXT.md`：`工作负载`、`下行操作（Ops 指令）`、`高危操作护栏`、`日志后端`、`结构化解析`、`字段检索`（含 `_避免_` 同义词）。
- 实现阶段测试面（本设计不写代码）：
  - `channel`：能力协商（不支持的 kind 不下发）、过期作废、结果回执幂等；
  - `container.Service`：脱敏（Secret/token 字段被替换）、事件分页上限、`container.logs` 不落 Server 存储；
  - 日志：解析失败保留原始行、`Fields` 过滤与既有 `Keyword/Regex` 叠加语义、游标与 `Truncated` 行为不变（回归既有测试）；
  - 安全：无 `container:read` 权限返回 403 并落审计；未开启本机护栏时任务不被领取。
- 验证命令（沿用仓库约定）：`go test ./...`、`go vet ./...`、`npm --prefix web test`、`npm --prefix web run build`。

## 风险与取舍

| 风险 | 取舍 |
|---|---|
| 交互延迟（≥1 个上报周期，默认 15s） | 显式定义为**异步任务**；提交后立即返回任务 ID，前端轮询/WS 推送；对"快速排障"场景通过缓存上次结果缓解 |
| 事件/日志拉取量不可控 | 强制 `limit` 与时间窗上限；结果只缓存最近 N 条与 TTL；不进 Server 日志存储 |
| 凭据边界被削弱 | 明确禁止 Server 直连 apiserver；`container.describe` 强制脱敏；Agent 侧护栏默认只开只读 |
| 结构化解析带来 CPU 开销 | 解析在 Agent 侧按来源声明开启（默认关闭 JSON/正则解析），并纳入既有采集周期预算 |
| 抽 `LogStore` 抽象引入打包复杂度 | 接口与第二适配器一起落地；默认不引入外部后端（ADR-0002） |
| `exec` 被当作"顺手功能"提前做 | 明确 P2 + 独立高危权限点 + 显式开启 + 审计，进入路线图而非首批 |
| 与集中日志职责混淆 | 用 §4.4 的边界表固化：历史检索 vs 按需拉取 vs 长期保留三条路径 |
