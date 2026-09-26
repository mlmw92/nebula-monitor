# Nebula Monitor 改造方案与批次一执行计划

> 适用范围：Nebula Monitor 全系统（Go Server + Go Agent + Vue 3 Web）。
> 本文档为改造路线与实施规格，供后续开发、测试与评审依据使用。
> 面向终端用户的操作说明见 `README.md`；角色权限模型细节见 `docs/role-permission-management.md`。
> 状态：**批次一执行中**（4 项决策已落定，见第 3 节与第 7 节）。
> 基线版本：`VERSION = 1.23.7`｜成文日期：2026-09-26。

---

## 1. 背景与依据

### 1.1 现状基线

Nebula Monitor 是「Agent 采集 → Server 接收 → 时序库持久化 → Web 展示 / 告警」的 C/S 监控系统：

- **Server 无状态**：指标经 remote_write 写入外部 TSDB（VictoriaMetrics / Mimir / Cortex / Thanos / Prometheus / custom），PromQL 查询；前端由磁盘目录托管（`internal/server/api/spa.go` + `config.WebDir`），**非 Go embed**。
- **Agent 三模式**：`collect`（采集）/ `edge` / `hub`（网闸 mTLS 隧道代理）。
- **能力面**：主机监控、10 类中间件（直连 + exporter 双模式）、拨测、巡检报告、安全中心（SSH 审计 / FIM / 基线 / 异常进程 / fail2ban 托管）、智能分析（只读）、RBAC、系统升级与版本归档切换。

### 1.2 外部对标结论（2026-09 快照）

与 8 个开源项目对比（Nezha / HertzBeat / Beszel / Nightingale / Netdata / Zabbix / Uptime Kuma / Telegraf）：

| 维度 | 结论 |
|---|---|
| 同类中最强项 | 网闸代理（mTLS 单端口多路复用隧道）、中间件拓扑角色识别、安全中心、交付运维体验、国密合规 |
| 最大差距 | **无采集插件/模板体系**（HertzBeat YML 模板、Telegraf 300+ 插件、Netdata 800+ 集成）；新增指标须改 6 处代码 |
| 次要差距 | 采集串行无超时隔离、无自动处置/自愈、无告警协作与 on-call、无集中日志、无 Server 高可用与水平扩展、无 i18n、测试覆盖薄弱 |
| 反向优势 | 无状态 Server + 可切换 TSDB 后端（对比 Nezha/Beszel/Netdata 的本地存储）；一键部署与 Web 升级回滚 |

### 1.3 已核实的事实修正（与 `README.md` 不一致）

| # | README 表述 | 代码实际 | 证据 |
|---|---|---|---|
| 1 | 路线图「前端用户与权限管理页（P0）待实现」 | **已实现** | `web/src/components/rbac/UsersView.vue`、`RolesView.vue`；`web/src/router/index.js:36-37` |
| 2 | 路线图「业务接口权限点与资源范围服务端校验（P0）」 | **属实**：全站仅 14 条 users/roles/permissions 路由受 `a.authz` 保护 | `internal/server/api/query.go:214-229`；`internal/server/api/auth.go:226` |
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

| 批次 | 内容 | 目标 |
|---|---|---|
| **批次一** | **E1**、**D2**、**F2** + **B1 设计件** | 用最低风险拿到采集稳定性与通知体验收益，并为鉴权改造定契约 |
| **批次二** | B1（实施）、D1、F1、D4、E2、E3、A3、F3 | 补齐授权正确性与告警降噪两块硬缺口 |
| **批次三** | **C1（分三阶段，首位）→ C2**、C3、A1 实施、A2/E4 评估 | 攻生态扩展与架构纵深 |
| 待办（不排期） | D3、C4、F4、A4 | 视需求启动 |

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

**版本**：单独发布 `1.23.8`（patch，向后兼容；Agent 与 Server 无需同步升级）。

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

**版本**：与 F2 合并发布 `1.23.9`（**需同步执行 `cd web && npm run build`，并把 web 产物并入升级包**）。

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
任务1 E1  ──►  go build ./... && go test -race ./internal/agent/...  ──►  可独立发布 1.23.8
任务2 D2  ──►  go test ./internal/server/alert/...  +  cd web && npm run build  ──┐
任务3 F2  ──►  文档逐条对照 RegisterRoutes                                        ──┼──►  合并发布 1.23.9
任务4 B1  ──►  设计件评审（无代码，落 docs/permission-matrix.md）                 ──┘
```

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
| 3 | D2 一期范围 | **含前端编辑页** | 一次交付可用功能；需同步执行 `cd web && npm run build`，并把 web 产物并入 `1.23.9` 升级包 |
| 4 | B1 / F2 产出位置 | B1 落 `docs/permission-matrix.md`；F2 API 表**全量补齐** | 一次性成本换取长期可维护性；映射表作为批次二实施依据 |

---

## 8. 实施进度追踪

> 状态标记：⬜ 未开始｜🟨 进行中｜✅ 已完成｜⏸ 阻塞。每完成一格即更新本表。

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
| D2 | | 5. 前端编辑页 + 预览 + 构建 | ⬜ | — | 系统设置子页 |
| D2 | | 6. TDD（含校验失败路径） | ✅ | 1.23.9 | 8 个管道单测（含空管道回归等价） |
| D2 | | 7. `AlertEvent` 标签视图（`Labels` 字段） | ✅ | — | 由事件字段派生 + 事件自有标签，与分组键命名一致 |
| F2 | 文档一致性 | 1. README 8 类 → 十类 | ⬜ | — | README:89 |
| F2 | | 2. vite.config / release.sh 注释修正 | ⬜ | — | embed 表述 |
| F2 | | 3. 路线图移除已实现的 RBAC 前端页 | ⬜ | — | 并补章节入口 |
| F2 | | 4. API 表全量补齐 | ⬜ | — | ~90+ 条 |
| F2 | | 5. Makefile build-web 复用脚本 | ⬜ | — | — |
| F2 | | 6. 措辞治理（面向终端用户） | ⬜ | 1.23.9 | — |
| B1 | 权限映射表设计件 | 1. 路由 → 权限点映射表 | ⬜ | — | 落 `docs/permission-matrix.md` |
| B1 | | 2. 兼容策略与分批顺序 | ⬜ | — | 批次二输入 |

### 批次二 / 批次三（占位）

| 批次 | 内容 | 状态 |
|---|---|---|
| 批次二 | B1 实施、D1、F1、D4、E2、E3、A3、F3 | ⬜ |
| 批次三 | C1（三阶段）→ C2、C3、A1 实施、A2/E4 评估 | ⬜ |
| 待办 | D3、C4、F4、A4 | ⏸ 不排期 |

### 变更记录

| 日期 | 变更 |
|---|---|
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
| 2026-09-26 | **D2 后端完成**：`alert/pipeline.go`（relabel/enrich/template + `PipelineStore` 热加载）、`AlertEvent` 新增标签视图 `Labels`、引擎 `notify`/`flushGroup` 双派发点接入、三个 API（GET/PUT/preview）。8 个管道单测通过，全部 internal 测试通过。**待办：D2 前端编辑页** |

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
