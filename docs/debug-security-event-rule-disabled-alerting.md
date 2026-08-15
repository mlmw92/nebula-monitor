# 排查记录：安全事件规则禁用后仍持续告警

> 适用版本：nebula-monitor 1.22.x（安全监测中心功能引入后）
> 记录时间：2026-08-15
> 现象：用户在告警中心禁用「安全事件」规则（`type=security_event`）后，仍持续收到该规则的告警通知，告警中心不断出现「SSH 认证中断」等安全事件。

---

## 一、问题背景

告警中心内置一条 `安全事件` 规则（示例 ID `r-msu7s3ta`，`type=security_event`），默认启用。
用户在前端将其**禁用**（`enabled=false`）后，预期不再产生任何安全事件告警。
但实际仍在持续收到告警，且「活跃」与「全部」列表中反复出现新的安全事件条目。

---

## 二、排查过程

### 第 1 步：确认运行态规则的真实状态

通过 `GET /api/v1/rules`（需登录 token）拉取运行态规则列表，确认内存中该规则：

```json
{ "id":"r-msu7s3ta", "name":"安全事件", "type":"security_event", "enabled":false, ... }
```

- `enabled` 确认为 `false`，排除「保存没生效 / 改错文件 / 配置没重载」的可能。
- 说明规则本身已禁用，但告警仍产生，问题出在告警生成或展示环节。

### 第 2 步：核查源码的告警注入路径（怀疑旧二进制）

当前源码 `internal/server/alert/engine.go` 的 `evaluate()`：

```go
for _, r := range rules {
    if !r.Enabled {
        continue
    }
    ...
    case RuleTypeSecurityEvent:
        e.evalSecurityEvents(r, nodes, now)
}
```

安全事件由 `evalSecurityEvents` 处理，逻辑上受 `enabled` 控制。但用户仍在收告警，说明**线上二进制与当前源码不一致**。

沿 git 历史回溯，关键提交：

- `a6811c5`（安全监测中心初版）的 `internal/server/receiver/receiver.go` 对每条 Agent 上报的安全事件 **直接调用**：

  ```go
  r.alerts.EmitSecurityAlert(ev.Node, payload.IP, ev.Category, ev.Severity, ev.Message, nil)
  ```

- `EmitSecurityAlert`（`engine.go`）**完全不检查任何规则的 `enabled`**，直接将事件灌入告警中心。
  因此那一版本里「禁用规则」对安全事件告警**毫无作用**。
- `2aa31c2` 将路径改为 `r.alerts.IngestSecurityEvents(...)`，事件进入引擎队列，由 `evaluate()` 统一按 `enabled` 判断。

结论：用户线上跑的是 `2aa31c2` 之前、仍走 `EmitSecurityAlert` 的旧二进制，禁用判断被绕过。

### 第 3 步：用户自助升级到 1.22.7

用户通过 Web 端「系统升级」部署新包后，校验 `GET /api/v1/version`：

```json
{ "server":"1.22.7", "buildTime":"2026-08-15T14:19:48Z", "goVersion":"go1.26.5" }
```

- `server` 与仓库 `VERSION` 一致，`buildTime` 为当天构建，确认升级成功。

### 第 4 步：禁用后变「已恢复」，刷新后又「告警中」

升级后用户反馈：禁用规则后告警先变「已恢复」，但刷新页面（或 Agent 持续上报）后又出现「告警中」，且「全部」列表里同一条事件出现两行（一行告警中、一行已恢复）。

排查 `internal/server/alert/store.go` 的 `Recent()`（对应「全部」tab），发现分组 key 包含 `state`：

```go
key := ser.Labels["rule"] + "|" + ser.Labels["host"] + "|" + ser.Labels["instance"] + "|" + ser.Labels["state"]
```

- firing 与 resolved 因 `state` 不同被当成两条独立时序序列，于是同一条事件在「全部」tab 同时展示 firing 与 resolved。
- 「活跃」tab 使用的 `Active()` 按 `rule|host|instance` 分组、只取最新状态，因此显示正确（resolved 覆盖 firing 后不显示）。

**修复（提交 `e8e5ebb`）**：

1. `Recent()` 分组 key 去掉 `state`，改为 `rule|host|instance`，只保留每个告警的最新状态。
2. `engine.go` 的 `cleanupInactiveRulesLocked` 与 `CloseRuleAlerts` 在关闭告警时**保留原始 message**（如 SSH 认证中断详情），并追加「规则已停用或删除」原因，避免 resolved 行丢失上下文。

### 第 6 步：复现并修复规则切换与事件处理之间的缺口

继续审计后确认，当前源码仍存在三个独立问题：

1. `evaluate()` 原先在取得引擎锁前读取 `rules.List()`。当评估循环获得旧的 `enabled=true` 快照、规则随后被禁用并完成 `DisableRule()` 清理、又恰有 Agent 事件入队时，本轮仍可能按旧快照消费该事件并写入新的 firing。该 firing 在此前清理之后产生，因此不会同步写入 resolved，表现为「已恢复为空，活跃/全部出现新的告警」。
2. `PUT /api/v1/rules/:id` 的普通编辑接口此前只保存 `enabled=false`，未调用 `DisableRule()`；只有 switch 使用的 `/toggle` 接口会即时关闭 firing 与清理队列。通过编辑窗口停用规则时，存在一个评估周期内的残留事件窗口。
3. 安全事件队列由全部安全规则共享。旧的 `clearSecurityEventsForRule()` 在禁用空 Category 的通配规则时会删除所有待处理事件，误伤仍启用的分类规则。

**修复**：

- 规则快照与节点快照移动至引擎锁内读取，保证规则停用后的评估不会再使用过期启用快照。
- 普通规则编辑由启用改为停用时，也调用 `DisableRule()`。
- 统一按仍启用的安全事件规则筛选共享队列：仅丢弃没有任何启用规则能处理的事件；禁用规则的去重记录仍会清理。

验证：新增通配规则禁用与分类规则并存的回归测试；`go test ./...` 通过。

---

## 三、根因总结

| 层级 | 根因 | 说明 |
| --- | --- | --- |
| 主因 | 线上 server 二进制滞后于修复代码 | 旧版本安全事件告警路径绕过规则 `enabled` 判断，禁用规则无效 |
| 次因（展示） | `Recent()` 以 `state` 作为分组维度 | 同一条事件同时展示 firing 与 resolved 两行，造成「禁用后又告警」的视觉误解 |

---

## 四、修复清单

| 提交 | 修复内容 |
| --- | --- |
| `2aa31c2` | 安全事件规则禁用后仍告警及内存无限增长：receiver 改走 `IngestSecurityEvents`，禁用即时清理事件队列与去重缓存 |
| `e8e5ebb` | 告警中心「全部」tab 对同一事件重复显示 firing/resolved 两行：`Recent()` 分组去 `state`，resolved 保留原始 message |
| 本次待提交修复 | 规则快照与安全事件队列同步、普通编辑停用即时清理、通配规则禁用不再误删分类规则事件 |

---

## 五、验证步骤

1. 重新编译部署最新源码后，执行 `GET /api/v1/version`，确认 `server` 版本与 `buildTime` 已更新为最新编译。
2. 在 UI 将「安全事件」规则**启用**，等待一条新的 SSH 登录/认证中断事件 → 应产生新告警。
3. 再将其禁用（开关或编辑保存均可）→ 当前 firing 立即关闭，后续安全事件不再产生该规则告警。
4. 同时保留一个启用的指定分类安全事件规则、禁用通配规则，确认该分类的新事件仍能产生告警。
5. 切换「活跃 / 全部 / 已恢复」tab，确认不再重复展示同一事件的两行状态。

---

## 六、相关代码位置

| 文件 | 关键函数 / 字段 | 作用 |
| --- | --- | --- |
| `internal/server/alert/engine.go` | `evaluate()` | 规则评估主循环，`if !r.Enabled { continue }` 跳过禁用规则 |
| `internal/server/alert/engine.go` | `evalSecurityEvents()` | 安全事件规则匹配并触发告警（受 enabled 控制） |
| `internal/server/alert/engine.go` | `IngestSecurityEvents()` | 接收 Agent 上报安全事件入队 |
| `internal/server/alert/engine.go` | `cleanupInactiveRulesLocked()` | 关闭已停用/删除规则遗留的 firing 状态，清理未处理事件 |
| `internal/server/alert/engine.go` | `EmitSecurityAlert()` | **旧版直接注入路径，已被 `2aa31c2` 移除调用，现为死代码** |
| `internal/server/alert/store.go` | `Recent()` | 「全部」tab 数据；分组 key 为 `rule\|host\|instance` |
| `internal/server/alert/store.go` | `Active()` | 「活跃」tab 数据；按 `rule\|host\|instance` 取最新状态 |
| `internal/server/receiver/receiver.go` | 安全事件上报处理 | `r.alerts.IngestSecurityEvents(payload.SecurityEvents)` |
| `internal/server/api` | `GET /api/v1/rules`、`GET /api/v1/version` | 规则状态与版本校验接口 |

---

## 七、经验沉淀

- 「禁用规则还告警」类问题，优先用 `GET /api/v1/version` 比对线上二进制与 `VERSION`/最新提交，往往不是逻辑 bug，而是**二进制未重新编译部署**。
- 安全事件告警有唯一入口 `receiver → IngestSecurityEvents → evalSecurityEvents`，任何不经过 `evaluate()` 直接 `alerts.Add` 的路径都会绕过规则生命周期。
- 事件型告警（firing/resolved 切换）写入 VM 时，`state` 是标签之一；查询聚合若把 `state` 纳入分组维度，会导致同事件重复展示。
