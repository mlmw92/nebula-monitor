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

### 7.4 仍未做（依赖日志侧）

「从 Pod 跳到集中日志检索」需要：`logship` 支持按路径通配采集容器日志目录（如 `/var/log/pods/<ns>_<pod>_<uid>/`）、从路径注入 pod 维度标签、以及结构化字段检索（9-06/9-07）。在这三项落地前，容器页的日志入口是**按需拉取**（`container.logs`），不是检索。这一依赖已写入全景表 §3.8 该行。

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
