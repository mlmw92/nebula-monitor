# A1 设计件：Server 高可用（多实例 + 状态共享）

> 状态：**设计件待评审**（未开始实施）。
> 范围：`cmd/server` 与 `internal/server/**` 的**运行期状态归属**改造；不涉及 Agent 与 Web 的协议变更（新增的实例标识只影响 Server 之间）。
> 关联：路线图 `docs/refactor-plan.md` 的 A1；权限矩阵 `docs/permission-matrix.md`；数据保留 `docs/…`（A3）。

---

## 1. 目标与非目标

**目标**

1. **高可用**：单台 Server 故障时，服务能在**分钟级**恢复（告警评估、拨测、上报接收不中断或短暂中断后可自愈）。
2. **可水平扩展**（多实例承接同一份数据的读流量），但**只做到「共享同一份状态」这一步**，不追求无限扩展。
3. **迁移路径明确**：现有单实例部署**不改配置也能继续跑**（零破坏）。这是本项目一贯的底线（E1 / C1 / C2 都遵守）。

**非目标（明确不做）**

- 不做分片（sharding）：数据量级（单集群数百节点）远未到需要分片的程度。
- 不做自研共识算法（Raft/Paxos）：需要共识的地方一律用**现成组件**（外部 DB 的行锁/唯一约束、etcd lease、或「主备 + VIP」）。
- 不改 Agent 协议与前端契约（新增字段一律可选）。

---

## 2. 事实基线：当前的单实例假设（已逐条核对到行号）

当前**所有运行期状态都在「本地文件」或「进程内存」里**，唯一天然的跨实例共享层是时序库（`storage.Storage`，`internal/server/storage/vm.go:110`）。逐类盘点：

### 2.1 包级单例 / 全局可变状态

| 位置 | 内容 | 影响 |
|---|---|---|
| `internal/server/api/auth.go:43-52` | 登录失败限流 `loginMu` / `loginFails`（包级 map） | 多实例各自计数 → 限流阈值被放大 N 倍，攻击者可轮询实例绕过 |
| `internal/server/receiver/receiver.go:42-44` | `ProcessCache`（进程快照） | 各实例只看到自己收到的上报 |
| `internal/server/receiver/receiver.go:97-103` | `ListenerCache` / `FirewallCache` / `FirewallStatusCache` | 同上（快照数据按实例分片） |
| `internal/server/instancereg/registry.go:35` | `var Default = New()` | 中间件实例注册表全局单例，多实例各一份 |
| `internal/server/alert/middleware_types.go:14` | 包级可变注册表引用 | 同进程单写，无实质影响 |
| `metrics/catalog.go:82`、`mwreg/builtin.go:11`、`nginxaccess/geo.go:45,114`、`alert/rules.go:414`、`analysis/forecast.go:29` | 只读常量/注册表（编译期或 `sync.Once` 初始化） | 多实例无影响 |

### 2.2 只写本地文件的持久化（逐类）

`users.yaml`（`auth/store.go`）、`nodes.json`（`node/manager.go:314`）、`alert_acks.json`（`alert/ackstore.go:286`）、`alert_inhibit.yaml` / `alert_grouping.yaml` / `alert_pipeline.yaml` / `maintenance.yaml` / `rules.yaml`（alert 包）、`templates.yaml`（`templates/store.go:218`）、`dialtest.yaml`（`dialtest/store.go:115`）、`dashboards.yaml`、`screen.yaml`、`ui.yaml`、`retention.yaml`、`security_store.json`、`defense_tasks.json`、`audit_events.json`、`notify.yaml`、集中日志分片（`logstore`）、巡检报告与 `history.json`、升级包/备份、地理库 xdb、`server.yaml`（改密）。

共同点：**内存缓存 + 原子写本地文件**（统一工具 `config.AtomicWrite`，`internal/server/config/save.go:16`）。多实例部署即各写各的文件，天然不一致。

### 2.3 内存态缓存 / 快照（重启即丢）

- 上述 `ProcessCache` / `ListenerCache` / `FirewallCache`、`instancereg.Default`、`nginxaccess.Window`（访问日志地理聚合窗口）、`node.Manager.lastPayloads` 与 `upgradeQueue`。
- **告警引擎的 firing 状态**（`alert/engine.go:55-61`）——注意它已有「真相来源」：`restoreActiveState`（`:129-148`）能从时序库恢复，但**恢复的是自己实例写过的**。
- 拨测调度器的 `upState` / `nextRun` / `failCount` / `firedDown`（`dialtest/scheduler.go:26-36`）。
- D1 收敛/去重的分组窗口 `Grouper.groups`（`alert/grouping.go:178-194`）。

### 2.4 必须「单实例执行」的定时任务

| 任务 | 启动点 | 多实例同时跑的后果 |
|---|---|---|
| 告警评估（默认 15s） | `alert/engine.go:151`，`main.go:307` | **重复通知**、`monitor_alert` 序列重复写 |
| 拨测调度（1s 唤醒） | `dialtest/scheduler.go:65`，`main.go:200` | **重复拨测 + 重复告警** |
| 离线检测（10s） | `main.go:363` | 各自改内存状态并竞争写 `nodes.json` |
| 保留清理（默认 24h） | `retention/retention.go:292`，`main.go:314` | 重复删除（幂等，但共享目录会竞争） |
| 升级 apply | `upgrade/upgrade.go`（`main.go:237-264`） | **各自重启/替换二进制** |
| 自监控上报（30s） | `selfmon/wrappers.go:154` | 各实例一组 `self_*` 序列（可按实例区分，天然可分） |

### 2.5 跨实例一致性需求

- **WebSocket Hub**（`api/ws.go:62-132`）：连接表是纯本地，`BroadcastAlert` 只推给本实例的浏览器 → HA 下「只看得见本实例产生的告警」。
- **D1 通知去重/收敛**：分组窗口在进程内 → 同一次风暴发 N 份通知。
- **D4 告警处置状态**：`alert_acks.json` 本地文件 → A 实例认领后 B 实例看不见。

### 2.6 已清除的历史障碍（本次核对确认）

`globalAuthStore` 包级单例**已彻底清除**（全仓 0 命中）：`auth.Store` 由 `main.go:100-114` 显式构造并注入，`AuthMiddleware` 已参数化（`api/auth.go:180`），`API` 持实例字段（`api/query.go:112`）。→ **`auth.Store` 是第一个可以安全替换后端的切入点**。

### 2.7 部署形态现状

- 配置只有 `mode: standalone`（`config/config.go:15-18`，`Load` 强制回落），**无实例 ID / 集群字段**。
- systemd 单 unit（`deploy/install-server.sh:847-864`），无多实例模板。
- TSDB 侧：VictoriaMetrics **天然支持多实例并发写**（`storage/vm.go` 走 remote_write）；部署脚本里已有 Mimir 集群示例（`install-tsdb.sh:352,383`）但非默认。

---

## 3. 方案选项

### 选项 B（推荐先做）：**状态外置 + 主备**

- 一个实例是 **active**（跑评估/拨测/清理/升级），其余是 **standby**（只接收上报、提供读接口）。
- 通过**外部健康检查 + VIP/反代**（或 K8s lease）做切换；切换时间目标 ≤ 30s。
- **状态外置到共享存储**：这是 HA 的**前提**而不是可选项——备机在另一台机器上，本地文件它读不到。分三类处理：
  1. **低频配置**（rules/inhibit/grouping/pipeline/maintenance/notify/screen/ui/dashboards/retention）：改成「共享目录（NFS/共享卷）+ 定时重读 + 版本号比对」。改动小、语义不变（本来就是「配置」）。
  2. **协作状态**（`alert_acks`、`dialtest` 任务、`users`/roles、`templates`、`nodes`）：换成**外部 DB**（复用项目已有的 `go-sql-driver/mysql` / `lib/pq` 依赖，不引入新组件）里的表；写路径加唯一约束/乐观锁。
  3. **高基数快照**（Process/Listener/Firewall/instancereg）：保持「每实例各自缓存」不变，但**读接口按上报来源路由**（谁收到的谁答）或降级为「最近上报的实例可见」——这类数据是「观测快照」，短暂不一致可接受（明确写进文档，不假装强一致）。

### 选项 C（后续可选）：**多活**

在 B 的基础上把 active 限定去掉，用**选举/租约**保证「同一时刻只有一个实例执行调度类任务」：

- 用外部 DB 的**租约行**（`UPDATE ... WHERE holder IS NULL OR expired`）或 etcd lease 实现 leader；
- D1 收敛窗口与 D4 处置状态**必须**上移到共享存储（否则重复通知是必然的）；
- WS Hub 跨实例广播：优先做「**事件表 + 各实例轮询**」（复用既有 DB），而不是引入消息总线——本项目已有「轮询文件」的成熟范式，一致性要求也不高（秒级延迟可接受）。

### 明确的取舍建议

| 决策 | 建议 | 理由 |
|---|---|---|
| 先 B 还是先 C | **先 B** | 「高可用」的真实诉求是**故障能接管**；水平扩展在当前数据量下不是瓶颈。B 的改动面比 C 小一个量级，且 C 的前置（状态外置）正是 B 的产物 |
| 共享存储选型 | **外部 DB（PostgreSQL/MySQL）+ 共享目录**，暂不引入 Redis/etcd | 复用已有驱动；Redis 虽是「锁 + 广播」的顺手工具，但多一个必须运维的组件，且本项目已有「文件轮询 + 版本号」范式 |
| 切换方式 | **优先反代/健康检查（无 VIP）**，其次 Keepalived VIP | 反代（Nginx/HAProxy）已是常见部署形态，不引入新网络特性；VIP 依赖二层网络，部分云环境不可用 |
| 上报接收 | 多实例**都接收**（写 TSDB 天然支持） | 上报是「写多份也行」的幂等操作（TSDB 侧去重靠时间戳），无需选举 |
| 快照类数据一致性 | **不追求强一致**，文档写清「按实例分片」 | 硬做强一致要引入分布式 KV，收益不抵成本 |

---

## 4. 实施分解（评审通过后按此推进）

| 子批次 | 内容 | 备注 |
|---|---|---|
| **0（先做，独立收益）** | **持久化抽象**：给「本地文件 store」抽统一接口（`KVStore`/`ConfigStore`），并把 `config.AtomicWrite` 作为默认实现的落点 | 不改行为、纯重构；为后续换后端铺路；**本批次即可独立评审** |
| **A** | **实例标识与配置**：`server.yaml` 增加 `instanceId`（默认自动生成）、`cluster.role`（`active`/`standby`/`auto`）、`cluster.sharedDir`；启动日志打印实例身份 | 零破坏：不配置即当前行为 |
| **B** | **低频配置共享化**：配置类 store 一律支持「共享目录 + 版本号重读」；写入仍只允许 active（standby 写入返回 409 并提示当前 active） | 覆盖 2.2 里的「配置类」十余项 |
| **C** | **协作状态外置**：`users`/`roles`、`templates`、`nodes`、`dialtest`、`alert_acks` 五类的 DB 后端（保留文件后端为默认） | 对外接口不变，靠接口切换实现 |
| **D** | **主备切换**：standby 检测 active 心跳（写共享目录心跳文件或 DB 租约行）→ 接管调度类任务；提供 `/readyz` 的 role 字段与「手动切换」接口 | 切换演练脚本进 `build/` |
| **E** | **跨实例可见性**：WS Hub 走「事件表 + 轮询」；D1 收敛窗口与 D4 处置状态上移共享存储 | 保证「不重复通知」与「处置状态全局可见」 |
| **F** | **文档与验收**：部署形态（反代两实例）、故障切换演练、容量与一致性边界说明 | 与既有 `build/verify-*.sh` 风格一致 |

---

## 5. 测试与验收

| 层次 | 内容 |
|---|---|
| 单测 | 持久化抽象的行为一致性（文件后端 vs DB 后端跑同一套用例）；standby 写入被拒；心跳超时接管；收敛窗口跨实例只发一次通知 |
| 双实例集成 | 同一台机器起两个实例（不同端口 + 同一共享目录/DB）：上报都接收、只一个实例发告警、处置状态双实例可见、WS 广播双实例可见 |
| 故障切换演练 | 杀掉 active → standby 在目标时间内接管（拨测与告警恢复）；演练脚本固定进 `build/verify-server-ha.sh` |
| 回归底线 | **不配置 cluster 时行为与现在完全一致**（与 E1/C1/C2 同一条底线） |

---

## 6. 风险与边界（诚实清单）

1. **快照类数据（进程/监听/防火墙/实例注册）不保证跨实例一致**：明确按实例分片并在界面标注「来自哪个实例」比假装一致更可靠。
2. **切换期间的告警与拨测会有一个空窗**（心跳超时 + 接管耗时，目标 ≤ 30s）：需要在文档里写清，并让 `self_*` 指标暴露 role 与切换次数。
3. **外部 DB 成为新的单点**：若采用，需同时说明其自身高可用（主从/云托管），否则只是把单点搬了个位置。
4. **升级流程（`upgrade apply`）在多实例下必须串行**：这是 A1 里最容易出事的一环——它替换二进制并重启进程；多实例各自执行会互相覆盖。建议：升级只允许 active 执行，且切换前先降为单实例。

---

## 7. 待你拍板

| # | 决策点 | 我的建议 |
|---|---|---|
| 1 | 先做 B（主备）还是直接 C（多活） | **先 B**（理由见 §3） |
| 2 | 共享状态后端用什么 | **外部 DB 复用现有驱动 + 共享目录**；暂不引入 Redis/etcd |
| 3 | 切换方式 | **反代健康检查**优先，VIP 作备选 |
| 4 | 是否接受「快照类数据按实例分片」 | **接受**（并在界面标注来源实例） |
| 5 | 子批次 0（持久化抽象）是否单独先做 | **是**（纯重构、零行为变化，可独立评审） |
