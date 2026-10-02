# CONTEXT.md — 领域术语表与核心模块导航

本文件是代码审查与日常沟通的统一语言参照。术语表只收「审查必用」的术语：易混淆、有精确边界或存在同名不同物的概念；一目了然的功能名不收录。功能细节与配置说明一律以 `README.md` 为准，本文不重复。

语言约定：中文定义，代码标识符（指标名、配置字段、路径、包名）保留英文原文。

---

## 一、领域术语表

### 拓扑与部署

**节点（Node）**
一被监控机上的一个 `agent.yaml` 部署实例，以 `agent.yaml` 的 `node` 字段为唯一标识。API 路径与代码标识为 `node`（如 `/api/v1/nodes`）。前端页面文案「主机」是同义词，指同一概念。两台机器上的 Agent 配置**相同 `node` 名**时，在 Server 视角合并为同一个节点（采集高可用兜底方案的基石）。

**节点分组（Group）**
节点的归属分组（`agent.yaml` 的 `group` 字段）。它同时是**访问控制资源范围的单位**：受限用户只能看到其范围内分组下的节点；资源范围校验在服务端执行。

**Agent 运行模式（mode）**
Agent 二进制的三种运行模式：`collect`（采集上报）、`edge`（网闸区 A 边界代理）、`hub`（网闸区 B 边界代理）。同一二进制，不同模式职责完全不同。

**Edge / Hub**
网闸两侧的代理模式 Agent。Edge 监听本地口汇聚采集 Agent 的上报、主动拨出 TLS 隧道到 Hub；Hub 接收隧道、还原请求转发给真实 Server。Server 对隧道无感知。自监控指标带 `proxy_*` 前缀。

**时序库（TSDB）**
独立部署的 PromQL 时序库（默认 VictoriaMetrics），经 `remote_write` 写入、PromQL 读取。Server 完全无状态，指标数据的持久化与保留期全部归属时序库（Server 侧「数据保留」页对指标数据只做只读呈现）。

### 采集

**中间件实例（Middleware Instance）**
Agent 配置中的一个被监控中间件连接项（如 `redisInstances[]` 的一个元素）。存活指标为 `<类型>_instance_up`（如 `mysql_instance_up`、`k8s_cluster_up`、`docker_container_up`）。实例凭据（密码/kubeconfig/token）**仅存 Agent 本地，永不上报**；磁盘上以国密 SM4 加密（`enc:<base64>`）。

**集中日志（Central Logs）**
Agent `logSources` 按「来源 + 路径 + patterns」采集日志并上行到 Server（`POST /api/v1/logs`，走 `X-Agent-Secret`）。关键语义：默认只上传命中模式的行；新文件从**文件尾**开始不回溯历史；读取进度落盘（`logOffsetsFile`）不丢不重。其指标（`<来源>_log_*_total`）是**每轮增量值而非累计计数器**——配阈值规则时禁止套 `rate()`/`increase()`。

### 告警

**告警事件与 firing 状态**
告警引擎按规则评估出的真实状态机：`firing` / `resolved`，含持续时长（For）与事件去重。**这是「监控条件的真实状态」，与人工处置无关。**

**告警处置（Ack 状态机）**
人工协作维度上的另一套状态机：**待处理 → 已认领 → 已关闭**，可重新打开。认领（ack）可指派处理人，关闭记录原因，支持评论时间线（存于 `alert_acks.json`）。**处置状态不改变 firing 状态**，不影响引擎重启后的状态恢复与规则评估。「确认」是 D4 之前的旧称，现一律为「认领/处置」。

**三层静默**
① 规则级单次静默（开关 + 截止时间）；② 规则级周期静默时段（星期 + 时间区间，支持跨天）；③ 全局维护窗口（抑制所有通知，事件仍记录）。命中任一即跳过评估或通知。三者是「按时间/范围跳过」的机制。

**告警抑制（Inhibition）**
按**告警间依赖关系**减噪：源告警 firing 时，抑制等价标签相同的目标告警的通知；事件仍记录并标记「已抑制」。源恢复后未再命中的目标告警自动补发通知。

**告警分组（Grouping）**
按**标签聚合**通知：相同分组标签的告警合并，`group_wait` 首次等待 + `group_interval` 汇总。只影响通知聚合方式，不改变告警事件本身。

**告警风暴收敛（Convergence）**
按「收敛维度（默认规则 + 级别）+ 收敛窗口」把多条同类告警压缩为**一条**汇总通知（头部告警原文 + 分布统计 + 前 N 条明细 + 智能分析关联结论）。与分组的关系：分组解决「何时发给谁」，收敛解决「一条通知里放什么」；开启收敛后**按收敛维度聚合、不再按分组标签细分**。分组开启时收敛默认开启，显式设 `converge: false` 才关闭。

**告警升级（Escalation）**
告警持续未恢复超过「升级时间」后提升级别 / 切换升级渠道 / 按间隔重复提醒。升级通知同样受维护窗口与静默约束。

**安全事件**
安全监测中心产出的事件（SSH 暴力破解、FIM 文件变更、基线不合规、异常进程/反弹 shell、sudo 提权），通过**复用告警引擎**派发通知，规则 ID 形如 `security-<检测类别>`。因此安全事件与普通告警共享静默/维护窗口/升级/抑制/分组全套机制。

**告警规则模板（Rule Template）**
「告警中心 → 新建规则」可一键载入的预置配置（阈值、主机离线、各中间件离线、主从切换、集群损坏）。当前系统中「模板」一律指本概念，禁用裸称「模板」。

### 状态语义

**未知 vs 异常（状态页）**
对外状态页（`/#/status`，免登录）上「未知」表示时序库读不到数据，「异常」表示拨测判定服务不可用——两者刻意区分，不能把自己的采集故障说成服务的故障。只有 `public: true` 的拨测任务才出现在状态页；对外展示**刻意不含**目标地址、节点名与错误原文。

**拨测 vs 巡检报告**
拨测（dialtest，主动探测 HTTP/HTTPS/TCP/ICMP 并产出 `dial_test_*` 指标）与巡检报告（report，周期生成 HTML 报告）是两个不相干的功能，中文名相近，审查时勿混。

**动态基线 vs 安全基线**
「动态基线」是智能分析用中位数 + MAD 构建的指标正常区间（持续偏离才算异常）；「安全基线」是安全中心的合规清单评分（0–100）。同名「基线」，两个领域。

**风险评分 / 根因关联**
智能分析输出的是**只读决策辅助**：风险等级、证据、关联置信度与排查建议。关联结果是线索，不是确认的因果；不执行任何自动化处置、不改变任何状态。

### 数据生命周期

**数据保留（Retention）**
Server 本地数据的按天清理：告警处置记录**只清理已认领/已关闭的**（待处理永不清理）、巡检报告按天清理、集中日志默认 7 天；审计与安全事件由内置上限（各 2000 条）淘汰。策略存 `retentionFile`。**指标数据不在此列**——其保留期由时序库启动参数决定。

**自监控指标（self_\* / proxy_\*）**
Server 自身指标以 `self_*` 前缀、代理模式指标以 `proxy_*` 前缀写入时序库，使其可被「指标浏览」与普通告警规则覆盖（如 `self_alert_eval_age_seconds`、`self_tsdb_write_errors_total`）。

**国密加密（crypto）**
两类用途勿混：Server 登录密码用 **SM3 加盐哈希**（`sm3:<salt>:<hash>`，不可逆）；Agent 本地凭据用 **SM4-CBC 对称加密**（`enc:<base64>`，可逆解密后使用）。注意后者的安全边界：`cryptoKey` 未配置时使用**编译进二进制的默认密钥**（Agent 二进制经 CDN 公开分发），此默认档位仅是本机配置混淆防护；真正的机密性依赖在 `agent.yaml` 配置自定义 `cryptoKey`。

**资源范围（Resource Scope）**
用户可配置的节点分组范围，**服务端强制校验**（非前端隐藏）。受限范围未选任何分组 = 无资源权限，不会扩大为全部资源。业务路由已统一配置权限点，节点分组范围在服务端校验（指标读取、hostname 目标、测试告警的权限边界已补齐）；新增接口仍需逐项纳入（见路线图）。

### 演进中术语（部分已落地）

本组术语来自《一体化运维平台》设计（`docs/superpowers/specs/2026-09-30-ops-platform-*`、`docs/adr/0001-0003`）。

**落地状态（2026-10-02）**：`资产`、`资产类型`、`配置项`、`资产关联`、`人工优先`、`关系抑制`、`发现来源`、`配置快照` **已实现**（`internal/server/asset` + `GET /api/v1/assets`，见 `2026-09-30-asset-cmdb-design.md` 的「实施记录」）；`差异巡检` **已实现**（`inspect_runs` / `inspect_findings` / `inspect_baselines`）；`工作负载`、`下行操作`、`高危操作护栏` **已实现**（`internal/server/ops` 动作目录与任务状态机 + `internal/agent/ops` 本机护栏与执行器；容器只读查询即 `container.*`，见 `2026-09-30-container-observability-design.md` 的「实施记录」）；`日志后端` **仍为规划**（ADR-0002，随第二适配器一起做）。全部落地后，把条目移入上方对应分组。

**资产（Asset）**
一个被管理的对象实例：一台主机、一个中间件实例、一个容器、一个服务端点。_避免_：资源、对象（均已被其他含义占用）。

**资产类型（Asset Type）**
资产的定义（字段集合、来源、是否可人工维护）。_避免_：资产模型、模板（「模板」已被告警规则模板占用）。

**配置项（CI）**
资产在某一时刻的**规范化属性集合**，是「资产 + 属性」的视图，**不是独立实体**。_避免_：把「配置项」与「资产」当成两个实体。

**发现来源（Discovery Source）**
属性值的来源：`discovery`（采集，来自 Agent）或 `manual`（人工维护）。人工值**不覆盖**采集值，两者并存且差异可见（与 `Node.DisplayName` 既有语义一致）。

**资产关联（Asset Link）**
两个资产之间的有向关系，当前只保留四类：`runs_on`（运行于）、`member_of`（成员属于）、`depends_on`（依赖）、`exposes`（暴露端点）。
关系自身带**来源**（`discovery` 采集建立 / `manual` 人工建立）：同一 `(起点, 类型, 终点)` 只有一条边，**来源是这条边的属性，不是身份的一部分**。

**人工优先（人工维护的关系优先）**
人工建立（或认领）的关系优先于采集自动发现：一条边被人工认领后，采集继续上报同一条边**不会**把它降级回 `discovery`。
_避免_：把它理解成"两份关系并存"——同一对资产之间只有一条边，人工改变的是这条边的来源。

**关系抑制（Link Suppression）**
人工**解除**一条关系时保留下来的「这条关系不存在」这个判断。解除是**逻辑删除**（删边 + 落一条抑制），采集侧据此不再重建它；
取消抑制即把这条关系交还给采集，采集若仍在上报，下一次就会重新出现。与「采集资产用忽略而不是删除」是同一个判断。
_避免_：把它当成"隐藏一条显示"——它表达的是一条关于现实世界的断言，不只是界面上少显示一行。

**配置快照（Snapshot）与差异巡检（Inspection）**
快照 = 某资产在某一时刻被抽取的**关注字段集合**；差异巡检 = 快照与前次快照、或与合规期望值的字段级比对。注意与「文件完整性监测（FIM）」区分：FIM 只比对**文件哈希**，不做字段级差异。

**工作负载（Workload）**
Kubernetes 中的 Deployment / StatefulSet / DaemonSet / Job 等对象集合。与「中间件实例」不同层面：一个工作负载通常由多个 Pod 组成。

**下行操作（Ops 指令）**
Server 经**上报响应**下发给 Agent 执行的结构化指令（与既有升级、入侵防御指令同一通道）。_避免_：下发任务、远程命令（「任务」留给批量作业，「命令」留给命令行）。

**高危操作护栏**
对可能改变被监控对象状态的操作（终端、配置下发等）的四道门控：① 本机护栏（`agent.yaml` 开关，机器自己掌握）→ ② 能力协商（Agent 只上报已开启的能力）→ ③ 中心授权与审计（专属权限点 + 审计留痕）→ ④ 超时作废。默认**只读**。

**日志后端（Log Backend）**
日志的存储实现：默认自研分片落盘，可选外部后端（如 VictoriaLogs）。与「时序库」并列的另一个存储角色，勿混。

**容器只读管理面（Container Read-only Console）**
「观测监控 → 容器与工作负载」页提供的只读视图：集群清单 / 工作负载 / Pod / 事件 / 对象详情。
它**不是 Server 直连 apiserver**——K8s 凭据只存 Agent 本地，查询以「下行操作」形态异步下发
（延迟约一个上报周期）。详情走**白名单投影**：Pod 环境变量只给变量名、ConfigMap 只给键名、
`secrets` 不在可选资源类型内，**不提供原始 YAML**（脱敏透出漏一个字段就是一次凭据泄露）。

---

## 二、核心模块导航

按审查优先级组织。标注 【安全敏感】 的包是攻击面集中区，标注 【逻辑复杂】 的包是易错区。`_backup/`、`dist/`、`web/node_modules` 不属审查范围；`cmd/yamlcheck` 为空目录。

### 第 1 批：接入与安全面

| 包 | 职责 | 提示 |
|---|---|---|
| `internal/server/receiver` | Agent 上报接收（`/api/v1/report` 鉴权）+ 集中日志上行 + 指令随上报响应下发（升级 / 入侵防御） | 【安全敏感】公开接口入口 |
| `internal/server/auth` | 登录会话、RBAC、用户/角色、登录限流 | 【安全敏感】 |
| `internal/server/crypto` | 国密 SM3 哈希 / SM4 凭据加密 | 【安全敏感】 |
| `internal/agent/defense` | fail2ban 托管（创建专属 jail、防火墙 action、封禁审计） | 【安全敏感】以 root 在被监控机执行 |
| `internal/agent/logship` | 集中日志采集与上行（限速/每日上限/偏移落盘） | 【安全敏感】数据离开被监控机 |
| `internal/server/agentdist` | Agent 二进制 CDN 分发与安装脚本下发 | 公开下载面 |

### 第 2 批：告警引擎与存储

| 包 | 职责 | 提示 |
|---|---|---|
| `internal/server/alert` | 规则评估、firing/resolved 状态机、三层静默、抑制、分组、收敛、升级、处置记录 | 【逻辑复杂】**通知渠道实现也在此包**（`notifier.go`：邮件/Webhook/钉钉/飞书/企业微信） |
| `internal/server/notify` | 通知配置管理器（`notifyFile` 读写、热加载回调、脱敏读取）——**不含渠道发送实现** | |
| `internal/server/storage` | 时序库客户端（remote_write 写入 / PromQL 查询，多后端适配） | |
| `internal/server/retention` | 本地数据按天清理（见术语「数据保留」） | |
| `internal/server/logstore` | 集中日志分片落盘、每日上限、游标式检索（时间倒序、truncated 语义） | 【逻辑复杂】 |

### 第 3 批：其余后端

| 包 | 职责 |
|---|---|
| `internal/server/node` | 节点注册、在线状态（offline 判定） |
| `internal/server/instancereg` / `mwreg` | 中间件类型注册表（内置类型 + 模板派生类型）、实例聚合 |
| `internal/server/dialtest` | 拨测任务调度与结果指标 |
| `internal/server/security` | 安全事件落库、入侵防御任务下发 |
| `internal/server/analysis` | 动态基线、容量预测、风险聚合、根因关联（只读） |
| `internal/server/metrics` | 指标目录 / 活跃指标 / CSV 导出 |
| `internal/server/nginxaccess` | access.log 解析与地理分布聚合 |
| `internal/server/upgrade` | Web 上传升级、备份替换重启、版本归档切换 |
| `internal/server/report` / `dashboard` / `screencfg` / `uicfg` / `audit` / `selfmon` / `config` / `api` | 巡检报告、自定义仪表盘、大屏配置、品牌配置、审计、自监控、全局配置、REST/WS 汇总入口 |
| `internal/agent/collector` | 主机/中间件/端口/安全各采集器（直连 + exporter 双模式） |
| `internal/agent/proxy` | tunnel/edge/hub/连接池/重连/内存缓冲 |
| `internal/agent/reporter` / `upgrader` / `config` / `crypto` | 上报、Agent 自升级、配置加载、本地凭据加密 |
| `internal/model` | Agent↔Server 共享数据模型（metric/log/security） |
| `cmd/agent` / `cmd/server` | 入口（`cmd/yamlcheck` 为空目录） |

### 第 4 批：前端与部署脚本

- `web/src`：Vue 3 + Vite；页面在 `components/`（HostsView、AlertsView、SecurityView、LogsView、StatusView 等），中间件每类一目录（`redis/`、`mysql/`、`k8s/`……），`api/` 为接口封装。
- `deploy/`：`install-server.sh` / `agent-install.sh`（含 `--mode edge/hub`、`--tls-auto`）/ `install-tsdb.sh` / `uninstall.sh`。
- `build/`：`cross-compile.sh` / `build-web.sh` / `fetch-packages.sh` / `release.sh`（full + upgrade 双包）。
