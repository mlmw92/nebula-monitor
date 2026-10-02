# 配置模板手册（Configuration Cookbook）

> 目的：平台里"需要配置、但字段语义不直观"的地方，这里给**可直接套用的模板**。
> 用法要么在页面上一键追加，要么整段粘贴到页面的「高级：YAML 直编」。
>
> 约定：模板里的阈值、团队名、域名等都是**起步值/占位值**，请按你们实际情况改。
>
> 覆盖进度：**第一章 告警事件管道**、**第二章 集中日志**（本版）。告警规则阈值、通知渠道
> 两章的模板与"想做什么 → 去哪配"索引见后续版本；在补齐之前，那几项的字段说明分别在
> README「配置说明」与 `docs/c2-central-logs.md`。

---

## 一、告警事件管道（站点与品牌 → 告警事件管道）

### 1.1 它做什么

在「告警已判定、待发送」阶段对事件做**纯变换**，不参与判定、不改状态机：

| 段 | 作用 | 执行时机 |
|---|---|---|
| `relabels` 标签重写 | `rename` / `delete` / `replace` / `set` 标签 | 派发入口，对事件副本执行一次 |
| `enrich` 标签增补 | 按条件注入固定标签 | 同上 |
| `templates` 消息模板 | 按渠道/级别/规则渲染通知正文 | **逐渠道**渲染，因此不同渠道可有不同文案 |

选择顺序：**渠道专属模板优先于通用模板**；都没命中、渲染失败或结果为空 → 回退系统内置描述
（**渲染问题不会让告警内容丢失**）。

### 1.2 可用变量（写模板前先看这里）

**内置标签**（由事件字段派生，一定存在）：

| 标签 | 含义 |
|---|---|
| `name` | 规则名称 |
| `rule` | 规则 ID |
| `node` | 节点名 |
| `instance` | 实例（Redis 等多实例指标才有，可能为空） |
| `severity` | 级别：`critical` / `warning` / `info` |
| `metric` | 指标名 |

其它标签（`team` / `owner` / `env` …）**只有事件本身带出来或你自己注入过才存在**——这是
最容易踩的坑：`when: { match: { env: prod } }` 在事件没有 `env` 标签时**永远不生效**。

**模板可见的事件字段**（Go `text/template`，数据对象是 `model.AlertEvent`）：

```
{{.RuleName}} {{.RuleID}} {{.Node}} {{.NodeIP}} {{.Instance}} {{.Metric}}
{{.Value}} {{.Operator}} {{.Threshold}} {{.Severity}} {{.State}} {{.Message}}
{{.StartsAt}} {{.EndsAt}}   // 毫秒整数，请用 ts 格式化
```

- 取标签：`{{index .Labels "team"}}`，或 `{{.Labels.team}}`（key 是合法标识符时）
- 判断状态：`{{if eq .State "resolved"}}已恢复{{else}}触发中{{end}}`
- **时间格式化**：`{{ts .StartsAt}}` → `2026-09-30 15:04:05`（本地时区）。
  直接写 `{{.StartsAt}}` 会输出 `1759211045000` 这种毫秒数字。

### 1.3 模板 A：对齐外部平台命名 + 隐藏内部地址

```yaml
relabels:
  - { op: rename, source: node, target: host }      # 外部平台认 host
  - { op: rename, source: name, target: alertname } # Alertmanager 风格
  - { op: delete, source: instance }                # 不把内部实例地址带出去
```

注意：`rename` 会**覆盖**目标标签的同名值；若事件本身已带 `host`，上面的规则会用它覆盖。

### 1.4 模板 B：级别本地化 + 团队/动作标签

```yaml
relabels:
  # 写进新标签，而不是改写 severity —— 原始级别仍供抑制 / 路由等既有逻辑使用
  - { op: set, target: severity_cn, value: 紧急, when: { match: { severity: critical } } }
  - { op: set, target: severity_cn, value: 警告, when: { match: { severity: warning } } }
  - { op: set, target: severity_cn, value: 信息, when: { match: { severity: info } } }

enrich:
  - { target: team,   value: sre,    when: { match: { severity: critical } } }
  - { target: action, value: page,   when: { match: { severity: critical } } }
  - { target: action, value: ticket, when: { match: { severity: warning } } }
```

还支持正则条件（页面表格只编辑等值条件，正则需用「YAML 直编」）：

```yaml
enrich:
  - { target: owner, value: dba, when: { matchRegex: { node: "^db-" } } }
```

### 1.5 模板 C：IM 单行 + 邮件详细（演示渠道优先级）

```yaml
templates:
  # 渠道留空 = 全部渠道兜底：IM 里单行最清楚
  - name: 单行精简版
    channel: ""
    template: '{{if eq .State "resolved"}}✅ 已恢复{{else}}🔥 {{.Severity}}{{end}} | {{.RuleName}} | {{.Node}} | {{.Metric}}={{.Value}}（阈值 {{.Threshold}}）'

  # 渠道专属优先于通用：邮件用它，IM 继续用上面那条
  - name: 邮件详细版
    channel: email
    severity: [critical, warning]   # 空 = 全部级别
    ruleIds: []                     # 空 = 全部规则；支持前缀匹配
    template: |
      {{if eq .State "resolved"}}告警已恢复{{else}}告警触发{{end}}
      规则：{{.RuleName}}（{{.RuleID}}）
      级别：{{.Severity}}{{if .Labels.severity_cn}}（{{index .Labels "severity_cn"}}）{{end}}
      节点：{{.Node}}{{if .NodeIP}}（{{.NodeIP}}）{{end}}
      {{if .Instance}}实例：{{.Instance}}
      {{end}}指标：{{.Metric}}
      当前值：{{.Value}} {{.Operator}} 阈值 {{.Threshold}}
      描述：{{.Message}}
      触发时间：{{ts .StartsAt}}{{if .EndsAt}}
      恢复时间：{{ts .EndsAt}}{{end}}
```

### 1.6 模板 D：主机名去掉域名后缀

```yaml
relabels:
  - op: replace
    source: node
    pattern: "\.example\.com$"
    replace: ""
```

`replace` 用的是 Go 正则，`replace` 里可用 `$1` 引用捕获组（例：`pattern: "^cn-(.*)$"`,
`replace: "CN-$1"`）。

### 1.7 怎么验证、别直接上生产

1. 页面「效果预览」→ 选渠道 → **按当前配置试算**（用的是你**尚未保存**的表单内容，不影响线上）；
2. 样例事件可自行编辑，把 `labels` 改成你们真实事件带出来的标签，验证条件是否命中；
3. 确认无误再点「保存」——保存即热生效，无需重启；
4. 想回滚：右侧「高级：YAML 直编」里保存前的内容可先复制出来留底（也可以在 YAML 里整段替换）。

### 1.8 已知边界

- 页面表格的「条件」列只支持**等值**条件（`k=v`）；正则条件请用「高级：YAML 直编」，
  否则页面上会出现一条"看不到条件"的规则。
- 模板函数只有一个 `ts`。模板的职责是排版，取值与判定逻辑不放进模板——需要派生字段请用
  `relabels` / `enrich` 先造好标签。
- 管道只变换"要发出去的内容"，**不会**改变告警的判定、状态机、抑制与分组结果。

---

## 二、集中日志（Agent 侧 `logSources`）

### 2.1 怎么启用、数据怎么流

- 在**被监控节点**的 `agent.yaml` 里加 `logSources` 段，**非空即启用**（没有额外的开关）。
  **不配置 = 一条日志都不采集**——这是刻意的隐私默认值。
- 改完**重启 Agent**（配置在启动期校验，写错会直接拒绝启动并给出原因）。
- 只上传**命中 `patterns` 正则**的行；要全量必须显式写 `all: true`。
- 落盘在 Server 的 `<DataDir>/logs/<来源>/<YYYY-MM-DD>/<节点>.log`，默认保留 **7 天**
  （`retention.logsDays`）、每来源每日上限 **1 GiB**。
- 页面检索需要权限点 `logs:read`。

### 2.2 约束速查（贴之前先对一遍，写错会拒绝启动）

| 字段 | 约束 |
|---|---|
| `id` | `^[a-z][a-z0-9_]{1,31}$`：**小写字母开头**，只含小写字母/数字/下划线，长度 2–32。它同时是存储分片名与指标前缀，**同一份配置里不能重复** |
| `patterns[].name` | `^[A-Za-z_][A-Za-z0-9_]{0,31}$`：字母或下划线开头，长度 1–32。它会**拼进指标名**（`<id>_log_<name>_total`），所以不能有空格、连字符、中文 |
| `patterns[].regex` | 必须可编译；为空直接拒绝 |
| `paths[]` | **绝对路径**、不含 `..`、**不支持通配符**（`*` 不会被展开）、必须是普通文件 |
| `patterns` / `all` | 二者至少有一个：没有 `patterns` 又没写 `all: true` → 拒绝启动 |
| `multiline.startPattern` | 必须可编译；`maxLines` 默认 50、上限 500 |
| `maxLinesPerRound` | 默认 2000、上限 20000 |
| `maxBytesPerRound` | 默认 4 MiB、上限 32 MiB |
| `logOffsetsFile` | 默认 `/var/lib/monitor-agent/log_offsets.json`（读取进度，重启不重复上传） |

产出的指标（**都是每轮增量，不是累计计数器**，配阈值规则别套 `rate()`）：

```
<id>_log_<name>_total{source,node}                     # 本来源第 <name> 个模式本轮命中行数
<id>_log_lines_total{source,node}                      # 本轮成功上传行数
<id>_log_up{source,node}                               # 本轮文件是否都读得到
<id>_log_dropped_total{source,node,reason}             # reason: cap|rate|size|dailyCap|unreachable
```

> 名称里有 `<id>`：例如 id 为 `applog`、模式名为 `err`，指标就是 `applog_log_err_total`。

### 2.3 模板 A：Nginx 访问日志只看 4xx/5xx

```yaml
logSources:
  - id: nginx_access
    paths: ["/var/log/nginx/access.log"]
    patterns:
      # access.log 里状态码前是 `" `（引号+空格），据此避免匹配到 URL 里的数字
      - { name: http_5xx, regex: '" 5[0-9][0-9] ' }
      - { name: http_4xx, regex: '" 4[0-9][0-9] ' }
```

### 2.4 模板 B：Nginx 错误日志

```yaml
logSources:
  - id: nginx_error
    paths: ["/var/log/nginx/error.log"]
    patterns:
      - { name: err, regex: '(?i)\[(error|crit|alert|emerg)\]' }
```

### 2.5 模板 C：Java / Spring 应用日志（堆栈跨行合并）

```yaml
logSources:
  - id: applog
    paths: ["/opt/app/logs/app.log"]
    patterns:
      - { name: err, regex: '(?i)\b(ERROR|Exception|Caused by)\b' }
      - { name: warn, regex: '(?i)\bWARN\b' }
    # 行首不匹配「日期开头」的行会被并入上一条 —— 这是让堆栈成为一条记录的关键
    multiline:
      startPattern: '^\d{4}-\d{2}-\d{2}'
      maxLines: 100
```

### 2.6 模板 D：系统认证 / sudo 审计

```yaml
logSources:
  - id: authlog
    # 按发行版保留实际存在的那个：CentOS/RHEL 是 /var/log/secure，Debian/Ubuntu 是 /var/log/auth.log
    paths: ["/var/log/secure"]
    patterns:
      - { name: failed, regex: '(?i)(Failed password|authentication failure|Invalid user)' }
      - { name: sudo, regex: '(?i)sudo:.*COMMAND=' }
```

（读不到的文件不会让 Agent 出错，只会在 Agent 日志里记一条"该来源本轮不可用"，所以列多余路径
不会中断采集，但会持续产生噪声——建议只保留实际存在的路径。）

### 2.7 模板 E：MySQL 慢查询

```yaml
logSources:
  - id: mysql_slow
    paths: ["/var/log/mysql/slow.log"]
    patterns:
      - { name: slow, regex: '^# Query_time' }
    multiline:
      startPattern: '^# Time:'
      maxLines: 200
```

### 2.8 验证与排障

1. 改完 `agent.yaml` → 重启 Agent；**启动失败会直接告诉你是哪一处写错了**（这是刻意的：
   日志配错的运行期症状是"界面上什么都没有"，而原因不会自己冒出来）。
2. 等 1 个采集周期（默认 15s），到「集中日志」页把时间范围拉宽后查询；来源下拉里应能看到
   你配置的 `id`（它来自各节点上报的能力清单）。
3. 查不到时按这个顺序排：
   - 页面空状态若是「没有任何节点配置日志来源」→ 配置没生效（没重启 / 写在了别的节点）；
   - 有来源但无命中 → 正则没覆盖到你的日志格式（先用很宽的正则如 `(?i)error` 验证链路，
     再逐步收紧）；
   - 命中后没上传 → 看 `<id>_log_dropped_total`（`cap`=超单轮上限、`dailyCap`=超每日 1 GiB、
     `rate`=被限速、`unreachable`=上行失败）。

### 2.9 已知边界（都别踩）

- **不支持通配符**：`/var/lib/docker/containers/*/*-json.log` 这类路径**不会被展开**，
  所以"采集所有容器日志"目前做不到——需要按容器逐个写具体路径，或用其它方式落盘成固定文件。
- **首次从文件尾开始**：首次见到文件时从末尾开始读，**不回溯历史**（避免一上线就把几十 GB
  历史日志灌进 Server）。想看历史日志请直接到机器上看。
- **轮转识别靠大小**：文件变小视为轮转并从头读；若被替换成**更长**的内容，偏移会落在新内容
  中间（会读到半行后跳过）——与既有 nginx access 日志实现同一取舍。
- **超限是"跳过"不是"稍后补"**：单轮超 `maxLinesPerRound`/`maxBytesPerRound` 时，本轮直接跳到
  文件末尾并计数（`_log_dropped_total{reason="cap"}`）。日志量大的来源要么调大上限，要么用
  `patterns` 收窄上传范围。
- **多行合并按行首模式**：`startPattern` 写太宽会把整个文件吸成一条（受 `maxLines` 兜底），
  写太窄则堆栈会被拆散——拿真实日志试一次再上生产。

## 三、告警规则（阈值与方向怎么定）

面向「知道要监控什么，但不确定该填多少」的场景。规则入口：「告警 → 告警规则」，
右上「新建规则」下拉里可直接套用内置模板（65 条，按中间件分组，每条都给出触发条件与阈值依据）。

### 3.1 指标从哪里来

指标下拉的内容来自**服务端指标字典**（`GET /api/v1/metrics/catalog`），它由守卫测试保证
「目录里出现的每个指标名都真的有人产出」。所以下拉里能选到的，就是能配的。

两类特殊标记：

| 标记 | 含义 | 怎么配 |
|---|---|---|
| **不常设阈值** | 累计计数器（`*_total`、`*_requests`）、容量信息（`*_total_bytes`、`*_cores`）、运行时长（`*_uptime*`） | 它们不是不能比较，而是比较结果没有运维含义（给 uptime 设阈值不会告诉你任何事）。要用就用「变化」语义关注，或改用同族的比例/速率指标 |
| **按维度命名** | 名字由运行时拼出：`mongodb_opcounters_query`、`nginx_access_requests_by_status`、`<来源>_log_<模式>_total` | 选中后**把占位部分替换成实际值**（日志类：先到「集中日志」页确认来源 ID 与模式名），否则规则查不到数据 |

### 3.2 两个「静默失效」陷阱（比阈值本身更容易出事）

这两类错误都**不会报错**，规则只是永远不触发——只能靠人来发现，所以表单专门做了提示：

1. **方向配反**。典型例子：给「证书剩余天数」（`dial_test_cert_expiry`）配成 `> 15`，
   语义变成"剩余天数大于 15 天才告警"——证书要过期了反而不告警。
   规则表单在选中指标后会按该指标的**变差方向**自动纠正明显矛盾的运算符（当前是 `>` 而指标越小越糟 → 改成 `<`），
   但**不会覆盖你特意选的方向**，所以自己也要看一眼。
2. **指标名写错**。手输的名字（exporter 透传名、日志模式指标）不校验存在性，
   拼错后规则照常保存、照常显示、永不触发。表单对不在字典里的名字会给橙色提示——
   如果那是 exporter 透传名或日志模式指标，忽略提示即可。

### 3.3 常见阈值速查

| 场景 | 指标 | 运算符 | 建议阈值 | 持续 | 说明 |
|---|---|---|---|---|---|
| CPU 使用率 | `cpu_usage` | `>` | 85 | 5m | 偶发尖峰正常，靠「持续」过滤 |
| 内存使用率 | `mem_used_percent` | `>` | 90 | 5m | 判真实余量看 `mem_available_bytes`（含可回收缓存） |
| 磁盘使用率 | `disk_used_percent` | `>` | 85 | 5m | 已做真实磁盘汇总；写满会让日志与数据库同时不可用 |
| 系统负载 | `load1` | `>` | 核数 × 2 | 5m | 8 核机器填 16 |
| Swap | `swap_used_percent` | `>` | 50 | 10m | 大量占用通常意味着物理内存不足 |
| TCP 重传 | `tcp_retransmit_rate` | `>` | 100 | 5m | 内网正常接近 0 |
| MySQL 主从延迟 | `mysql_seconds_behind_master` | `>` | 30 | 5m | 读从库会拿到旧数据 |
| MySQL 连接数 | `mysql_threads_connected` | `>` | 上限 × 0.8 | 5m | 上限见 `mysql_max_connections`（默认 500 → 400） |
| MySQL 缓冲池命中率 | `mysql_innodb_buffer_pool_hit_rate` | `<` | 95 | 10m | 低于 95% 说明缓冲池装不下热数据 |
| Redis 内存率 | `redis_used_memory_percent` | `>` | 85 | 5m | 需已设置 `maxmemory` |
| Redis 命中率 | `redis_hit_rate` | `<` | 80 | 10m | 骤降常见于缓存穿透或键集中过期 |
| Redis 碎片率 | `redis_memory_fragmentation_ratio` | `>` | 1.5 | 15m | 可开 `activedefrag` 或安排重启 |
| Redis 集群故障槽 | `redis_cluster_slots_fail` | `>` | 0 | 5m | 非 0 即这部分键不可用 |
| Nginx 连接丢弃 | `nginx_connection_drop_rate` | `>` | 1 | 5m | `accepts - handled` 增长，backlog 打满 |
| Kafka 副本不足分区 | `kafka_under_replicated_partitions` | `>` | 0 | 5m | 持续非 0：有 Broker 掉线 |
| Kafka 离线分区 | `kafka_offline_partitions` | `>` | 0 | 2m | 任何非 0 都意味着分区不可读写 |
| Kafka 消费积压 | `kafka_consumer_lag_max` | `>` | 10 万 | 10m | 取所有消费组最大值，按业务吞吐调整 |
| Kafka Controller | `kafka_active_controller_count` | `!=` | 1 | 2m | 正常恒为 1；>1 脑裂、=0 无主 |
| ES 集群状态 | `es_cluster_status` | `>=` | 1（黄）/ 2（红） | 5m / 2m | 0 绿 1 黄 2 红；黄=无冗余、红=数据不可用 |
| ES 未分配分片 | `es_unassigned_shards` | `>` | 0 | 10m | 黄/红的直接原因，先查磁盘水位 |
| ZooKeeper 排队 | `zookeeper_outstanding_requests` | `>` | 10 | 5m | 常见于磁盘慢或事务日志同盘 |
| RabbitMQ 队列积压 | `rabbitmq_queue_messages` | `>` | 1 万 | 10m | 消费者跟不上生产 |
| RabbitMQ 文件描述符 | `rabbitmq_fd_used` | `>` | 5 万 | 10m | 接近 ulimit 会出现连接被拒 |
| ClickHouse 合并 | `clickhouse_merges_running` | `>` | 20 | 10m | 写入过快或分区粒度过细 |
| MongoDB 副本延迟 | `mongodb_repl_lag` | `>` | 10 | 5m | 读从库会拿到旧数据 |
| RocketMQ 积压 | `rocketmq_message_accumulation` | `>` | 1 万 | 10m | 按业务吞吐调整 |
| FastDFS 离线 Storage | `fastdfs_storage_offline_count` | `>` | 0 | 5m | 容量与冗余同时下降 |
| K8s 异常 Deployment | `k8s_deployments_unhealthy` | `>` | 0 | 10m | 多为镜像拉取或探针失败 |
| K8s Pending Pod | `k8s_pods_pending` | `>` | 10 | 10m | 资源不足或调度约束无法满足 |
| 拨测失败 | `dial_test_up` | `<=` | 0 | 3m | 按拨测任务维度上报 |
| 拨测延迟 | `dial_test_latency` | `>` | 2000 | 5m | 含 DNS 与 TLS，按 SLO 调整 |
| 证书即将到期 | `dial_test_cert_expiry` | `<` | 15（天） | 1h | **越小越糟**，方向别配反 |
| 端口不可达 | `port_up` | `<=` | 0 | 5m | Agent 侧 TCP 探测 |
| 日志采集不可用 | `log_up` | `<=` | 0 | 10m | "配了却没有数据"的第一现场信号 |
| 日志丢弃 | `log_dropped_total` | `>` | 0 | 10m | 丢弃是正常结果，但不该持续发生 |

### 3.4 配之前先确认「有数据」

规则**不会**因为指标没有数据而报错，只会安静地不触发。所以新配一条规则前，建议先到
「指标浏览」页搜一下这个指标名：

- 能查到数据 → 直接配；
- 一条序列都没有 → 先解决采集（指标浏览会把目录里但未上线的指标标出来），
  否则你会以为"规则配好了"，实际等到故障发生才发现它从来没生效。

## 四、下行操作（节点操作页 / `guards.ops`）

「运维操作 → 节点操作」可以把一条白名单动作下发给指定节点执行，并看到回执。
**默认只放行只读动作**；要让某台机器接受写操作（目前只有"重启服务"），必须在那台机器上改配置。

### 4.1 为什么默认是关的

Agent 以 root 运行，平台上的一个写权限 ≈ 一批机器的 root。所以「能不能在这台机器上执行」
由**机器自己的配置**决定（本机护栏），中心只能决定"要不要下发"。四道护栏缺一不可：

| 护栏 | 在哪 | 默认 |
|---|---|---|
| 本机护栏 | 目标机器 `agent.yaml` 的 `guards.ops` | 只读放行，写全禁 |
| 能力协商 | Agent 上报 `Capabilities.Ops`（即本机放行清单），Server 只下发声明过的动作 | 无声明则不下发 |
| 中心授权 | 权限点 `ops:read`（看）/ `ops:exec`（下发，高风险）+ 参数校验 + 审计 | 需显式授予 |
| 默认只读 | 首批 3 个动作里 2 个只读 | — |

### 4.2 放行写操作（在目标机器上）

```yaml
# /etc/nebula-monitor/agent.yaml（或你的实际路径）
guards:
  ops:
    readOnly: true          # 只读动作（诊断包、查服务状态）；默认 true
    write: true             # 放行写操作；默认 false
    units:                  # 允许写操作的单元白名单（可省略 .service 后缀）
      - nginx
      - redis.service
    container: true         # 容器/K8s 只读查询；默认 true，见 4.5
```

两个条件缺一不放行：`write: true` **且** 单元在 `units` 里——避免"开了一个总开关就放开了所有服务"。
改完重启 Agent，启动日志里会打印放行结果：

```
下行操作本机护栏已就绪 readOnly=true write=true units=[nginx redis.service] supported=[node.diagnostics svc.status svc.restart]
```

`supported` 就是上报给 Server 的能力清单：**它不在里面，平台上那个动作就是灰的**。
配了 `k8sInstances` 且 `guards.ops.container` 未被关掉时，`supported` 里还会多出
`container.workloads / container.pods / container.describe / container.events` 四个只读动作（见 4.5）。

### 4.3 怎么用、怎么排障

1. 「节点操作 → 下发操作」：选节点 → 选动作 → 填参数（如 `nginx.service`）→ 填原因（强烈建议）。
2. 指令随该节点**下一次上报**送达（默认 15s 内），然后结果随再下一轮回来；列表只对未完成任务自动刷新。
3. 常见现象与原因：

| 现象 | 原因 | 怎么办 |
|---|---|---|
| 下发按钮点了报「尚未声明支持下行操作」 | Agent 版本过低，或该机 `guards.ops` 把只读也关了 | 升级 Agent / 改本机配置 |
| 报「未放行写操作…需在 guards.ops 中把 write 设为 true」 | 本机护栏没放行（这是最常见的） | 去那台机器改 `guards.ops` |
| 动作在列表里是灰的 | 该节点能力清单里没有它 | 同上；灰化即"本机不同意" |
| 节点离线时下发被拒绝（409） | 指令只能随上报响应送回，离线节点必然等到过期 | 等节点恢复在线 |
| 任务一直「排队中」 | 该节点没在领取（Agent 停了 / 网络不通） | 看节点是否在线 |
| 任务「已超时」 | 30 分钟内没完成 | 查 Agent 日志；重发一条 |
| 结果里某节写「命令不可用」 | 那台机器没装该命令（如 `ss`） | 不是故障，是如实说明 |

### 4.4 批量下发与撤回

**批量下发**：下发对话框里可以多选节点，或按分组整选（「按分组选」→ 选一个分组 = 该组所有**在线**节点）。
离线节点在列表里是禁选的：指令只能随上报响应送回，给离线节点建任务是"一条注定过期的任务"。

选完节点后，动作下拉会标出每个动作的**放行比例**（如 `3/5 台放行`）——这是批量场景最需要的信息，
否则只能在结果里一台台看失败原因。下发后弹「批量下发结果」，逐台给出「已下发（任务号）」或
「未下发（具体原因：节点不在线 / 未放行写操作 / 节点不存在）」。

上限 **200 台/次**，超限会明确报错并提示分批（不静默截断）。

**撤回**：只有仍在**排队中**（queued）的任务能撤。一旦已下发（delivered），指令已经随某轮上报
发出去了，此时界面会说"已取消"而机器照样执行——所以服务端直接拒绝，并在整批撤回时**逐条**列出
哪些撤不回来：

```
已撤回 3 条；2 条无法撤回（多半已被节点领取）
```

取消不会删除记录，而是留一条 `已取消` 的记录（否则"我明明下过这条指令"会变成悬案）。
清列表用**删除**：只对已结束（成功/失败/超时/已取消）的记录开放，且写审计（谁删了什么）。

任务列表支持按**批次号**筛选（点批次号即筛该批），批量下发的批次号形如 `ob-7`。

### 4.5 容器只读查询（容器与工作负载页）

「观测监控 → 容器与工作负载」可以只读查看某个 K8s 集群的工作负载、Pod、事件与对象详情。

**它不是 Server 直连 apiserver**：K8s 的 kubeconfig / token 只存在 Agent 本地
（`agent.yaml` 的 `k8sInstances`），Server 侧只知道"有这么个集群"。因此你看到的每一次查询
都是一条**异步任务**——指令随该节点下一次上报下发，结果随再下一轮回来，通常 15 秒内出结果；
超过 60 秒界面会明确报超时，而不是一直转圈。

**本机护栏**：容器查询读的不是本机，而是这台机器的凭据能够到的**整个集群结构**，
所以除了只读总开关，它还有自己的开关（默认放行）：

```yaml
guards:
  ops:
    readOnly: true       # 关掉它，容器查询也一起关掉
    container: false     # 显式关掉容器查询：仍可做节点诊断，但不再交出集群结构
```

**能力与权限**：
- 没配 `k8sInstances` 的机器**不声明**容器查询能力，页面上显示该节点不支持，
  而不是下发一条注定失败的任务；
- 查看需权限点 `container:read`（内置「全局运维管理员」已含）；下发查询动作仍走 `ops:exec`。

**已知取舍（别当缺陷）**：
- `container.describe` 返回的是**白名单投影**而不是原始 YAML：只给排障需要的字段；Pod 的环境变量只给
  **变量名**，ConfigMap 只给**键名**——它们的内容在生产里经常就是口令；
- `secrets` 不在可选资源类型里。真要看 Secret 请直接上 `kubectl`，监控平台不该成为一条取密路径；
- 列表按行数上限截断（工作负载 300 / Pod 500 / 事件 200），截断时页面会明说"结果已截断"，
  不会假装"集群里就这么多"。

### 4.6 已知边界

- **一次只下一个节点**：批量下发未实现，需要按节点逐个下发。
- **同一节点同时只执行一条**：避免两条写操作互相打架；排队中的任务会依次领取。
- **指令延迟 = 一个采集周期**：这是"不新建长连接"的代价，换来的是不用开放任何入站端口。
- **不做任意命令/脚本执行**：动作是服务端目录里的白名单，参数被两端校验（`exec` 类能力是后续独立评估项）。
- **只读动作也要 `ops:exec`**：即使只是取诊断包，它也是"在一批机器上执行东西"的能力。
