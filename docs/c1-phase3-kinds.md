# C1 阶段三设计件：`jdbc` / `exec` / `file` 三类取数方式

> 前置：C1 阶段一（模板 DSL + 执行）与阶段二（Server 存管下发 + 类型注册表）已完成，
> 现有三类 kind 均为**网络取数**（`prometheus-exporter` / `http-json` / `http-text`）。
> 本件设计的是**「从本机或数据库取数」**的三类 kind —— 它们与前两批有本质区别：
> **`exec` 与 `file` 会以 root 身份触碰被监控机本身**，安全模型因此是本件的重点，而不是附带章节。
> 状态：**待评审**（决策点见 §11）。实施分解见 §10。

---

## 1. 目标与非目标

**要解决的问题**（模板化目前覆盖不到的三类真实需求）：

| 场景 | 现在做不到的原因 | 目标 kind |
|---|---|---|
| 指标在数据库里（业务表计数、慢查询数、复制延迟算出来的值） | 模板只能拉 HTTP 端点 | `jdbc` |
| 值只能靠命令拿到（`redis-cli info`、自研脚本输出、`kubectl` 自定义查询） | 同上 | `exec` |
| 值写在文件/日志里（自研应用的状态文件、非 nginx 的日志） | 同上 | `file` |

**非目标（阶段三明确不做）**：

- **不经过 shell**：`exec` 只按 argv 直接执行，**不解释 `|`、`>`、`$()`、`&&`** —— 解释器会让「白名单命令」形同虚设。
- **不做任意 SQL**：只允许读取类语句（见 §5.3），不支持写入/DDL/多语句。
- **不做文件写入**，也不支持读目录。
- **不做脚本分发**：`exec` 要执行的程序必须由运维自己放在机器上；Server 只下发「调用哪个已存在的程序」。
- **不做增量 tail**（阶段三的 `file` 是快照读取，见 §4.3）。

---

## 2. 事实基线（已具备，均已核对）

| 事实 | 证据 | 对本件的意义 |
|---|---|---|
| MySQL / PostgreSQL 驱动已在依赖中 | `go.mod:7` `go-sql-driver/mysql v1.7.0`、`go.mod:10` `lib/pq v1.12.3` | **`jdbc` 不引入任何新依赖** |
| 两套 DSN 构造范式可照抄 | `collector/mysql.go:63`（`timeout=5s&readTimeout=5s`）、`collector/postgres.go:63-69`（`sslmode` + `connect_timeout=5`） | 连接参数口径一致，不另造一套 |
| 现有 `exec` 命令**全部硬编码**，无白名单机制 | `collector/firewall.go`（约 17 处）、`collector/security.go`（4 处） | `exec` 的安全机制要**从零建立**，没有可复用的白名单 |
| Agent **以 root 运行** | `deploy/agent-install.sh:643` `User=root`（代理模式同，`:1529`） | `exec` / `file` 的风险等级是「本机 root 能力」 |
| 已有「危险能力默认关闭」惯例 | `agent/config/config.go:97-117` `CollectorToggle`（Redis/Port/Kafka… 注释明确「默认关闭」），`Default()` 只开主机类与 Security | 新增三类 kind 的本地开关**沿用同一惯例** |
| 已有增量文件读取实现（含轮转处理） | `collector/nginx_access.go:186-189`（`state{path,offset}`）、`:292-294`（文件变小即从头）、`:295`（Seek） | `file` 快照读取的边界处理可参考；**它也说明阶段三不必重复做 tail** |
| 模板写操作已鉴权 + 已审计 | `api/query.go:201-204`（`middleware:write`）、`api/audit.go:25-73` `AuditMiddleware`、`cmd/server/main.go:308` | Server 侧门控已存在，本件只需补「危险 kind」的额外提示 |
| 模板下发有**能力协商**通道 | `model/security.go:166-175` `ClientCapability{Templates,TemplateRevision}`、`receiver/receiver.go:382-396` | **可直接扩展为「只把 exec 模板下发给声明启用的节点」** |
| kind 的扩展点明确 | `template/dsl.go:30-37`（常量）、`:396-401`（kind 合法性）、`:541-559`（按 kind 校验规则适用性）、`collector/template.go:105-115`（执行分派） | 三类 kind 的接入点是**收敛的**，改动面可控 |

---

## 3. 安全模型（本件的重点）

### 3.1 威胁模型

模板可由持有 `middleware:write` 的账号在 Web 端编辑，并下发到**分组内的全部节点**（Agent 以 root 运行）。
因此新增 `exec` / `file` 之后，一条模板就等于**一次面向全网的 root 命令执行 / 任意文件读取请求** ——
攻击面不是「某个接口能被越权」，而是「Web 端一个写权限 = 一批机器的 root」。

据此确定总原则：**机器自身的同意（本地护栏）优先于中心的授权**。

### 3.2 三层门控（缺一不可）

| 层 | 位置 | 作用 | 默认 |
|---|---|---|---|
| ① 本地护栏 | 被监控机自己的 `agent.yaml` | 决定**这台机器**是否允许 `exec` / `file` / `jdbc`，以及允许的命令与路径白名单 | **三类均关闭** |
| ② 能力协商 | Agent 上报 → Server 按能力过滤下发 | 未启用的节点**根本收不到**这类模板（不产生 `up=0` 噪音、不产生误报） | 上报「已启用的 kind 清单」 |
| ③ 中心授权 + 审计 | `middleware:write` + `AuditMiddleware` | 谁改的模板、改了什么，可追责 | 已有，沿用 |

第 ① 层是**兜底**：即便 Server 被入侵或运维误配一条 `exec` 模板，也只影响**已被本机白名单明确允许**的命令。

### 3.3 `exec` 的具体约束

- **argv 直传，不经 shell**：命令与参数分别给（`command` + `args[]`），不解释任何 shell 语法。要管道/重定向的，请自己写脚本、把脚本路径放进白名单。
- **命令必须是绝对路径**，且**必须命中本机白名单**（精确路径匹配，不支持通配——通配会让白名单变成猜谜）。
- **环境变量显式声明**（默认只给 `PATH`）：避免把 Agent 进程的敏感环境变量（如 `cryptoKey`、其他服务凭据）泄露给被执行的程序。
- **工作目录固定**（`/`），不接受配置。
- **超时独立**：`timeoutSec`（默认 5s，上限 30s），到期 `CommandContext` 直接杀进程树。
- **只读 stdout 作为数据**；`stderr` 仅在做**失败日志**时截断记录（前 512 字节），**永不进入上报体**。
- **退出码非 0 = 采集失败**（只产 `template_target_up=0`，不产数据，与阶段一失败语义一致）。

### 3.4 `file` 的具体约束

- **路径必须命中本机白名单**（精确绝对路径），且必须是**普通文件**（非目录、非设备）。
- **解析软链接后重新校验**：`/etc/shadow` 之类可通过软链绕过白名单，因此先 `EvalSymlinks` 再比对白名单。
- **只读打开**（`O_RDONLY`），不创建、不写入。
- **读取上限**：默认读文件**末尾 1 MiB**（快照语义下"最新值"通常在末尾，且避免大文件读爆内存）。
- **存在性即语义**：文件不存在 → 采集失败（`up=0` + 日志给出路径与原因），不静默产 0 值。

### 3.5 `jdbc` 的具体约束

- **只允许读取类语句**：`SELECT` / `SHOW` / `EXPLAIN` 开头（大小写不敏感），且**只允许单条语句**（去掉结尾 `;` 后不得再出现分号）。
- **超时**：连接与查询各有超时（沿用既有 5s 口径 + 模板任务级超时兜底）。
- **结果集上限**：只取首行 + 列数上限（防「不小心写出大结果集把 Agent 拖死」）。
- **凭据沿用 `enc:` 密文**（与中间件实例密码同一套 AES-GCM 解密流程），**不打 json tag、不进日志、不进上报体**。
- 连接**每次采集新建、用完即关**（与既有 MySQL / PostgreSQL 采集器一致，避免连接池在采集进程里长期持有）。

---

## 4. DSL 设计

### 4.1 通用：三类都复用现有骨架

- `kind` 取值：`jdbc` / `exec` / `file`
- `targets[]`：沿用「一个 target = 一台取数对象」；`instance` 语义不变（写入 `instance` 标签）
- 产出仍受 `MaxMetricsPerTemplate` 上限与截断告警保护
- 失败语义仍为「只产 `template_target_up=0`，不产数据」（避免旧值被误读为当前值）

### 4.2 `jdbc`

```yaml
- id: bizdb
  kind: jdbc
  driver: mysql                 # mysql | postgres（只用已依赖的两个驱动）
  targets:
    - instance: biz-db-01       # 写入 instance 标签
      addr: 10.0.0.5:3306
      database: appdb
      auth:
        basic: { user: monitor, password: "enc:xxxx" }   # 沿用既有解密流程
      # params: { sslmode: disable }                     # 按驱动透传（postgres: sslmode；mysql: tls）
  rules:
    metrics:
      - name: order_count
        label: 近1小时订单
        query: "SELECT COUNT(*) FROM orders WHERE created_at > NOW() - INTERVAL 1 HOUR"
      - name: deadlocks
        query: "SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'"
        column: "Value"        # 可选：按列名取值；缺省取第一列
```

**每个指标 = 一次查询**（口径简单、可分别排查）；`column` 缺省取第一列，用 `SHOW ... LIKE` 这类两列结果时显式指定。

### 4.3 `exec`

```yaml
- id: redisinfo
  kind: exec
  targets:
    - instance: cache-01
      command: /usr/local/bin/redis-cli        # 必须绝对路径 + 必须命中本机白名单
      args: ["-h", "127.0.0.1", "INFO"]        # argv 直传，不经 shell
      timeoutSec: 5                             # 可选，默认 5，上限 30
  rules:
    metrics:                                     # 解析沿用 http-text 的 pattern 语义
      - { name: ops_per_sec, pattern: "instantaneous_ops_per_sec:(\\d+)", label: 每秒操作数 }
      - { name: used_memory, pattern: "used_memory:(\\d+)", label: 内存占用 }
```

### 4.4 `file`

```yaml
- id: appstate
  kind: file
  targets:
    - instance: app-01
      path: /var/lib/myapp/metrics.txt          # 必须绝对路径 + 必须命中本机白名单
      # maxBytes: 1048576                        # 可选：只读末尾 N 字节（默认 1 MiB）
  rules:
    metrics:
      - { name: queue_depth, pattern: "^queue_depth (\\d+)$", label: 队列深度 }
```

**阶段三只做快照读取**（每轮读末尾 N 字节 → 正则取「当前值」）。增量 tail（按行累计、需要偏移持久化与轮转处理）留待有真实需求时再评估——`nginx_access.go` 已有该实现，但那是面向特定日志的专用采集器。

---

## 5. 校验（启动期 fail-fast，沿用阶段一「一次报全所有错误」）

### 5.1 通用

- `kind` ∈ 五类；`targets` 非空；`rules.metrics` 必填（三类新 kind 都是「声明式取值」）
- `aggregate` / `promoteLabel` **不适用于三类新 kind**（无响应标签可塌缩/提升）→ 配置了即拒绝（而不是静默忽略）

### 5.2 `exec` / `file`

- `command` / `path` 必须为**绝对路径**，长度 ≤ 512
- `args[]` 元素不得含 NUL 字节；数量 ≤ 32
- `timeoutSec` ∈ [1, 30]
- `path` / `command` 在白名单校验上**只比对本机配置**（Server 侧无法知道各节点白名单，故 Server 侧只做格式校验，白名单在 Agent 侧执行）——**这一点必须在 Web 端提示**，否则用户会以为「服务端校验通过就等于能跑」

### 5.3 `jdbc`

- `driver` ∈ {`mysql`, `postgres`}
- `query` 非空，**必须**以 `SELECT` / `SHOW` / `EXPLAIN` 开头（大小写不敏感，允许前置空白），**且仅单条语句**
- `addr` 非空且能拆出 host:port；`database` 必填；`auth.basic.user` 必填
- `column` 若有则为合法列名（`^[A-Za-z_][A-Za-z0-9_]*$`）

---

## 6. 本地护栏与能力协商（Agent 侧）

### 6.1 配置（新增段，默认全关）

```yaml
# 模板的「从本机取数」能力护栏：默认全部关闭，需按机器逐个开启（与既有 CollectorToggle 同一惯例）
templateGuards:
  exec:
    enabled: false
    allow:                       # 精确绝对路径，不支持通配
      - /usr/local/bin/redis-cli
  file:
    enabled: false
    allow:
      - /var/lib/myapp/metrics.txt
  jdbc:
    enabled: false
    # allowHosts: []             # 可选：留空表示不限制（加固时逐条列目标库）
```

**键名说明**：不叫 `templates`（该键已被「本机模板列表」占用），叫 `templateGuards` ——「对模板取数方式的护栏」。

### 6.2 能力协商

Agent 在既有 `capabilities` 上增加一项「已启用的取数方式清单」（只上报已启用的）：
`capabilities.templateKinds: ["jdbc"]`（示例：仅开了 jdbc）。

Server 侧复用既有过滤点（`receiver.go:382-396`）扩一条规则：**kind 不在该节点声明的清单里则不下发该模板**。
收益：未启用的节点不会收到 → 不产生 `up=0` 噪音与误报；Server 可在模板页提示「该模板对 N 个节点无效」。

---

## 7. 执行模型

- 仍**每个模板一个采集任务**（继承 E1 的 per-task 超时与失败隔离）
- 失败一律降级为 `template_target_up=0` + 一条含模板 id / instance / 原因（**截断、不含响应体与凭据**）的日志
- 三类新 kind 都是**声明式取值**：`rules.metrics[]` 每条产出一条序列（`jdbc` 一次查询、`file`/`exec` 一次正则匹配）
- 解析复用：`exec` / `file` 复用现成的正则取值语义（与 `http-text` 一致，含「未命中不产出」）

---

## 8. 基数与可见性

- 三类产出量都很小（几条到几十条），仍受 `MaxMetricsPerTemplate`（2000）截断 + 告警保护
- **`jdbc` 的多行展开不在阶段三范围**（见 §11 决策 1）：一旦按行展开，基数就由数据库内容决定，需要配 `labelColumns` / `valueColumn` 与基数上限，复杂度与风险不匹配当下需求
- 护栏未启用导致「模板存在但节点不执行」时，必须**在 Web 端可见**（Server 侧按能力清单算「未生效节点数」），否则又是一次「为什么没数据」的排查

---

## 9. 测试与验收

### 9.1 单测

| 对象 | 方式 |
|---|---|
| 校验器 | 逐 kind 的拒绝路径（写操作 SQL、多语句、相对路径、超时越界、`aggregate`/`promoteLabel` 误用…） |
| `jdbc` 取值 | **把「执行查询」与「结果 → 指标」拆开**：前者薄、后者是纯函数，用假的行集合测（不引入 sqlmock 之类新依赖） |
| `exec` | 用真实存在的程序（如 `/bin/echo`）或测试自己写的临时脚本；覆盖退出码非 0、超时、stdout 为空 |
| `file` | 临时文件；覆盖不存在、超上限、软链接指向白名单外、正则未命中 |
| 护栏 | 未启用 / 命令不在白名单 / 路径不在白名单 / 软链绕过 → 拒绝且日志给出精确原因 |
| 能力协商 | 未声明 `templateKinds` 的节点不下发对应模板；声明后正常下发 |

**红先行**：先写断言再实现（与前两批一致；前两批均靠这一步抓到过真实错误）。

### 9.2 实机验证（dev-server）

- `exec` / `file`：完整往返（护栏开启 → 采集 → 落库写入路径）
- `jdbc`：先用**不存在的库**验证失败语义与日志；再确认 dev-server 是否有可用 MySQL/PostgreSQL 实例，有则建一张表做真实查询
- 护栏验证：白名单外的命令/路径必须被拒绝（且不影响其他模板——失败隔离）
- 更新 `build/verify-templates.sh`（阶段一脚本）覆盖三类新 kind

### 9.3 验收标准

- 三类 kind 的采集、护栏、失败隔离、能力协商全部有测试与实机证据
- 未启用护栏的节点：**不执行、不产噪音、Web 端可见原因**
- 凭据不出现在日志与上报体中（沿用阶段一的检查方式）

---

## 10. 实施分解（评审通过后按此推进）

| 子批次 | 内容 | 依赖 |
|---|---|---|
| **A** | DSL：三类 kind 的常量、字段、按 kind 校验（§5）；测试 | — |
| **B** | 本地护栏 `templateGuards` + 能力协商上报与下发过滤（§6）；测试 | A |
| **C** | 执行侧三类（§7）：`jdbc`（查询与结果转换分离）、`exec`（argv/超时/env/stderr 处理）、`file`（快照/软链/上限） | A、B |
| **D** | 前端：kind 选项、每类字段提示、校验错误展示、**未生效节点数** | C |
| **E** | 实机验证（§9.2）+ 文档（README 与 agent-install.sh 示例、设计件实施记录） | C、D |

---

## 11. 待你拍板（6 项）

| # | 决策点 | 我的建议 | 理由 |
|---|---|---|---|
| 1 | `jdbc` 是否支持「多行展开为多序列」 | **阶段三只做标量查询**（一行一列，`column` 可选指定列） | 覆盖绝大多数用法，且基数不受数据库内容影响；多行展开需要 `labelColumns`/`valueColumn` + 基数上限，等有真实需求再设计 |
| 2 | `file` 是否做增量 tail | **只做快照读取**（末尾 N 字节） | tail 需要偏移持久化与轮转处理，且「增量累计」与「当前值」是两种不同语义，混在一起容易误解；`nginx_access.go` 已有专用实现可参考 |
| 3 | 本地护栏键名与默认值 | `templateGuards.{exec,file,jdbc}`，**三类默认关闭**；`allow` 为**精确绝对路径、不支持通配** | 与 `CollectorToggle` 的「危险能力默认关闭」惯例一致；通配会让白名单变成猜谜，多条目就多写几行 |
| 4 | `exec` 的环境变量 | **默认只给 `PATH`**，其余需在模板里显式声明 | 避免把 Agent 进程的敏感环境变量泄露给被执行的程序 |
| 5 | Server 侧是否需要新权限点 | **沿用 `middleware:write`**，但新增「未生效节点数」提示 | 真正的门控在机器自身的白名单（①层）；再加权限点收益有限，反而让「谁该有这个权限」变成新问题 |
| 6 | `jdbc` 的凭据来源 | **沿用 Server 下发 + `enc:` 密文**（与中间件实例密码同一套） | 复用既有解密与「不进日志/上报」约束，不另造凭据通路 |

---

## 附录：与阶段一/二的差异一览

| 维度 | 阶段一/二（网络取数） | 阶段三（本机/数据库取数） |
|---|---|---|
| 风险性质 | 拉取外部端点，失败即无数据 | **`exec`/`file` 以 root 触碰本机** |
| 门控 | 分组 + 能力（`capabilities.templates`） | 分组 + 能力 + **本机白名单（新增①层）** |
| 校验位置 | Server 与 Agent 同源（DSL 校验器） | 格式校验同源；**白名单只能在 Agent 侧**（Server 不知道各机白名单） |
| 取数失败语义 | 只产 `up=0` | 同左（保持一致性） |
