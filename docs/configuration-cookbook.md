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
