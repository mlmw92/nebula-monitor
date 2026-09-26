# C1 阶段一设计件：采集项模板化（Agent 侧最小闭环）

> 版本基线 `VERSION = 1.25.0`（阶段一已实现，版本号按「整批合并递增」约定留待发布时统一提升）｜成文 2026-09-26｜状态：**阶段一已实现并实机验证**
> 本文只覆盖 **C1 阶段一**：三类模板（`prometheus-exporter` + `http-json` + `http-text`）在 **Agent 侧**跑通，
> 产出仍走 `model.Metric` + remote_write。阶段二（模板 CRUD / 下发 / 前端）与阶段三（`jdbc` / `exec` / `file` + 内置模板集）另文。
>
> 评审结论（2026-09-26，按建议通过）：① 配置放 `agent.yaml`；② 每模板一个采集任务；
> ③ `http-json` 路径自研极简实现；④ 顺带修的指标名缺陷族已单独提交（`284bc7d`，实际范围 27 处）；
> ⑤ `template_target_up` 暂不纳入「服务离线」告警白名单。

## 1. 目标与非目标

### 1.1 要解决的问题（量化）

新增一种中间件监控类型，当前需要改动 **约 30 个文件 / 55 处**，其中 **10 套完全同构的重复形态**：

| 重复形态 | 位置 | 套数 | 规模 |
|---|---|---|---|
| 采集器 | `internal/agent/collector/{mysql,redis,…}.go` | 10 | ~118 KB |
| `*InstanceConfig` / `*Instance` 结构体 | `internal/model/metric.go:139-447` | 10 对 | ~200 行 |
| `instancereg` 的 `Set*` / `*Instances()` 方法对 | `internal/server/instancereg/registry.go:62-260` | 10 | ~199 行 |
| API handler | `internal/server/api/middleware_api.go`（8 个）+ `query.go:1224` | 10 | ~1000+ 行 |
| 前端 `XxxTab.vue` | `web/src/components/{mysql,redis,…}/*.vue` | 10 | ~216 KB |
| 报告 `mwDefs` + 5 处 `switch m.Type` | `internal/server/report/report.go` | 10 + 5 | ~250 行 |

（完整清单与行号见本文附录 A；由探索代理逐处核对）

### 1.2 阶段一目标

让「**拉取一个 HTTP 端点、把响应映射成指标**」这类中间件，**只写 YAML 模板、不改 Go 代码**即可采集上报。

约束：**Server 零改动**。模板产出的指标走 `ReportPayload.Metrics` 通用路径（`internal/model/metric.go:101-135`），
落库路径与现有指标完全一致，因此 Server 不需要新增字段、路由或 handler。

### 1.3 明确的非目标（阶段一不做）

| 不做 | 何时做 |
|---|---|
| 模板 CRUD / 下发 / 前端编辑与校验 | 阶段二 |
| `jdbc` / `exec` / `file` 三类模板 | 阶段三 |
| 模板产出的中间件**出现在「中间件监控」Tab / 首页概览 / 报告 / 大屏** | 阶段二（需要「模板 → 中间件类型」注册） |
| 模板指标的**服务离线告警联动**（`*_instance_up` 语义） | 阶段二 |
| MySQL Group Replication / Redis cluster 这类**拓扑语义**采集 | 不做（保留专用采集器） |
| 模板热加载（改模板需重启 Agent） | 阶段二 |

> **必须提前说清的一条**：阶段一结束后，模板采集到的指标会出现在「指标浏览」与自定义仪表盘里，
> 但**不会**出现在中间件 Tab、首页概览、报告与「服务离线」告警里。因此
> **E2（更多中间件）要等到 C1 阶段二才能"退化为写一个模板"**——阶段一只解决"能否采到"，
> 阶段二才解决"能否被展示与告警"。路线图上 E2 的位置应据此理解为「C1 阶段二之后」。

## 2. 事实基线（可复用底座，均已存在）

阶段一**不新建机制**，全部复用 E1 已落地的能力：

| 能力 | 位置 | 复用方式 |
|---|---|---|
| 并发调度 + per-collector 超时隔离 | `internal/agent/collector/collect_all.go:48-53`（`runTasks(ctx, c.timeout, tasks)`） | 每个模板作为一个 `collectTask` 挂入 `tasks()` |
| ctx 感知 HTTP 拉取（含状态码校验、默认 8s 超时） | `internal/agent/collector/fetch.go:24-48`（`fetchMetrics`） | 模板的 HTTP 拉取直接调用 |
| Prometheus 文本解析（注入 node/instance 标签） | `internal/agent/collector/mysql.go:350-374`（`parsePrometheusTextWithPrefix`） | `prometheus-exporter` 模板复用（同包内可直接调用） |
| 地址规范化（回环替换为真实 IP + 默认端口） | `internal/agent/collector/mysql.go:376-383`（`normalizeRemoteAddr`） | `instance` 归一化 |
| 指标追加的锁约定 | `collect_all.go:60-68`（`addMetrics` 在锁内 append） | 模板任务同样走 `addMetrics` |
| 凭据解密（AES-GCM，`enc:` 前缀） | `internal/agent/config/config.go:213-242`（`decryptInstancePasswords`） | 模板 `auth` 字段纳入同一解密流程 |

**协议边界**：`POST /api/v1/report` 的 body 是 `model.ReportPayload`。新增中间件属**加字段式演进**，
Server 忽略未知字段、旧 Agent 缺字段时 Server 侧解码为 `nil`，双向兼容——本阶段正是利用这一点实现「Server 零改动」。

## 3. 模板 DSL

### 3.1 设计原则

1. **只描述「取数 → 映射」，不描述「计算」**：不做表达式求值、不做跨指标运算（那会引入语言与沙箱成本）。
2. **能力边界 = 拉取 + 解析 + 改名 + 打标签 + 过滤**；超出即"该写专用采集器"。
3. **默认安全**：默认只读、默认不发凭据、默认有上限；危险项需显式开启。

### 3.2 配置位置

阶段一放在 **`agent.yaml` 新增 `templates:` 段**（不引入独立文件路径）：少一处"文件在哪"的运维问题，
阶段二抽出独立文件并支持下发时再迁移（迁移是纯搬运）。

```yaml
# agent.yaml
templates:
  - id: rabbitmq                    # 唯一标识；同时作为指标名前缀与标签，见 3.5 命名约束
    title: RabbitMQ                 # 仅用于日志与阶段二展示
    kind: prometheus-exporter       # prometheus-exporter | http-json | http-text
    interval: 0                     # 0 = 跟随全局采集间隔（阶段一不实现独立周期）
    targets:
      - instance: mq-01:15692       # 实例标识（写入 instance 标签）
        addr: http://127.0.0.1:15692/metrics
        headers:                    # 可选
          Accept: text/plain
        auth:                       # 可选，三种之一
          basic: { user: monitor, password: "enc:xxxx" }
      - instance: mq-02:15692
        addr: http://10.0.0.12:15692/metrics
    rules:
      keep: "^rabbitmq_"            # 只保留匹配的指标名（正则，可选）
      drop: "_bucket$|_sum$|_count$" # 丢弃（正则，可选；与 keep 同时存在时先 keep 后 drop）
      rename:                       # 指标改名（值支持 $1 反向引用 group）
        - { match: "^rabbitmq_queue_messages$", to: "rabbitmq_queue_depth" }
      labels:                       # 追加静态标签（白名单校验，见 3.5）
        cluster: prod
      unlabel: ["job", "namespace"] # 删除响应里不需要的标签
```

`http-json` 示例（`addr` 返回 JSON）：

```yaml
  - id: ownapp
    kind: http-json
    targets:
      - { instance: app-01:8081, addr: "http://127.0.0.1:8081/stats" }
    rules:
      metrics:                      # JSON 路径 → 指标名
        - { name: ownapp_requests_total, path: "http.requests.total", type: counter }
        - { name: ownapp_errors_total,   path: "http.errors.total" }
        - { name: ownapp_queue_depth,    path: "worker.queue.size" }
      labels:
        env: prod
```

`http-text` 示例（正则抓取纯文本，如 Nginx stub_status 之外的页面）：

```yaml
  - id: customtext
    kind: http-text
    targets:
      - { instance: web-01, addr: "http://127.0.0.1/status" }
    rules:
      metrics:
        - { name: customtext_active_conns, pattern: 'active connections:\s+(\d+)' }
```

### 3.3 字段表

**通用**（三种 kind 共用）

| 字段 | 必填 | 说明 |
|---|---|---|
| `id` | ✓ | `^[a-z][a-z0-9_]{1,31}$`；作为指标前缀与 `template` 标签值 |
| `title` | | 展示名（阶段一仅日志） |
| `kind` | ✓ | 三种之一 |
| `targets[]` | ✓ | 至少 1 个；数量 ≤ `maxTargetsPerTemplate`（默认 32） |
| `targets[].instance` | ✓ | 实例标识；留空则用 `addr` 的 host:port |
| `targets[].addr` | ✓ | 仅 `http` / `https` |
| `targets[].headers` | | 自定义请求头 |
| `targets[].auth` | | `basic{user,password}` / `bearer{token}` / `header{name,value}`；密码/token 支持 `enc:` 前缀并纳入既有解密流程 |
| `rules.keep` / `rules.drop` | | 指标名正则 |
| `rules.rename[]` | | `{match, to}` |
| `rules.labels` | | 追加静态标签（白名单，见 3.5） |
| `rules.unlabel[]` | | 删除响应自带标签 |
| `rules.metrics[]` | `http-json`/`http-text` 必填 | `{name, path}` 或 `{name, pattern}` |

**上限（硬编码默认，不做成配置项以免运维调错）**

| 限制 | 默认 | 目的 |
|---|---|---|
| `maxTemplates` | 20 | 任务数上限（每个模板一个 `collectTask`） |
| `maxTargetsPerTemplate` | 32 | 单模板拉取目标数 |
| `maxMetricsPerTemplate` | 2000 | 单轮单模板产出条数，超出截断并告警 |
| `maxLabelsPerMetric` | 16 | 标签数 |
| `maxLabelValueLen` | 128 | 标签值长度 |
| `maxBodyBytes` | 8 MiB | 响应体上限（复用 `io.LimitReader`） |

### 3.4 标签与 `up` 指标

引擎**统一注入**（模板不可覆盖）：`node`（Agent 主机名）、`instance`（target 的 instance）、`template`（模板 id）。
模板只能追加**白名单外**的额外标签，且**禁止**出现 `node` / `instance` / `group` / `template` 四个保留名。

每轮每个 target 产出：

```
template_target_up{template="rabbitmq", instance="mq-01:15692"} = 1 | 0
```

- `up=1`：本轮拉取与解析成功；`up=0`：失败（连接失败 / 非 200 / 解析失败）。
- **失败时只产出 `up=0`，不产出其它指标**：避免上一轮的值被误读为"当前实时值"，也让"静默无数据"变成可见信号。

> 这条与既有实现的一个已知陷阱有关：`*_instance_up` 目前有**两种产出范式**——多数由 Agent 采集器直接产出
> （如 `mysql.go:136`），而 Redis 与 K8s 由 Server 的 receiver 合成（`internal/server/receiver/receiver.go:192-238`）。
> 模板产出统一由 **Agent 侧**产出 `template_target_up`，**前缀与既有 `*_instance_up` 不同**，因此不会产生双序列冲突。

### 3.5 命名与基数约束（防止污染时序库）

1. `id` 不得与**保留前缀**冲突，启动时校验并拒绝加载整个模板：

   ```
   cpu_ mem_ disk_ net_ network_ load1 swap_ host_ process_
   redis_ mysql_ postgres_ nginx_ nginx_access_ kafka_ docker_ rocketmq_ k8s_ mongodb_ fastdfs_
   template_ self_ proxy_ monitor_ alert_ security_
   ```

2. 模板产出的指标名 = **`<id>_` + 响应中的原名（可经 `rename` 改写）**。若响应原名已带该前缀则不重复添加。
3. `rules.labels` 的键必须匹配 `^[a-zA-Z_][a-zA-Z0-9_]{0,63}$` 且不在保留名中；值不得超长。
4. 单轮超限（3.3 的 `maxMetricsPerTemplate`）时**截断并打 Warn 日志**，日志里带模板 id 与截断条数——
   宁可丢数据也必须让运维看见，不能静默放大基数。

### 3.6 模板校验（启动期，fail-fast）

启动时对全部模板做一次校验，**任一不合法则拒绝启动并打印明确原因**（不静默跳过——静默跳过会变成"为什么没数据"的长期悬案）：

- `id` 唯一、格式合法、不与保留前缀冲突、不与其它模板 id 前缀互相包含（避免 `a` 与 `a_b` 混淆）
- `kind` 合法；`targets` 非空、`addr` 为 http/https 且可解析
- 正则编译通过；`rename.match` 合法
- `labels` 白名单校验；`unlabel` 不含保留名
- 模板数 ≤ `maxTemplates`

## 4. 执行模型

```
Collector.CollectAll(ctx)
  └─ runTasks(ctx, collectTimeout, tasks)
       ├─ host / redis / mysql / …        （既有，不变）
       └─ template:<id>                   （新增：每个模板一个任务）
            └─ for each target: fetchMetrics(ctx) → parse → map → addMetrics
```

**要点**

1. **每个模板一个 `collectTask`**：直接获得 E1 的 per-collector 超时与失败隔离（一个模板卡住不影响其它模板与主机采集）。
   代价是任务数 = 模板数；由 `maxTemplates = 20` 封顶，超出则在启动校验阶段直接报错（而不是运行时降级）。
2. **超时**：沿用全局 `collectTimeout`（默认 8s）；HTTP 客户端自身再设 5s（与 `mysql.go:221` 一致），两者谁先到谁生效。
3. **无模板 = 零行为变化**：`templates` 段缺失或为空时，`tasks()` 不追加任何任务，
   与改造前**完全等价**（这是阶段一最重要的回归保证，需有对照测试）。
4. **不做独立周期**：模板跟随全局采集间隔（`interval` 字段在阶段一仅接受 0，非 0 给出警告并忽略）。

## 5. 安全与信任模型

**先说清楚：模板不是新增的攻击面。** Agent 现在就支持对用户配置的任意 `exporterURL` 发起 HTTP 拉取
（`mysql.go:220-227` 等），模板只是把这个能力从"每类中间件写死代码"变成"配置即可"——**信任级别不变**：
模板是 Agent 本机配置文件的一部分，只有能改 agent.yaml 的人才能写模板（也就能直接改 host 采集行为）。

在此前提下仍加护栏：

| 护栏 | 说明 |
|---|---|
| 协议白名单 | 仅 `http` / `https`；拒绝 `file://`、`gopher://` 等 |
| 可选主机白名单 | `templates[].allowHosts`（阶段一可先不实现，但要预留字段位置） |
| 响应体上限 | `io.LimitReader(8MiB)`，避免被超大响应拖垮内存 |
| 标签保护 | 禁止覆盖 `node` / `instance` / `group` / `template`，否则可伪造他机数据 |
| 凭据不落日志 | 日志只打模板 id、target instance 与 URL（**不含** `auth`），错误信息不回显响应体 |
| 凭据不报表 | `auth` 字段无 json tag（沿用 `model.RedisInstanceConfig.Password` 的做法），永不进入 `ReportPayload` |
| 超时兜底 | 任务级 ctx + HTTP 客户端超时双层 |

## 6. 兼容、灰度与回滚

| 场景 | 行为 |
|---|---|
| 不配置 `templates`（绝大多数存量节点） | **零变化**：任务表不变、上报体不变、指标不变 |
| 新 Agent + 旧 Server | 模板指标照常写入 VM（走通用 `Metrics` 路径），但 Tab/告警不可见（阶段二解决） |
| 新 Server + 旧 Agent | Server 完全不感知模板，无影响 |
| 回滚 | 删除 `templates` 段并重启 Agent 即回到改造前；无状态需要清理（已写入 VM 的序列会按 TSDB 保留期自然过期，如需立刻清理由运维决定） |

**灰度建议**：先在 1 台节点上配 1 个模板（建议 `rabbitmq` 或 `elasticsearch` 的 exporter），
观察 1 个采集周期后在「指标浏览」确认指标与标签，再铺开。

## 7. 测试与验收

### 7.1 单元测试（新增 `internal/agent/collector/template_test.go`）

| 用例 | 断言 |
|---|---|
| 模板校验：非法 id / 保留前缀 / 重复 id / 前缀互相包含 | 启动校验返回错误且错误信息含具体原因 |
| 模板校验：非法 kind / 非法 addr / 正则不合法 / labels 保留名 | 同上 |
| `prometheus-exporter` 映射：keep/drop/rename/unlabel/labels | 产出指标名与标签集精确匹配预期 |
| `http-json` 路径取值（含数组下标、缺失路径） | 缺失路径不 panic、不产出该指标 |
| `http-text` 正则抓取 | 命中产出、未命中不产出 |
| 失败语义：目标不可达 / 非 200 / 解析失败 | 仅产出 `template_target_up=0` |
| 基数护栏：超 `maxMetricsPerTemplate` / 超标签数 / 标签值超长 | 按规则截断或丢弃，并有可断言的返回值 |
| 无模板回归：`templates` 为空 | 任务表与改造前一致（对照快照） |

### 7.2 实机验证（dev-server，复用 `/tmp/verify` 的假 exporter 思路）

1. 起 3 个假端点（prometheus 文本 / JSON / 纯文本），配 3 个模板；
2. 跑一轮，确认 Agent 上报体里出现预期指标与 `template_target_up`；
3. 停掉其中一个假端点，确认该 target 的 `up=0` 且**其它 target 不受影响**（E1 隔离生效）；
4. 与"不带模板的同一二进制"做**上报体结构对照**，确认零模板时逐字节等价（沿用 `build/verify-agent-parity.sh` 的对照思路）。

### 7.3 验收标准（阶段一结束的判定条件）

- [x] **不改任何 Go 代码**，仅凭 YAML，能把 exporter/HTTP 型中间件采到指标并落 VM。
      本次验证方式：三类 kind 各起一个假端点（Prometheus 文本 / JSON / 纯文本）+ 端到端跑通
      「Agent → Server → remote_write（时序库写入路径）」，断言指标与标签确实到达写入路径（11 项）。
      **未逐一启动** RabbitMQ / Elasticsearch / ClickHouse / Etcd / ZooKeeper 真实产品端点——
      那属部署时的接入动作；这 5 类里 4 类就是 `prometheus-exporter`，Elasticsearch/Nacos 走 `http-json`，
      两种 kind 的机制均已在假端点上验证。
- [x] 无模板配置时，与改造前二进制上报体**结构等价**（实机对照：顶层字段、指标名集合、条数逐项一致）。
- [x] 全部单测通过；`go vet` 干净；实机验证 4 步全过（28 项断言）。
- [x] 文档：`deploy/agent-install.sh` 的 agent.yaml 注释模板中新增 `templates:` 示例段；
      README 新增「采集项模板（`templates`）」章节。

## 8. 与 E2 / 阶段二三的衔接

### 8.1 E2 的 7 个中间件各自归属

| 中间件 | 阶段一能否只写模板 | 备注 |
|---|---|---|
| RabbitMQ | ✅（`prometheus-exporter`） | 官方 prometheus 插件或 rabbitmq-exporter |
| Elasticsearch | ✅（`http-json`） | `_cluster/health` + `_nodes/stats` |
| ClickHouse | ✅（`prometheus-exporter`，`:9363/metrics`） | |
| Nacos | ⚠️（`http-json`） | 需确认指标端点形态；若无指标端点则退化 |
| Etcd | ✅（`prometheus-exporter`，`:2379/metrics`） | |
| ZooKeeper | ✅（`prometheus-exporter`） | 依赖 zookeeper-exporter |
| MariaDB | ⚠️ | exporter 前缀为 `mysql_`，可直接复用既有 MySQL 采集器的 exporter 通路，**不必新写模板** |

> 结论修正：E2 不是"7 个都能靠模板"。**MariaDB 走既有 MySQL exporter 路径即可**，
> 真正需要模板的是 5~6 个 exporter/HTTP 型中间件——这已足够兑现"不再改 30 文件"的收益。

### 8.2 阶段二/三预留的接口

- 阶段二需要「**模板 → 中间件类型**」注册：把模板产出的指标接入 `middlewareTypes`（`middleware_api.go:754-765`）、
  `mwSummarySpecs`（`656-711`）、报告 `mwDefs`（`report.go:900-950`）与 `alert.engine.serviceMetric()`（`engine.go:600-623`）。
  **接口设计要点**：这些位置现在都是硬编码 switch，阶段二应改为读取一份运行时注册表（模板下发时填充），
  否则"写模板即可"仍会在展示层被卡住。
- 阶段三的 `jdbc` / `exec` / `file` kind 应复用本阶段已定的**校验器 + 上限 + 标签保护**框架，只新增「取数器」。

## 9. 风险与缓解

| 风险 | 缓解 |
|---|---|
| 指标基数爆炸（自定义 exporter 动辄上千序列） | `maxMetricsPerTemplate` 截断 + Warn 日志 + 文档给出"用 keep 收窄"的示例 |
| 模板写错 → 静默无数据 | `template_target_up=0` + 采集失败 Warn（含模板 id/instance/URL）+ 阶段二在 UI 显示"模板未产出" |
| 与既有指标名冲突 | 保留前缀清单 + 启动期拒绝加载（fail-fast），而非运行时才发现双序列 |
| 模板数量失控导致采集周期拉长 | `maxTemplates` 启动期封顶；后续如需更多，走阶段二的"模板周期"能力 |
| 用户误以为阶段一就有 Dashboard/告警 | 本文 1.3 与 8.1 明确写出；阶段一交付说明里再强调一次 |

## 10. 实施分解（评审通过后按此推进）

| 步骤 | 产出 | 预估 | 状态 |
|---|---|---|---|
| 1 | `internal/agent/template/dsl.go`：DSL 结构体 + 校验器（含保留前缀、上限） | 中 | ✅（位置与设计件不同，见 §12.1） |
| 2 | `internal/agent/collector/template.go`：三种 kind 的取数与映射 + `up` 指标 | 中 | ✅ |
| 3 | `collect_all.go` 挂任务（每模板一个 `collectTask`）+ `config.go` 新增 `templates` 段 + 凭据解密接入 | 小 | ✅ |
| 4 | 单测（7.1 全表）+ 无模板回归对照 | 中 | ✅（28 个用例） |
| 5 | 实机验证（7.2）+ agent-install.sh 注释示例 + README 文档 | 小 | ✅ |

**版本号按仓库既有约定「整批合并递增」处理**：阶段一完成时**不单独** bump `VERSION`，
与后续项（阶段二/其它批次）合并到一次发布时统一提升为 minor；变更记录里须写明
"阶段一仅采集可见，展示与告警在阶段二"。

## 12. 实施记录（阶段一，2026-09-26）

### 12.1 与设计件的三处偏离（均因实现约束，非取舍）

1. **DSL 放在共享包 `internal/template`，而非 `collector/template_dsl.go`。**
   阶段一先落在 `internal/agent/template`：`collector` 已 import `agent/config`，而 `config.Config` 必须持有
   `Templates` 字段——若 DSL 定义在 `collector` 内即成 import 环。阶段二该包**上移到 `internal/template`**：
   Server 侧保存模板时要用**同一份校验器**（两处各写一份必然漂移），而让 `server/` 去 import `agent/` 下的包
   在语义上不成立。本包零依赖（仅标准库），两端都能引用。
2. **响应体上限只加在模板拉取路径，未改共享的 `fetchMetrics`。**
   设计件 §2 写的是"复用 `fetchMetrics` 的 `io.LimitReader`"，但共享 helper 实际**没有**上限；
   直接给它加 8 MiB 截断会让既有 exporter 路径（尤其 kube-state-metrics 在大集群）静默少解析若干行，
   属回归风险。故 `fetch`（模板专用）按 `maxBodyBytes` 限制并**显式判断超限**（多读 1 字节），
   既有路径行为不变。
3. **静态标签上限为 13（`MaxLabelsPerMetric` − 保留标签数）而非 16。**
   总标签上限 16 需给 `node`/`instance`/`template` 留位，否则"静态标签写满"会把来源标签挤掉。

### 12.2 落地清单

| 文件 | 内容 |
|---|---|
| `internal/agent/template/dsl.go` | `Config`/`Target`/`Auth`/`Rules` 等 DSL + `ValidateAll`/`Validate`（一次报全）+ 上限与保留前缀常量 |
| `internal/agent/collector/template.go` | `TemplateRunner`：三种 kind 的取数与映射、`template_target_up`、标签/基数护栏、凭据不回显 |
| `internal/agent/collector/collector.go` / `collect_all.go` | 每模板一个 `collectTask`（为空时不追加任务） |
| `internal/agent/config/config.go` | `templates` 段 + 启动期 fail-fast 校验 + 模板凭据接入既有 AES-GCM 解密 |
| `cmd/agent/main.go` | 传入 `cfg.Templates` |
| `internal/agent/template/dsl_test.go`（13 例）/ `internal/agent/collector/template_test.go`（15 例） | 校验器、三种映射、失败语义、护栏、无模板任务表对照 |

### 12.3 验证结果

- 单测：校验器 13 项 + 模板执行 15 项全绿；`go vet ./internal/agent/...` 干净；`go build ./...` 通过。
- 实机（dev-server）：28 项断言全过——三模板正常采集（keep/drop/rename/unlabel/labels、三种 kind 取值）、
  一个 target 失败时**只产该 target 的 `up=0`** 且其它模板不受影响、无模板时与 pre-C1 二进制上报体逐项等价。
  该验证已固化为可重复脚本：`bash build/verify-templates.sh [pre-C1-Agent-二进制]`
  （自建假端点与上报捕获器，不依赖外部服务）；阶段二改 DSL 或取数逻辑后应重跑一次。
- 端到端（补"落 VM"）：Agent → Server → `remote_write`，11 项断言全过——模板指标与
  `cluster`/`template`/`instance`/`node` 标签确实到达时序库写入路径；keep/drop 未生效的指标未写入。
  验证用假时序库（仅解压 snappy 落盘）以避免依赖外网下载 VictoriaMetrics。

### 12.4 阶段一的已知边界

- `interval` 只接受 `0`（跟随全局采集间隔）；`allowHosts` 字段已预留但未实现。
- 模板指标**只做到"能采到、能在指标浏览查到"**：中间件 Tab、首页概览、巡检报告、大屏、
  「服务离线」告警均需阶段二的"模板 → 中间件类型"注册，且这些位置当前都是硬编码 switch，
  阶段二应改为读运行时注册表（否则"写模板即可"仍会在展示层被卡住）。
- 真实产品端点（RabbitMQ/ES/ClickHouse/Etcd/ZooKeeper）未在本次验证中逐一启动。

## 11. 待你拍板（评审点）

| # | 决策点 | 我的建议 |
|---|---|---|
| 1 | 模板配置放 `agent.yaml` 还是独立 `templates.yaml`？ | **阶段一放 agent.yaml**（少一个路径配置），阶段二抽出并下发 |
| 2 | 每个模板一个任务（隔离优先）还是单任务内串行（资源优先）？ | **每模板一个任务** + 20 个封顶：隔离是 E1 的核心收益，模板卡住不应拖累主机采集 |
| 3 | `http-json` 的路径语法：自研极简 `a.b[0].c` 还是引入库？ | **自研极简**（约 80 行，零依赖，可直接单测）；模板 DSL 不应引入依赖树 |
| 4 | 顺手修「指标目录名与实现不一致」6 处 + `serviceMetric` 漏 mongodb/fastdfs？ | **建议单独一次修复提交**，先修缺陷再加新机制，避免两件事混在一个 diff 里 |
| 5 | 阶段一是否要把 `template_target_up` 也纳入「服务离线」告警白名单？ | **暂不**（属阶段二的"模板 → 中间件类型"注册），阶段一先保证可见性 |

## 附录 A：新增中间件的**完整**联动点（55 处，供阶段二/三评估用）

### A.1 Agent 采集
`collector.go:23-38`（聚合结构体字段）｜`collector.go:42-56`（`New()` 参数逐个列举）｜`collector.go:67-105`（开关门控构造）｜
`collect_all.go:15-22`（`Result` 字段）｜`collect_all.go:77-94`（硬编码 task 列表）｜新建 `xxx.go`（结构体 + `CollectCtx` + 指标名硬编码 + 实例组装 + exporter 解析 + `normalizeXxxAddr`）

### A.2 上报模型
`model/metric.go:139-447`（10 对 Config/Instance 结构体）｜`metric.go:115-124`（`ReportPayload` 10 个字段）｜
`agent/config/config.go:37-46`（yaml 字段）｜`config.go:100-104`（开关）｜`config.go:213-242`（密码解密）｜
`cmd/agent/main.go:113-115`（`collector.New` 实参）｜`main.go:280-289`（组装 `ReportPayload`）

### A.3 服务端实例注册
`instancereg/registry.go:20-32`（map 字段）｜`38-51`（初始化）｜`62-260`（10 对同构 Set/Get，~199 行）｜
`receiver/receiver.go:242-251`（落库调用）｜`receiver.go:192-238`（Redis/K8s 的 `*_up` 合成）

### A.4 服务端 API
`api/query.go:166-178`（13 条路由）｜`api/middleware_api.go:18/141/264/399/515/863/1005/1190/1478` + `query.go:1224`（10 个 handler）｜
`middleware_api.go:656-711`（`mwSummarySpecs`）｜`754-765`（`middlewareTypes`）

### A.5 指标目录
`metrics/catalog.go:19-52`（`Category` 常量）｜`metrics/middleware_catalog.go:8-53`（`Register` 调用）

### A.6 前端
`components/MiddlewareView.vue:13-92 / 110-119 / 120-129 / 132`（tab + 异步组件 + 图标 + 深链白名单）｜
新建 `components/xxx/XxxTab.vue`（11~77 KB）｜`assets/img/xxx.svg`｜`components/overview/middlewareConfig.js:12-117`｜
`src/metrics/dictionary.js:60-88`

### A.7 第 7 类（易被漏掉，实际必须改）
`alert/rules.go:47-59`（内置默认规则）｜`rules.go:409-413`（`validService` 白名单）｜
`alert/engine.go:600-623`（`serviceMetric()` 映射）｜`report/report.go:900-950 / 950-956 / 998-1020 / 1172-1195 / 1369-1460`（`mwDefs` + 5 处 switch）｜
`report/template.html:248`｜`analysis/analyzer.go:273-280`（`MiddlewareRef` 聚合）｜
`deploy/agent-install.sh:510-522 / 849-860`｜`build/verify-agent-parity.sh:242-245`｜
大屏 5 个组件（`screen/*`）

## 附录 B：探索中发现的指标名不一致缺陷（**已修复**，本设计不依赖它）

> 口径更正：设计件初稿根据抽样核对写成「目录 7 处不符」。评审前做了**全量审计**
> （产出方 = `internal/agent/collector/*.go` + `internal/server/receiver/*.go` 的指标名字面量；
> 消费方 = api / report / metrics / analysis 与前端源码），真实范围是
> **目录 30 条里 17 条无产出方，消费侧共 27 处引用了不存在的名字**。

| # | 缺陷 | 真实范围（全量审计） | 影响 | 状态 |
|---|---|---|---|---|
| B1 | 指标目录登记名与产出方不符 | **17 条**：`redis_up`/`mysql_up`/`postgres_up`/`nginx_up`/`kafka_up`/`rocketmq_up` 应为 `<mw>_instance_up`；`postgres_connections`→`postgres_numbackends`；`kafka_brokers`→`kafka_broker_count`；`kafka_under_replicated`→`kafka_under_replicated_partitions`；`docker_container_count`→`docker_containers_total`；`nginx_active`→`nginx_active_connections`；`nginx_requests_per_sec`→`nginx_requests`；`redis_used_memory_bytes`→`redis_used_memory`；`redis_qps`→`redis_ops_per_sec`；`mongodb_connections`→`mongodb_connections_current`；`rocketmq_produce_tps`→`rocketmq_producer_tps`；`k8s_node_mem_used_bytes`→`k8s_node_mem_usage_bytes`；另 FastDFS **整类无目录条目**（连 `CatFastDFS` 常量都缺）、`k8s_cluster_up` 与 `docker_container_up` 未登记 | 「指标浏览」按目录查询永远无数据；`/metrics/active` 标为未上线，排查时易误判为「采集没开」 | ✅ 已修 |
| B2 | `serviceMetric()` 漏 mongodb / fastdfs，落 `default` 回退为 `redis_instance_up` | 8 个 case + default | 「服务离线」规则对这两类判断错误对象 → 离线告警永不触发或随 redis 误触发 | ✅ 已修 |
| B3 | 首页概览 `middlewareConfig.js` 只有 8 类，缺 mongodb / fastdfs | 同左 | 首页概览与「中间件监控」Tab 不一致 | ✅ 已修 |
| B4 | 巡检报告引用了不存在的名字，字段恒为空 | `report.go`：`redis_max_clients`、`redis_used_memory_bytes`、`redis_replication_lag_seconds`、`mysql_buffer_pool_hit_rate`、`nginx_5xx`；中间件总览摘要：`nginx_5xx_rate`、`fastdfs_storage_total` | 报告里 Redis 连接上限 / 内存占用 / 主从延迟、MySQL 缓冲池命中率、Nginx 5xx 恒为空或 0 | ✅ 已修（其中 `redis_maxclients` 属**产出方缺失**，已在采集器补齐） |

**防止复发的守卫**（已进 `go test` 门禁，无需新增 CI 脚本）：
`internal/server/metrics/catalog_guard_test.go` 扫描产出方源码，校验「目录登记的每个中间件指标名都有产出方」
与「每个中间件的存活指标都已登记」；`internal/server/alert/service_metric_test.go` 校验
「服务映射 ↔ 指标目录」一致。守卫已做过**注入验证**：把 `mysql_instance_up` 改回 `mysql_up` 后测试立即失败并报出该名字。

## 13. 阶段二实施记录（进行中）

阶段二把模板从「逐台写 agent.yaml」变成「Web 端统一存管 + 下发」，分四个子批次。
评审决策（2026-09-26）：① 下发范围按**节点分组**（`groups` 必填）；② 只走响应下发，不加 SIGHUP；
③ 每个模板 = 一个独立中间件类型（前端用一个通用 Tab 组件渲染）；④ `template_target_up` 纳入「服务离线」告警。

| 子批次 | 内容 | 状态 |
|---|---|---|
| 0 | DSL 移到共享包 `internal/template`（Server 与 Agent 用同一份校验器，否则两处必然漂移） | ✅ |
| A | Server 侧存储（CRUD + 校验 + 原子落盘 + revision）+ CRUD/校验 API + 权限门（`middleware:read` / `middleware:write`） | ✅ |
| B | 下发与热生效（响应携带 + 分组过滤 + 能力/版本协商 + Agent 原子替换） | ✅ |
| C | 注册化：模板 → 中间件类型（后端 5 处 + 前端 4 处收敛到一份运行时注册表） | ✅ 后端完成 |
| D | 前端模板管理页 + 中间件 Tab 动态化 + 「已配置但无数据」提示 | ✅ |

### 13.1 子批次 B 的关键取舍

1. **`groups` 在共享 DSL 里可选、在 Server 侧必填**：`agent.yaml` 的本机模板天然只对本机生效，
   强制填写会多一个不起作用的字段；而 Server 管理的模板若允许留空并默认「全部节点」，
   一台只跑某中间件的机器配一个模板，会让其余节点每轮各报一个 `up=0`（序列与日志双噪声）。
2. **不需要 SIGHUP、也不需要重启**：`CollectAll` 每轮都重建任务表（`tasks()`），
   因此下发只需原子替换模板集，下一个采集周期自然生效。
3. **三条下发闸门**：Agent 必须声明 `capabilities.templates`（旧 Agent 发了也白发）、
   版本号必须与已生效版本不同（否则每轮心跳背负整份配置，随节点数成倍放大）、按分组过滤。
4. **空集合照样下发**：某分组内模板被删空时必须让 Agent 收到「清空」这个事实，
   否则它会继续跑已被删除的模板。
5. **非法下发保留旧模板**：校验失败只记一次告警（按版本号去重）并继续用旧配置；
   Server 会持续重发，配置修好后自动恢复——宁可暂时用旧配置，也不能因模板把采集打断。
6. **首次接管时的替换是显式的**：Agent 日志明确提示「本机 agent.yaml 中的模板被 Server 下发替换」，
   避免运维困惑于「本地写的模板怎么不见了」。

### 13.4 子批次 D：前端

| 位置 | 改动 |
|---|---|
| `components/templates/TemplatesView.vue`（新） | 模板列表 + 新建/编辑弹窗 + 「校验」按钮（调 `/templates/validate`，把服务端精确原因列出来，避免「保存失败再猜」）。列表里直接显示**采集情况**（在线/失败数，或「已配置但无数据」标记） |
| `components/mw/TemplateTab.vue`（新） | 模板派生类型的**通用** Tab：实例表（实例/节点/分组/采集状态 + 模板声明的摘要指标）。空状态给出排查方向（生效分组是否匹配、目标是否可达、Agent 是否已收到下发） |
| `components/MiddlewareView.vue` | Tab 由 `/middleware/overview` 的 `kind=template` 类型动态追加；深链 `?tab=` 白名单随之动态化；头部加「采集项模板」入口；暂无数据的模板在 Tab 上打提示点 |
| `components/OverviewView.vue` + `overview/MiddlewareOverview.vue` | 首页卡片追加模板派生类型（数据取自总览接口）；空状态文案可被覆盖——模板是「已配置但无数据」，与内置类型的「尚未配置」排查方向不同 |
| `components/screen/*` | 大屏的类型清单本就来自总览接口（注册表一生效即自动出现）；补上模板类型的实例列表与参数趋势指标（用模板声明的摘要指标） |
| `Sidebar.vue` / `router` / `MainLayout.vue` | 新增「采集项模板」菜单与路由（读 `middleware:read`，写按钮另受 `middleware:write` 门控） |

**写操作的表单取舍**：`rules` 规则较丰富（keep/drop/rename/labels/unlabel/metrics），弹窗里用 **JSON 文本域** + 服务端校验，
而不是为每种规则做一套表单控件——结构化字段（id/title/kind/groups/targets）照常用表单，规则区保留完整表达力。

**已知小项**：`/api/v1/groups` 需 `groups:read`，仅有 `middleware:read` 的用户打开模板页时分组下拉为空
（可手动输入，`allow-create` 已开）；如需完全顺畅，可给这类账号一并授予 `groups:read`。

### 13.3 子批次 C：类型注册表（`internal/server/mwreg`）

把「有哪些中间件类型、每类的存活指标是什么、卡片与报告展示哪些指标」从原先**四处硬编码**
收敛为一份数据源：

| 原位置 | 现状 |
|---|---|
| `api/middlewareTypes`、`api/mwSummarySpecs` | 读注册表（总览卡片与类型清单） |
| `report/mwDefs` + `docker` 特例 + `throughputMetric()` | 读注册表（分节、明细、吞吐列、存活判定） |
| `alert/serviceMetric()` | 读注册表（存活指标映射） |
| `alert/KnownServices`、`validService()` | 内置清单保留为文档，校验改读注册表（模板类型也能被规则监控） |

收敛过程中发现并处理的问题：

1. **两处真实漂移**：报告侧把 Kubernetes 的类型键写成 `kubernetes`（其余处用 `k8s`）；报告侧**完全没有 FastDFS** 条目。
2. **一处看似漂移、实为刻意**：报告侧 Docker 用容器**总数**指标是否存在判定存活，而非容器 up 值——
   因为「0 个运行容器」不该被判成离线。这不是缺陷，而是同一类中间件在「报告」与「告警」眼里
   本就是不同对象，因此注册表保留了 `ReportUpMetric` / `ReportPresenceUp` 两个显式字段而非强行统一。
   （我最初把它当成缺陷，读过代码后改回原语义并在测试里固定下来。）
3. **模板类型的存活告警必须带标签过滤**：所有模板共用 `template_target_up`，
   因此 `upLabelsFor` 给出 `{"template": <id>}` 并透传进 `QueryInstant`，
   否则「A 模板离线」会被 B 模板的实例误触发。
4. 新增模板类型的**通用实例接口** `GET /api/v1/middleware/{type}/instances`：
   内置类型各自的字面量路由优先命中，模板类型走通用形态（实例 + 摘要指标），供前端一个通用 Tab 渲染。

### 13.2 子批次 B 的验证

- 单测：receiver 6 例（分组过滤 / 版本一致跳过 / 缺能力跳过 / 未注入不下发 / 空集合清空 / 分组匹配细节）
  + collector 2 例（热替换无需重启、非法下发保留原配置）+ 存储 9 例 + API 5 例。
- 实机端到端（`bash build/verify-template-delivery.sh`）：Agent 的 `agent.yaml` **不配任何模板**，
  因此被测指标只能来自下发路径；9 项断言全过——模板经 Server → 上报响应 → Agent 应用
  （日志 `已应用 Server 下发的采集项模板 count=1 revision=1`）→ 采集 → 落库写入路径，
  且 `rename` / `keep` / 静态标签均正确生效。假时序库见 `build/template-fakes/fakevm`。
