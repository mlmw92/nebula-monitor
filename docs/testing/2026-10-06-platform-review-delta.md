# 平台全面审查增量结论（2026-10-06）

代码基线：`VERSION` = `1.30.33`（审查对象为当时**尚未提交**的工作树：45 个已修改文件 + 9 个新增测试文件）。

本文件是 [`2026-10-05-platform-validation-matrix.md`](./2026-10-05-platform-validation-matrix.md) 的**增量**：那篇给出逐功能点用例设计与第一批执行证据，本篇只记录**本轮新做的复核、发现的缺陷与修复**，不重复功能清单（清单见 `docs/superpowers/specs/2026-09-30-ops-platform-capability-map.md` §三，14 域 96 项：52 已实现 / 8 部分实现 / 36 未实现）。

## 一、审查方式与边界

- 手段：全量 `go build` / `go vet` / `go test`、依赖一致性（`go mod tidy -diff`）、格式化漂移检查、逐文件阅读未提交 diff、对高风险包做针对性只读审计、新增回归用例。
- **不覆盖**：真实 15 类中间件与第三方 exporter 实机核对、Linux raw socket ICMP、公网/私有 CA 证书链、浏览器端到端（真实登录→SQLite→下载闭环）。
- `go test -race` 在本机无法执行（报 `-race requires cgo`，本机无 gcc），竞态结论一律以既有 Linux CI 记录为准，不因本机跑不了而写成「无竞态」。

## 二、未提交工作树检查结果

| 检查项 | 命令 | 结果 |
|---|---|---|
| 编译 | `go build ./...` | 通过 |
| 静态检查 | `go vet ./...` | 通过（0 告警） |
| 全量单测 | `go test -count=1 ./...` | 26 个包全绿、0 失败 |
| 依赖一致性 | `go mod tidy -diff` | 无差异（`golang.org/x/net` 因新增 ICMP 导入由 indirect 提升为直接依赖，`go.sum` 未变） |
| 格式化漂移 | `go fmt ./...` | 见下方「注意」 |
| 前端单测 | `npm --prefix web test` | 10 文件 55 项通过 |
| 前端构建 | `npm --prefix web run build` | 通过；Element Plus 分页 `small` 弃用告警与大 chunk 告警仍在（`index` 约 1.80MB、`world` 约 1.01MB），非阻断 |
| 历史抖动项复现 | `go test ./internal/server/api -run '^TestWS_RegisterRejectedAfterClose$' -count=15` | 15/15 通过，**本机未复现**此前记录的间歇失败；仍按「未定性」处理，不视为已修复 |

**注意（工具副作用，已处理）**：`go fmt ./...` 会顺带改写 85 个与本批次无关的文件（仅行尾/空行/对齐噪声），这些改动已全部还原，工作树只保留本批次相关改动。后续复核请优先用 `gofmt -l <目标文件>` 而不是 `go fmt ./...`。

## 三、发现并修复的缺陷

| ID | 级别 | 位置 | 现象（触发条件） | 修法 |
|---|---|---|---|---|
| D1 | 中 | `internal/server/api/asset_api.go`（`assetAllowedNodes`） | 受限身份遇到 `nodeMgr == nil` 时返回 `nil`，下游（`asset.ListFilter.Nodes`、`InspectRunsInNodes`、`BaselinesInNodes`）把 `nil` 解释为「全局、不过滤」→ 资产列表 / 巡检记录 / 差异项 / 标杆全部可见。同一语义下 `nodeInScope` 与 `visibleMetricNodes` 都是 fail-closed，两者取向相反 | 受限身份一律返回**空切片**（fail-closed），与 `nodeInScope` 对齐 |
| D2 | 中 | `internal/server/retention/retention.go`（`cleanup`） | `if removed, err := …; err == nil` 把清理错误整个丢弃：库不可用或磁盘写失败时界面显示「清理完成、删除 0 条」，而 `alert_acks` 实际在无界增长 | `CleanupResult` 新增 `Errors []string`；失败写入结果并 `slog.Warn`；前端改为 warning 展示 |
| D3 | 中 | `internal/agent/collector/fetch.go`（`firstMetricValue`） | 存活判定取「首个命中序列」，与文档承诺的「原生 `*_up 0` 优先判离线」不符：exposition 中 `mysql_up 1` 在前、`mysql_instance_up 0` 在后时，实例被误判为**在线** | 命中顺序加优先级：平台目录名 `<mw>_instance_up` 优先于上游惯用名；同级仍取首个序列 |
| D4 | 低 | `internal/server/api/alert_collab.go`、`internal/server/alert/ackstore.go`（`Mark`/`Assign`） | 对**已关闭**的记录再次认领时不清 `CloseReason`/`CloseTime`，回写出现「已认领却带着关闭原因与关闭时间」的自相矛盾记录（`Reopen` 会清，两条路径口径不一） | 认领路径一并清空关闭信息，`AckTime` 保留 |
| D5 | 低 | `internal/server/asset/service.go`（`SetBaselineIfCurrent`/`ClearBaselineIfCurrent`） | 受限身份无任何可见节点时条件写必然不命中 → 返回 `ErrBaselineChanged` → 前端收到 409「标杆已变更，请刷新后重试」，用户会反复重试一个永不成功的操作 | 新增 `ErrOutOfScope`，API 映射为 404「资产不存在」 |
| D6 | 低 | `web/src/components/settings/RetentionSubView.vue` | D2 的配套：即使服务端已能报错，界面仍以绿色成功弹窗呈现 | 按 `errors` 切换为 warning 并列出失败分项 |

### 新增回归用例（均已验证通过，且每条都能在修复前失败）

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestAssetAllowedNodesFailsClosedWithoutNodeManager` | `internal/server/api/scope_test.go` | 受限 + 无 nodeMgr → 非 nil 且长度为 0；全局/未认证 → nil |
| `TestManager_CleanupFailureIsReported` | `internal/server/retention/retention_test.go` | 落盘失败 → `Errors` 非空、`AcksRemoved == 0`、内存记录不得删除 |
| `TestExporterHealthPrefersPlatformInstanceUp` | `internal/agent/collector/exporter_contract_test.go` | 平台口径与上游名两种排列都以平台口径为准 |
| `TestAckStore_ReAckClearsStaleCloseInfo` | `internal/server/alert/ackstore_test.go` | 再次认领清空关闭信息、保留认领时间 |
| `TestRoutes_AlertCollab_ReAckClearsCloseInfo` | `internal/server/api/alert_collab_test.go` | 接口回写与落库均无 `closeReason`/`closeTime` 残留 |
| `TestBaselineConditionalWritesRejectEmptyScope` | `internal/server/asset/service_test.go` | 空范围 → `ErrOutOfScope`；全局 → 成功 |

### 已记录、本轮未改（低优先）

| 项 | 证据与影响 |
|---|---|
| `inspect_run_members.node_at_run` 只写不读 | 可见性按**当前**资产节点计算（`asset/store.go` 的 `inspectRunsInNodes`/`inspectFindingsInNodes` 均 `JOIN assets`）。后果：成员资产被删除后该 run 及其差异项对受限用户整体消失、节点迁移会追溯性改变历史可见性——与该表「冗余身份字段让结论可读」的注释意图相悖。要么删掉该列，要么用 `COALESCE(a.node, m.node_at_run)` 之类让被删资产仍可判定归属 |
| 全局分支对不存在的 run 返回可见 | `asset/service.go`：`nodes == nil` 时直接 `InspectFindings` 并返回 `visible=true`，不存在的 run 得到 `200 + 空差异`；受限分支同场景返回 404。同一接口两套语义，前端难以区分「空结果」与「记录不存在」 |
| 节点范围两套口径 | `assetAllowedNodes` 用 `ListHostNodes`（剔除 edge/hub），`nodeInScope` 用 `GetNode`（含 edge/hub）。方向是「列表更严、详情更宽」，**不构成越权**，但会出现「列表看不到、详情能打开」。建议统一为一个可见节点集合定义 |
| 标杆写入前的孤儿快照 | `Service.SetBaselineIfCurrent` 在条件写之前先 `recordSnapshot`，条件写失败（并发替换/范围不命中）会留下一条无引用的快照 |

## 四、本轮覆盖到的功能点

`1-02`（exporter 实例存活）、`1-04`（拨测 ICMP/证书）、`1-05`/`1-06`（告警处置状态机）、`1-07`（通知脱敏）、`2-03`/`2-08`（标杆与差异巡检）、`2-07`/`13-02`（资源范围）、`9-04`（留存）、`12-05`（审计导出）。

## 五、有升级价值的改进建议（延续 `2026-09-30-ops-platform-open-source-research.md`）

1. ~~**P0：补上「exporter 模式缺 `<mw>_instance_up`」的告警空洞。**~~ **已于 2026-10-06 实施**（见 §六.1 D7）：receiver 侧按实例元信息补出这 6 类序列并按「指标名 + instance」判重。**剩余待验证**：真实 exporter 部署下的告警触发闭环（本机只做了 handler 级用例）。
2. **P0：告警 ↔ 资产 ↔ 变更闭环。** 告警可跳转资产及最近配置变化，按业务影响定位；再接已有参数化处置任务与审批状态机。参考 NetBox 的对象关系建模与 Nightingale 的 target/busi_group 组织（只借设计）。
3. **P0：容器只读面增强。** 补 Pod 日志（时间/行数/范围限额）、事件、跨工作负载与资产导航；敏感字段继续白名单投影，不做 exec。参考 Headlamp 的信息架构与「UI 反映权限」。
4. **P1：日志结构化与有界检索。** JSON/受限正则提字段、标签基数限额、字段检索、日志→Pod/资产跳转；保留现有有界扫描作回退，达规模瓶颈再评估 VictoriaLogs（LogsQL）后端适配。
5. **P1：把「失败必须可见」做成横向约定。** 本轮 D2 暴露的是一类模式问题：清理、审计落盘、通知投递失败都可能被静默吞掉。建议统一约定——凡有副作用的批处理，结果结构必须能承载错误，前端不得用「成功」样式呈现部分失败；关键失败补 `self_*` 自监控指标。
6. **P1：可复现发布与测试门禁。** CI 增 `go test -race`（Linux）、exporter 契约核验（对真实 exporter 各 `curl /metrics` 一次并固化指标名）、权限矩阵回归、浏览器端到端；发布 OpenAPI 契约；给大屏/主包定体积预算。
7. **P2：审批与双人复核。** 参考 JumpServer 的能力边界划分，自研「事前审批 + 高危操作复核 + 超时失效」，与现有三道门控绑定；不引入 GPL 系外部平台。

**明确不建议**：重新引入任意脚本/在线终端、默认打开写权限、仅靠前端隐藏实现 RBAC、用一次单测全绿宣称平台「全面验证通过」。

---

## 六、第二轮：剩余缺陷收口 + P0 首批增量（2026-10-06 续）

上一轮把 6 项缺陷修复并入库，同时留下 4 项「已记录、未改」的低优先项与 1 项「需单独决策」的结构性缺口。本轮把这两类全部收口，并按 §五 P0 首批范围推进一个未实现功能。

### 6.1 剩余缺陷修复

| ID | 级别 | 位置 | 现象 | 修法 |
|---|---|---|---|---|
| D7 | 中 | `internal/server/receiver/receiver.go` | exporter 模式下 mysql/postgres/nginx/kafka/mongo/fastdfs 不产出 `<mw>_instance_up`，**「中间件离线」告警对这 6 类永不触发**（采集看起来正常，故障无人知晓）。上一轮记为"需单独决策后再实施" | 在 receiver 侧按实例元信息补出这 6 类序列（与 redis/k8s 同一形态）；判重按「指标名 + instance」，直连模式采集器已自产同名序列时不重复写 |
| D8 | 低 | `internal/server/asset/service.go` | 全局身份对**不存在的巡检记录**返回 `200 + 空差异`，受限身份同场景返回 404——同一接口两套语义，前端无法区分「空结果」与「记录不存在」 | 全局分支先做存在性判定，统一为「不存在 → 不可见 → 404」 |
| D9 | 低 | `internal/server/asset/store.go` | 巡检可见性只看成员资产的**当前**节点：资产被删除后该 run 及其差异项对所有人消失，与「巡检记录是证据、冗余存身份字段」的设计意图相悖 | 引入 `memberNodeExpr`：资产在 → 用当前节点（节点迁移不追溯授权）；资产已删 → 回退 `node_at_run` |
| D10 | 低 | `internal/server/api/asset_api.go` | 资产可见节点集合用 `ListHostNodes`（剔除 edge/hub），而 `nodeInScope` 用 `GetNode`（含 edge/hub）——两处口径不一致，会出现「列表看不到、详情能打开」 | 统一为全部注册节点（资产不会挂在代理节点上，放宽不额外暴露资产） |
| D11 | 低 | `internal/server/asset/service.go` | 设标杆的条件写失败时，之前建的快照留在库里：它会成为该资产的「最近一次快照」，被下一次巡检当成 L2 比对基准 | 条件写未生效时回收刚建的快照（`Store.deleteSnapshot`），回收失败只记警告不掩盖原错误 |
| D12 | 中 | `web/src/components/container/ContainerView.vue` | 容器页「撤回」按钮引用了**未定义的 `query`**，点下去必抛 `ReferenceError`（控制台报错、界面看着没反应） | 改用与 `run` 同一处取法 `containerQueryState(clusterKey, tab)`；补组件用例钉住 |

### 6.2 未实现功能推进：容器/Pod 日志（全景表 8-05，P0）

按 §五「P0 首批 → 容器与可观测 → 只读管理面（工作负载/事件/**日志**）」推进。既有只读管理面只缺日志，故本项落地即补全该批次。

- **协议与目录**：新增只读动作 `container.logs`（`internal/model/ops.go` 常量与数值参数白名单 + `internal/server/ops/catalog.go` 目录项）。参数：`cluster`/`namespace`/`name` 必填，`container`/`tailLines`/`sinceSeconds` 可选。
- **护栏（沿用四道门控）**：本机 `guards.ops.readOnly` + `guards.ops.container`；能力协商（`containerKinds`）；中心 `ops:exec` 权限 + 参数校验 + 审计；参数在 Agent 侧**再校一次**——命名空间必填、Pod 名与容器名走既有字符集、行数与时间窗超界一律拒绝（**不静默夹到上限**，否则用户会误判"日志里没有那条错误"）。
- **执行侧**：`internal/agent/collector/k8s_query.go:QueryLogs` 调 apiserver `/log` 子资源（`tailLines`/`sinceSeconds`/`container`）。边界：单次正文预算 96 KiB（超出截断并丢弃半行、显式标注）、单行 500 字符截断并计数、行数打满即标 `truncated`。
- **前端**：Pod 行新增「日志」按钮 + 日志抽屉（`<pre>` 保留原始空白与换行），与详情共用「异步下发 + 缓存 + 上次刷新」状态机；缓存键带上容器名，避免把上一个容器的日志显示在新容器名下。
- **刻意不做**：全量日志下载、按时间点回溯、把 Pod 日志接入集中日志链路（后者是 8-07 的联动项）。

### 6.3 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestHandleReportSynthesizesInstanceUpForExporterMode` | `internal/server/receiver/instance_up_test.go` | 6 类各产出 1 条 `<mw>_instance_up`，离线实例写 0 |
| `TestHandleReportDoesNotDuplicateCollectorInstanceUp` | 同上 | 直连模式已有序列时不重复写（避免同实例两条序列） |
| `TestHandleReportSynthesizesPerInstance` | 同上 | 同机多实例按 instance 逐个补，不互相顶掉 |
| `TestInspectFindingsUnknownRunIsNotVisible` | `internal/server/asset/service_test.go` | 未知记录不判可见、不报错 |
| `TestInspectVisibilityKeepsDeletedAssetEvidence` | 同上 | 资产删除后其所属节点仍可读到该记录；范围外节点看不到 |
| `TestSetBaselineConditionalFailureReclaimsSnapshot` | 同上 | 条件写失败不留孤儿快照 |
| `TestAssetAllowedNodesMatchesNodeScope` | `internal/server/api/scope_test.go` | 可见节点集合与 `nodeInScope` 同口径（含 edge/hub） |
| `TestRoutes_InspectUnknownRunIsNotFound` | `internal/server/api/inspect_api_test.go` | 未知记录对全局用户也是 404 |
| `TestValidate_ContainerActions`（新增 6 条拒绝 + 2 条通过） | `internal/server/ops/catalog_test.go` | 日志参数的白名单与必填项 |
| `TestContainer_LocalParamValidationRunsBeforeQuery`（新增 8 条） | `internal/agent/ops/container_test.go` | 参数不合法时**一次都不调用查询实现** |
| `TestContainer_LogsAppliesDefaultsAndPassesArgs` | 同上 | 默认行数落到模型常量、边界值被接受且原样下发 |
| `TestK8sQueryLogs*`（5 条） | `internal/agent/collector/k8s_logs_test.go` | `/log` 子资源路径与参数、行数截断、字节预算、错误透出、未知集群 |
| 「排队中的任务可撤回」 | `web/src/components/container/ContainerView.test.js` | 撤回确实撤到那条任务（修复前必抛 `ReferenceError`） |
| 「Pod 行上的「日志」下发 container.logs 并按原样展示日志行」 | 同上 | 下发参数正确、抽屉按原样展示日志与说明 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` 10 文件 57 项通过；`npm --prefix web run build` 通过。`go test -race` 本机仍不可执行（无 cgo）。

**未覆盖边界（不得视为通过）**：真实集群的 Pod 日志拉取（含多容器 Pod、RBAC 受限 SA、apiserver 压缩/分块响应）、exporter 模式下的告警触发闭环、浏览器端到端（真实登录 → 下发 → 轮询 → 展示）。

### 6.4 本轮之后仍未实现的功能（按文档 §五 优先级）

P0 首批剩余：资产拓扑视图（2-03/2-11）、日志结构化解析与字段检索（9-06/9-07）、日志后端抽象（9-08）、容器↔日志↔资产联动（8-07）。P1/P2 及「明确不做」项见全景表 §三与 §四对账表，其中链路追踪、会话录制/堡垒机、在线终端、移动端/i18n 为已确认的暂缓或不做项，不应计入"未完成"。

---

## 七、第三轮：8-07 容器 ↔ 资产联动（2026-10-06 续二）

按 §六.4 给出的顺序推进 8-07。该功能原文是「容器 → 日志/资产联动（Pod 打标回到资产与日志检索）」，其中**日志检索那一半依赖日志侧的标签注入**（`logship` 只采本机文件、`paths` 不支持通配符、没有 pod 维度标签），不在本批可闭环范围内；因此本批把**资产联动双向打通**，并把日志侧的依赖如实记录（见 §7.4）。

### 7.1 新增能力

| 层 | 内容 |
|---|---|
| Server | 新增 `GET /api/v1/assets/lookup`（权限 `assets:read`）：按 `type + key` 精确查一条资产；容器类资产允许直接给身份（`cluster`/`namespace`/`name`，工作负载再加 `kind`），**由服务端拼自然键**。范围外与不存在一律 404；身份缺一块返回 400（不能拼半截键去查，那会把"请求写错"显示成"没进台账"） |
| Server | `asset.ParseContainerKey(typeKey, naturalKey)`：从自然键解出集群/命名空间/kind/名字（集群含 `://`，从右往左取三段）；`assetView.container` 在容器类资产上带出该身份 |
| 前端（容器页） | Pod / 工作负载行新增「台账」按钮：按 **apiserver 地址**（台账自然键用的就是它，不是别名）查台账，命中跳资产详情，404 明确提示"尚未进入台账（清单上报约一个采集周期）" |
| 前端（台账页） | 支持 `?id=` 直接打开资产详情抽屉；容器类资产详情新增「查看容器日志 / 查看容器工作负载」→ 容器页 |
| 前端（容器页） | 支持 `?cluster=&namespace=&pod=`（或 `tab=`）深链：选中集群、切到对应 Tab、并按需打开该 Pod 的日志抽屉；参数只认领一次（消费后清掉 query） |
| 前端（主机页） | 修复**死链**：`AlertsView` 的「跳主机」早已 push `/hosts?node=`，但 `HostsView` 从不读该参数——点了只是切页，没有落到那台机器上。现按精确归属落到那一台（见 §7.3） |

### 7.2 设计取舍

- **自然键的拼与解都放服务端**：前端只传身份。集群是 apiserver 地址（含 `://` 与可能的端口），前端 split 一次就可能把 `https:` 当成命名空间；而**解错的联动不会报错**，只会跳到另一个对象上——这类缺陷只能靠"不在前端做这件事"来根除。
- **`lookup` 不复用列表的 keyword**：模糊搜索一旦拼错就会命中另一条资产，比"没找到"更糟（用户会点进一条不相干的记录）。
- **404 是正常结果**：清单上报有一个采集周期，刚建的 Pod 可能还没进台账；`lookupAsset` 刻意不用通用 `http.get`（它会把 404 抛成异常，调用方只能靠文案猜）。

### 7.3 顺带修复的联动缺陷（D13）

`AlertsView.gotoNode` → `/hosts?node=<主机名>` 的参数此前**无人消费**：页面切过去了，筛选条件没带过去，用户看到的是全部主机。修法：`HostsView` 消费该参数并落到那一台。第一版只回填关键词，用例随即暴露不足——关键词是**模糊**匹配（含 IP 与显示名），`web-01` 会同时筛出 `web-011`。最终改为「关键词让人看见筛的是谁 + `pinnedNode` 精确归属只留那一台」，用户一改关键词即交还给模糊匹配。

### 7.4 仍未做（依赖日志侧）→ **2026-10-07 已收口（见 §十三）**

「从 Pod 跳到集中日志检索」需要：`logship` 支持按路径通配采集容器日志目录（如 `/var/log/pods/<ns>_<pod>_<uid>/`）、从路径注入 pod 维度标签、以及结构化字段检索（9-06/9-07）。在这三项落地前，容器页的日志入口是**按需拉取**（`container.logs`），不是检索。这一依赖已写入全景表 §3.8 该行。

**三项依赖已于 2026-10-06 全部落地**（通配采集 + 身份随批次上报见 §十二；结构化字段检索见 §八），
2026-10-07 把界面接起来（§十三）：容器页 Pod 行与日志抽屉都能一跳进入按容器身份收窄的集中日志检索。

### 7.5 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestParseContainerKeyRoundTrip` / `Rejects` | `internal/server/asset/model_test.go` | 拼解互逆（含 `://`、端口、路径、带点的名字）；非容器类型与残缺键拒绝 |
| `TestHandleAssetLookupByNaturalKey` | `internal/server/api/asset_lookup_api_test.go` | 命中返回资产（naturalKey/typeKey/node 正确） |
| `TestHandleAssetLookupMissIsNotFound` | 同上 | 同命名空间不同名、同名不同命名空间、不同集群、不同类型一律 404 |
| `TestHandleAssetLookupRespectsScope` | 同上 | 范围外 404、范围内 200 |
| `TestHandleAssetLookupValidationAndDisabled` | 同上 | 缺参数 400、未启用资产能力 503 |
| `TestHandleAssetLookupByContainerIdentity` | 同上 | 身份三元组命中且 `container` 身份解出；身份缺一块 400 |
| `TestRoutes_AssetLookupPermissionAndRouting` | 同上 | 缺 `assets:read` 403；`/lookup` 未被 `/{id}` 抢先匹配 |
| 「Pod 行上的「台账」命中后跳到资产详情，未命中给明确提示」 | `web/src/components/container/ContainerView.test.js` | 下发参数含 **apiserver 地址**；命中 push `/assets?id=`；404 不跳转并给出提示 |
| 「带 ?cluster/namespace/pod 进入时直接定位并打开日志抽屉」 | 同上 | 清掉深链参数、选中该集群、下发 `container.logs` |
| 「带 ?node= 进入时精确落到那一台，且参数只认领一次」 | `web/src/components/HostsView.test.js` | 前缀相同的 `web-011` 被排除（按表格行断言）、参数消费后清空 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` 10 文件 **60 项**通过；`npm --prefix web run build` 通过。

**未覆盖边界（不得视为通过）**：真实集群的台账联动闭环（需要 K8s 清单上报 + 前端点击的端到端）、浏览器端到端（真实登录 → 跳转 → 日志抽屉）。

---

## 八、第四轮：9-06 日志结构化解析与字段检索（2026-10-06 续三）

按 §七 的顺序推进 9-06。这是三项里改动面最大的一项，落地时把范围钉在两件事上：**字段解析**与**按字段检索**；不碰"索引"与"日志建规则"（见 §8.4）。

### 8.1 关键决策：解析放在 Server 侧

`logship` 已经在每一行上带了命中模式名，而 Agent 是**离线分发**的：把提字段做在 Agent，就必须重分发全部 Agent 才能让已部署的机器受益；做在 Server 侧则**现有 Agent 无需升级**即可用上字段检索。代价是 Server 的每行解析开销——用"每行字段数上限 + 名字字符集 + 值长度"把它压到常数级。

### 8.2 落地内容

| 层 | 内容 |
|---|---|
| 解析器 | 新包 `internal/server/logparse`：JSON 对象取顶层标量、一层嵌套用点号连接（`http.status`），数组与 null 跳过；无结构行退到 `key=value` 扫描（引号/单引号/无引号三种值写法）；**坏 JSON 不丢原文**（退回 key=value，再不行就没有字段） |
| 解析器边界 | 名字走 `model.IsValidLogFieldName`（与来源名/模式名同一套"服务端与检索侧必须一致"的规则）；单行 ≤16 字段、值 ≤256 字节（按 UTF-8 边界截断）；**凭据类键名不进字段表**（`password`/`token`/`authorization`… 含嵌套的最后一段） |
| 落盘 | 字段与原文写在**同一条 JSON** 里（`model.LogHit.fields`）：不需要另建索引，检索仍是"顺序读 + 有界扫描"；老分片没有该字段 → nil，向后兼容 |
| 基数控制 | 每来源字段名上限 128（`MaxFieldNamesPerSource`）：超限后**只接受已见过的名字**，新名字被忽略而原文与已有字段照常落盘；目录落盘到 `<root>/field_names.json`（否则重启后候选变空、上限也被重置） |
| 检索 | `model.LogQuery.Fields`：扫描时逐行**精确等值**匹配，多条件为「与」；命中行带回 `fields` |
| 接口 | `GET /api/v1/logs?field=key:value`（可重复，形态与台账的 `label=key:value` 一致）；`GET /api/v1/logs/fields?sources=` 返回各来源**真的见过**的字段名（候选） |
| 前端 | 检索页新增字段过滤输入（回车成条件、可删、同字段只允许一个值）、候选字段名一键填入、结果里把字段作为 chips 贴在原文上方；深链支持 `?field=`（可重复） |

### 8.3 设计取舍

- **字段与原文同存而不是另建索引**：现有检索的全部保证（分片挑选、反向逐行、字节/行数预算、游标绝对偏移）原样适用；另建索引会同时引入"索引与分片不一致"这一类新缺陷，而 9-07 的百万行级性能要求需要独立立项（含索引一致性与删除语义），不该顺手带出来。
- **字段过滤是精确等值而不是模糊**：关键词已经覆盖模糊场景，字段的价值恰恰在"不误命中别的字段的值"。
- **`field=key:value` 而不是多个参数名**：与台账 `label=key:value` 同一形态，同一平台不该有两套写法。
- **候选字段只列"见过的名字"**：列"可能存在的字段"会让用户按一个永远查不到的名字去筛，然后怀疑功能坏了。

### 8.4 仍未做（本轮明确不做）

- **全文/字段倒排索引**与百万行级性能保证（9-07）：需要独立立项，含索引一致性、删除与压缩语义。
- **由日志内容直接建告警规则**（9-05）：现状是"日志指标 + 阈值规则"，从检索页一键建规则需要规则表单与日志字段的双向绑定。
- **把 Pod 日志接入集中检索**（8-07 的另一半）：仍需 `logship` 支持路径通配与 pod 维度标签注入。

### 8.5 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestExtractJSONObject` | `internal/server/logparse/parse_test.go` | 顶层标量 + 一层嵌套（点号）；长整数不退化成科学计数法；数组/null 跳过 |
| `TestExtractFallsBackOnBrokenJSON` | 同上 | 残缺 JSON 不丢原文：退回 key=value，扫不到则返回 nil |
| `TestExtractKeyValue` / `TestExtractKeyValueStopsAtWhitespace` | 同上 | 三种引号写法；无引号值到空白为止（避免吞掉后续字段） |
| `TestExtractSkipsSensitiveKeys` | 同上 | `password` / 嵌套 `access_token` / `passwd` 不进字段表，普通字段保留 |
| `TestExtractBounds` | 同上 | 非法字段名丢弃、值按 UTF-8 边界截断并留标记、每行字段数封顶 |
| `TestExtractNoStructure` | 同上 | 空行与纯文本不产出字段 |
| `TestAppendExtractsFieldsToDisk` | `internal/server/logstore/fields_test.go` | 字段与原文同落盘；无结构行无字段 |
| `TestQueryFiltersByField` | 同上 | 精确等值、多条件为与、不存在字段返回空（不退化成不过滤） |
| `TestFieldCatalogPersistsAndCapsCardinality` | 同上 | 名字封顶 128、重启后目录与上限都在、超限新名字被忽略而原文保留 |
| `TestFieldCatalogFileIsNotTreatedAsSource` | 同上 | 目录文件不被当成来源目录 |
| `TestLogsQueryByFieldFilter` | `internal/server/api/logs_fields_api_test.go` | 单条件/多条件命中与空结果；命中行带回字段 |
| `TestLogsQueryByFieldRespectsScope` | 同上 | 字段过滤不能绕过资源范围 |
| `TestLogsQueryFieldFilterValidation` | 同上 | 缺冒号/缺键/缺值/名字非法/值超长/条件过多一律 400 |
| `TestLogsFieldsEndpoint` | 同上 | 候选只含见过的名字；缺 `logs:read` 403 |
| LogsView 四条用例 | `web/src/components/LogsView.test.js` | 条件拼进查询、形态不对本地拒绝、同字段只允许一个值、候选点击填入、深链 `field` 生效且结果展示字段 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` **11 文件 64 项**通过；`npm --prefix web run build` 通过。

**未覆盖边界（不得视为通过）**：真实 Agent 上行的日志（含多行合并后的堆栈行、超长行、非 UTF-8 内容）在字段解析下的表现、大分片（百万行级）下字段过滤的扫描耗时、浏览器端到端。

---

## 九、第五轮：资产关系图 / 拓扑视图（2026-10-06 续四）

P0 首批的最后一项（全景表 §3.2「资产关系与拓扑」的拓扑视图 + §3.2「配置项模型页 / 关系视图页」的关系视图）。关系数据（`runs_on` / `member_of` / `depends_on` / `exposes`、来源标记、人工抑制）此前都已就绪，缺的是**可视化与导航**。

### 9.1 落地内容

| 层 | 内容 |
|---|---|
| 存储 | `Store.topologyAround(rootID, depth, maxNodes)`：**逐层 BFS** 展开邻域，返回节点（含跳数）、边与是否被上限截断；`assetsByIDs` 复用既有的 `assetSelectColumns` + `scanAssets`（列清单只有一处，不会与列表漂移） |
| 服务 | `Service.Topology(ref, depth, maxNodes, allowedNodes)`：**资源范围在这里裁剪**——范围外的节点与其相关边都不进结果集，中心不在范围内返回 `ErrOutOfScope`（与标杆条件写同一语义）；跳数 1..3、节点数 ≤500 一律夹紧 |
| 接口 | `GET /api/v1/assets/{id}/topology?depth=&limit=`（权限 `assets:read`，与 `/links` 同权限同口径）。节点带 `key`（`typeKey\|naturalKey`，与关联表格的寻址一致）、类型标题、状态、责任人、是否已隐藏；边带 `kind` 与 `source`（区分采集发现 / 人工维护） |
| 前端 | 详情抽屉「关联关系」页签新增「关系图」：ECharts 力导向图，节点按类型着色、大小按跳数（中心最大）、失联标红圈、已隐藏半透明；人工维护的边画虚线；**点节点即以它为中心重新展开**；跳数 1/2/3 可切；截断时显式告警 |

### 9.2 设计取舍

- **逐层 BFS + 硬上限，而不是递归 SQL**：一台跑了几十个实例的主机在两跳内就能连到全库；上限若在展开完成之后才生效，代价已经付过了。上限按**层**立即生效，被挡在门外的端点其边也一并丢弃（图里不该出现指向不存在节点的边）。
- **范围裁剪放服务层而不是 API 层"取回再过滤"**：范围外的节点一旦进入结果集，任何一处忘记过滤都会变成越权；让它们根本不出现才是唯一稳妥的做法。**只留一半的边也不行**——边本身携带对端的节点名与自然键。
- **节点身份用 `typeKey|naturalKey` 而不是自增 id**：台账界面一直以它寻址（关联关系的对端就是它），图与表用同一套标识才不会各说各话。
- **点节点=换中心**，而不是"打开另一个抽屉"：图上的"走下去"就是换中心，比在抽屉与弹窗之间来回切更贴合看图直觉。
- **截断必须显式告警**：静默省略会让人以为"关系就这么多"，从而漏掉真实影响面——那正是这张图存在的意义。

### 9.3 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestTopologyExpandsByDepth` | `internal/server/asset/topology_test.go` | 1 跳/2 跳的节点与边集合、中心标记与真实跳数；`depth=99` 夹紧到上限而不是报错 |
| `TestTopologyIsolatedAsset` | 同上 | 无关系资产：只有中心节点、零条边，不是错误 |
| `TestTopologyHandlesCycles` | 同上 | 互为依赖的环不死循环、不重复计节点 |
| `TestTopologyTruncatesAtNodeLimit` | 同上 | 达到上限即标截断、节点数不超限、**边不指向图里不存在的节点** |
| `TestTopologyRespectsScope` | 同上 | 范围外邻居与跨范围边都不出现；范围外/空可见集合的中心报 `ErrOutOfScope` |
| `TestTopologyHidesUnattributedNodesFromRestrictedUsers` | 同上 | 归属节点为空的资产对受限用户不可见、对全局可见 |
| `TestTopologyOrderIsStable` | 同上 | 两次查询节点与边顺序一致（前端图不会每次打开都重新洗牌） |
| `TestRoutes_AssetTopologyPayload` | `internal/server/api/asset_topology_api_test.go` | 载荷结构（root/nodes/edges）、节点类型标题与状态、边两端寻址与来源 |
| `TestRoutes_AssetTopologyClampsParams` | 同上 | `depth=99` 夹紧为 3（200 而不是 400） |
| `TestRoutes_AssetTopologyRespectsScope` | 同上 | 受限用户图里无范围外节点/跨范围边；范围外中心 404 |
| `TestRoutes_AssetTopologyPermissionAndMissing` | 同上 | 缺 `assets:read` 403；未知 id 404；未注入资产能力 503 |
| AssetListView 五条用例 | `web/src/components/asset/AssetListView.test.js` | 请求跳数正确；节点/边映射（中心更大、失联红圈、人工边虚线）；截断提示；点节点换中心（点中心不重查）；切跳数重查；失败给出错误而不是空图 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` **12 文件 69 项**通过；`npm --prefix web run build` 通过。

### 9.4 仍未做

- **配置项模型页**（`asset_types.schema` 的字段 Schema 编辑）：目前 `schema` 只存不解释。
- **全库关系视图**：当前以单个资产为中心（N 跳邻域），没有"整张图"的入口——那需要先解决布局与聚合（全库规模下力导向图不可读）。
- `depends_on` / `exposes` 仍**只有人工来源**：这是刻意的"关系宁少而准"，不是缺失。

**未覆盖边界（不得视为通过）**：真实集群规模下的图渲染性能（数百节点力导向）、浏览器端到端（真实点击 → 换中心 → 视觉确认）、大屏/暗色主题下的配色可读性。

---

## 十、第六轮：日志后端抽象与 VictoriaLogs 适配器（2026-10-06 续五）

P0 首批的最后一项（全景表 §3.9「外部日志后端」）。ADR-0002 的口径是**接口与第二适配器同时落地**（只有一个适配器时抽接口是"假想接缝"），因此本批同时交付接口、适配器、配置与契约用例。

### 10.1 落地内容

| 层 | 内容 |
|---|---|
| 接口 | `logstore.LogStore`：`Append(model.LogBatch)` / `Query(model.LogQuery, Cursor)` / `Backend()` / `Sources()` / `FieldNames()`。签名按**实际调用面**落地，修正 ADR 初稿（初稿写 `Write([]model.LogEntry)`，但上行用的是 `Append` + `LogBatch`，且限额丢弃是正常结果不是错误） |
| 游标 | `Cursor` 增加 `Backend`：跨后端重放**必须被拒**（偏移语义不同，硬用会静默跳行）。零值游标（从头开始）任何后端都接受；有偏移但无标识的历史游标只对本地后端有效 |
| 适配器 | `logstore.VictoriaLogs`：写入 `/insert/jsonline`（NDJSON，`_stream_fields=node,source`，时间用 RFC3339 避免单位歧义）、检索 `/select/logsql/query`（LogsQL 翻译 + JSON Lines 解析 + 客户端归一排序）、元数据 `field_names` / `stream_field_values` |
| 配置 | `logBackend: local\|victorialogs` + `logVictoriaLogs.{addr,queryTimeout,writeTimeout}`；**后端名写错即启动失败**（静默降级会把配置笔误变成"日志功能消失了"，而日志恰好是排障时才去看的东西） |
| 装配 | `cmd/server`：工厂选后端；receiver 与 api 共用同一实例（不会"写到 A、读 B"）；retention 只对自研落盘生效，外部接管时**显式声明** |
| 错误分类 | `logstore.ErrBackendUnavailable` → 接口回 **502**、Agent 上行回 502。运维看到 400 会去改查询条件，而真实情况是"日志后端连不上"——错误码指错方向比不给错误码更费时间 |
| 前端 | 保留策略页：外部后端下显示"由该后端负责（如 VictoriaLogs 的 -retentionPeriod）"，保留天数标注"不生效" |

### 10.2 关键设计取舍

- **方言按一手文档写，不靠记忆**：2026-10-06 取官方 Querying / LogsQL / Data Ingestion 三页核对（`/select/logsql/query` 的参数与 JSON Lines 响应、`_time` 语义、`limit`+`offset` 分页、`/insert/jsonline` 的 `_stream_fields`/`_time_field`/`_msg_field`）。
- **检索语义逐条对齐本地后端**（这是抽象的全部意义）：
  - 关键词是**子串** → 正则过滤器 `~"QuoteMeta(kw)"`（不用 `*x*`：含空格/引号/通配符时拼接极易出错）；两层转义（先转正则元字符、再转 LogsQL 字面量）。
  - 字段是**精确等值** → `field:="v"`。**绝不用 `field:v`**——那是按词匹配（`status:500` 会命中 `status:5000` 那种"看起来对、其实是别的行"的结果）。
  - 字段名含连字符/点号时**必须加引号**：`-` 在 LogsQL 里是取反运算符，`status-code:="500"` 会被解析成 `status` 且非 `code:="500"`——语法合法、结果全错。
  - 时间闭区间 → `end = To+1ms`：后端 `end` 是**开区间**，少补 1ms 会静默丢掉"到某一毫秒为止"的那一行。
  - 字段**读取时从正文重新提取**（而不是采信后端返回的顶层字段）：后端里的字段可能来自别的写入工具，重新提取才能保证两个后端的 `Fields` 完全一致。
  - 保留字段（`_time`/`_msg`/`node`/`source`/`pattern`）**不可被正文覆盖**：否则一条日志就能伪造自己的来源与节点。
- **如实暴露能力差异**（不制造"能力对齐"的假象）：外部后端没有逐文件扫描，三项扫描诊断就返回 0（界面只在"没命中"时展示诊断，因此不会显示成"扫了 0 个文件"的假象）；单来源每日上限不适用（容量归后端），但上行的限速与请求体上限仍然生效（在 receiver 侧）。
- **元数据接口失败不拖垮检索**：候选列表拿不到就返回空列表（界面退化成手动输入字段名），而不是让整个检索页报错。

### 10.3 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestBackendContract_*`（10 组） | `internal/server/logstore/backend_contract_test.go` | **同一套断言跑两个适配器**：全量检索与时间倒序、字段提取一致、关键词子串（且不匹配模式名）、正则优先于关键词、字段精确等值（`500 timeout` 不得被 `500` 命中）、节点/来源/叠加过滤、**闭区间时间**（`From==To==某行` 必须返回）、分页不重不漏且截断显式、游标跨后端被拒、目录候选口径、写入校验一致、后端标识唯一 |
| `TestBuildLogsQL`（13 组） | `internal/server/logstore/victorialogs_test.go` | 空条件→`*`；子串→正则；正则优先；元字符/引号/反斜杠/控制字符的**两层转义**；`field:=`（不是 `field:`）；含连字符与点号的字段名加引号；多条件显式 AND + 括号；字段顺序稳定 |
| `TestVictoriaLogs_AppendRequestShape` | 同上 | `_stream_fields=node,source`、时间/消息字段绑定、时间用 RFC3339、结构化字段平铺、**正文不得覆盖保留字段** |
| `TestVictoriaLogs_QueryRequestShape` | 同上 | `limit=Limit+1`（多取一行判断截断）、首页无 `offset`、续读同时带 `limit`+`offset`、`end = To+1ms`、`start = From` |
| `TestParseVLTime`（11 组） | 同上 | RFC3339（秒/纳秒/带时区偏移）、Unix 秒/毫秒/微秒/纳秒（按量级判断）、字符串数字、空值/非法值/类型不符 |
| `TestParseVLLines` | 同上 | 坏行与无时间戳的行跳过（不整页失败）、字段从正文重新提取、时间戳精确解析 |
| `TestVictoriaLogs_ErrorClassification` / `TestVictoriaLogs_BackendUnavailableIsClassified` | 同上 / 契约文件 | 5xx 与网络失败归类为 `ErrBackendUnavailable`，且**不得报告"已接受行"**（否则丢日志在指标上完全看不见） |
| `TestNewVictoriaLogs_ValidatesAddr` / `TestNewBackend` | 同上 | 地址校验（空/缺 scheme/末尾斜杠）、未知后端名报错、本地未配置目录返回**真正的 nil 接口**（不能是"非 nil 接口"，否则 503 判断失效） |
| `TestVictoriaLogs_CatalogsDegradeGracefully` | 同上 | 元数据接口失败时返回空候选而不是让检索不可用 |
| `TestRoutes_LogsQueryBackendUnavailable` | `internal/server/api/logs_backend_test.go` | 后端不可用 502、查询问题 400、正常 200 |
| `TestManager_ExternalLogBackendIsReported` | `internal/server/retention/retention_test.go` | 状态报告 `logsBackend`；清理结果说明"由外部后端负责"且不报删除量；注入本地存储后回到 local 口径 |
| RetentionSubView 三条 | `web/src/components/settings/RetentionSubView.test.js` | 本地口径展示文件/分片；外部后端说明由后端负责且保留天数不生效；后端标识缺失（旧服务端）时按本地口径、不误报外部后端 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` **13 文件 72 项**通过；`npm --prefix web run build` 通过。

### 10.4 外部后端联调清单（**2026-10-06 已在真实实例上完成**）

联调环境：dev-server（124.223.77.206），VictoriaLogs **v1.53.0**（linux-amd64，官方发布包），
用新增的 `deploy/install-logs.sh` 离线安装为 `victoria-logs.service`（`-storageDataPath=/var/lib/victoria-logs-data`、`-httpListenAddr=:9428`、`-retentionPeriod=7d`）；Server 1.30.34 把 `logBackend` 切为 `victorialogs` 后重启。**联调结束已恢复 `local` 并停用 VL**（安装保留，`systemctl enable victoria-logs` 可再启用）。

| # | 项 | 结果与判据 |
|---|---|---|
| 1 | 写入 | **通过**。VL 自报 `vl_rows_ingested_total{type="jsonline"}=13`、`vl_bytes_ingested_total=3646`、`vl_rows_dropped_total{reason="too_many_fields"}=0`；样本行含 `_time`/`_msg`/`_stream` 且 `_stream={node="VM-0-10-ubuntu",source="nginx_access"}`（`_stream_fields=node,source` 生效）；JSON 文本行解析出的 `level`/`status`/`path` 均作为字段落下 |
| 2 | 时间 | **通过**。取一条真实日志的毫秒时间戳 T：`to=T` 时命中（闭区间含端点）、`to=T-1ms` 时不命中——`end=To+1ms` 的换算被双向证实 |
| 3 | 分页 | **通过**。`limit=1` 首页给游标（解出 `{"b":"victorialogs","k":1}`，带后端标识）；第 2 页与第 1 页不重复、时间倒序；两页合并**逐条等于**直接 `limit=2` 的结果（不重不漏） |
| 4 | 方言 | **通过**。关键词（子串）、正则（裸模式 `status.{0,4}500`、`(?i)LEVEL`）、`sources`、`field=source:nginx_access`、`nodes` 五种形态均被真实解析器接受并返回正确命中数；`regex` 传带斜杠的 `/x/` 不匹配属**预期**（界面约定是裸模式，占位提示"正则，如 disk\|oom"） |
| 5 | 元数据 | **通过**。`POST /select/logsql/stream_field_values` 与 `/field_names` 实测返回 `{"values":[{"value":...,"hits":N}]}`；平台 `/api/v1/logs/fields` 在字段写入后列出 `{"nginx_access":["level","path","status"]}` 且**不泄露** `node/source/pattern` 等流字段；`field=status:500` 恰好命中 1 条并带 `fields`，`field=status:404` 命中 0 条 |
| 6 | 容量 | **通过**。`systemctl show` 证实 `-retentionPeriod=7d`（与平台自研落盘默认 7d 一致）；平台 `/system/retention` 自报 `logsBackend="victorialogs"`；执行一次清理后 `logDirsRemoved=0 / logFilesRemoved=0`，且 `/var/lib/monitor-server/logs` **逐字节未变**（外部后端的数据平台不碰） |
| 7 | 部署 | **通过**。`build/fetch-packages.sh` 已纳入 VL（三架构，可选组件失败不阻断发布）；`deploy/install-logs.sh` 在实机完成"离线包扫描 → 解压装二进制 → 写 systemd → 存活 `/health` 与查询 `/select/logsql/query` 双健康检查"；`deploy/uninstall.sh --logs`、`build/release.sh` 的 full 包与 `install.sh logs` 子命令配套齐 |
| 8 | 故障演练 | **通过**。停 VL 后检索回 **502**（`日志后端不可用: ... dial tcp 127.0.0.1:9428: connect: connection refused`）而非 400；Agent 上行同样回 502（Agent 会重试并计入丢弃指标）；**重启 VL 后无需重启 Server 即恢复 200**（适配器每次请求新建客户端） |

**联调中的发现（已处理或已记录）**：

1. **写入到可检索有秒级延迟**：上行成功后立刻查询可能查不到，数秒后才可见（VL 的内存数据 flush 周期）。**不是缺陷**，但必须在文档里写清——现场看到"刚上报的日志搜不到"极易判成"日志丢了"。判定方法：用 `vl_rows_ingested_total` 确认已落库，再等几秒重查。
2. **接收端的 `source not declared by node` 守卫在联调中被触发**：构造测试上行时用了节点未声明的来源，被如实拒绝（400）。这是既有守卫的正确行为，记录以免下次误判成"写入缺陷"——验证字段链路时要用节点**已声明**的来源。
3. **`ProbeTSDBRetention` 对真实 VictoriaMetrics 必然失败（已修）**：VM 的 `/flags` 是**纯文本**（`-flag="value"` 逐行、`text/plain`、只列显式设置过的参数），而实现按 JSON 解析，于是保留策略页的 TSDB 一段一直显示"解析 /flags 失败: invalid character 'h' in numeric literal"。原单元测试用的是自造的 JSON 响应，恰好把不一致盖住了。已改为解析纯文本并补 `parseTSDBFlagLine` 边界用例，测试数据改用**真实格式**。该机 VM 未显式设保留期，修复后的预期展示是"时序库未显式暴露保留参数，当前保留期由其后端默认值决定"（需随下一次 Server 升级生效）。

**未覆盖边界（不得视为通过）**：VL 多副本/集群版与多租户（`AccountID/ProjectID`）路径、大流量下的写入吞吐与背压、字段基数爆炸场景、`v1.53.0` 以外的版本、浏览器端到端（真实点击检索/翻页/字段筛选的视觉确认）。

### 10.5 仍未做

- **全文倒排索引**（9-07 的另一半）：字段过滤仍是**有界扫描**；引入外部后端是"换存储"，不是"建索引"。
- ~~`build/fetch-packages.sh` / `deploy/install-tsdb.sh` 的脚本改动~~ → **已于 2026-10-06 落地**：`build/fetch-packages.sh` 纳入 VL（三架构，可选组件失败不阻断发布）、新增 `deploy/install-logs.sh`（独立脚本，不塞进 `install-tsdb.sh`——那个脚本的 `--backend` 语义是"指标时序库四选一"，VL 是并存的日志存储）、`deploy/install-server.sh` 新增 `--log-backend/--log-addr/--log-dir` 并写入 `server.yaml`、`deploy/uninstall.sh --logs`、`build/release.sh` 的 full 包与 `install.sh logs` 子命令。

---

## 十一、第七轮：§五 P1 候选四项（2026-10-06 续六）

P0 首批落地后，按全景表 §五「建议的 P1 候选」推进。本批完成四项，并对两项给出**不做**的理由。

| 项 | 全景表 | 结果 |
|---|---|---|
| 告警 ↔ 资产 ↔ 变更 ↔ 波及范围 | §3.1 | 已实现 |
| 容器/日志 ↔ 资产联动（日志侧） | §3.8 | 部分实现 → 日志侧联动落地（Pod 日志进检索仍待 Agent 协议扩展） |
| 由日志检索条件建规则 | §3.9 | 已实现 |
| 巡检周期化调度（自动生成报告） | §3.11 | 已实现 |
| 日志字段倒排索引 | §3.9 | **不做**（需独立立项，见 §11.5） |
| 批量命令/脚本执行 | §3.4 | **不做**（与平台既定边界冲突，见 §11.5） |

### 11.1 告警 ↔ 资产 ↔ 变更 ↔ 波及范围

- **接口**：`GET /api/v1/alerts/impact?node=&instance=&depth=`（权限 `assets:read`）。用**指标标签**寻址而不是告警 ID：告警恢复后就从活跃列表消失，而排障往往发生在之后。
- **解析规则**：主机按名**忽略大小写**匹配（`GetHostByName`：Agent 上报的 hostname 与台账规范键可能只差大小写，严格比对会把同一台机器判成"没有资产"）；实例按**地址后缀**匹配（告警只带实例标签、不带中间件类型，而模板派生类型的指标名与类型 key 不一一对应——猜类型会命中**另一条**资产）。命中多条时**全部返回**，由界面区分。
- **三件事**：命中资产 + 该资产近期变更（合并多资产后重排、条数有上限）+ 波及范围（以**主机**为中心，主机才是"这台机器出事会影响谁"的起点）。
- **口径**：波及范围是**关系邻域**（runs_on / member_of / 人工 depends_on 展开），**不是**从指标推断的依赖图——平台不猜测拓扑。
- **范围与降级**：范围外资产不出现；没匹配到资产时返回 200 + **可操作的说明**（"节点还没上报""实例地址写法不一致"是三件不同的事），而不是 404 或空列表。
- **实现细节**：关系图载荷抽成 `assetTopologyPayload` 供 `/assets/{id}/topology` 与影响面**共用**（两处的范围裁剪与字段口径必须一致，否则同一张图在两个入口会给出不同可见范围）。

### 11.2 日志 ↔ 资产联动（Server 侧绑定，**不改 Agent**）

- **声明放在资产侧**：人工属性 `logSource`（与 `owner` 同族的约定键）。理由：日志行只带 (source, node)，而"这个来源属于哪条资产"是运维的声明、不是采集能推断的事实；放资产上则 Agent 与日志协议都不用改（改协议意味着全部 Agent 重分发）。**只认人工值**（采集侧不可能上报它）。
- **反查**：`GET /api/v1/logs` 响应新增 `assets` 映射（键 `<source>|<node>`），只解析**本页命中行**出现过的组合（数量受页大小约束，避免把一次检索变成全表扫描），范围裁剪与其它入口同口径；任一步失败只降级为"这一页没有映射"，绝不让"日志查到了"变成"页面报错"。
- **前端**：来源列显示资产标签（点击跳台账）+「只看该资产」（= 节点 + 来源两个筛选，因此**不需要**新的查询参数）。
- **节点参与匹配**：同一个来源名在多台机器上都会配置，只按来源名找会把所有机器混成一条——那比"找不到"更糟。

### 11.3 由日志检索条件建规则

- **接口**：`GET /api/v1/logs/rule-template?source=&pattern=&node=` 返回一条可直接保存的 `model.AlertRule`。
- **为什么由服务端拼指标名**：日志模式的指标名是 `<来源>_log_<模式>_total`，**拼错的症状是"规则配好了却永远没有数据"**——没有任何一处会报错。为此把拼法收敛到 `model.LogPatternMetricName`，并让 **Agent 侧产出指标也改用它**（两边逐字同源）；来源名/模式名非法即 400（模式名会拼进指标名，这正是 `LogPatternNamePattern` 存在的原因）。
- **不新增规则类型、不改告警引擎**：底层就是既有日志指标 + `RuleTypeThreshold`。
- **范围**：从某台机器的日志建规则时把规则范围收到那台（`scope=specified`），避免同名来源的其它机器一起触发。
- **前端**：「建规则」按钮按 `alerts:write` 显示（UI 反映权限），跳到告警页由那边取模板并打开表单（表单只有一处实现）。

### 11.4 巡检周期化调度（自动生成报告）

- **`Scheduler`**：配置与**运行状态同文件**（`report_schedule.yaml`）。运行状态必须跨重启保留——否则频繁重启的机器每次启动都会重新生成一份报告，而报告要拉一整个周期的数据。
- **默认关闭**；启动时**不**立即生成；改配置**热生效**（每分钟检查一次"是否到点"，而不是用 `time.Ticker(interval)`——后者改配置不生效，与平台"Web 可编辑配置保存即热生效"的约定不符）。
- **失败必须可见**：生成失败写进结果与状态（界面提示"上次自动生成失败"），同时写日志。
- **手动生成与自动生成共用 `RunNow`**：否则界面上会出现"我明明刚生成过，调度却说到点该生成了"。
- **接口**：`GET /api/v1/report/schedule`（`report:read`）、`PUT /api/v1/report/schedule` 与 `POST /api/v1/report/schedule/run`（`report:export`——它们都会产出报告文件、占用磁盘）。
- **不做通用定时任务**：平台里需要定时的只有报告与数据保留（后者已有自己的循环）；为两条用例引入 cron 表达式解析 + 任务注册表，收益只是"看起来更通用"，代价是多一套要维护与排障的调度器。

### 11.5 明确不做的两项

- **日志字段倒排索引**（§3.9）：9-07 原文要求的"百万行级 + 索引一致性"是独立立项的规模（索引格式、与分片生命周期的一致性、与外部后端的口径差异）。本批不引入半套索引——**半套索引比没有索引更危险**：它会让"查不到"变成"以为数据不全"。
- **批量命令/脚本执行**（§3.4）：与平台既定边界冲突（"不提供任意脚本或在线终端作为默认扩展方向"），且用户 2026-09-26 已明确暂缓自动化处置与自愈。此处**不推进**，需要时按独立设计件（护栏 + 审批 + 审计）启动。

### 11.6 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestRoutes_AlertImpact*`（5 组） | `internal/server/api/alert_impact_api_test.go` | 主机+实例同时命中、变更带资产身份、波及范围以主机为中心、只给一个标签可用、未命中给出**可操作说明**、范围外不出现且不给范围图、参数/权限/未启用 |
| `TestInstancesByAddr` | `internal/server/asset/topology_test.go` | 同地址不同类型全部返回；`_` 按字面匹配（不转义会捞进 `dbX1:6379`）；空/未知返回空 |
| `TestAssetsByLogSource` / `...IgnoresDiscoveryValues` | `internal/server/asset/logsource_test.go` | 来源+节点精确命中、同名来源多候选全返回、节点不匹配不命中、**只认人工值** |
| `TestRoutes_LogsQueryReturnsAssetMapping` 等 3 组 | `internal/server/api/logs_asset_api_test.go` | 映射键与内容、未声明来源不凭空出现、未注入台账时检索照常且无映射字段、范围外不出现 |
| `TestRoutes_LogRuleTemplate*`（3 组） | 同上 | 指标名 `applog_log_err_total`、默认条件与范围、非法来源/模式名 400、权限 |
| `TestScheduler*`（8 组） | `internal/server/report/schedule_test.go` | 非法值校正、跨重启保留配置、**失败写进结果与状态**、未启用/未到间隔不生成/到点生成、下次时间只在启用时给、运行状态不被保存覆盖、ctx 取消退出、nil 安全 |
| `TestRoutes_ReportSchedule*`（4 组） | `internal/server/api/report_schedule_api_test.go` | 读/存/立即生成、非法请求体 400、失败 502、权限分离、未注入 503 |
| LogsView 资产联动 3 组 | `web/src/components/LogsView.test.js` | 资产标签渲染与「只看」（节点+来源）、点击跳台账、建规则按权限显示并带来源/模式/节点 |
| AlertsView 影响面 3 组 | `web/src/components/AlertsView.impact.test.js` | 打开详情带 node+instance 请求影响面、抽屉内展示命中资产/变更/波及范围、未命中给原因 |
| ReportView 周期化 6 组 | `web/src/components/ReportView.test.js` | 上次运行与下次时间、从未运行如实说明、失败显式提示、503 如实说明、保存失败回读真实状态、立即生成刷新历史 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿；`npm --prefix web test` **15 文件 84 项**通过；`npm --prefix web run build` 通过。

### 11.7 仍未做

- ~~**Pod 日志进入集中日志检索**（§3.8 的另一半）~~ → **已于 2026-10-06 落地**（见 §十二）：实现路径与当初的设想不同——**不从 Agent 上报路径、也不需要 Server 解析路径**，而是采集侧就地解析出容器身份随批次上报（只上报身份）。仍然需要 Agent 升级（新配置项 `podLogs` 与新字段），但**协议只加了一个可选字段**，旧 Server 收到会忽略它、旧 Agent 不升级则行为不变。
- **通知消息里带资产与影响面**（§3.1 的延伸）：通知模板目前只有事件字段与 labels；要在通知里写"影响 N 个资产"，需要先定义资产信息的模板占位符与截断口径。
- **日志字段倒排索引**、**批量命令/脚本执行**：见 §11.5。
- ~~上一批的联调清单（§10.4）仍未执行~~ → **已于 2026-10-06 在真实 VictoriaLogs v1.53.0 上执行完毕**（八项全过，见 §10.4）。

---

## 十二、第八轮：Pod 日志进集中日志检索（2026-10-06 续七）

§3.8 的另一半。此前标注「需 Agent 协议扩展、属独立立项」，本批做完并明确了一条**更小的**协议扩展路径。

| 层 | 内容 |
|---|---|
| 配置 | `logSources[].podLogs: true`（默认 false）；开启时**启动期校验** paths 落在 `/var/log/pods/` 下（身份从路径解析，路径不在那里就必然解析不出身份） |
| 采集 | paths 支持通配符（`/var/log/pods/*/*/*.log`）；单轮文件数上限 200 且截断可见；通配符零匹配不算失败；pod 来源清理已消失文件的偏移 |
| 协议 | 批次级 `model.LogBatch.Origin{namespace,pod,container}`；**只上报解析出的身份，不上报路径**；`model.NormalizeLogOrigin` 采集侧与服务端共用同一条规则 |
| 落库 | 本地后端随行写同一条 JSON；VictoriaLogs 写 `k8s_namespace`/`k8s_pod`/`k8s_container` 并**列入保留字段**（正文不得覆盖身份），且不进字段目录 |
| 检索 | `pods=<命名空间>\|<Pod>` 过滤（可重复，服务端上限 8 个）；命中行带 `origin`；两个后端语义一致（没有身份的行不命中任何容器条件） |
| 联动 | 响应新增 `podAssets`（键 `<命名空间>\|<Pod>`）；`Store.assetsByPodIdentity` 按「节点 + 命名空间 + Pod 名」反查，**类型必须是 pod**、节点为空时不匹配空串 |
| 前端 | 行上展示「命名空间/Pod · 容器」并标到 Pod 资产（与主机来源映射分开展示，两套归属不串台）；「只看」按容器收窄（节点+来源+容器）；生效中的容器条件可删 |

### 12.1 设计取舍

- **身份从文件路径解析，而不是读 K8s API 或按容器 ID 反查**：采集侧读的就是那台机器上的文件，"这个文件属于哪个 Pod"是本地事实——不需要 Agent 持有集群凭据（与只读管理面走 kubeconfig 是两条独立路径），也没有 API 往返与缓存失效问题。代价是解析规则绑定 kubelet 的目录布局，因此开启时**启动期校验路径**，把"配错了却没有任何提示"堵在配置期。
- **只上报身份、不上报路径**：路径会暴露被监控机的目录结构，而检索与联动只需要"属于哪个 Pod"。这也让协议扩展只有一个可选字段（`LogBatch.Origin`）：旧 Server 忽略它、旧 Agent 行为不变。
- **身份是协议字段，不是业务字段**：独立过滤参数 `pods=`（不让它走 `field=` 通道，那等于把"哪些名字是身份"交给用户去记）；VL 侧写成**保留字段**——否则一条日志的正文就能伪造自己的归属（把自己标到别人的 Pod 资产上）；也不进字段目录（避免污染界面的字段筛选候选）。
- **批次级携带身份**（一批 = 一个文件）：同一文件的行必然同属一个容器，逐行重复既费流量又给"同批出现两个身份"留口子。
- **非法身份回 400 而不是"丢掉身份照收"**：定位不到资产是可接受的降级，**定位错**不是；而合法 Agent 永远不会发出非法身份。
- **通配符零匹配不算失败**（`up` 保持 1）：Pod 是短命的，此刻没匹配到文件是正常状态；记成离线会让"日志采集不可用"在每次缩容时误报，最后没人再看这个信号。
- **只对 pod 来源清理偏移**：容器日志路径带 Pod UID，文件回收后永不再出现，留着会让偏移文件无界增长（而它每轮都要整体重写）；普通文件可能轮转后重建，删偏移会静默跳过开头内容——那是丢日志。
- **容器资产反查不用自然键**：Pod 自然键含**集群**，而容器日志路径里没有集群标识；「节点 + 命名空间 + 名字」在同一节点上足以唯一定位（同名多候选只在"同机多集群"时出现，调用方原样呈现候选）。

### 12.2 本轮新增用例与执行证据

| 用例 | 文件 | 断言 |
|---|---|---|
| `TestParsePodLogPath`（14 组） | `internal/agent/collector/podlogpath_test.go` | 标准布局/重启序号/带点号 Pod 名/大写折叠；缺容器目录、多一层、下划线分段不符、文件名非序号、空路径、非法字符一律解析失败 |
| `TestExpandLogPaths` / `TestExpandSourcePaths_CapsFileCount` | `internal/agent/collector/podlogs_test.go` | 无通配符原样返回（既有配置零行为变化）、通配符结果有序、非法模式不致命；文件数被夹到上限且**回报跳过数** |
| `TestLogCollector_PodOriginPerFile` | 同上 | 一个来源匹配到多个 Pod 文件时**每个文件带自己的身份**、每文件各一批 |
| `TestLogCollector_PrunesVanishedPodOffsets` / `...NonPodSourceKeepsOffsets` | 同上 | pod 来源清理已消失文件偏移；非 pod 来源**不清理**（轮转重建不能丢开头） |
| `TestLogCollector_GlobZeroMatchKeepsUp` | 同上 | 通配符零匹配时 `up=1` |
| `TestNormalizeLogOrigin`（17 组）/ `TestLogOriginEmpty` | `internal/model/log_test.go` | 合法/规范化/全空视为无身份；缺一段、下划线、斜杠、空格、中文、首尾非法、超长一律拒绝 |
| `TestLogSources_*`（podLogs 两组 + YAML） | `internal/agent/config/config_logs_test.go` | podLogs 路径不在容器日志目录即拒绝；`podLogs` 的 YAML tag 真的能解析出来（写错会静默变 false） |
| `TestBackendContract_LogOrigin` | `internal/server/logstore/origin_contract_test.go` | 身份落得住读得回、**正文里的 `k8s_pod` 不得变成行上身份**、非法身份两个后端都拒 |
| `TestBackendContract_PodFilter` | 同上 | 单个容器精确命中（同名不同命名空间不算）、多容器「或」、**没有身份的行不命中**、不存在的容器无命中 |
| `TestHandleLogs_RejectsInvalidOrigin` | `internal/server/receiver/logs_test.go` | 非法/半截身份 400；合法身份 200 且**按小写规范化落盘** |
| `TestAssetsByPod`（7 组）/ `...RequiresPodType` | `internal/server/asset/podidentity_test.go` | 节点+命名空间+名字精确命中、不指定节点返回全部候选、节点/命名空间/名字不匹配为空、空值为空、**同名主机资产不算 Pod** |
| `TestRoutes_LogsQueryReturnsPodAssetMapping` / `...PodMappingIsOptional` | `internal/server/api/logs_asset_api_test.go` | 行带 `origin`、`podAssets` 键与内容、未注入台账时日志照常返回 |
| `TestRoutes_LogsQueryPodFilter` | 同上 | 单容器/多容器过滤、命中行带身份、非法形态 400（不退化成"不限"） |
| LogsView 容器日志 3 组 | `web/src/components/LogsView.podlogs.test.js` | 展示身份与 Pod 资产并跳台账、普通文件行仍走主机映射（不串台）、「只看」拼出 `pods=` 且条件可删 |

**执行结果**：`go build ./...`、`go vet ./...` 通过；`go test -count=1 ./...` 全绿（30 包）；`npm --prefix web test` **16 文件 87 项**通过；`npm --prefix web run build` 通过。

### 12.3 仍未做 / 未覆盖边界

- ~~**未在真实 k3s 集群上端到端验证**~~ → **已于 2026-10-07 在 dev-server 的真实 k3s 上完成**（见 §12.4），期间发现并修复了一个**只在真实 kubelet 文件上才暴露**的缺陷（CRI 框架未剥离）。
- **容器过滤不含 `container` 维度**：排障时关心的是"这个 Pod 怎么了"，同 Pod 多容器一起看才有上下文；按容器收窄留作需要时再加。
- **`/var/log/containers/*.log`（docker 时代的软链目录）未支持**：只认 kubelet 的 `/var/log/pods/` 布局（软链名不含 UID，解析规则不同）。用软链路径配置会被解析成"没有身份"并给出告警，而不是猜。
- **Pod 资产与日志行的时钟/命名空间一致性**：身份靠名字匹配，若台账里的 Pod 名与 kubelet 目录里的名字写法不一致（理论上不会），表现为"有身份但没有资产标签"（不报错）。

### 12.4 实机验证（2026-10-07，dev-server 真实 k3s）与发现的 CRI 缺陷

**环境**：dev-server（124.223.77.206）上真实 k3s（coredns/metrics-server 在线），测试 Pod
`nebula-podlog/logtest` 每 3s 往 stdout 打一行 `ERROR podlog-e2e tick=N`；Agent 侧加一段 `podLogs` 来源
指向 `/var/log/pods/nebula-podlog_logtest_*/logtest/*.log`。Server 1.30.36 + Agent 1.30.37（仅 Agent 含本批修复）。

| # | 判据 | 结果 |
|---|---|---|
| 1 | 通配符匹配真实文件 | **通过**。Agent 启动后逐文件打印"首次采集，从文件末尾开始"，路径为 `/var/log/pods/nebula-podlog_logtest_631f4919-…/logtest/0.log` |
| 2 | 身份随行上报 | **通过**。每行 `origin={namespace:"nebula-podlog",pod:"logtest",container:"logtest"}`（注意 `origin` 是**对象**，不是字符串） |
| 3 | 标到 Pod 资产 | **通过**。响应 `podAssets: {"nebula-podlog\|logtest":[{id:50,typeKey:"pod",name:"logtest",node:"VM-0-10-ubuntu",status:"online"}]}` |
| 4 | 按容器收窄 | **通过**。`pods=nebula-podlog%7Clogtest` 命中；不存在的 Pod 名 0 条 |
| 5 | 指标 | **通过**。`podlog_log_up=1`，无丢弃序列 |
| 6 | 无解析告警 | **通过**。Agent 未报"路径不符合容器日志格式" |
| 7 | **时间取行内值** | **通过**（决定性判据）。平台返回的 `ts` 与容器日志文件里的行内时间戳**逐毫秒相等**：`1791329428636` == `2026-10-07T07:30:28.636355229+08:00`（5/5 行比对一致） |
| 8 | 普通文件来源不受影响 | **通过**。`sources=nginx_access` 的行未被误当 CRI 行改写 |

**发现的缺陷（已修，1.30.37）**：采集侧**完全没有解析 kubelet 加的 CRI 框架**——容器日志行不是应用那一行，
而是 `<RFC3339Nano 时间> <stdout\|stderr> <F\|P> <应用正文>`。实机症状两条：正文带着
`2026-10-07T07:29:43.613118896+08:00 stdout F ` 噪声；时间只能用"采集时刻"，于是**同一轮采集的行挤在同一个毫秒上**。
实测对照（同一台机、同一个 Pod，仅换 Agent 二进制）：

| | 换装前（1.30.36） | 换装后（1.30.37） |
|---|---|---|
| 一轮内多行的时间戳 | **15 行只有 3 个时间戳**（每轮 5 行共用一个采集毫秒） | **每行各自一个**，25 行 25 个 |
| 相邻行间隔 | 轮内为 0，跨轮 = 采集周期 | **3002/3001 ms**（= 应用 3s 打点） |
| 与文件行内时间的关系 | 无关（最大差约一个采集周期） | **逐毫秒相等** |

修复要点：新增 `parseCRILogLine`（只对 `podLogs` 来源调用），剥框架后**用行内时间**、模式匹配作用于正文；
普通文件来源零行为变化。用例 `criline_real_test.go` 的夹具**逐字取自真实 kubelet 文件**（9 位纳秒 + `+08:00` 偏移、
正文自带一个 UTC 时间——正是"取第一个空格之后"就会写错的地方），含 14 组边界；另有 `k8s_logs_test.go` 覆盖
"剥框架 / 普通文件不改写"两种采集行为。

**一条必须写进运维文档的性质**：**升级前已入库的行不会被重写**。所以现场核对时要按"Agent 换装时刻"划界——
换装前的行仍带框架、且同轮共用一个时间戳（历史数据的形态），换装后的行才干净。实测分界：带框架的最大
`ts=1791329384003` < 干净的最小 `ts=1791329386614`，无重叠。

**未覆盖边界（不得视为通过）**：多容器 Pod 与同一 Pod 的多个重启序号文件、`P` 标记跨行拼接的实际形态、
日志驱动非 `json-file`（如 `journald`）时的落盘路径、超大 Pod 日志文件下的偏移推进、浏览器端到端
（真实点击"只看该容器"并视觉确认资产跳转）。

---

## 十三、第九轮：容器页 → 集中日志检索（2026-10-07）

收 §7.4 的欠账。批次 3（§十二）把容器日志送进了集中日志检索，但**容器页只连到按需拉取那一条**：
Pod 行上的「日志」是 `container.logs` 现拉的（200 行 / 24 小时封顶），而平台里那份可翻页、带字段与
资产联动的集中日志，在容器页**没有任何入口**。本批把 §4.4 的三条路径在界面上接起来。

| 层 | 改动 |
|---|---|
| 检索页 | `applyDeepLink` 认领 `pods=`（可重复）；形态校验与服务端 `parseLogPodFilters` 同规则，非法项丢弃、超 8 个截断 |
| 容器页 | Pod 行「检索日志」+ 日志抽屉「在集中日志中检索」→ `/logs?pods=<命名空间>\|<Pod>`；操作列宽度按按钮数给 |
| 权限 | 两处入口都按 `logs:read` 门控；无权时空态提示不提该路径 |

**执行结果**：`npm --prefix web test` 16 文件 **93 项**通过（新增 6 项）；本批未动后端
（`go build`/`go vet`/`go test ./...` 仍全绿）。

**关键取舍**：① 不带节点（容器页不知道 Pod 此刻在哪台机器上，猜错就是空结果）；
② 不带时间窗（用检索页自己的默认，避免同一概念两处各有一套默认）；
③ 形态校验提前到前端——与服务端同规则地挡一道，让手改坏的 URL 不会把整个查询打成 400（那是一片报错，
而问题只是地址里多了个字符）。

**未覆盖边界（不得视为通过）**：浏览器端到端（真实点击 → 跳转 → 结果视觉确认）、
目标节点**未开** `podLogs` 时跳过去的空态文案（用例只覆盖了权限侧的门控）、
多节点下同名 Pod 会被一并命中（`pods=` 不含节点维度，刻意的"宁可多给也不漏"，未实测）。

## 十四、第十轮：变更历史 ↔ 操作审计的强关联（2026-10-07）

收能力全景 §3.13 那条 P0 的最后一句欠账（"仍缺与操作审计的显式交叉链接"）。此前两者**只共享
操作人字符串**：审计回答"谁在什么时候调了哪个接口"，变更记录回答"哪个字段从什么变成什么"，
而「**那一次调用**把哪些资产的哪些字段从什么改成了什么」——单看任一侧都答不了。本批用一个
**请求 id** 把两侧串起来，并给出双向入口。

| 层 | 改动 |
|---|---|
| 关联键 | 审计中间件**在执行业务 handler 之前**给请求发 id（8 字节随机十六进制）放进 context，两侧各写一份 |
| 表 | `asset_changes.request_id` / `audit_events.request_id`：纯附加列、不升 `schemaVersion`、自愈 ALTER 补列 + 两条部分索引 |
| 写入 | 资产侧三条人工写路径（`Apply` / `SetLabels` / `ResetManual`）+ 审计侧全部写入点（中间件与各显式 `Record`） |
| 接口 | 变更历史带 `requestId`；审计列表支持 `requestId=` **精确**过滤；新增 `GET /api/v1/assets/changes?requestId=`（跨资产、范围下推） |
| 前端 | 变更历史 →「查看对应审计」（门控 `audit:read`）；审计页认领 `?requestId=` 深链 + 每行「本次改动」抽屉（门控 `assets:read`） |

**为什么用请求 id 而不是审计 id**：审计行在 handler **返回之后**才写（要拿状态码与成功标志），
写变更记录那一刻审计 id 还不存在；反过来先给请求发号就没有顺序问题，也不需要跨包事务。

**关键取舍**：① 发号放在"跳过判断**之后**"——只读请求 / 登录 / Agent 上报不发号，于是
「有 id 的变更 ⇔ 有一次可查的审计」这个不变式成立，采集侧写的变更天然留空（不编一个查不到的 id）；
② 空 id 一律拒绝（它对应采集侧写的**全部**变更，放行等于把整张表当成"这一次操作"）；
③ 历史行**不回填**——按"时间 + 操作人"猜出来的关联看着像证据、实际是猜的，比没有更糟；
④ 依赖新列的**部分索引必须排在补列之后**，否则存量库启动会在建索引时报 `no such column` 而
起不来（已用旧形态的两张表把顺序钉住）。

**执行结果**：`go build` / `go vet` / `go test ./...` **30 包全绿**（新增用例覆盖：人工四类写入带 id
与采集侧留空、按 id 反查带资产身份、业务标签与节点两个维度的范围收窄、空选择器恒不匹配、
条数上限显式回报截断、"表已存在但缺列"的升级路径、审计侧子串不算命中、只读请求不发号、
反向端点缺/空 id 一律 400）；前端 `npm test` **18 文件 106 项**通过（新增 8 项）、`vite build` 通过。

**未覆盖边界（不得视为通过）**：实机端到端（真实写操作 → 两侧拿到同一个 id → 按范围收窄）、
升级分界（升级前两侧都没有关联 id，"按 id 查不到"是预期，界面文案已说明但没在真机上按换装时刻划过界）、
浏览器实点（真实点击两个入口并视觉确认，目前只有组件用例）。
