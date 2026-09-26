# Nebula Monitor 改造方案与批次一执行计划

> 适用范围：Nebula Monitor 全系统（Go Server + Go Agent + Vue 3 Web）。
> 本文档为改造路线与实施规格，供后续开发、测试与评审依据使用。
> 面向终端用户的操作说明见 `README.md`；角色权限模型细节见 `docs/role-permission-management.md`。
> 状态（2026-09-26）：**批次一、批次二已完成**（批次一 = E1 / D2 / F2 / B1 设计件；批次二 = B1 实施子批次 A~E / D1 / F1 / D4 / E3 / A3 / F3）；**批次三进行中**——已交付 **C1 阶段一 + 阶段二**（「新增一类中间件只写模板、不改 Go 代码」端到端成立）与 **E2（6 个开箱预设 + `rules.aggregate` / `rules.promoteLabel` 两处表达力修复）**；未开始的有 C1 阶段三（`jdbc` / `exec` / `file`）、C2 / C3 / A1 / A2 / E4。
> **命名提醒**：本节的「批次一 / 二 / 三」是**路线图批次**；`docs/permission-matrix.md` 中的「批次 A~E」是 **B1 实施**内部的子批次，两者不是同一层级。
> 基线版本：`VERSION = 1.23.7`｜当前版本：`VERSION = 1.25.0`（批次二合并递增：B1 实施 / D1 / F1 / F3 / D4 / E3 / A3）。
> **批次三的工作尚未递增版本号**（按「整批合并递增」约定留待批次三收口时统一提升），因此**已发布的 `1.25.0` 安装包不含批次三内容**（批次三提交于该次打包之后）——需要模板体系与预设时须重新打包。
> 成文日期：2026-09-26。

---

## 1. 背景与依据

### 1.1 现状基线

Nebula Monitor 是「Agent 采集 → Server 接收 → 时序库持久化 → Web 展示 / 告警」的 C/S 监控系统：

- **Server 无状态**：指标经 remote_write 写入外部 TSDB（VictoriaMetrics / Mimir / Cortex / Thanos / Prometheus / custom），PromQL 查询；前端由磁盘目录托管（`internal/server/api/spa.go` + `config.WebDir`），**非 Go embed**。
- **Agent 三模式**：`collect`（采集）/ `edge` / `hub`（网闸 mTLS 隧道代理）。
- **能力面**：主机监控、10 类内置中间件（直连 + exporter 双模式）+ **模板派生类型的中间件**（`templates` 配置与下发，见 `docs/c1-collector-templates.md`）、拨测、巡检报告、安全中心（SSH 审计 / FIM / 基线 / 异常进程 / fail2ban 托管）、智能分析（只读）、RBAC、系统升级与版本归档切换。

### 1.2 外部对标结论（2026-09 快照）

与 8 个开源项目对比（Nezha / HertzBeat / Beszel / Nightingale / Netdata / Zabbix / Uptime Kuma / Telegraf）：

| 维度 | 结论 |
|---|---|
| 同类中最强项 | 网闸代理（mTLS 单端口多路复用隧道）、中间件拓扑角色识别、安全中心、交付运维体验、国密合规 |
| 最大差距 | ~~**无采集插件/模板体系**；新增指标须改 6 处代码~~ → **已于 2026-09-26 补齐**（C1 阶段一+二：模板 DSL + Server 存管下发 + 类型注册表，新增中间件只写模板不改 Go 代码；E2 已交付 6 个开箱预设，并补齐 `rules.aggregate` / `rules.promoteLabel` 两处表达力）。差距性质从「能力有无」转为**「模板生态厚度」**：HertzBeat / Telegraf / Netdata 的现成模板数以百计，本项目为 6 个预设 + 待补的三类 kind（`jdbc` / `exec` / `file`） |
| 次要差距 | ~~采集串行无超时隔离~~（**E1 已解决**）、~~测试覆盖薄弱~~（**F1 已解决**：关键路径单测 + CI 门禁）、~~无告警协作~~（**D4 已解决**：认领 / 指派 / 关闭三态流转与评论，但**排班 on-call 仍无**）、无自动处置/自愈（D3 待办）、无集中日志（C2）、无 Server 高可用与水平扩展（A1）、无 i18n（F4） |
| 反向优势 | 无状态 Server + 可切换 TSDB 后端（对比 Nezha/Beszel/Netdata 的本地存储）；一键部署与 Web 升级回滚 |

### 1.3 已核实的事实修正（与 `README.md` 不一致）

> 本表是**立项时的核对快照**。其中 1 / 2 / 3 / 4 / 5 / 6 项均已处置完毕：1、3、4、6 由 **F2**（文档一致性治理）修正，5 为随行为一并修正注释（服务端对上传/升级参数确会校验，仅措辞理由不成立），2 由 **B1 实施**（子批次 A~E）落实——业务路由已按权限点 + 资源范围强制校验（现状见 `docs/permission-matrix.md`）。保留本表以留痕：它是当时「文档与实现不一致」的证据。

| # | README 表述 | 代码实际 | 证据 |
|---|---|---|---|
| 1 | 路线图「前端用户与权限管理页（P0）待实现」 | **已实现** | `web/src/components/rbac/UsersView.vue`、`RolesView.vue`；`web/src/router/index.js:36-37` |
| 2 | 路线图「业务接口权限点与资源范围服务端校验（P0）」 | **当时属实**：全站仅 14 条 users/roles/permissions 路由受 `a.authz` 保护 → **已由 B1 实施解决**（业务路由全面挂 `permit` / `permitNode`，含 `/ws` topic 级授权） | `internal/server/api/query.go`；`docs/permission-matrix.md` |
| 3 | `README.md:89`「中间件监控（**8 类**组件健康度总览）」 | 实为 **10 类**，与 `README.md:18`、`:1395` 自相矛盾 | `README.md:89` / `:18` / `:1395` |
| 4 | `web/vite.config.js:4`「由 Go embed 内嵌托管」 | 磁盘托管 | `internal/server/api/spa.go`、`config.WebDir` |
| 5 | `build/release.sh:38/62/69`「Server 二进制 embed 内嵌前端，前端更新必须重编译」 | 理由不成立（行为保守无害） | 同上 |
| 6 | README API 表约 45 条 | `RegisterRoutes` 实际注册约 90+ 条 | `internal/server/api/query.go:123+` |

---

## 2. 候选改造点总览

| # | 改造点 | 域 | 必要性 | 难度 | 风险 | 见效周期 |
|---|---|---|---|---|---|---|
| **E1** | Agent 采集并发化 + per-collector 超时隔离 | 代码/性能 | 高 | 低 | 低 | 短 |
| **B1** | 业务接口服务端鉴权全覆盖（权限点 + 资源范围） | 架构/安全 | 高 | 中 | 中 | 中 |
| **C1** | 自定义指标接入（采集项模板化，HertzBeat 式 YML） | 代码/生态 | 高 | 高 | 高 | 长 |
| **D1** | 告警风暴收敛（聚类 + 根因降噪） | 流程 | 高 | 中 | 低 | 中 |
| **F1** | 测试体系补齐（Go 关键路径 + 前端 Vitest） | 工程 | 高 | 中 | 低 | 中 |
| **D2** | 事件管道 / relabeling / 自定义消息模板 | 流程 | 中高 | 低 | 低 | 短 |
| **F2** | 文档与代码一致性治理 | 文档 | 中 | 低 | 低 | 短 |
| **C3** | 状态页对外发布（复用拨测数据） | 业务 | 低中 | 低 | 低 | 短 |
| **D4** | 告警认领与协作流转（评论/指派/状态机） | 流程 | 中 | 低中 | 低 | 中 |
| **A4** | 代理增强（磁盘缓冲 / 批量合并 / 主备切换） | 架构 | 中 | 中 | 中 | 中 |
| **E2** | 更多中间件（MariaDB/RabbitMQ/ES/ClickHouse/Nacos/Etcd/ZK） | 代码 | 中 | 低 | 低 | 短 |
| **E3** | 容量预测扩展（内存/CPU/网络） | 代码 | 低中 | 低 | 低 | 短 |
| **A3** | 数据保留策略（TSDB retention 纳管） | 架构 | 中 | 低 | 低 | 短 |
| **F3** | 自监控与健康检查端点 | 工程 | 中 | 低 | 低 | 短 |
| **C2** | 集中日志分析（采集 + 检索 + 日志告警） | 架构/能力 | 中高 | 高 | 中高 | 长 |
| **D3** | 自动化处置与自愈（脚本 + 审批 + 审计） | 流程/安全 | 中高 | 中高 | 高 | 长 |
| **C4** | Web 终端 + 定时任务 | 功能 | 中 | 中 | 高 | 中 |
| **A1** | Server 高可用（多实例 + 状态共享） | 架构 | 中高 | 高 | 高 | 长 |
| **A2** | 边缘/本地告警引擎（n9e-edge 式，断网可告警） | 架构 | 中 | 高 | 中 | 长 |
| **E4** | 依赖拓扑与影响传播 | 能力 | 中 | 高 | 中 | 长 |
| E5 | Windows 节点支持 | 平台 | — | 高 | 中 | — |
| F4 | 多语言 i18n | 体验 | 低 | 中 | 低 | 中 |

### 优先级矩阵

| | 易实施（低/中难度、低风险） | 难实施（高难度或高风险） |
|---|---|---|
| **必要性高** | 🟢 **立刻做**：E1、F1、D2、F2 | 🔴 **立项攻坚**：B1、C1、D1 |
| **必要性中** | 🟡 **择机快赢**：C3、D4、E2、E3、A3、F3 | 🔵 **长期规划**：C2、D3、C4、A1、A2、E4 |
| **必要性低** | ⚪ 可延后：F4 | ⚪ 可延后：（E5 已排除） |

---

## 3. 已确认决策（2026-09-26）

| 项 | 决策 |
|---|---|
| 推进方式 | **按批次推进，先做批次一，循序渐进** |
| 攻坚顺序 | **C1 先于 C2**（先做采集项模板化，再做集中日志） |
| D3 自动化处置与自愈 | **列入待办，暂不实施**；仅在用户明确要求时启动 |
| Windows 节点支持（E5） | **暂不支持**，移出路线图 |
| Server 高可用（A1） | **明确需求**，优先级由「低/长期」提升，需单独立项 |
| 提交习惯 | 不假定自动提交；每完成一项需先展示改动再确认 |
| 打包升级 | 仅在用户明确要求时执行；只打包，不代替用户做 server 端升级 |
| **E1 超时语义** | **一次到位**：ctx 贯穿全部可取消的外部 I/O（HTTP / DB / dial / socket / exec），本机 syscall 类仅做入口 ctx 检查 |
| **E1 并发上限** | **不加 `maxParallel`** 配置（任务数固定约 16，属 YAGNI） |
| **D2 一期范围** | **含前端编辑页**：后端 pipeline + `alert_pipeline.yaml` + API + 前端编辑与预览 |
| **B1 / F2 产出位置** | B1 映射表落 `docs/permission-matrix.md`；F2 的 README API 表**全量补齐** |

---

## 4. 调整后的路线图

| 批次 | 内容 | 目标 | 状态 |
|---|---|---|---|
| **批次一** | **E1**、**D2**、**F2** + **B1 设计件** | 用最低风险拿到采集稳定性与通知体验收益，并为鉴权改造定契约 | ✅ 已完成（`1.24.0`） |
| **批次二** | B1（实施）、D1、F1、D4、E3、A3、F3（**E2 推迟**） | 补齐授权正确性与告警降噪两块硬缺口 | ✅ 已完成（`1.25.0`） |
| **批次三** | **C1 阶段一 ✅ → 阶段二 ✅ → 阶段三 ⬜**、**E2 已完成 6 个开箱预设 ✅**（含两处表达力修复：`rules.aggregate` / `rules.promoteLabel`）、C2、C3、A1 实施、A2/E4 评估 | 攻生态扩展与架构纵深 | 🟨 进行中（版本号待收口递增、未打包） |
| 待办（不排期） | D3、C4、F4、A4 | 视需求启动 | ⏸ |

### A1（Server 高可用）前置调研要点

当前为「文件 + 进程内内存态」架构，HA 必须处理以下单例状态：

- `internal/server/alert/engine.go`：`firing` / `ruleState` / `certState` / `securityActive`（多实例将重复通知；现状靠 `restoreActiveState` 从 VM 恢复）
- `internal/server/alert/grouping.go`：`Grouper` 聚合等待队列
- `internal/server/node/manager.go`：离线判定与节点状态
- 文件型配置/存储：`users.yaml`、`notify.yaml`、`screen.yaml`、`ui.yaml`、`dashboards.yaml`、`alert_inhibit.yaml`、`alert_grouping.yaml`、`alert_acks.json`、`security_store.json`、`defense_tasks.json`、`audit_events.json`、`dialtest.yaml`、`nodes.json`
- `internal/server/api/ws.go`：`Hub` 进程内 WebSocket 广播
- `internal/server/receiver/receiver.go`：进程/监听/防火墙内存快照缓存
- `internal/server/api/auth.go:47`：`loginFails` 进程内限流；`:217` `globalAuthStore` 包级单例
- `internal/server/agentdist`：本地磁盘 CDN 目录

可选路线（调研输出对照）：

- **路线 A｜主备（active-standby + VIP/keepalived）**：改动最小，文件与内存态天然单写；切换存在秒级中断。
- **路线 B｜状态外置多活（引入 Redis/PG 协调层 + 共享存储）**：可实现多活，改动最大，涉及上述全部组件。

---

## 5. 批次一详细执行计划

### 5.1 任务 1｜E1 Agent 采集并发化 + ctx 一次到位

**改造目标**：单轮采集从「串行」改为「采集器级并发 + 每任务独立超时」，且超时/取消后**在途 I/O 真正中止**（无 goroutine 驻留），单实例慢不再拖慢整轮上报。

**现状问题**：`cmd/agent/main.go:253-367` 的 `collectAndReport` 顺序调用全部采集器；`internal/agent/collector/collector.go` 无并发、无超时包裹；各采集器 HTTP / DB / dial 调用均未接 ctx，超时只能靠各调用点自设的固定超时，聚合后无上界。

**必要性**：**高**（稳定性硬伤，随中间件实例数线性恶化）。

**影响范围**

| 类型 | 文件 |
|---|---|
| 修改 | `internal/agent/collector/collector.go`（新增 `CollectAll(ctx)`、`Result` 结构） |
| 修改 | `cmd/agent/main.go`（`collectAndReport` 改为消费 `Result`） |
| 修改 | `internal/agent/config/config.go`（新增 `collectTimeout`，默认 8 秒，0 = 不限制） |
| **修改（一次到位）** | 全部采集器签名改为接收 `ctx`：`redis.go`、`mysql.go`、`postgres.go`、`nginx.go`、`nginx_access.go`、`kafka.go`、`docker.go`、`rocketmq.go`、`k8s.go`、`mongo.go`、`fastdfs.go`、`port.go`、`security.go`、`listener.go`、`firewall.go`、`cpu.go`、`disk.go`、`network.go`、`process.go`、`memory.go`、`load.go`、`users.go`、`hostinfo.go` |
| 新增 | `internal/agent/collector/collect_all_test.go` |
| **不动** | `internal/model/metric.go`、`internal/agent/reporter`、**Server 全侧**（协议零变更，无需同步升级） |

**并发安全性（已核实）**

- 各子采集器自带 `sync.Mutex`：`cpu.go:14`、`disk.go:14`、`network.go:13`、`security.go:64`、`redis.go:25`（`clusterSeedsMu`）。
- 每轮每个采集器只被调用一次，不同采集器之间无共享可变状态（`node` / `group` / `labels` 只读）→ 采集器级并发**无竞态**。
- **无包级共享可变状态**（`skippedFSTypes` / `skippedMountPrefixes` / 正则变量均为只读）。
- 需注意的实例级无锁状态：`nginx_access.go` 的 `files` / `lastAt`（`collector.go` 内单次调用，无并发）；`security.go` 的 `sshOff` 读未加锁（`security.go:169/338` 读、`:217-219` 写）——因每轮单次调用不构成竞态，但若未来改为同一采集器并发调用需补锁。
- **唯一依赖**：`CollectFirewallStatus(ruleCount)` 本身不依赖规则内容，仅调用方传入条数（`cmd/agent/main.go:283-284`）。二者各自 exec 探测同一批后端命令，**同一轮会把防火墙后端探测两次** → 本次改造将二者合并为**同一个任务**，任务内串行执行，既保留依赖又消除重复探测。

**设计要点**

1. 保持现有「聚合器 + 各 `CollectXxx()`」结构，**不引入统一 Collector 接口**（各方法签名差异大，强行统一违反 YAGNI），仅把签名统一加上 `ctx context.Context`。落地方式：每个采集器新增 `CollectCtx(ctx)`，旧的无 ctx 方法暂留为薄包装（`CollectCtx(context.Background())`），待全部转换完成、`main.go` 切到 `CollectAll` 后再删除包装——**保证每个中间步骤均可编译**。
2. 新增 `Result` 聚合结构与 `CollectAll(ctx) Result`；任务清单（16 个）：host 指标、redis、mysql、postgres、nginx、nginxAccess、kafka、docker、rocketmq、k8s、mongo、fastdfs、security、listeners、**firewall（rules+status 合并）**、hostInfo。
3. 并发模型：内核为 `sync.WaitGroup` + 每任务 `context.WithTimeout(ctx, collectTimeout)`（见 `internal/agent/collector/task.go` 的 `runTasks`，已有独立单元测试）；**任务内部错误不向上返回错误**（降级为空结果 + `slog.Warn`），单个任务失败不会级联取消其它任务；父 ctx 取消（进程退出）会传播到全部任务。
4. **等待语义**：`CollectAll` 等待全部任务结束（`g.Wait()`），不采用「硬超时返回部分结果 + 遗留 goroutine」——ctx 一次到位后，可取消的 I/O 会在超时时立即中止，任务可及时返回；不可取消的本机采集本身耗时有上界。
5. **panic 隔离**：每个任务外层 `defer recover()`，单任务 panic 不影响其他任务与 Agent 进程。
6. **ctx 一次到位（可取消的外部 I/O 全部改 ctx 版本）**：
   - HTTP 拉取（各 exporter 模式、nginx `stub_status`、rocketmq、k8s apiserver、docker socket）：`http.NewRequestWithContext`；**新增统一辅助 `fetchMetrics(ctx, url, timeout)` 替换 9 处分散的 `client.Get`**（原 `fetchPrometheusText` 无 ctx，扩展为带 ctx 版本）；
   - 数据库（MySQL / PostgreSQL）：`PingContext` / `QueryContext` / `QueryRowContext`；
   - Redis：`net.Dialer.DialContext` + **`context.AfterFunc(ctx, func(){ conn.SetDeadline(time.Now()) })`** 使在途 `ReadString` / `io.ReadFull` 真正可取消（原实现无读 deadline）；
   - Docker（unix socket）/ Mongo（官方驱动原生支持）/ K8s（REST）：全部 ctx；
   - FastDFS / 端口探测：`net.Dialer.DialContext`；
   - 本机命令（防火墙、安全基线、fail2ban 探测）：`exec.CommandContext`。
7. **不可取消项的处理**：gopsutil 本机指标、日志/基线/kubeconfig 文件读取、`/proc` 枚举无 ctx 接口 → 改为「入口 `ctx.Err()` 检查 + 依赖其自身耗时上界」；sarama（Kafka）无 ctx 接口 → 在 `sarama.Config` 显式设置 `Net.DialTimeout` / `Net.ReadTimeout` / `Net.WriteTimeout`（现状已有 5s，予以对齐与注释说明），**标注为已知边界**。
8. **实例循环内每轮检查 `ctx.Err()`**，超时后立即停止后续实例。
9. `collectTimeout` 默认 8s（< 默认 `interval` 15s）；设 `0` 表示不限制。

**TDD 用例（先写测试）**

| 测试 | 断言 |
|---|---|
| `TestCollectAll_SlowTaskDoesNotBlockOthers` | 注入 sleep > timeout 的假任务，整体耗时 ≈ timeout，其余任务产出完整 |
| `TestCollectAll_TaskPanic_OthersUnaffected` | 单任务 panic 被 recover，Agent 不退出，其余任务产出完整 |
| `TestCollectAll_AllTasksFail_StillReturnsHostMetrics` | 全部中间件任务失败仍返回主机指标 |
| `TestCollectAll_TimeoutZero_NoTimeout` | `collectTimeout=0` 时不做超时截断，与旧版行为等价 |
| `TestCollectAll_FirewallTaskRunsOnce` | 防火墙后端探测在本轮只发生一次（rules 与 status 合并为单任务） |
| `TestCollector_CtxCanceledStopsInstanceLoop` | 实例循环中 ctx 取消后不再尝试后续实例 |
| `TestHTTPCollector_HonorsCtxCancel` | `httptest` 慢响应 + ctx 取消：请求立即返回 `context.Canceled` |
| `TestRedisCollector_CtxCanceledOnDial` | 不可达地址 + 短超时：dial 在超时时刻返回，无残留 goroutine |
| `TestCollectAll_NoGoroutineLeak` | 前后 `runtime.NumGoroutine()` 对比，无泄漏 |

**风险与缓解**

| 风险 | 等级 | 缓解 |
|---|---|---|
| ~~超时后 goroutine 驻留~~ | — | **已被一次到位消除**；仍用 `TestCollectAll_NoGoroutineLeak` 断言 |
| 改动面扩大到 19+ 采集器文件，回归风险上升 | 中 | 逐采集器小步改动、每步保持可编译；每改完一个跑该采集器既有测试 + `-race`；**保持指标名与 label 完全不变** |
| sarama 无法取消，极端情况下 Kafka 采集仍超出上游超时 | 低 | 显式设置 sarama 网络超时；在文档与代码注释中标注为已知边界 |
| `Result` 结构替换 `main.go` 组装逻辑时遗漏字段 | 中 | 逐字段对照旧 `ReportPayload` 赋值（`cmd/agent/main.go:293-329`）核对；指标名/结构由回归用例覆盖 |

**验收标准**：`go test -race ./internal/agent/...` 全绿；模拟 1 个卡死 exporter 时整轮耗时从「Σ 全部实例」降到「≈ collectTimeout」且其余实例指标正常上报；`ReportPayload` 结构与指标名与旧版逐字段一致。

**版本**：原计划单独发布 `1.23.8`（patch，向后兼容；Agent 与 Server 无需同步升级）；**实际与 D2/F2/B1 合并为一次递增 `1.24.0`**。

---

### 5.2 任务 2｜D2 告警事件管道 / relabel / 自定义消息模板（含前端编辑页）

**改造目标**：告警派发前可做标签重写、字段增补、消息渲染；Web 端可编辑与预览。

**现状问题**：通知内容由 `internal/server/alert/notifier.go` 内置模板固化，无法按团队定制（对比 Nightingale 事件管道 + 自定义模板）。

**必要性**：中高。

**影响范围**

| 类型 | 文件 |
|---|---|
| 新增 | `internal/server/alert/pipeline.go`（`PipelineStore` + 阶段执行器） |
| 新增 | `internal/server/alert/pipeline_test.go` |
| 修改 | `internal/server/alert/engine.go`（`notify` 前插入 pipeline 阶段） |
| 修改 | `internal/server/alert/notifier.go`（内置模板改为可加载 + 兜底） |
| 新增配置文件 | `alert_pipeline.yaml`（沿用 `filepath.Dir(rulesFile)` 约定） |
| 修改 | `internal/server/api/alert_extra.go` + `internal/server/api/query.go`：新增 `GET` / `PUT /api/v1/alert-pipeline`、`POST /api/v1/alert-pipeline/preview` |
| 新增（前端） | `web/src/components/settings/PipelineSettingsSubView.vue`（阶段编辑 + YAML 直编 + 校验），入口挂 `web/src/components/settings/SettingsView.vue` |
| 修改（前端） | `web/src/api/http.js` 增加 pipeline 三个接口方法 |

**设计要点**

1. `PipelineStore` 完全复用 `alert/inhibit.go`、`alert/grouping.go` 的既有模式：内存态 + YAML + `sync.RWMutex`，`Save` 即写盘并**热生效**，不重启。
2. 一期只支持 3 类阶段（YAGNI）：
   - `relabel`：标签重命名 / 删除 / 正则捕获替换
   - `enrich`：注入静态标签（`team` / `owner` / `env`）
   - `template`：按渠道渲染标题与正文（Go `text/template`，仅暴露事件只读字段）
3. 执行点在 `engine.notify(ev)` 之前，对 `ev` 做浅拷贝后变换，**不污染 `firing` 状态机**。
4. 兜底：pipeline 为空时行为与旧版**完全一致**；模板语法错误降级为内置模板 + `slog.Warn`，**不允许因配置错误丢失告警**。
5. 安全约束：模板仅可访问事件字段，不开放文件 / 网络 / 函数调用。
6. **API 契约**：`GET` 返回当前 pipeline 与校验结果；`PUT` 全量替换并即时热生效（复用 `notify` 的写法）；`POST .../preview` 用给定样例事件渲染，供前端实时预览。
7. **前端编辑页**：挂在「系统设置」下新增子页「告警管道」，提供阶段增删改、YAML 直编与语法校验、样例事件预览；保存后热生效无需重启。前端仅作交互与校验，以服务端为准。
8. **权限**：本期沿用现有登录保护（B1 未实施前不加 `authz`），文档标注待批次二接入 `alerts:manage` 权限点。

**TDD 用例**：relabel 重命名 / 删除 / 正则替换；enrich 注入；模板渲染（给定事件 → 期望文本）；空 pipeline 回归等价；模板语法错误降级不丢告警；preview 接口与 PUT 接口的校验失败路径。

**风险**：低（唯一注意点为模板执行安全边界）。

**版本**：原计划与 F2 合并发布 `1.23.9`（**需同步执行 `cd web && npm run build`，并把 web 产物并入升级包**）；**实际整批合并为 `1.24.0`**。

---

### 5.3 任务 3｜F2 文档与代码一致性治理

**改造目标**：消除文档/注释与实现的偏差，避免后续开发者被误导。

**已核实清单与处置**

| # | 问题 | 证据 | 处置 |
|---|---|---|---|
| 1 | `README.md:89`「8 类组件」与 `:18`、`:1395`「十类 / 10 类」矛盾 | `README.md:89` / `:18` / `:1395` | 统一为十类 |
| 2 | `web/vite.config.js:4`「Go embed 内嵌托管」 | 实际磁盘托管（`api/spa.go`） | 修正注释 |
| 3 | `build/release.sh:38/62/69`「Server 二进制 embed 内嵌前端」 | 同上，理由不成立 | 修正注释理由；**保留**保守重建行为，注明为历史 embed 方案遗留 |
| 4 | 路线图「前端用户与权限管理页（P0）待实现」 | 已实现（`rbac/UsersView.vue`、`RolesView.vue`，路由 `/system/users`、`/system/roles`） | 从路线图移除，并在「角色权限管理」章节补前端入口说明 |
| 5 | 路线图「业务接口服务端鉴权（P0）」 | 属实（仅 14 条路由受 `a.authz`） | 保留，标注批次二实施 |
| 6 | README API 表约 45 条 vs 实际约 90+ 条 | `internal/server/api/query.go:123+` | **全量补齐**（已决策） |
| 7 | `Makefile:24-28` 的 `build-web` 内联 npm+vite，与 `build/build-web.sh` 重复 | 两处实现同一逻辑 | 统一为调用 `build/build-web.sh` |
| 8 | 措辞治理 | — | 保持「面向终端用户」口吻，清理内部决策口吻（「推荐」「零代码改动」「（进阶）」） |

**第 6 项需补齐的路由**（示例，非穷举）：`/api/v1/query/listeners`、`/api/v1/query/firewall`、`/api/v1/query/firewall/status`、`/api/v1/nodes/upgrade`、`/api/v1/nodes/{name}/display-name`、`/api/v1/rules/export`、`/api/v1/rules/import`、`/api/v1/rules/templates`、`/api/v1/rules/{id}/toggle`、`/api/v1/rules/{id}/toggle-silence`、`/api/v1/alerts/acks`、`/api/v1/alerts/ack`、`/api/v1/audit/events`、`/api/v1/security/*`、`/api/v1/security/defense/*`、`/api/v1/system/geoip/*`、`/api/v1/screen/config`、`/api/v1/ui/settings`、`/api/v1/metrics/catalog`、`/api/v1/metrics/active`、`/api/v1/metrics/export`、`/api/v1/dashboards`、`/api/v1/auth/me`、`/api/v1/users`、`/api/v1/roles`、`/api/v1/permissions/catalog`、`/api/v1/version`。

**风险**：低。**注意不破坏 README 目录锚点与章节结构**（如 `[角色权限管理](#角色权限管理)` 依赖标题文本）。

**验收方式**：README 每条 API 与 `RegisterRoutes` 逐条对照；锚点链接可用性检查。

---

### 5.4 任务 4｜B1 权限映射表设计（仅设计件，不写代码）

**产出物**：`docs/permission-matrix.md`——「路由 → 权限点 → 资源范围」映射表（约 90+ 路由）+ 兼容策略 + 分批实施顺序。

**关键设计约束（已核实）**

- 兼容分支必须放行：`auth.enabled=false` 全放行；`auth.MigrateSingleAdmin` 单管理员场景按超级管理员处理，否则升级即锁死。
- 公开白名单边界：`internal/server/api/auth.go:141` 的 `isPublicPath`（`/api/v1/login`、`/api/v1/report`、`/api/v1/agent/check`、`/install/`、`/bin/`）；新增公开路由须显式登记。
- 权限点命名与 `GET /api/v1/permissions/catalog`（`internal/server/api/permission_api.go`）**同源**，禁止两套定义。
- 资源范围过滤统一走 `internal/server/auth/policy.go` 的 `FilterByGroup[T]` / `CheckBatchGroups`，不允许各 handler 自行实现。
- 分批顺序：`nodes/*` → `rules/*` + `alerts/*` → `notify` → `security/defense` → `system/upgrade` + `geoip`。
- 已知障碍：`internal/server/api/auth.go:217` 的 `globalAuthStore` 为包级单例（A1 调研时需一并处理）。

---

## 6. 执行顺序与质量门禁

```
任务1 E1  ──►  go build ./... && go test -race ./internal/agent/...  ──┐
任务2 D2  ──►  go test ./internal/server/alert/...  +  cd web && npm run build  ──┤
任务3 F2  ──►  文档逐条对照 RegisterRoutes                                        ──┼──►  整批合并递增为 1.24.0
任务4 B1  ──►  设计件评审（无代码，落 docs/permission-matrix.md）                 ──┘
```

> 实际执行与上图不同：四任务（以及后续 B1 实施子批次 A~E）全部完成后，**一次性递增到 `1.24.0`**；图中原写的 `1.23.8` / `1.23.9` 未执行，原因见第 8 节「版本与发布」。

- 每个任务遵守 TDD：**先写测试 → 跑红 → 实现 → 跑绿 → 重构**（Superpowers Pro 工作流）。
- 版本号按仓库约定以根目录 `VERSION` 为唯一来源递增；编译走 `build/cross-compile.sh`；打包走 `build/release.sh`。
- **不自动 commit**；每完成一项先展示改动再确认是否提交（提交信息用中文）。
- 打包/升级仅在明确要求时执行，且只打包、不代替用户做 server 端升级。

---

## 7. 决策记录（2026-09-26 已确认）

| # | 事项 | 决策 | 理由与连带影响 |
|---|---|---|---|
| 1 | E1 超时语义 | **一次到位**：ctx 贯穿全部可取消的外部 I/O | 避免二期返工与 goroutine 驻留；代价是批次一需改动 19+ 采集器文件、回归面扩大 → 必须配套逐文件小步验证与 `-race` |
| 2 | E1 并发上限 | **不加 `maxParallel`** | 任务数固定约 16，配置项属 YAGNI；若未来任务膨胀再引入 |
| 3 | D2 一期范围 | **含前端编辑页** | 一次交付可用功能；需同步执行 `cd web && npm run build`，并把 web 产物并入升级包（实际为 `1.24.0`） |
| 4 | B1 / F2 产出位置 | B1 落 `docs/permission-matrix.md`；F2 API 表**全量补齐** | 一次性成本换取长期可维护性；映射表作为批次二实施依据 |

---

## 8. 实施进度追踪

> 状态标记：⬜ 未开始｜🟨 进行中｜🚧 部分完成（该行拆分了交付范围，已完成一部分）｜✅ 已完成｜⏸ 阻塞（不排期）。每完成一格即更新本表。

### 当前状态小结（2026-09-26）

- **已完成**：批次一（E1 / D2 / F2 / B1 设计件，`1.24.0`）、批次二（B1 实施 A~E / D1 / F1 / D4 / E3 / A3 / F3，`1.25.0`）、批次三的 **C1 阶段一 + 阶段二**（模板 DSL → Server 存管下发 → 类型注册表 → 前端管理页，实施记录见 `docs/c1-collector-templates.md`）、**E2**（6 个开箱预设；过程中发现并修掉两处表达力缺口：「汇总维度指标会静默损坏数据」与「一族的多种含义无法拆成独立指标名」）。
- **进行中 / 下一步候选**：C1 阶段三（`jdbc` / `exec` / `file`）、C2 集中日志、C3 状态页、A1 前置调研、A2 / E4 评估；E2 已无待补形态——**再加中间件只需加一份预设数据**（MariaDB 复用既有 MySQL exporter 通路；真实产品端点的现场核对属部署动作）。
- **未发布**：批次三内容**未递增版本号、未重新打包**——当前 `1.25.0` 产物不含它们；按「整批合并递增」约定留待收口时统一提升。
- **文档同步状态**：README API 表与 `internal/server/api/query.go` 的 `RegisterRoutes` 已重新校准——**代码侧 138 条路由在 README 表中全部有行（该方向差集为 0）**；批次三新增的 7 条（模板 CRUD/校验 5 + 预设 1 + 通用实例 1）已补齐。README 表另含 5 条不由该文件注册的路由：`/`（静态托管）、`/bin` 与 `/install/agent-install.sh`（Agent 分发）、`/ws`（WebSocket，无方法前缀）、`POST /api/v1/report`（上报接收）。`docs/permission-matrix.md` 一直被同步维护（含模板路由与预设行），无需补。

### 批次一

| ID | 任务 | 子项 | 状态 | 版本 | 备注 |
|---|---|---|---|---|---|
| E1 | 采集并发化 + ctx 一次到位 | 0. 并发调度内核 `runTasks` + 单元测试（7 用例） | ✅ | — | `collector/task.go`、`task_test.go`；本地已验证（含 3 次重复） |
| E1 | | 1. 采集器签名统一加 `ctx`（`CollectCtx` + 兼容包装） | ✅ | — | 12 个中间件采集器 + 主机类 cpu/disk/network/nginx-access/host-info（`*Ctx` + 入口门控）。内存/负载/进程/监听为不可取消的本机只读采集，门控在 `CollectCtx` 调用点完成，**不添加空转 ctx 参数**（设计决策） |
| E1 | | 2. `Result` 结构 + `CollectAll(ctx)`（per-task timeout） | ✅ | — | `collect_all.go`：16 个任务并发调度 |
| E1 | | 3. 统一 `fetchMetrics(ctx,…)` 替换分散 `client.Get` | ✅ | — | redis、mysql、postgres、nginx×2、rocketmq×2、k8s、docker×3、mongo、fastdfs、kafka；`fetchPrometheusText` 已删除 |
| E1 | | 4. DB（mysql/postgres）改 `*Context` 版本 | ✅ | — | 共 15 处（mysql 6 + postgres 9） |
| E1 | | 5. Redis dial ctx + `SetDeadline` 取消在途读 | ✅ | — | `DialContext` + `context.AfterFunc`→`SetDeadline`，有专项测试 |
| E1 | | 6. exec 改 `CommandContext`（firewall/security） | ✅ | — | 22 处（firewall 18 + security 4） |
| E1 | | 7. 实例循环内 `ctx.Err()` 检查 | ✅ | — | 12 个采集器均已接入（含 redis 专项测试） |
| E1 | | 8. sarama 网络超时对齐 + 注释标注边界 | ✅ | — | Kafka：`Net.DialTimeout`/`Net.ReadTimeout` 5s + 代码注释标注 |
| E1 | | 9. `collectTimeout` 配置项 | ✅ | — | `config.go` 默认 8s，0=不限；已接入 `collector.New` |
| E1 | | 10. `main.go` 接线到 `CollectAll` | ✅ | — | `collectAndReport` 改为消费 `Result`，上报体字段逐项对照迁移 |
| E1 | | 11. TDD 测试 | ✅ | — | 15 个用例：内核 7 + redis 3 + CollectAll 5，全部通过 |
| E1 | | 12. `-race`（Linux/CI） | ✅ | — | 已在 Ubuntu 24.04 + Go 1.27.1 实机执行：`CGO_ENABLED=1 go test -race -count=1 ./internal/agent/...` 全部通过（collector 2.46s），无竞态报告 |
| E1 | | 13. 实机回归比对（指标名/label 与旧版一致） | ✅ | — | 新旧 Agent 在同一节点各跑一轮真实采集，结构比对 **IDENTICAL**：78 条指标 / 66 个指标签名、43 条监听、93 条安全事件、9 类中间件实例结构、firewallStatus、hostInfo 全部一致；仅进程 Top-N 条数随实时状态浮动（153 vs 152）。已固化为 `build/verify-agent-parity.sh` |
| D2 | 告警事件管道 + 前端编辑 | 1. `pipeline.go` + `PipelineStore` 热加载 | ✅ | — | 仿 inhibit/grouping：relabel/enrich/template 三阶段 |
| D2 | | 2. engine 接入 pipeline 阶段 | ✅ | — | `notify` + `flushGroup` 两个派发点；空管道零开销直通 |
| D2 | | 3. 消息模板可加载 + 渲染失败兜底 | ✅ | — | 渲染失败/为空回退内置描述，绝不丢告警 |
| D2 | | 4. API：GET / PUT / preview | ✅ | — | preview 支持携带待保存配置试算 |
| D2 | | 5. 前端编辑页 + 预览 + 构建 | ✅ | — | 「系统设置 → 告警管道」标签页；三阶段表格编辑 + 试算预览 + 配置直编；`npm run build` 通过 |
| D2 | | 6. TDD（含校验失败路径） | ✅ | 1.24.0 | 8 个管道单测（含空管道回归等价） |
| D2 | | 7. `AlertEvent` 标签视图（`Labels` 字段） | ✅ | — | 由事件字段派生 + 事件自有标签，与分组键命名一致 |
| F2 | 文档一致性 | 1. README 8 类 → 十类 | ✅ | — | README:89 |
| F2 | | 2. vite.config / release.sh 注释修正 | ✅ | — | 三处 embed 表述；保留保守重建行为并注明历史遗留 |
| F2 | | 3. 路线图移除已实现的 RBAC 前端页 | ✅ | — | 从路线图移除；新增「权限管理界面」小节说明入口与权限 |
| F2 | | 4. API 表全量补齐 | ✅ | — | 44 行 → 127 行（126 条路由，`/ws` 按两种 topic 分列）；双向差集为空 |
| F2 | | 5. Makefile build-web 复用脚本 | ✅ | — | 改为调用 `build/build-web.sh` |
| F2 | | 6. 措辞治理（面向终端用户） | ✅ | 1.24.0 | 随改动章节中性化；高风险操作补注服务端校验实际覆盖范围 |
| B1 | 权限映射设计件 | 1. 路由 → 权限点映射表 | ✅ | — | `docs/permission-matrix.md`：126 条逐条映射（双向差集为空） |
| B1 | | 2. 兼容策略与分批顺序 | ✅ | — | 7 条兼容策略 + 5 批实施顺序 + 7 项开放问题；批次二输入 |

### 批次二

| ID | 任务 | 子项 | 状态 | 版本 | 备注 |
|---|---|---|---|---|---|
| B1 | 业务接口鉴权实施 | 1. 子批次 A：基础设施（`permit` / 新增 2 权限点 / 去包级单例） | ✅ | — | `7f98eb0`；11 个测试 |
| B1 | | 2. 子批次 B：主机与指标 + `/ws` topic 授权 | ✅ | — | `442785c`；23 条路由；14 个测试（含修掉真实越权缺口） |
| B1 | | 3. 子批次 C：中间件（含实例范围过滤与 overview 重算） | ✅ | — | `fb660cb`；13 条路由；8 个测试 |
| B1 | | 4. 子批次 D：告警与通知 | ✅ | — | `7aa0a8b`；26 条路由；7 个测试 |
| B1 | | 5. 子批次 E：安全、系统与其余 + 审计导出拆分 | ✅ | — | `b1bf1b4`；38 条路由；4 个测试 |
| B1 | | 6. 前端守卫同步（菜单 `perm` + 路由 `meta.perm`） | ✅ | — | 共 8 个菜单项 + 11 条路由 |
| B1 | | 7. README / 设计件路由表同步（127 条，双向差集为空） | ✅ | — | 每次新增路由均同步校准 |
| B1 | | 8. 版本递增（整批合并为 `1.24.0`） | ✅ | 1.24.0 | 前端版本号已同步；打包（`full`/`upgrade`）待你明确指示后执行 |
| D1 | 告警风暴收敛 | 1. `Grouper` 增加「时间窗 + 标签相似度」聚类 key | ✅ | — | `alert/grouping.go`：新增 `converge` / `convergeBy`（默认 rule+severity）/ `convergeWindow`（默认 10m）；**开启收敛后收敛维度取代分组维度**——否则 `groupBy` 含 host 时键只会更细，收敛恒不生效（实现中发现并修正） |
| D1 | | 2. 组内按严重度与依赖排序，推选头部告警 + N 条摘要 | ✅ | — | `alert/converge.go`：头部取「级别最高、同级最早」，正文附「规则 / 范围 / 级别分布」+ Top N 明细 + 折叠计数；`headCount` 默认 5、上限 20；摘要以**追加**方式写入头部 Message（故必须在渠道模板渲染之后收敛） |
| D1 | | 3. 附注 `analysis/correlation.go` 的关联结论 | ✅ | — | alert 侧定义 `CorrelationProvider` 接口，API 侧 `(*API).CorrelationNotes` 实现并随 `SetAnalyzer` 注入：analysis 依赖 alert（`SetAlertStore`），只能反向注入；节点去重 + 限量（3 节点 / 6 条）。接口实现为 `internal/server/api/correlation_provider.go` |
| D1 | | 4. 参数热生效 + 测试 | ✅ | — | `SetGrouping` 统一 `normalize()` 后重建 `Grouper`；前端「告警中心 → 高级设置 → 告警分组」新增收敛开关与参数；新增 19 个测试（17 alert + 2 api） |
| F1 | 测试体系补齐 | 1. Go 高价值单测（remote_write 编码 / `buildExpr` 注入防护 / firing 状态机 / 鉴权中间件 / SM2·SM3·SM4） | ✅ | — | 5 个目标点全部落地：`storage/writer_test.go`（protobuf **逐字节 wire 断言** + varint 边界 + 分组/重试策略）、`storage/buildexpr_test.go`（注入转义 + 白名单边界）、`alert/engine_state_test.go`（去重键 / 活跃索引 / 抑制链路 / 重启恢复）、`api/token_test.go`（token 往返/篡改/过期 + `AuthMiddleware` 四条 401 分支与 Cookie 兜底）、`agent·server/crypto` 错误分支与随机化 |
| F1 | | 2. 前端引入 Vitest | ✅ | — | `vitest@2.1.9`（须与 Vite 5 配套；Vitest 3+ 要求 Vite 6+）+ jsdom；覆盖 `api/http.js`（9 用例：token 注入 / 401 失效 / 错误文案提取）与 `useAuth` 权限语义（5 用例）；`npm test` 已接入。**未做组件级测试**（Element Plus 组件需全局插件注册，留待后续） |
| F1 | | 3. CI 将 `go test ./...` 设为发布门禁 | ✅ | — | 新增 `.github/workflows/ci.yml`（push/PR：go vet + build + test + `-race` + 前端构建与单测）；`release.yml` 增加发布门禁 step，并**修正其 `go-version: 1.22` → `1.25`**（与 `go.mod` 的 `go 1.25` 不一致，原值会让发布因版本不足直接失败） |
| D4 | 告警认领与协作 | 1. `AckStore` 扩展为 pending / ack / closed 状态机 | ✅ | — | `alert/ackstore.go`：`Mark`/`Assign`/`Close`/`Reopen`/`Comment`/`Get`；`IsMarked` 更名 `IsHandled`（语义变为「认领或关闭」，重新打开的告警会回到待处理）；旧记录无 `status` 字段 → `EffectiveStatus()` 一律视为已认领（升级不改变既有语义） |
| D4 | | 2. 评论与指派（复用 `audit` 记录） | ✅ | — | 评论/指派/关闭原因存入处置记录（前端时间线的数据源），同时经既有 `AuditMiddleware` 自动写入管理操作审计；评论上限 50 条/告警、单条 2000 字符，按字符截断避免切断汉字 |
| D4 | | 3. 前端处置流 | ✅ | — | 「告警中心」：状态列改为处置状态机标签（待处理/已认领/已关闭/已恢复，读 `acks` 的 `status`，旧记录按已认领兜底）；详情抽屉新增处置区（处理人/关闭原因）、处置时间线、评论输入框与「认领 / 指派 / 关闭告警 / 重新打开 / 评论」 |
| E2 | 更多中间件 | 1. MariaDB / RabbitMQ / Elasticsearch / ClickHouse / Nacos / Etcd / ZooKeeper | 🚧 已完成 6 个开箱预设 + 两处表达力修复 | — | **前置已解除**（C1 阶段二完成，「写模板即可」端到端成立）。① 第一批交付 **RabbitMQ / Elasticsearch / Etcd / ClickHouse / ZooKeeper** 预设（`internal/template/presets.go` + `GET /api/v1/middleware/templates/presets` + Web 端「从预设创建」），规则按各 exporter 的真实输出形态逐一核对（keep 收窄指标族、丢弃 `*_created` 与直方图 `_bucket`），并附前置条件说明。② 第二批补 **Nacos** 预设并修掉两处**表达力缺口**：`unlabel` 丢维度会产出「同名同标签」多序列（last-write-wins 静默损坏）→ 新增 `rules.aggregate`（sum/max/min/avg）+ 冲突护栏（未声明聚合时只留第一条并告警）；Nacos 那类「一族多含义靠标签区分」（`nacos_monitor{name=...}`）无法分别看趋势 → 新增 `rules.promoteLabel`（把标签取值提升为指标名、净化取值、无法表示时保持样本原样）。MariaDB 仍复用既有 MySQL exporter 通路，不写模板。剩余：真实产品端点的现场核对（属部署动作），其余中间件按需加预设（每加一个只加数据、不改代码） |
| E3 | 容量预测扩展 | 1. 内存 / CPU / 网络容量预测 | ✅ | — | `forecastMetric` 泛化：百分比类（磁盘/内存/CPU）估算打满时间，速率类（网络收发）无上限只估算增长（不报耗尽）；文案/单位复用指标目录；`collectMetric` 一次取数供基线与预测共用（净增 2 次查询） |
| E3 | | 2. 中间件容量预测 | ⬜ | — | 需为各类中间件定义容量上限语义（如 Redis `maxmemory`、MySQL 连接数上限），单独立项 |
| A3 | 数据保留策略 | 1. 本地数据清理 + TSDB retention 呈现 | ✅ | — | 新增 `internal/server/retention`：告警处置记录（**只清已处置且超期**，待处理一律保留）与巡检报告（文件 + 历史同步删）按天清理，支持开关 / 周期 / 立即清理，策略文件 Web 可改；TSDB 保留期经 `/flags` **只读呈现**（运行期由时序库启动参数决定，不假装纳管）；审计与安全事件改为导出上限常量并展示现状 |
| F3 | 自监控与健康检查 | 1. `/healthz` + 自监控指标 | ✅ | — | 新增 `internal/server/selfmon`（快照 + `self_*` 指标 + Storage/Notifier 装饰器 + 30s 上报）；`GET /healthz`（存活）、`GET /readyz`（就绪：时序库真实查询 + 告警评估节拍，未就绪 503）均公开；`GET /api/v1/self/status`（`dashboard:read`）；`api.MetricsMiddleware` 置于中间件链最外层（401/403 也计入）；前端「系统设置 → 系统自监控」页 |

### 批次三

| ID | 任务 | 子项 | 状态 | 版本 | 备注 |
|---|---|---|---|---|---|
| C1 | 采集项 YML 模板化 | 1. 阶段一：`prometheus-exporter` + `http-json` + `http-text` 模板最小闭环 | ✅ | —（待整批递增） | 见 `docs/c1-collector-templates.md` 的实施记录：DSL 落在新包 `internal/agent/template`（`collector` 已依赖 `config`，DSL 放 collector 会成 import 环）；每模板一个采集任务（继承 E1 隔离）；失败仅产 `template_target_up=0`；启动期 fail-fast 校验（保留前缀/上限/正则/标签越权）；实机验证 28 项断言 + 端到端「落库」11 项断言全过，并固化为 `build/verify-templates.sh`；**Server 零改动** |
| C1 | | 2. 阶段二：模板 CRUD + 下发（复用上报响应通道）+ 前端编辑与校验 | ✅ | —（待整批递增） | 0：DSL 上移到 `internal/template` 供两端同源校验；A：Server 侧存储 + CRUD/校验 API（`middleware:write` 由此启用，此前是预留权限点）；B：响应携带下发 + 按节点分组过滤 + 能力/版本协商 + Agent 原子替换（**无需重启**）；C：`internal/server/mwreg` 类型注册表把四处硬编码收敛为一份数据源，模板即成为独立中间件类型（总览卡片/首页卡片/报告分节/服务离线告警）并新增通用实例接口；D：模板管理页 + 中间件 Tab 动态化 + 「已配置但无数据」提示。实机端到端见 `build/verify-template-delivery.sh`（含前端所消费接口的契约断言） |
| C1 | | 3. 阶段三：`jdbc` / `exec` / `file`（+ 内置模板集） | 🚧 | — | **内置模板集已随 E2 第一批交付**（`internal/template/presets.go`：RabbitMQ / Elasticsearch / Etcd / ClickHouse / ZooKeeper，经 `GET /api/v1/middleware/templates/presets` 与前端「从预设创建」下发）。三类 kind 未开始：`jdbc` 需连接池与 SQL 取值口径、`exec` 需命令白名单与超时、`file` 需解析器选择，均待设计件 |
| C2 | 集中日志分析 | 1. 日志采集 + 检索 + 日志告警联动 | ⬜ | — | **依赖已就绪**（C1 阶段一+二完成：模板体系与类型注册表已在，可直接启动） |
| C3 | 状态页对外发布 | 1. 公开只读 `/status` + 字段脱敏 | ⬜ | — | 复用拨测数据，需进公开白名单 |
| A1 | Server 高可用 | 1. 前置调研（主备 vs 状态外置的成本收益对照） | ⬜ | — | 明确需求；`globalAuthStore` 等障碍已在 B1 子批次 A 清除 |
| A1 | | 2. 实施（按调研结论二选一） | ⬜ | — | — |
| A2 | 边缘/本地告警引擎 | 1. n9e-edge 式本地告警（断网仍可告警） | ⬜ | — | 与网闸代理模式叠加 |
| E4 | 依赖拓扑与影响传播 | 1. 基于实例注册与拨测推导依赖图 | ⬜ | — | — |

### 待办（不排期）

| ID | 任务 | 状态 | 启动条件 |
|---|---|---|---|
| D3 | 自动化处置与自愈 | ⏸ | 仅在你明确要求时启动（安全边界需先评审） |
| C4 | Web 终端 + 定时任务 | ⏸ | 视需求 |
| F4 | 多语言 i18n | ⏸ | 视需求 |
| A4 | 代理增强（磁盘缓冲 / 批量合并 / 主备切换） | ⏸ | 视需求 |

### 版本与发布

| 项 | 现状 |
|---|---|
| `VERSION` 文件 | ✅ **已递增为 `1.25.0`**（2026-09-26；批次二合并为一次递增）；**批次三（C1 阶段一+二、E2 第一批）尚未递增**——按整批递增约定待收口时统一提升 |
| 前端版本号 | ✅ 已同步：`npm run version:sync` 生成 `WEB_VERSION = "1.25.0"`（`web/src/version.js` 为构建产物，不入库） |
| 已提交 | 批次一（E1 / D2 / F2 / B1 设计件，`1.24.0`）+ 批次二（B1 实施 A~E / F1 / D1 / F3 / D4 / E3 / A3，`1.25.0`）+ **批次三**：设计件 `47a89cf`、指标名缺陷族修复 `284bc7d`、C1 阶段一 `8dc1118` + 验证脚本 `71591b7`、DSL 共享化 `599c0d8`、C1 阶段二 A `d861601` / B `f839fcb` / C `9538c4f` / D `b6c91e9`、E2 第一批 `97afa94`。工作区干净 |
| 编译与打包 | ✅ **已执行**（2026-09-26）：`build/release.sh` 交叉编译 linux/amd64·arm64·arm 并组装 `nebula-monitor-v1.25.0-full.tar.gz`（138M）/ `-upgrade.tar.gz`（66M）；脚本自带的 manifest 自校验通过，另做独立校验：解包后 `sha256sum -c` 全通过（upgrade 94/94、full 101/101）。**注意**：该产物提交于批次三之前，**不含模板体系、预设与相关前端页面** |
| 产物校验 | full `sha256 b7f75a69…a2290`；upgrade `sha256 bea3c08d…c654d`；包内二进制内嵌 `1.25.0` 与构建时间 `2026-09-26T05:04:00Z`，前端 `web/assets/version-*.js` 内嵌 `1.25.0`（三者一致） |
| 打包环境 | 本机（Windows）经 Git Bash 运行官方脚本，**未上传任何源码**；服务器仅用于 Linux 侧验证。打包前修正 `.gitattributes`（见下条），否则 `build/release.sh` 在 Windows 检出下为 CRLF、无法执行 |
| 仓库约定 | `VERSION` 是版本号唯一来源；编译走 `build/cross-compile.sh`；打包走 `build/release.sh` |
| 递增依据 | 次版本号递增：批次二含三项**运行时行为变更**（B1 业务接口按权限点与资源范围强制校验、D1 风暴收敛默认开启、D4 告警处置改为三态状态机），并新增 14 条路由（audit/export ×1、健康探针 ×2、self/status ×1、告警处置 ×3、数据保留 ×3、其余为 B1 实施期间拆分）与 2 个权限点 |
| 待递增依据（批次三收口时预计） | 新增 7 条路由（模板 CRUD/校验 5 条 + 预设 1 条 + 模板派生类型的通用实例 1 条）、`middleware:write` 由预留权限点变为真正启用、10 类内置中间件之上新增「模板派生类型」这一可动态增长的类型来源（总览卡片 / 首页概览 / 报告分节 / 服务离线告警均动态出现）、前端新增「采集项模板」页与动态 Tab；是否按次版本号递增待收口时判定 |
| 待你决定 | 是否分发 / 升级；**升级动作不代替你执行**（`deploy/install-server.sh` 由你在目标机运行） |

### 变更记录

> 排序：**最新在上**（新条目追加到表首）。早期条目为逐条追加式，未按时间重排，故下半部分的时间序不严格；如需精确时序以 `git log` 为准。

| 日期 | 变更 |
|---|---|
| 2026-09-26 | **E2 第二批：`rules.promoteLabel`（标签值提升为指标名）+ Nacos 预设**。E2 第一批留下的边界 2「无法把标签值提升为指标名」在真实中间件上被验证是刚需：Nacos 把多种含义塞进同一个指标族、用 `name` 标签区分（`nacos_monitor{module="config",name="longPolling"}`），不拆时它们在「指标浏览」里全挤在 `nacos_monitor` 一个名字下——**无法分别看趋势，也无法按含义配告警**。新增 `rules.promoteLabel`（`{match, label}`；match 匹配响应原名、先于 `rename` 生效；提升后从标签集删除该标签，避免同一含义出现两处）。设计取舍逐条对应具体的坑：① 取值**净化**（指标名不允许的字符→下划线），**净化后为空则不提升、保持原名与原标签**——中文取值净化后只剩一串下划线，既无信息又极易与别的取值撞名，宁可留一个未拆分的样本，也不产出含义不明的指标名或丢数据；② 净化撞名（`a/b` 与 `a.b` 都变成 `a_b`）交给已有的**重复序列护栏**兜住：静态查不出「哪些取值会撞」，运行期只保留第一条并告警，比事后排查错乱数据便宜；③ `label` 不得是保留标签（等于允许伪造 `node`/`instance` 来源）、不得与 `unlabel` 同时出现（提升后又被删除 = 规则静默失效），两者都在**启动期拒绝**；④ 仅 `prometheus-exporter` 可用（另两类 kind 的响应标签不参与映射，静默忽略会让人以为生效了）。交付 Nacos 预设（`/nacos/actuator/prometheus`，2.x 需开启 metrics）+ 8 个新测试（校验 3：match/label 必填与合法、保留标签、unlabel 冲突、match 重复、kind 限制；行为 5：按取值拆名并删标签、取值净化、缺标签或取值无法表示时保持原样、首条规则生效、净化撞名被护栏兜住），并把 Nacos 加入预设的真实形态夹具。**实现过程中测试立刻抓到我自己的一个错误**：4 个用例的断言用了不带模板前缀的名字，而测试辅助把 id 硬编码成了 `nacos`——已把 id 改为参数。样本形态经检索核对（Nacos 官方监控手册 + 社区实例：2.x 为 `{module,name}`，1.x 为只有 `name`），端点与前置条件写进预设说明。README 增 `promoteLabel` 说明与示例、`templates` 示例补两条新规则、预设数量 5→6；设计件 §3.3 字段表 / §3.6 校验清单 / §7.1 测试清单 补齐两条规则，新增 §14.4 记录本修复、§14.3 边界 2 标记已解决；前端规则示例补上 `aggregate` 与 `promoteLabel`（此前只列了 keep/drop/rename/labels/unlabel） |
| 2026-09-26 | **文档补齐（本轮）**：把「开发过程中未同步」的内容一次性对齐实际状态。① 文档头状态由「批次二进行中（B1 实施）」更正为「批次一二已完成、批次三进行中（C1 阶段一+二、E2 第一批已完成）」，并说明**批次三未递增版本号、已发布的 `1.25.0` 产物不含批次三内容**；② 「1.2 差距结论」中「最大差距 = 无采集插件/模板体系」已于 C1 落地，改写为「差距性质转为模板生态厚度」；③ 第 4 节路线图表增「状态」列（批次一 ✅ / 批次二 ✅ / 批次三 🟨）；④ 第 8 节状态图例补「🚧 部分完成」并新增**当前状态小结**（已完成 / 进行中与下一步候选 / 未发布 / 文档同步状态）；⑤ C1 阶段三行由 ⬜ 改为 🚧（**内置模板集已随 E2 第一批交付**，剩 `jdbc`/`exec`/`file`），C2 依赖改为「已就绪」；⑥ 「版本与发布」表补批次三提交列表、未递增说明、以及**已打包产物不含批次三**的提示，并预登记批次三的递增依据；⑦ 顺带修掉一个真实缺口：**README API 表缺批次三新增的 7 条路由**（F2/B1 建立的「README 表 ↔ 路由双向差集为空」约定被打破）——已补齐，并用脚本复核：**代码侧 138 条路由在 README 表中全部有行（差集 0）**，反方向仅剩 5 条由静态托管 / Agent 分发 / 安装脚本 / WebSocket / 上报接收模块注册的路由。另：本次核对确认 `docs/permission-matrix.md` 一直被同步维护（含模板路由与预设行），无需补 |
| 2026-09-26 | **E2 第一批（5 个开箱预设）+ 模板表达力缺口修复**：把「写模板即可」拿到真实中间件上走一遍。交付 `internal/template/presets.go` 里 RabbitMQ / Elasticsearch / Etcd / ClickHouse / ZooKeeper 的预设（规则按各 exporter 真实输出形态核对：`keep` 收窄到该中间件指标族以排除 exporter 自身的 `go_*`/`process_*`、丢弃新版 client 的 `*_created` 时间戳序列与直方图 `_bucket`；每个预设带前置条件说明，如 ES 需启用 prometheus 模块、ZK 需第三方 exporter），经 `GET /api/v1/middleware/templates/presets` 下发到 Web 端「从预设创建」。**走真实场景立刻暴露了一个会静默损坏数据的表达力缺口**：像 RabbitMQ 按队列暴露指标时，用户想「汇总所有队列」最自然的写法是 `unlabel: ["queue"]`，那会产出多条「同名 + 同标签 + 不同值」的序列，写进时序库后是 last-write-wins（数值无意义且不报错）。修复分两层：新增 `rules.aggregate`（match 匹配**最终指标名** + sum/max/min/avg）让「汇总」这个需求可以正确表达；再加冲突护栏——未声明聚合却出现重复序列时只保留第一条并告警（同一模板同一指标只告警一次），绝不写入互相覆盖的多条。实现中先按「还原原名再匹配」写了 `TrimPrefix`，测试立刻证明它站不住：`EnsurePrefix` 是幂等的，最终名可能本就不含引擎加的前缀，还原反而截断了名字——遂改为直接匹配最终名（语义上也更贴近用户看到的指标名），并删掉该函数。测试新增 9 例：预设合法性/可共存/元信息齐全、5 个预设各用真实形态样本核对留与挡、保留维度、未声明聚合只留一条、四种聚合方式取值正确 |
| 2026-09-26 | **C1 阶段二 D 完成 → 阶段二收口**：前端新增「采集项模板」管理页（列表直接显示各类的**采集情况**或「已配置但无数据」标记；新建/编辑弹窗对 id/title/kind/groups/targets 用表单、规则区用 JSON 文本域 + 服务端校验，把精确原因直接列给用户）；中间件页的 Tab 改为**由总览接口动态追加**模板派生类型（深链 `?tab=` 白名单同步动态化），模板类型共用一个**通用 Tab 组件**（实例表 + 模板声明的摘要指标 + 空状态排查指引）；首页卡片追加模板类型（空状态文案区分「已配置但无数据」与「尚未配置」）；大屏类型清单本就来自总览接口，补上实例列表与参数趋势指标。`build/verify-template-delivery.sh` 增加「前端所消费的两个接口」契约断言（含未知类型 404）；读路径中依赖真实 TSDB 的三项显式标 SKIP 并指向覆盖它们的单测。前端构建与 14 项单测通过 |
| 2026-09-26 | **C1 阶段二 C 完成（后端全部就绪）**：新增 `internal/server/mwreg` 中间件类型注册表，把「有哪些中间件类型 / 每类存活指标 / 卡片与报告展示哪些指标」从四处硬编码收敛为一份数据源（api 的 middlewareTypes 与 mwSummarySpecs、report 的 mwDefs 与 docker 特例与 throughputMetric、alert 的 serviceMetric 与 KnownServices）。**模板即成为一个独立中间件类型**：出现在中间件总览卡片、巡检报告分节，并可用「服务离线」规则监控。收敛时发现两处真实漂移（报告侧 Kubernetes 类型键写成 kubernetes、报告侧完全没有 FastDFS 条目）并修正；另有一处**看似漂移实为刻意**（报告侧 Docker 以容器总数指标是否存在判定守护进程存活，避免 0 容器误判离线）——改回原语义并在注册表里用 `ReportUpMetric`/`ReportPresenceUp` 显式声明。模板类型的存活告警透传 `{"template": <id>}` 标签过滤，否则会被其它模板的实例误触发。新增模板类型通用实例接口 `GET /api/v1/middleware/{type}/instances`。测试 24 例（注册表 8 + API 6 + 告警 3 + 收发 6 + 存储/CRUD 复用）。仅剩 D（前端：模板管理页 + 中间件 Tab 动态化） |
| 2026-09-26 | **C1 阶段二 A+B 完成**：模板从「逐台写 agent.yaml」变为「Web 端统一存管 + 下发」。A：`internal/server/templates` 存储（按 id 索引、保持顺序、单调 revision、**校验整个候选集合**而非单条——id 互为前缀是跨条约束）、临时文件+rename 原子落盘、失败回滚内存；CRUD/校验 API（新建 id 冲突返回 409 而非覆盖、更新禁止改 id——id 决定指标名前缀）；`middleware:write` 由此从「预留权限点」变为真正启用。B：下发走**上报响应体**（不新建接口、Agent 不需开放入站端口），三道闸门（声明 `capabilities.templates` / 版本号落后 / 按分组过滤），空集合照样下发以让 Agent 清空已删模板；Agent 侧 `ApplyDelivered` 先整体校验再原子替换，非法下发**保留旧模板**并按版本号去重告警（Server 持续重发，配置修好后自愈）；**无需重启、无需 SIGHUP**——`CollectAll` 每轮重建任务表，换掉模板集下一轮即生效。`groups` 在共享 DSL 里可选（本机模板填它无意义）、在 Server 侧必填（否则无关节点每轮各报一个 `up=0`）。验证：单测 22 例 + 实机端到端 9 项（`build/verify-template-delivery.sh`，Agent 不配任何本地模板，指标只能来自下发）全过。剩余 C（模板→中间件类型注册化）与 D（前端页面） |
| 2026-09-26 | **C1 阶段一完成**：采集项模板（`prometheus-exporter` / `http-json` / `http-text`）落地，新增中间件类型**只写 YAML 不改 Go 代码**。DSL 落在新包 `internal/agent/template`（`collector` 已依赖 `config`，DSL 放 collector 会成 import 环）；执行侧 `collector/template.go`；每个模板挂一个独立采集任务（继承 E1 隔离）；失败只产 `template_target_up=0` 且不产其它指标（避免旧值被误读为当前值）；标签注入 `node`/`instance`/`template` 且保留名不可覆盖；启动期 fail-fast 校验（id 格式/重复/互为前缀、保留指标族前缀冲突、kind、addr 协议白名单、正则、标签越权），一次报全所有错误；上限硬编码（模板≤20、target≤32、单轮产出≤2000 截断告警、标签≤16、响应体≤8MiB）。**Server 零改动**：产出走 `ReportPayload.Metrics` 通用通路。凭据（basic/bearer/header）支持 `enc:` 密文并接入既有 AES-GCM 解密，不打 json tag、永不进上报体与日志。测试 28 个新用例；实机验证 28 项断言（三模板采集 / 失败隔离 / 无模板与 pre-C1 二进制上报体等价）+ 端到端 11 项断言（Agent → Server → remote_write，用假时序库避免外网依赖）。**阶段一仅解决"能采到"**：中间件 Tab、首页概览、巡检报告与「服务离线」告警需阶段二的"模板 → 中间件类型"注册。版本号按整批递增约定留待发布时提升 |
| 2026-09-26 | **修复指标名不一致缺陷族**（独立提交，不掺新机制）。起因是设计件探索时发现「指标目录登记名与实现不符」，评审前做了**全量审计**（产出方 = agent collector + receiver 的指标名字面量；消费方 = api / report / metrics / analysis + 前端），真实范围比抽样所见大得多：**目录 30 条里 17 条无产出方，消费侧共 27 处引用了不存在的名字**。修复覆盖：目录 17 处改名 + 补 FastDFS 整类（此前连 `CatFastDFS` 常量都缺）与 `k8s_cluster_up`/`docker_container_up`；中间件总览摘要 `nginx_5xx_rate`→`nginx_access_requests_rate`、`fastdfs_storage_total`→`fastdfs_storage_count`，并修正 FastDFS 空间卡片把字节标成 MB 的单位错误；巡检报告 5 处改名；`serviceMetric()` 补 mongodb/fastdfs（此前落 default 用 redis 的存活指标判断这两类是否在线）+ `validService` 白名单同步为可遍历的 `KnownServices`；FastDFS 空间类指标实际单位是字节（已按字节标注）。其中 `redis_maxclients` 属**产出方缺失**（INFO 里有 `maxclients` 但未采集），已在 `redis.go` 补齐，使报告字段真正可用。新增两条守卫测试进现有 `go test` 门禁：`catalog_guard_test.go`（目录每个指标名必须有产出方 + 每个中间件的存活指标必须已登记）、`service_metric_test.go`（服务映射 ↔ 目录一致），并做**注入验证**确认守卫能失败。前端首页概览补 MongoDB / FastDFS 两张卡片（8 类 → 10 类，与 Tab 对齐） |
| 2026-09-26 | **C1 阶段一设计件产出**（`docs/c1-collector-templates.md`，当时待评审；**已评审通过并按 §10 实施完毕**，见后续条目）：先用探索代理把「新增一种中间件」的**完整联动面**盘清——实际为 **约 30 文件 / 55 处变更、10 套同构重复形态**（此前估计的「6 处」偏窄，漏了第 7 类：告警默认规则与 `serviceMetric`、报告 `mwDefs` 与 5 处 switch、指标字典、大屏 5 组件、安装脚本与 parity 脚本）。设计件据此定为：阶段一在 **Agent 侧**加 3 类 kind（`prometheus-exporter` / `http-json` / `http-text`），复用 E1 的 `runTasks` 隔离与 `fetchMetrics`、`parsePrometheusTextWithPrefix`，每模板一个任务（20 个封顶）、失败仅产 `template_target_up=0`、保留前缀与基数上限启动期 fail-fast、凭据沿用 `enc:` 解密且不落日志；**Server 零改动**。同时修正两处判断：① E2 真正"只写模板"需等**阶段二**（模板→中间件类型注册），阶段一指标只进「指标浏览」；② 7 类中 MariaDB 复用既有 MySQL exporter 通路即可。另记录探索中发现的三处**现存缺陷**——指标目录名与实现不符（**7 处，已逐一核对**）、`serviceMetric` 的 8 个 case 漏 mongodb/fastdfs（落 default 回退 `redis_instance_up`）、首页概览缺 2 类——建议各自单独修（详见设计件附录 B） |
| 2026-09-26 | **打包 1.25.0**：本机经 Git Bash 运行 `build/release.sh`（**未上传源码**），交叉编译 linux/amd64·arm64·arm 并组装 `nebula-monitor-v1.25.0-full.tar.gz`（138M）与 `-upgrade.tar.gz`（66M）。脚本自带 manifest 自校验 + 解包后独立 `sha256sum -c` 全通过（upgrade 94/94、full 101/101）；包内二进制与前端产物内嵌版本均为 `1.25.0`。打包前发现并修正一个**部署级缺陷**：`.gitattributes` 为空且该克隆 `core.autocrlf=true`，使 `build/release.sh` 被检出为 CRLF（同目录其它脚本为 LF），在 Git Bash / Linux 下执行会报 `$'\r': command not found`；`deploy/*.sh` 同理——**安装脚本本身可能完全跑不起来**。已在 `.gitattributes` 为 `*.sh` 与 `VERSION` 固定 `eol=lf` |
| 2026-09-26 | **批次二收口 + 版本递增 `1.24.0` → `1.25.0`**：批次二完成 B1 实施（子批次 A~E）、F1 测试体系、D1 告警风暴收敛、F3 自监控与健康检查、D4 告警协作处置、E3 容量预测扩展（主机侧）、A3 数据保留策略；**E2 按决策推迟到 C1 之后**（现架构下每加一个中间件需改 6 处联动，C1 模板化后退化为写模板）。`VERSION` 与前端 `WEB_VERSION` 同步为 `1.25.0`。编译与打包尚未执行 |
| 2026-09-26 | 初稿：候选改造点、优先级矩阵、批次一计划 |
| 2026-09-26 | 落定 4 项决策（E1 一次到位、不加并发上限、D2 含前端、B1 落 docs/ + F2 全量补齐）；新增第 8 节进度追踪表与附录 C I/O 清点 |
| 2026-09-26 | E1 实施：完成并发调度内核 `runTasks` + 7 个单元测试；`config.go` 新增 `collectTimeout`（默认 8s）。本地 `go build ./...` 通过、`go test ./internal/agent/...` 全绿 |
| 2026-09-26 | E1 实施：新增 `fetch.go` 统一 ctx 感知 HTTP 拉取；完成 rocketmq、nginx、fastdfs、port 四个采集器的 `CollectCtx`（HTTP + 裸 TCP）。提交 `54e6a71` |
| 2026-09-26 | E1 实施：完成 mysql、postgres 的 `CollectCtx`，Ping/Query/QueryRow 全部改 `*Context`（15 处）。提交 `e19169e` |
| 2026-09-26 | E1 实施：完成 redis、k8s、docker、mongo、kafka 的 `CollectCtx`；Redis 接入 `context.AfterFunc`+`SetDeadline` 取消在途 RESP 读；HTTP 拉取统一完成；新增 redis 3 个专项测试；修正 mysql 缩进。提交 `52299ec` |
| 2026-09-26 | E1 实施：完成 firewall/security 的 exec 改造（22 处 `exec.CommandContext`）与 collector 的 `*Ctx` 变体；顺带用 gofmt 规范化 firewall.go 的错乱缩进。提交 `8334273` |
| 2026-09-26 | E1 实施：完成主机采集器入口门控（collector/cpu/disk/network）；不可取消的本机只读采集不添加空转 ctx（设计决策）。提交 `21cc0cb` |
| 2026-09-26 | E1 主体完成：`Result` + `CollectAll`（16 任务并发）接线到 `main.go`；删除 11 个会绕过 ctx 的中间件包装；防火墙规则+状态合并单任务。新增 5 个 CollectAll 测试（共 15 个用例全绿） |
| 2026-09-26 | **E1 收尾验证完成**（Ubuntu 24.04 / Go 1.27.1 / 2C2G 实机）：① `CGO_ENABLED=1 go test -race -count=1 ./internal/agent/...` 全绿无竞态；② 新旧 Agent 同节点各跑一轮真实采集，结构比对 IDENTICAL（78 指标/66 签名、43 监听、93 安全事件、9 类实例结构全一致）。**E1 完成，可发布 1.23.8** |
| 2026-09-26 | 新增 `build/verify-agent-parity.sh`：把上述对照验证环境固化为可复用脚本（远端辅助文件由脚本自身生成，含假 exporter / 假 Redis / 上报捕获 / 结构比对），任何 Agent 采集改动均可一键回归。用法 `bash build/verify-agent-parity.sh --baseline <ref> [--race] [--install-go] [--keep]`。实测通过 |
| 2026-09-26 | **D2 后端完成**：`alert/pipeline.go`（relabel/enrich/template + `PipelineStore` 热加载）、`AlertEvent` 新增标签视图 `Labels`、引擎 `notify`/`flushGroup` 双派发点接入、三个 API（GET/PUT/preview）。8 个管道单测通过 |
| 2026-09-26 | **D2 前端完成**：`settings/AlertPipelineSubView.vue`（三阶段表格编辑 + when 简写 + 试算预览 + 配置直编）、SettingsView 注册标签页、http.js 三个方法；接口错误响应 JSON 化（前端可展示校验文案）。另补 6 个 API 冒烟测试。`npm run build` 通过 |
| 2026-09-26 | **直编改 YAML**：引入 `js-yaml`（v4 具名导入），直编面板改为与服务端落盘同格式的 YAML，新增语法检查与类型规整（`str/toArray/asArray/whenOrNull`）。`npm run build` 通过 |
| 2026-09-26 | **F2 完成**：README 统一「十类」、路线图移除已实现的 RBAC 前端页并新增「权限管理界面」小节、API 章节按代码重写（44 行 → 127 行，修正 `docker/containers` 错路径，双向差集为空）；`vite.config.js` / `release.sh` 三处 embed 表述修正；Makefile `build-web` 改为调用 `build/build-web.sh` |
| 2026-09-26 | **B1 完成**：产出 `docs/permission-matrix.md`（126 条路由逐条映射、7 条兼容策略、5 批实施顺序、7 项开放问题）；校正现状 authz 覆盖为 **14 条**（原记 16）；发现 `/ws` 存在 topic 级授权与范围校验缺口。**批次一全部完成** |
| 2026-09-26 | **批次二启动：B1 批次 A（基础设施）完成**：新增 `dashboard:write` / `system:config` 权限点并补齐内置角色；新增 `api.API.permit(next, perm)`（未启用认证放行 / 未登录 401 / 缺权限 403 + 授权拒绝审计）；移除 `globalAuthStore` 包级单例，`AuthMiddleware` 改为显式传参。新增 11 个单测，全量测试绿 |
| 2026-09-26 | **B1 批次 B（主机与指标 + `/ws`）完成**：23 条路由挂载授权（`permit` / `permitNode`），节点列表、全节点指标、分组列表按资源范围过滤，批量升级整体校验，**`/ws` 补齐 topic 级授权与节点范围校验**（修复设计件 8.2 越权缺口）；告警管理员补 `nodes:read`/`groups:read`；前端 3 条路由与 2 个菜单项补 `perm`。新增 14 个测试，全量测试与前端构建均绿 |
| 2026-09-26 | **B1 批次 C（中间件）完成**：13 条路由挂 middleware:read；10 类实例列表（含 Nginx 访问汇总）按「实例→所属节点→分组」过滤，新增 `nodeGroup`/`nodeInScope`/`nodeOfKey`/`filterByNodeScope` 辅助；`overview` 的实例计数与告警计数改为过滤后重算；K8s 工作节点与 Pod 按可见集群归属过滤；前端中间件菜单与路由补 `perm`。新增 8 个测试（含 fake 存储端到端用例）。聚合类数值未过滤列为已知限制（设计件 8.8） |
| 2026-09-26 | **B1 批次 D（告警与通知）完成**：26 条路由挂权限点（事件 / 规则 / 抑制 / 分组 / 事件管道 / 维护窗口 / 通知）；告警列表、确认记录、统计看板按节点范围过滤与重算，确认接口对范围外节点 403；事件管道 `preview` 按「只读试算」语义只需 `alerts:read`（不按 HTTP 方法机械推导）；规则临时静默归 `silence:write`；前端 3 个菜单与 3 条路由补 `perm`。新增 7 个测试（含 AlertStore / RulesProvider 替身） |
| 2026-09-26 | **B1 批次 E（安全、系统与其余）完成，B1 实施全部收尾**：37 条路由挂权限点 + **新增 `GET /api/v1/audit/export`**（按设计件 8.3 拆分「查看/导出」）；安全事件、基线、防护状态与任务列表按节点范围过滤；`permitNode` 扩展支持 `{node}` 路径参数；前端审计导出改调新路由，8 个菜单与 6 条路由补 `perm`。新增 4 个测试（含 12 个权限点区分用例）；既有审计鉴权测试迁移到 `permit` 层并加强。README API 表同步新增路由（127 条）。**五个子批次 A~E 全部完成** |
| 2026-09-26 | **进度追踪补全**：批次二 / 批次三由占位表展开为逐子项追踪表（批次二 27 行、批次三 9 行、待办 4 行）；新增「版本与发布」现状表，明确 `VERSION` 仍为 `1.23.7`、本批未递增版本号；修正批次一中两处把计划版本当作实际版本的表述 |
| 2026-09-26 | **版本递增**：按决策将 E1/D2/F2/B1（含子批次 A~E）整批合并递增为 **`1.24.0`**（次版本号递增：已启用认证部署的运行时行为变更 + 新增 1 条路由与 2 个权限点）；前端版本号经 `npm run version:sync` 同步为 `1.24.0`。原计划的分次递增（1.23.8 / 1.23.9）未执行，第 6 节执行顺序图与第 8 节「版本与发布」表已同步实际结果。打包仍待明确指示 |
| 2026-09-26 | **打包**：`build/release.sh` 组装出 `nebula-monitor-v1.24.0-full.tar.gz`（138 MB）与 `-upgrade.tar.gz`（66 MB）；包内 `manifest.json` 版本、二进制注入版本、前端 `WEB_VERSION` 均为 `1.24.0`，包内 `SHA256SUMS` 逐条校验通过；未执行任何部署/升级 |
| 2026-09-26 | **F1 完成**：Go 侧补齐 5 个目标点单测（storage 编码与注入防护、告警状态机、鉴权 token、两处国密错误分支）；前端引入 Vitest（`api/http.js` + `useAuth`，14 用例）；新增 `ci.yml` 并把测试设为 `release.yml` 发布门禁；顺带修正 `release.yml` 的 Go 版本（1.22 → 1.25，与 go.mod 一致）。全量 Go 测试与前端构建均绿 |
| 2026-09-26 | **A3 完成**：数据保留策略。新增 `internal/server/retention`（配置热生效 + 周期清理 + 立即清理 + 现状汇总）。纳管两类真正会无界增长的数据：**告警处置记录**（只清理「已处置且超过保留期」的记录——待处理的必须保留，否则「重新打开」的告警会被静默清掉）与**巡检报告**（HTML 文件与历史记录必须同步删除，只删其一会分别造成「点开 404」与「文件永远占磁盘」）。审计与安全事件本就有 2000 条内置上限，改为导出常量并在界面展示现状，不再无界增长。时序库保留期**只读呈现**（探测 VictoriaMetrics `/flags`），因为运行期由时序库启动参数决定，Server 不假装纳管。新增 3 条接口（`system:config`）与「系统设置 → 数据保留」页。新增 10 个测试（7 retention + 3 api） |
| 2026-09-26 | **E3 完成（主机侧）**：容量预测由磁盘扩展到内存 / CPU / 网络。核心是「按指标语义分模型」——百分比类（`disk_used_percent`/`mem_used_percent`/`cpu_usage`）预测达到上限的天数，速率类（`network_recv_rate`/`network_sent_rate`）无绝对上限，只给预测值与增长状态 `rising`（要求拟合度 ≥0.6 且 7 天增幅 ≥20%，避免日常波动刷屏），不产生 DaysRemaining。结论文案与单位来自指标目录（新增 `metrics.Meta`），并把完整结论放进 `Forecast.Summary`，前端不再拼中文与单位。CPU 用「打满」而非「耗尽」措辞，建议也按指标区分（内存→泄漏/扩容、CPU→热点进程、网络→带宽）。`collectBaseline`+`collectDisk` 合并为 `collectMetric`：同一指标一份数据同时服务基线与预测，查询次数 3 → 5（而非 8）。新增 11 个测试（含「速率类无上限」「指标目录元数据不变量」「单位格式化」）。**中间件容量预测保留为独立项**（需定义各类中间件的容量上限语义） |
| 2026-09-26 | **D4 完成**：告警认领与协作。`AckStore` 由「一次性确认标记」升级为处置状态机（pending/ack/closed + 指派 + 关闭原因 + 评论时间线），旧记录通过 `EffectiveStatus()` 视为已认领以保证升级语义不变；新增 `POST /alerts/close|reopen|comment`（均 `alerts:write`，节点资源范围经 `decodeAlertAction` 统一校验），`/alerts/ack` 扩展为可同时指派与评论；接口统一回写最新处置记录，前端无需二次拉取。前端状态列与详情抽屉接入处置流。新增 12 个测试（7 store + 5 路由级，含「认领后移出待处理、重新打开后回到列表」的联动断言）。新增路由 3 条 → 133 条 |
| 2026-09-26 | **D1 默认值调整**：风暴收敛改为**开启分组即默认收敛**。`Converge` 由 `bool` 改为三态指针：字段缺失（nil）= 默认开启，显式 `converge: false` 才关闭——普通 bool 无法区分「未配置」与「显式关闭」，会把用户写下的 false 悄悄翻回默认值。新增 `ConvergeEnabled()` 访问器（调用方一律用它，不得直接读指针）与 `DefaultGroupingConfig()`；`GET /api/v1/grouping` 在未注入存储时也返回完整默认值，避免前端拿不到收敛字段而显示成「关闭」。新增三态专项测试（YAML/JSON 两路 + 显式关闭不被翻回） |
| 2026-09-26 | **F3 完成**：自监控与健康检查。新增 `internal/server/selfmon`（进程/HTTP/时序库/通知/WebSocket/告警/节点指标，快照与 `self_*` 指标同源）；存储与通知器用**装饰器**采集，不改动既有调用点；指标每 30 秒写入现有 TSDB（复用查询、图表与告警规则，而非另造 `/metrics`）；新增公开探针 `/healthz`（存活）与 `/readyz`（就绪：时序库真实即时查询 + 告警评估节拍，未就绪 503 并给出逐项原因）；新增 `GET /api/v1/self/status`（`dashboard:read`）；`api.MetricsMiddleware` 置于中间件链**最外层**（401/403 亦计入）并实现 Hijack/Flush 透传（否则 `/ws` 握手会全部失败）；引擎新增 `SetEvalObserver`/`ActiveCounts`。前端新增「系统设置 → 系统自监控」页（探针徽标 + 关键指标 + 按渠道通知 + 近 1 小时趋势）。新增 17 个测试（9 selfmon + 8 api） |
| 2026-09-26 | **D1 完成**：告警风暴收敛落地。`GroupingConfig` 新增 `converge`/`convergeBy`/`convergeWindow`/`headCount` 并统一 `normalize()`；`Grouper` 新增相似度聚类键与时间窗换代（旧代异步发出，避免在持有 engine 锁的调用栈内回调造成自锁）；新增 `alert/converge.go` 实现「头部告警 + 规则/范围/级别统计 + Top N + 折叠计数 + 关联结论」；关联结论经 `CorrelationProvider` 由 API 层反向注入（analysis 依赖 alert，不可反向 import）；前端分组面板新增收敛开关与参数。初始默认关闭（随后按决策改为「开启分组即默认收敛」，见下方记录）。新增 19 个测试 |

---

## 附录 A：对标项目速查（2026-09 快照）

| 项目 | 语言/许可 | Star | 架构要点 | 关键差异 |
|---|---|---|---|---|
| Nezha 哪吒监控 | Go / Apache-2.0 | 10.3k | Agent + Dashboard，gRPC/Protobuf，SQLite | 轻量、Web 终端/定时任务；**无中间件监控** |
| Apache HertzBeat | Java 25 / Apache-2.0 | 7.4k | Agentless，Manager+Collector+Warehouse+Alerter，YML 协议模板 | 功能面最接近；**模板化扩展**是其杀手级优势 |
| Beszel | Go+PocketBase / MIT | 25.8k | Hub-Agent，SQLite | 主机/Docker/S.M.A.R.T./GPU 覆盖细；**无中间件** |
| Nightingale 夜莺 | Go / Apache-2.0 | 13.3k | 告警引擎为核心，不自带采集（配 Categraf），Remote Write | 告警治理最强（四层规则/事件管道/自愈/MCP） |
| Netdata | C/C++ / GPL-3.0 | 80.7k | Agent 自带分层 TSDB + UI（:19999），边缘 ML | 800+ 集成、逐秒采集；GPL 传染、以单机视角为主 |
| Zabbix | C+PHP / AGPL-3.0 | 6.4k | Server/Proxy/Agent，需 MySQL/PG | 功能最全、模板最厚；部署与学习成本最高 |
| Uptime Kuma | Node.js / MIT | 91.8k | 主动拨测，Socket.IO，SQLite | 拨测与通知渠道最丰富；**无资源监控** |
| Telegraf | Go / MIT | 17.8k | 300+ 插件，静态单二进制，TOML | 通用采集器；**无存储/告警/UI** |

---

## 附录 B：证据索引

| 主题 | 位置 |
|---|---|
| Server 启动与依赖装配 | `cmd/server/main.go` |
| Agent 启动与采集主循环 | `cmd/agent/main.go:77-154`；`collectAndReport` 在 `:253-367`；`firewallStatus` 依赖在 `:283-284` |
| 采集聚合器 | `internal/agent/collector/collector.go:11-100`（`New`）、`:104-284`（`Collect*`） |
| 采集器内部锁 | `collector/cpu.go:14`、`disk.go:14`、`network.go:13`、`security.go:64`、`redis.go:25` |
| 路由总入口 | `internal/server/api/query.go:123+` |
| RBAC 包装器 | `internal/server/api/auth.go:226`；公开白名单 `:141`；单例 `:217`；登录限流 `:47` |
| 资源范围过滤 | `internal/server/auth/policy.go`（`FilterByGroup` / `CheckBatchGroups`） |
| 前端磁盘托管 | `internal/server/api/spa.go`；`internal/server/config/config.go`（WebDir） |
| 告警引擎 | `internal/server/alert/engine.go`（`evaluate` / `fire` / `resolve` / `maybeEscalate`） |
| 告警抑制与分组（Store 模式参考） | `internal/server/alert/inhibit.go`、`grouping.go` |
| 通知渠道 | `internal/server/alert/notifier.go` |
| 告警认领 | `internal/server/alert/ackstore.go` |
| 上报接收与鉴权 | `internal/server/receiver/receiver.go` |
| remote_write 编码 | `internal/server/storage/writer.go` |
| RBAC 前端页 | `web/src/components/rbac/{UsersView,RolesView}.vue`；`web/src/router/index.js:36-37` |
| 构建与发布 | `build/{cross-compile.sh,release.sh,build-web.sh}` |

---

## 附录 C：E1 ctx 改造 I/O 清点（行号为改造前基线）

> 用途：会话上下文丢失后可直接据此恢复「哪些调用点必须改 ctx 版本、哪些不可取消」。
> 路径根：`internal/agent/collector/`。

### C.1 HTTP 拉取（可传 ctx，统一改 `fetchMetrics(ctx,…)`）

| 文件 | 调用点 | 现状 | 自身超时 |
|---|---|---|---|
| `redis.go` | `collectExporter` L321 `client.Get(ExporterURL)` | 无 ctx | 5s（L320） |
| `mysql.go` | `collectExporter` L213 `client.Get` | 无 ctx | 5s（L212） |
| `postgres.go` | `collectExporter` L148 `client.Get` | 无 ctx | 5s（L144） |
| `nginx.go` | `collectStubStatus` L70 `client.Get`；`collectExporter` L148 `client.Get` | 无 ctx | 5s（L64 / L145） |
| `rocketmq.go` | `getRocketMQJSON` L204 `client.Get`（被 L69/98/108/118/135 调用）；`collectExporter` L170 | 无 ctx | 5s（L51 / L169） |
| `k8s.go` | `getJSON` L316 `client.Do(req)`（req 由 L308 `http.NewRequest` 构造）；`collectExporter` L334 | 无 ctx | 10s（L431/L476 / L333） |
| `docker.go` | L245 `listContainers`、L259 `listImages`、L273 `getContainerStats` | 无 ctx（但 unix 传输层 L221-223 已用 `DialContext`） | 10s（L227/L230） |
| `mongo.go` | **公共 helper** `fetchPrometheusText(rawURL)` L288-304，被 `mongo.go:68` 与 `fastdfs.go:67` 复用 | 无 ctx | 8s（L290） |
| `kafka.go` | `collectExporter` L197-201 | **已有 ctx（正确范式，可照搬）** | 5s |

> 说明：**拉取分散、解析统一**。公共解析函数为 `parsePrometheusTextWithPrefix(text,node,instance,prefix,now)`（`mysql.go:348`，被 mysql/postgres/nginx/kafka/rocketmq/mongo/fastdfs 复用）；Redis 用自有 `parsePrometheusText`（`redis.go:763`）；K8s 用 `countKSMSeries`/`sumKSMValue`（`k8s.go:369/383`）。

### C.2 数据库（MySQL / PostgreSQL，改 `*Context` 版本）

| 文件 | 调用点（行号） | 改法 |
|---|---|---|
| `mysql.go` | `db.Ping()` L61 | `PingContext` |
| `mysql.go` | `db.Query` L281（global status）、L299（global variables）、L318（slave status）、L265（group replication role） | `QueryContext` |
| `mysql.go` | `db.QueryRow` L256（stmt latency） | `QueryRowContext` |
| `postgres.go` | `db.Ping()` L67 | `PingContext` |
| `postgres.go` | `db.QueryRow` L201、L232、L243/251/260、L270、L277、L285/294 | `QueryRowContext` |

### C.3 裸 TCP（改 `net.Dialer.DialContext`）

| 文件 | 调用点 | 备注 |
|---|---|---|
| `redis.go` | `dialRedis` L658 `net.DialTimeout(3s)` | **另需** `context.AfterFunc` + `conn.SetDeadline`，否则 L698 `ReadString` / L719 `io.ReadFull` 仍不可取消 |
| `fastdfs.go` | `collectLiveness` L118 `net.DialTimeout(3s)` | dial 后即关闭 |
| `port.go` | `Collect` L39 `net.DialTimeout(3s)` | 逐端口循环，需加 `ctx.Err()` 检查 |

### C.4 exec（改 `exec.CommandContext`）

| 文件 | 调用点 |
|---|---|
| `firewall.go` | `collectFirewalld` L40/44/50/58；`collectIptables` L119/123；`collectNftables` L188/192；`collectUfw` L277/281；`systemdUnitState` L342/346/349；`collectFirewallStatus` L367-379 / L395-404 / L422-432 / L443-450 |
| `security.go` | `ufw status` L515；`firewall-cmd --state` L524；`iptables -L -n` L530；`fail2ban-client status` L545；`cmdExists`→`LookPath` L679 |

### C.5 不可取消（仅入口 `ctx.Err()` 检查）

| 类别 | 文件 |
|---|---|
| gopsutil 本机指标 | `cpu.go`（`cpu.Times` L27、`cpu.Counts` L54/67、`cpu.Info` L64、`host.Info` L77）、`disk.go`（L37/L69/L78）、`network.go`（L26/L76）、`process.go`（L20 + L32-76）、`memory.go`（L10/L29）、`load.go`（L11）、`users.go`（L14）、`listener.go`（L22/L66-69/L136）、`hostinfo.go`、`addr.go` |
| 文件 / `/proc` 读取 | `nginx_access.go`（`os.Open` L221/L270、`Seek` L285、`ReadString` L300、`Stat` L222/L277）、`security.go`（FIM 基线 L105/L128、SSH 日志 L170、sudo 日志 L339、`sshd_config` L467、`/etc/shadow` L558、`sha256File` L644、`enumProcesses` L729 / L741 / L743）、`k8s.go`（kubeconfig L491、CA/cert/key L521/L532/L537） |
| sarama（Kafka） | `kafka.go` broker RPC 无 ctx 接口（L58/L102/L122/L136/L127/L171/L184）→ 由 `Net.DialTimeout`/`Net.ReadTimeout`（L53/L54，5s）兜底，**标注为已知边界** |

### C.6 并发正确性关注点

| 项 | 结论 |
|---|---|
| 采集器内部锁 | `cpu.go:14`、`disk.go:14`、`network.go:13`、`security.go:64`、`redis.go:25`（`clusterSeedsMu`） |
| `redis.go` `clusterSeeds` | 有锁（L297/306），实例级 |
| `security.go` `sshOff` | **读未加锁**（L169/L338），写加锁（L217-219）——每轮单次调用不构成竞态，若future 改为并发调用需补锁 |
| `nginx_access.go` `files` / `lastAt` | 无锁（L197-198、L249、L334），实例级，单次调用安全 |
| 包级共享可变状态 | **未发现**（`skippedFSTypes`、`skippedMountPrefixes`、正则均为只读） |
