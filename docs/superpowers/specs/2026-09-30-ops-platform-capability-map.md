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
| 1 | 监控告警 | 9 | 6 | 0 | 3 | P1 |
| 2 | 资产 / CMDB | 9 | 0 | 1 | 8 | **P0** |
| 3 | 配置管理 | 6 | 2 | 0 | 4 | P2 |
| 4 | 自动化执行 | 6 | 0 | 3 | 3 | P1 |
| 5 | 发布部署 | 4 | 1 | 0 | 3 | P2 |
| 6 | 工单流程 | 4 | 0 | 1 | 3 | P2 |
| 7 | 堡垒机 / PAM | 7 | 3 | 0 | 4 | P2 |
| 8 | 容器 / K8s | 8 | 2 | 0 | 6 | **P0** |
| 9 | 日志 | 8 | 5 | 1 | 2 | P0 |
| 10 | 链路追踪 | 3 | 0 | 0 | 3 | P3 |
| 11 | 巡检合规 | 6 | 3 | 2 | 1 | P1 |
| 12 | 报表大屏 | 8 | 6 | 0 | 2 | P2 |
| 13 | 权限审计 | 7 | 5 | 0 | 2 | P0 |
| 14 | 开放集成 | 7 | 4 | 0 | 3 | P2 |
| | **合计** | **92** | **37** | **8** | **47** | |

**一句话结论**：采集、告警、存储、权限、审计、离线交付这一侧**已经很扎实**（37 项已实现多集中于此）；缺口集中在**"从数据到管理"的四件事——模型化（资产）、操作面（容器/执行）、索引化（日志/检索）、闭环化（工单/审批/自愈）**。

## 三、逐领域功能点表

### 3.1 监控告警

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 主机指标采集（CPU/内存/负载/磁盘/网络/进程/硬件信息） | 已实现 | `internal/agent/collector/`（hostinfo 等），`README.md` §主机监控 | — | — | — | — |
| 中间件监控（10 类，直连 + exporter 双模式） | 已实现 | `internal/agent/collector/{redis,mysql,postgres,nginx,kafka,docker,rocketmq,k8s,mongo,fastdfs}.go` | — | — | — | — |
| 服务拨测（HTTP/HTTPS/TCP/ICMP + 证书到期） | 已实现 | `internal/server/dialtest/scheduler.go` | — | — | — | — |
| 规则类型（阈值/主机离线/中间件离线/主从切换/集群损坏/安全事件） | 已实现 | `internal/model/metric.go:649-661`（`RuleTypeThreshold`…`RuleTypeSecurityEvent`） | — | — | — | — |
| 告警治理（三层静默 / 抑制 / 分组 / 收敛 / 升级 / 维护窗口） | 已实现 | `internal/server/alert/`（含 `engine.go` 求值、`rules.go:DefaultTemplates`） | — | — | — | — |
| 通知渠道（邮件 / Webhook / 钉钉 / 飞书 / 企业微信） | 已实现 | `internal/server/alert/notifier.go` | — | — | — | — |
| 告警协作处置（认领 / 指派 / 关闭 / 评论） | 已实现 | `internal/server/alert/ackstore.go`、`internal/server/api/alert_collab.go` | — | — | — | — |
| **告警 ↔ 资产/变更关联闭环** | 未实现 | 无资产模型可关联；`README.md` 路线图「依赖拓扑与影响传播」 | **P1** | M | 资产与配置（D2） | D2 |
| 值班排班（On-Call）/ 电话短信 · 外部平台（PagerDuty 等） | 未实现 | `README.md` 路线图 P2 | P3 | M | — | — |

### 3.2 资产 / CMDB

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| 资产自动发现（主机信息/分区/进程/中间件实例/容器清单） | **已实现** | 上报链路已接入：`internal/server/receiver/assets.go:applyAssets`（主机 + 15 类中间件实例，幂等）；测试 `internal/server/receiver/assets_test.go` | **P0** | M | — | D2 |
| 资产台账（资产类型 / 资产实例 / 属性/标签 / 负责人） | 部分实现 | 台账读写已落地：`internal/server/asset`（SQLite）、`GET /api/v1/assets`；**缺人工维护写接口与负责人/标签字段**（`assets:write` 未设）；前端未做 | **P0** | M | 关系型持久化（ADR-0001，已完成） | D2 |
| 资产关系与拓扑（依赖 / 归属 / 影响传播） | 部分实现 | `runs_on`（实例 → 主机）已随上报自动建立且幂等（`asset.Service.Link`）；`member_of/depends_on/exposes` 与拓扑视图未做 | **P0** | L | 资产台账 | D2 |
| 变更历史与审计（字段级 diff / 操作留痕） | 部分实现 | 字段级 diff 已落地：`asset_changes` + `asset.Service.History` + `GET /api/v1/assets/{id}/history`（**仅真变化才记录**，首建只写一条 initial）；缺前端时间线与人工写操作的审计联动 | **P0** | M | 资产台账 | D2 |
| 采集值与人工值分离（来源标记 / 冲突可见） | **已实现** | 属性联合主键含 `source`，`Asset.Value` 取生效值（人工优先），接口返回 `values` + `attrs`（两来源并存） | P1 | M | 资产台账 | D2 |
| 资产生命周期（上线/下线/退役/成本/维保） | 未实现 | `README.md` 路线图「CMDB（P2）：资产台账、生命周期与变更记录」 | P2 | M | 资产台账 | D2 |
| 资产与资源范围的映射/迁移 | **已实现** | 接口按资产所属节点走既有范围判定（`api/nodeInScope`）：范围外资产按 404 返回、先过滤再计数；权限点 `assets:read` 已注册并授予运维/只读角色 | **P0** | M | 资产台账 | D2 |
| 配置快照与差异巡检（配置项级） | 部分实现 | 快照写入/读取已实现：`snapshots` + `snapshot_fields`（同事务）、`asset.Service.Snapshot`；**差异比对与巡检运行未做**；现行 FIM 仍只做文件 SHA256（`internal/agent/collector/security.go:loadFIMBaseline/sha256File`） | **P0** | M | 资产台账 + 配置采集 | D2 |
| 资产台账页（列表/详情/关系图/变更时间线） | 未实现 | 后端接口已就绪，前端无 `asset/` 目录（顶层页面清单见 §3.13） | **P0** | M | 后端接口（已完成） | D2 |

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
| 任务下发通道（Server → Agent 执行后回传） | 部分实现 | 已有两条专用链路（升级、fail2ban），未泛化：`internal/server/node/manager.go:ConsumeUpgrade`、`internal/agent/defense/executor.go` | **P0**（作为 D3 前置） | M | — | D1/D3 |
| 执行护栏（本机护栏/能力协商/中心授权+审计） | 部分实现 | 现仅用于 defense 与采集项模板（后者已移除）；审计入口 `api/audit.go:AuditMiddleware`、`auth/policy.go:HighRiskPermissions` | **P0** | M | — | D1 |
| 批量命令 / 脚本执行 | 未实现 | `README.md` 路线图「批量命令 / 脚本执行 P1」 | P1 | L | 任务下发通道 + 护栏 | — |
| Web 在线终端 | 未实现 | 前端无终端组件；`grep` 无实现 | P2 | L | 批量执行通道 | — |
| 文件分发 | 未实现 | — | P2 | M | 任务下发通道 | — |
| 定时计划任务 | 部分实现 | 仅拨测有调度器：`internal/server/dialtest/scheduler.go`；无通用作业调度 | P2 | M | 任务下发通道 | — |

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
| 会话录制 / 回放 | 未实现 | 全仓无会话代理实现（`bastion\|replay\|pam` 仅文档命中） | P3 | L | 会话代理 | — |
| 统一访问入口（SSH/RDP/K8s/DB） | 未实现 | — | P3 | L | — | — |
| 高危命令拦截 | 未实现 | — | P2 | M | 会话代理或命令审计通道 | — |
| 高风险操作双人复核 | 未实现 | `README.md` 路线图 P2 | P2 | M | 工单引擎 + 权限 | — |

### 3.8 容器 / K8s

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| K8s 集群指标采集（集群健康/Node/工作负载副本/Pod 统计） | 已实现 | `internal/agent/collector/k8s.go:collectPods/collectWorkloads`（只读 GET apiserver） | — | — | — | — |
| Docker 容器指标采集 | 已实现 | `internal/agent/collector/docker.go`（Docker Engine API） | — | — | — | — |
| 工作负载列表 / 详情 / YAML 只读视图 | 未实现 | Server 侧无 `*k8s*` handler；仅有指标读取 `GET /api/v1/middleware/k8s/instances` | **P0** | L | 下行通道（ADR-0003）+ 凭据只在 Agent | D3 |
| 事件（Event）查看 | 未实现 | — | **P0** | S | 同上 | D3 |
| 容器/Pod 日志拉取 | 未实现 | Agent 侧 `logship` 只能采文件，不能拉 Pod 日志 | **P0** | M | 同上 | D3 |
| exec 终端（默认关闭、按权限开放） | 未实现 | 需三道门控 + 审计；凭据永不上行 | P2 | L | 只读管理面先落地 | D3 |
| 容器 → 日志/资产联动（Pod 打标回到资产与日志检索） | 未实现 | — | P1 | M | 资产台账 + 日志标签注入 | D2/D3 |
| 镜像/配置安全扫描 | 未实现 | 可选参考 `aquasecurity/trivy`（Apache-2.0） | P3 | M | — | — |

### 3.9 日志

| 功能点 | 状态 | 现状与依据 | 优先级 | 复杂度 | 前置依赖 | 归属 |
|---|---|---|---|---|---|---|
| Agent 日志采集（来源+路径+patterns、多行合并、限速、每日上限、偏移落盘） | 已实现 | `internal/agent/logship/ship.go:Shipper.send`、`internal/agent/collector/logcollect.go:mergeMultiline` | — | — | — | — |
| 日志上行接收（`X-Agent-Secret`） | 已实现 | `internal/server/receiver/logs.go:HandleLogs`（`POST /api/v1/logs`） | — | — | — | — |
| 检索（关键词/正则 + 时间范围 + 节点/来源过滤 + 游标分页 + truncated） | 已实现 | `internal/server/logstore/query.go:Store.Query`、`internal/server/api/logs_api.go:handleLogsQuery`；预算 `DefaultScanBudgetBytes=64MiB`、`DefaultScanBudgetLines=200000`、`DefaultLimit=200`/`MaxLimit=1000`；游标 `Cursor{File,Offset}` | — | — | — | — |
| 日志保留策略（默认 7 天，按天清理） | 已实现 | `internal/server/retention/`（`README.md` §数据保留） | — | — | — | — |
| 由日志创建告警规则（基于日志指标的阈值告警） | 部分实现 | 指标侧规则已就绪（`RuleTypeThreshold`）；**在检索页直接由日志内容建规则未实现** | P1 | M | 日志字段化 | D3 |
| 结构化解析（JSON/正则字段提取、标签注入） | 未实现 | 仅有整行/正则匹配，无字段解析 | **P0** | M | — | D3 |
| 字段检索 / 全文索引 | 未实现 | 无索引，只有有界扫描 | P1 | L | 结构化解析 | D3 |
| 外部日志后端（默认增强自研 + 可选 VictoriaLogs） | 未实现 | 需抽出后端接口（ADR-0002） | P2 | L | 后端抽象 | D3 |

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
| 配置项级差异巡检（配置快照 vs 合规模板） | 未实现 | FIM 只覆盖文件哈希；无字段级 diff | **P0** | M | 资产与配置（D2） | D2 |
| 巡检周期化调度（自动生成报告） | 部分实现 | 报告由 Web 端触发；通用定时任务未实现（`README.md` 路线图「定时计划任务」含自动生成报告） | P2 | M | 定时计划任务 | — |
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
| **资产的资源范围扩展**（资产分组/业务范围） | 未实现 | 现范围只覆盖节点分组；需与资产模型映射且**不得退化为前端隐藏** | **P0** | M | 资产台账（D2） | D2 |
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
| 代理增强（磁盘缓冲/批量合并/主备） | P1 | 平台治理 | P1 | 保留 |
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
- **复核清单**：①每行依据可跳转命中；②对账表覆盖 README 全部 21 条路线图项；③新增项均标注来源设计件；④P2→P3 的降档建议经确认后再写入 `README.md`。
