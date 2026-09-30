# 权限矩阵设计（路由 → 权限点 → 资源范围）

> 本文为 B1 改造的设计件，用于评审后进入实施。
> 事实基线：VERSION 1.25.0；路由清点自 `internal/server/api/query.go` 的 `RegisterRoutes`（122 条）加 `ws.go`（1）、`spa.go`（1）、`cmd/server/main.go`（1）、`internal/server/agentdist/agentdist.go`（2），合计 **127 条**（批次 E 新增 `GET /api/v1/audit/export` 前为 126 条）；F3 新增 3 条（`GET /healthz`、`GET /readyz` 公开，`GET /api/v1/self/status` 需 `dashboard:read`）；D4 新增 3 条（`POST /api/v1/alerts/close|reopen|comment`，均 `alerts:write`）；A3 新增 3 条（`/api/v1/system/retention` 及 `/cleanup`，均 `system:config`），当前共 **136 条**。

## 实施状态

> **命名提醒**：下表的「批次 A~E」是本文档对 **B1 实施** 的内部拆分，属于路线图 `docs/refactor-plan.md` 第 4 节「**批次二 → B1（实施）**」这一项之下，**不是**路线图批次二/三的并列项。
> 另注意与候选改造点 ID 的区别：这里的「批次 C 中间件授权」与路线图里的 **C1**（采集项 YML 模板化，批次三首位）**无关**。

| 子批次 | 状态 | 已落地内容 |
|---|---|---|
| **A｜基础设施** | ✅ 已完成 | 新增 `dashboard:write`、`system:config` 两个权限点并补齐内置角色；新增 `api.API.permit(next, perm)` 业务接口权限包装器（未启用认证放行 / 未登录 401 / 缺权限 403 + 授权拒绝审计）；去除 `globalAuthStore` 包级单例，`AuthMiddleware` 改为显式接收 `*auth.Store`；11 个单元测试 |
| **B｜主机与指标（含 `/ws`）** | ✅ 已完成 | 23 条路由挂载 `permit` / `permitNode`：`nodes/*`（8）、`groups/*`（3）、`query/*`+`processes`+`listeners`+`firewall`（6）、`metrics/*`（3）、`analysis/*`（2，范围过滤本已存在）、`/ws`（1）。列表类按范围过滤（`handleNodes` / `handleNodesLatest` / `handleGroups`），单节点类由 `permitNode` 统一校验（路径 `{name}` 或查询 `node`），批量升级用 `CheckBatchGroups` 整体校验；**`/ws` 补齐 topic 级授权**（`metrics`→`nodes:read`+节点范围、`alerts`→`alerts:read`、未知 topic 拒绝）。告警管理员补齐 `nodes:read`/`groups:read`（告警页面分组筛选与规则目标选择依赖）；前端 `hosts` / `node/:name` / `metrics/explore` 补 `meta.perm` 与菜单 `perm`。14 个新测试 |
| **C｜中间件** | ✅ 已完成 | 13 条路由挂载 `middleware:read`；实例列表按「实例 → 所属节点 → 分组」过滤（Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / K8s / MongoDB / FastDFS 及 Nginx 访问汇总的实例列表）；`overview` 的实例计数与告警计数改为**过滤后重算**；K8s 工作节点/Pod 无 Agent 节点标签，按可见集群的 instance 归属过滤；前端中间件菜单与路由补 `perm`。8 个新测试 |
| **D｜告警与通知** | ✅ 已完成 | 26 条路由挂载权限点：告警事件（`alerts:read` / 确认 `alerts:write`）、规则 CRUD（`alerts:read` / `alerts:write`，临时静默为 `silence:write`）、抑制 / 分组 / 事件管道（`alerts:read` / `alerts:write`，`preview` 按语义只需 `alerts:read`）、维护窗口（`silence:read` / `silence:write`）、通知（`notify:read` / `notify:write` 高危）。范围过滤：告警列表、确认记录列表、统计看板均按节点范围过滤/重算，确认接口对范围外节点返回 403；规则、抑制、分组、管道、维护、通知为**全局配置**，不设范围。前端告警中心 / 智能分析 / 通知配置菜单与路由补 `perm`。7 个新测试 |
| **E｜安全、系统与其余** | ✅ 已完成 | 37 条路由 + **新增 1 条**（`GET /api/v1/audit/export`）挂载权限点：安全中心（`security:read` / 下指令 `security:write`）、审计（`audit:read` / 导出 `audit:export`）、系统升级（`system:upgrade`）、地理库与大屏品牌（`system:config`）、仪表盘（`dashboard:read` / `dashboard:write`）、安装信息（`agent:secret:read`）、代理状态（`agent:read`）、拨测（`probe:read`/`probe:write`）、报告（`report:read`/`report:export`）。范围过滤：安全事件、安全基线、防护状态列表、防护任务列表按节点范围过滤；`permitNode` 扩展支持 `{node}` 路径参数。**审计导出已按 8.3 拆为独立路由**（旧 `?format=csv` 保留兼容并额外判权）。前端审计导出改调新路由，8 个菜单与 6 条路由补 `perm`。4 个新测试（含 5 个审计拆分用例与 12 个权限点区分用例） |

---

## 1. 目标

1. **授权缺陷修复**：业务接口在服务端按「权限点 + 资源范围」强制校验，前端隐藏仅作体验（OWASP A01）。
2. **不改协议**：仅增加服务端校验，不改变请求/响应结构与既有路由路径。
3. **可分批上线**：每批可独立发布、独立回滚，且不存在「升级即锁死」的中间态。
4. **单一事实来源**：权限点与 `GET /api/v1/permissions/catalog` 同源（`auth.PermissionCatalog()`），不新增第二套定义。

## 2. 现状

| 项 | 现状 |
|---|---|
| 认证 | `api.AuthMiddleware`（`internal/server/api/auth.go:161`）包裹整个 mux；`auth.enabled=false` 时全放行 |
| 凭据来源 | `Authorization: Bearer <token>`，为空时回退到 **HttpOnly Cookie `nebula_token`**（登录时由 `handleLogin` 种下，`auth.go:282`） |
| 公开白名单 | `isPublicPath`（`auth.go:141`）：`/`、`/assets/*`、`/api/v1/login`、`/api/v1/report`、`/api/v1/agent/check`、`/install/*`、`/bin/*`；另有 `GET /api/v1/ui/settings` 匿名只读（`auth.go:172`） |
| 授权 | `api.authz(next, perm)`（`auth.go:226`）当前仅用于 **14 条** 权限管理路由（`users:manage` 8 条、`roles:manage` 3 条、`roles:read` 3 条） |
| 权限点目录 | `auth.PermissionCatalog()`（`internal/server/auth/model.go:195`），**27 个**权限点 / 13 个域 |
| 高风险权限点 | `auth.HighRiskPermissions`（`internal/server/auth/policy.go:71`），**8 个**：`system:upgrade`、`agent:secret:read`、`agent:upgrade`、`security:write`、`notify:write`、`users:manage`、`roles:manage`、`audit:export` |
| 资源范围工具 | `auth.FilterGroups`（列表过滤）、`auth.FilterByGroup[T]`（泛型项过滤）、`auth.CheckBatchGroups`（写操作批量校验）、`Principal.CanAccessGroup`（单条校验） |
| 范围语义 | `Scope.Mode` 为 `global` 或 `restricted`；`restricted` 且分组为空 = **无资源权限**（不放大为全部） |

**结论**：认证已到位，缺口在**授权**——除权限管理接口外，节点升级、Agent 密钥、入侵防御、通知、系统升级、数据导出等 110 条路由仅要求「已登录」。

## 3. 权限点目录

### 3.1 现有（27 个，不可重命名）

| 域 | 权限点 |
|---|---|
| 仪表盘 | `dashboard:read` |
| 主机 / 节点 | `nodes:read`、`nodes:write` |
| 节点分组 | `groups:read`、`groups:write` |
| 告警 | `alerts:read`、`alerts:write` |
| 通知 | `notify:read`、`notify:write` |
| 静默 / 维护 | `silence:read`、`silence:write` |
| 拨测 | `probe:read`、`probe:write` |
| 报告 | `report:read`、`report:export` |
| 数据导出 | `metrics:export` |
| 中间件 | `middleware:read`、`middleware:write` |
| 安全中心 | `security:read`、`security:write`、`agent:secret:read` |
| Agent | `agent:read`、`agent:upgrade` |
| 系统 | `system:upgrade`、`audit:read`、`audit:export`、`users:manage`、`roles:manage`、`roles:read` |

### 3.2 建议新增（2 个）

| 权限点 | 说明 | 理由 |
|---|---|---|
| `dashboard:write` | 管理自定义仪表盘 | 现目录仅有 `dashboard:read`；若允许多用户编辑看板，`dashboard:read` 会把「查看」与「增删改」绑成同一权限，违反最小权限 |
| `system:config` | 系统配置（IP 地理库、数据大屏配置、品牌配置） | 现目录中系统级配置无对应权限点；若复用 `system:upgrade`，会把「上传地理库」与「升级 Server 二进制」绑成同一权限，且 `system:upgrade` 已列入高风险 |

### 3.3 后续批次新增（已实施）

| 域 | 权限点 | 授予的内置角色 | 备注 |
|---|---|---|---|
| 资产 | `assets:read`、`assets:write`、`inspect:read`、`inspect:run` | 运维管理员（全）；只读角色按需 | `assets:write` 入高风险（人工维护会改变运维判断依赖的台账）；`inspect:run` 与 `assets:write` 刻意分开 |
| 集中日志 | `logs:read` | 运维管理员 | 日志含敏感内容，不默认给只读/告警/安全/审计角色 |
| 节点操作 | `ops:read`、`ops:exec` | 运维管理员 | `ops:exec` 入高风险（可在一批机器上执行东西）；**执行还须目标机器 `guards.ops` 放行**，权限只是四道护栏之一 |

**刻意只授予超级管理员的权限点**（`superAdminOnlyKeys`）：`users:manage`、`roles:manage`、
`system:upgrade`——前两者防自提权，后者是"改平台自身"的动作。

> 新增权限点必须同步两处，否则**超级管理员也拿不到该权限**：
> ① `auth.PermissionCatalog()`；② `auth.BuiltinRoles()` 中对应内置角色（至少超级管理员、运维管理员）。
> 这是批次 A 的第一个检查项。
>
> **2026-09-30 实机验证的教训**：`ops:read` / `ops:exec` 只加了 ①（目录）而漏了 ②（运维管理员角色），
> 后果是**功能装了却没人看得到**——路由 `meta.perm` 与侧边栏都按权限点门控，内置角色里没有它，
> 于是只有超级管理员能用，而这类遗漏不会报错。现已把守卫从"手工清单"改为**遍历目录**
> （`TestEveryCatalogKeyIsGrantedBySomeRole`）：目录里每个权限点都必须被某个非超级管理员的内置角色覆盖，
> 例外必须写进 `superAdminOnlyKeys` 并注明理由。该守卫上线时顺带发现了 `system:upgrade` 属未记录的例外。

## 4. 路由 → 权限点 → 资源范围

约定：
- **范围** 列 `分组` 表示需按节点分组过滤/校验；`节点` 表示需按单节点归属分组校验；`—` 表示不涉及范围。
- 「权限点」列 `—` 表示登录即可访问，不增加校验。

### 4.1 公开接口（8 条，不动）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| POST | `/api/v1/login` | 公开 | — |
| POST | `/api/v1/report` | 公开（`X-Agent-Secret`） | — |
| POST | `/api/v1/logs` | 公开（`X-Agent-Secret`） | 集中日志上行（C2）。与上报同一套接入凭据；必须同时在登录认证的公开白名单 `isPublicPath` 内，否则启用登录后会被 401 |
| GET | `/api/v1/status` | **公开（免登录）** | 对外状态页（C3）。免登录是设计目的；暴露范围由「拨测任务是否勾选 `public`」控制，响应**不含** target / 节点 / 错误原文。白名单按方法判定，**仅放行 GET** |
| GET | `/api/v1/logs` | `logs:read` + 节点分组范围 | 集中日志检索（C2）。**与上行共用路径**，因此白名单必须按方法判定（只有 POST 免登录）——按路径前缀放行会让浏览器请求没有 Principal，`permit` 直接 401 且范围过滤静默失效 |
| GET | `/api/v1/agent/check` | 公开（`X-Agent-Secret`） | — |
| GET | `/healthz` | 公开（存活探针，F3 新增） | — |
| GET | `/readyz` | 公开（就绪探针，F3 新增） | — |
| GET | `/install/agent-install.sh` | 公开 | — |
| GET | `/bin/` | 公开 | — |
| GET | `/` | 公开（SPA） | — |

### 4.2 主机与节点（11 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/nodes` | `nodes:read` | 分组 |
| GET | `/api/v1/nodes/latest` | `nodes:read` | 分组 |
| GET | `/api/v1/nodes/{name}` | `nodes:read` | 节点 |
| DELETE | `/api/v1/nodes/{name}` | `nodes:write` | 节点 |
| PUT | `/api/v1/nodes/{name}/group` | `nodes:write` | 节点 |
| PUT | `/api/v1/nodes/{name}/display-name` | `nodes:write` | 节点 |
| POST | `/api/v1/nodes/{name}/upgrade` | `agent:upgrade`（高风险） | 节点 |
| POST | `/api/v1/nodes/upgrade` | `agent:upgrade`（高风险） | 分组（`CheckBatchGroups`） |
| GET | `/api/v1/groups` | `groups:read` | 分组 |
| POST | `/api/v1/groups` | `groups:write` | — |
| DELETE | `/api/v1/groups/{name}` | `groups:write` | 分组 |

### 4.3 指标查询与浏览（9 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/query/range` | `nodes:read` | 节点 |
| GET | `/api/v1/query/latest` | `nodes:read` | 节点 |
| GET | `/api/v1/processes` | `nodes:read` | 节点 |
| GET | `/api/v1/query/listeners` | `nodes:read` | 节点 |
| GET | `/api/v1/query/firewall` | `nodes:read` | 节点 |
| GET | `/api/v1/query/firewall/status` | `nodes:read` | 节点 |
| GET | `/api/v1/metrics/catalog` | `nodes:read` | — |
| GET | `/api/v1/metrics/active` | `nodes:read` | 分组 |
| GET | `/api/v1/metrics/export` | `metrics:export` | 节点 |

### 4.4 中间件监控（13 条）

> 实例的可见性由其**所属节点**的分组决定；`overview` 的计数需在过滤后重新聚合，否则会泄露不可见分组的实例数量。

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/middleware/overview` | `middleware:read` | 分组（过滤后聚合） |
| GET | `/api/v1/middleware/redis/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/mysql/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/postgres/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/nginx/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/kafka/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/docker/containers` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/rocketmq/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/k8s/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/mongodb/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/fastdfs/instances` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/nginx/access/summary` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/nginx/access/geo` | `middleware:read` | 分组 |
| GET | `/api/v1/middleware/{type}/instances` | `middleware:read` | 分组（模板派生类型的通用实例接口；内置类型由各自的字面量路由优先命中） |
| GET | `/api/v1/middleware/templates` | `middleware:read` | 见下方说明 |
| GET | `/api/v1/middleware/templates/presets` | `middleware:read` | 内置预设（只读的模板样例，不含任何环境信息） |
| POST | `/api/v1/middleware/templates` | `middleware:write` | 见下方说明 |
| POST | `/api/v1/middleware/templates/validate` | `middleware:write` | 见下方说明 |
| PUT | `/api/v1/middleware/templates/{id}` | `middleware:write` | 见下方说明 |
| DELETE | `/api/v1/middleware/templates/{id}` | `middleware:write` | 见下方说明 |

> **采集项模板（C1 阶段二）**：读写权限点刻意分离——读只是看配置，写会改变各 Agent 去拉取哪些地址
> （等于让被监控机主动出站访问指定 URL），属高危操作，故归 `middleware:write`。
> 至此 `middleware:write` 不再是预留权限点（此前无对应路由，中间件采集配置在 `agent.yaml` 侧维护）。
> 模板的**下发范围为「按节点分组」**：模板必须声明生效的 `groups`（见阶段二设计），
> Server 只把命中该 Agent 所属分组的模板随上报响应下发；因此这几条路由本身不做资源范围过滤
> （它们操作的是「将要下发的内容」，与调用者能看哪些节点无关），危险点由权限点分离来兜。

### 4.5 智能分析（2 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/analysis/summary` | `nodes:read` | 分组 |
| GET | `/api/v1/analysis/hosts/{name}` | `nodes:read` | 节点 |

### 4.6 告警（26 条）

> 规则类资源当前为**全局配置**（不设范围）；若后续规则支持绑定分组，需补充分组维度过滤。

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/alerts` | `alerts:read` | 分组 |
| GET | `/api/v1/alerts/stats` | `alerts:read` | 分组 |
| GET | `/api/v1/alerts/acks` | `alerts:read` | 分组 |
| POST | `/api/v1/alerts/ack` | `alerts:write` | 分组 |
| POST | `/api/v1/alerts/close` | `alerts:write`（D4） | 分组 |
| POST | `/api/v1/alerts/reopen` | `alerts:write`（D4） | 分组 |
| POST | `/api/v1/alerts/comment` | `alerts:write`（D4） | 分组 |
| POST | `/api/v1/alerts/test` | `notify:write`（本次变更） | — |
| GET | `/api/v1/rules` | `alerts:read` | — |
| POST | `/api/v1/rules` | `alerts:write` | — |
| PUT | `/api/v1/rules/{id}` | `alerts:write` | — |
| DELETE | `/api/v1/rules/{id}` | `alerts:write` | — |
| POST | `/api/v1/rules/{id}/toggle` | `alerts:write` | — |
| POST | `/api/v1/rules/{id}/toggle-silence` | `silence:write` | — |
| GET | `/api/v1/rules/export` | `alerts:read` | — |
| POST | `/api/v1/rules/import` | `alerts:write` | — |
| GET | `/api/v1/rules/templates` | `alerts:read` | — |
| GET | `/api/v1/inhibit` | `alerts:read` | — |
| PUT | `/api/v1/inhibit` | `alerts:write` | — |
| GET | `/api/v1/grouping` | `alerts:read` | — |
| PUT | `/api/v1/grouping` | `alerts:write` | — |
| GET | `/api/v1/alert-pipeline` | `alerts:read` | — |
| PUT | `/api/v1/alert-pipeline` | `alerts:write` | — |
| POST | `/api/v1/alert-pipeline/preview` | `alerts:read` | — |
| GET | `/api/v1/maintenance` | `silence:read` | — |
| PUT | `/api/v1/maintenance` | `silence:write` | — |
| GET | `/api/v1/notify` | `notify:read` | — |
| PUT | `/api/v1/notify` | `notify:write`（高风险） | — |
| POST | `/api/v1/notify/test` | `notify:write` | — |

> **变更标注**：上表 `POST /api/v1/alerts/test` 一行非 1.25.0 基线——该路由在基线时**没有权限点**（任何登录用户可调用），本次（2026-09 资源范围与权限边界修复）改为 `notify:write`；表中其余行仍为 1.25.0 基线快照。

### 4.7 安全中心与审计（9 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/security/summary` | `security:read` | 分组 |
| GET | `/api/v1/security/events` | `security:read` | 分组 |
| GET | `/api/v1/security/baselines` | `security:read` | 分组 |
| GET | `/api/v1/security/defense/status` | `security:read` | 分组 |
| GET | `/api/v1/security/defense/status/{node}` | `security:read` | 节点 |
| POST | `/api/v1/security/defense/{node}/{action}` | `security:write`（高风险） | 节点 |
| GET | `/api/v1/security/defense/tasks` | `security:read` | 分组 |
| GET | `/api/v1/security/defense/tasks/{node}` | `security:read` | 节点 |
| GET | `/api/v1/audit/events` | `audit:read`（兼容：携带 `format=csv` 时额外要求 `audit:export`） | — |
| GET | `/api/v1/audit/export` | `audit:export`（高风险，批次 E 新增路由） | — |

### 4.8 拨测与巡检报告（8 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/dialtest/tasks` | `probe:read` | — |
| POST | `/api/v1/dialtest/tasks` | `probe:write` | — |
| PUT | `/api/v1/dialtest/tasks/{id}` | `probe:write` | — |
| DELETE | `/api/v1/dialtest/tasks/{id}` | `probe:write` | — |
| GET | `/api/v1/dialtest/latest` | `probe:read` | — |
| POST | `/api/v1/report/generate` | `report:export` | 分组 |
| GET | `/api/v1/report/download` | `report:export` | 分组 |
| GET | `/api/v1/report/history` | `report:read` | 分组 |

### 4.9 升级、配置与展示（22 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/api/v1/version` | — | — |
| GET | `/api/v1/self/status` | `dashboard:read`（F3 新增：运维只读概览类，与「查看概览」同级） | — |
| GET | `/api/v1/system/retention` | `system:config`（A3：数据保留属系统配置，与品牌 / 大屏 / 地理库同级） | — |
| PUT | `/api/v1/system/retention` | `system:config`（A3） | — |
| POST | `/api/v1/system/retention/cleanup` | `system:config`（A3） | — |
| GET | `/api/v1/install-info` | `agent:secret:read`（高风险） | — |
| GET | `/api/v1/proxy/status` | `agent:read` | — |
| POST | `/api/v1/system/upgrade/upload` | `system:upgrade`（高风险） | — |
| GET | `/api/v1/system/upgrade/current` | `system:upgrade`（高风险） | — |
| POST | `/api/v1/system/upgrade/apply` | `system:upgrade`（高风险） | — |
| GET | `/api/v1/system/upgrade/history` | `system:upgrade`（高风险） | — |
| GET | `/api/v1/system/upgrade/archive` | `system:upgrade`（高风险） | — |
| POST | `/api/v1/system/upgrade/rollback-to` | `system:upgrade`（高风险） | — |
| GET | `/api/v1/system/geoip` | `system:config`（新增） | — |
| POST | `/api/v1/system/geoip/upload` | `system:config`（新增） | — |
| POST | `/api/v1/system/geoip/reset` | `system:config`（新增） | — |
| GET | `/api/v1/system/geoip/test` | `system:config`（新增） | — |
| GET | `/api/v1/ui/settings` | 公开（匿名只读） | — |
| PUT | `/api/v1/ui/settings` | `system:config`（新增） | — |
| GET | `/api/v1/screen/config` | `system:config`（新增） | — |
| PUT | `/api/v1/screen/config` | `system:config`（新增） | — |
| GET | `/api/v1/dashboards` | `dashboard:read` | — |
| POST | `/api/v1/dashboards` | `dashboard:write`（新增） | — |
| GET | `/api/v1/dashboards/{id}` | `dashboard:read` | — |
| PUT | `/api/v1/dashboards/{id}` | `dashboard:write`（新增） | — |
| DELETE | `/api/v1/dashboards/{id}` | `dashboard:write`（新增） | — |

### 4.10 认证与权限（19 条）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| POST | `/api/v1/logout` | —（自身） | — |
| GET | `/api/v1/auth-info` | — | — |
| POST | `/api/v1/auth/change-password` | —（自身） | — |
| GET | `/api/v1/auth/me` | —（自身） | — |
| PUT | `/api/v1/auth/me` | —（自身） | — |
| GET | `/api/v1/users` | `users:manage`（高风险，**已生效**） | — |
| POST | `/api/v1/users` | `users:manage` | — |
| GET | `/api/v1/users/{username}` | `users:manage` | — |
| PUT | `/api/v1/users/{username}` | `users:manage` | — |
| DELETE | `/api/v1/users/{username}` | `users:manage` | — |
| POST | `/api/v1/users/{username}/reset-password` | `users:manage` | — |
| POST | `/api/v1/users/{username}/disable` | `users:manage` | — |
| POST | `/api/v1/users/{username}/enable` | `users:manage` | — |
| GET | `/api/v1/roles` | `roles:read`（**已生效**） | — |
| POST | `/api/v1/roles` | `roles:manage`（**已生效**） | — |
| GET | `/api/v1/roles/{name}` | `roles:read` | — |
| PUT | `/api/v1/roles/{name}` | `roles:manage` | — |
| DELETE | `/api/v1/roles/{name}` | `roles:manage` | — |
| GET | `/api/v1/permissions/catalog` | `roles:read` | — |

### 4.11 WebSocket（2 条用法 / 1 条路由）

| 方法 | 路径 | 权限点 | 范围 |
|---|---|---|---|
| GET | `/ws?topic=metrics&node=` | `nodes:read` | 节点（**当前缺失**，见 8.2） |
| GET | `/ws?topic=alerts` | `alerts:read` | 分组（**当前缺失**） |

## 5. 兼容策略（实施前必须逐条确认，否则升级即锁死）

| # | 场景 | 处理 |
|---|---|---|
| 1 | `auth.enabled=false` | 全放行，行为与旧版一致（`AuthMiddleware` 已实现，勿改） |
| 2 | 启用认证但 `users.yaml` 缺失、由 `server.yaml` 的 `auth.username/password` 迁移 | `MigrateSingleAdmin` 生成的账号按**超级管理员**处理，拥有全部权限点 |
| 3 | `Principal(r) == nil`（未登录） | 403，但公开白名单不受影响 |
| 4 | 新增权限点后，已有自定义角色 | 不自动授予（保持既有权限集合），需管理员显式勾选 |
| 5 | 新增权限点后，**内置角色** | 必须在 `auth.BuiltinRoles()` 同步补齐（至少超级管理员、运维管理员），否则新权限无角色可用 |
| 6 | 旧版 `users.yaml` 中不存在的权限点 key | `AllPermissionKeys` 校验时忽略，不影响登录 |
| 7 | WebSocket | 保持走 Cookie 兜底（已在 `AuthMiddleware` 实现），不得加入公开白名单 |

## 6. 资源范围策略

| 资源形态 | 工具 | 说明 |
|---|---|---|
| 节点列表 / 主机列表 | `auth.FilterByGroup[T]` | `groupOf` 取节点所属分组 |
| 单节点详情 / 单节点操作 | `Principal.CanAccessGroup(group)` | 不允许时返回 403（不是 404，避免掩盖资源存在性） |
| 批量节点操作（批量升级） | `auth.CheckBatchGroups` | 返回被拒分组列表，整体拒绝并回报 |
| 中间件实例 | `auth.FilterByGroup[T]` | 先由实例名解析所属节点，再取分组 |
| 概览类聚合计数 | 过滤后再聚合 | 否则泄露不可见分组的实例数 |
| 分组列表本身 | `auth.FilterGroups` | 受限用户只看到自己范围内的分组 |
| 统一原则 | — | 各 handler **不得自行实现**范围判断，一律调用 `auth` 包工具（`internal/server/auth/policy.go`） |

## 7. 分批实施顺序

| 批次 | 范围 | 路由数 | 前置 |
|---|---|---|---|
| **A｜基础设施** | 新增 `dashboard:write`、`system:config` 两个权限点并补齐内置角色；新增 `requirePerm(next, perm)` 统一包装（含 403 响应体与 `RecordPermissionDenied` 审计）；补单元测试骨架 | 0 | 无 |
| **B｜主机与指标** | `nodes/*`、`groups/*`、`query/*`、`processes`、`metrics/*`、`analysis/*` + **`/ws` topic 授权与范围校验** | 23 | A |
| **C｜中间件** | `middleware/*` 全部 13 条 + 概览聚合口径修正 | 13 | A |
| **D｜告警与通知** | `alerts/*`、`rules/*`、`inhibit`、`grouping`、`alert-pipeline`、`maintenance`、`notify` | 26 | A |
| **E｜安全、系统与其余** | `security/*`、`audit/*`（含新增的 `audit/export`）、`system/*`、`ui/*`、`screen/*`、`dashboards/*`、`install-info`、`proxy/status`、`dialtest/*`、`report/*` | 38 | A |

每批交付物：
1. 路由挂载 `requirePerm`（已有 `authz` 的 16 条保持不动）；
2. 「无权限 → 403」单测（逐路由表驱动）；
3. 「越范围 → 过滤 / 403」单测（至少覆盖列表类与单节点类各一条）；
4. `auth.enabled=false` 回归（全部 200）；
5. 前端 `meta.perm` 与菜单 `perm` 同步（避免用户看不到菜单却能调接口）。

## 8. 开放问题与风险

### 8.1 `globalAuthStore` 包级单例 —— 已在批次 A 解决
原 `internal/server/api/auth.go` 的 `globalAuthStore` / `SetAuthStore` / `authStoreFromContext` 是包级单例，测试之间会互相污染，且与 **A1（Server 高可用）** 议题直接冲突（多实例下 Principal 解析依赖实例内状态）。
批次 A 已改为 `AuthMiddleware(next, cfg, *auth.Store)` 显式传参（`cmd/server/main.go` 同步调整），包级单例已移除，A1 的实施障碍相应减少。

### 8.2 WebSocket 授权缺口 —— 已在批次 B 解决
`/ws` 的**认证**原本已由 Cookie 兜底完成，但缺少 topic 级授权与范围校验：任何已登录用户都能 `GET /ws?topic=metrics&node=<任意节点>` 订阅任意节点实时指标，绕过节点分组资源范围。
批次 B 已改为由 `API.RegisterWS` 注册、经 `wsAuthorize` 在握手前完成「topic 合法性 + 权限点 + 节点归属分组」三重校验（未知 topic 直接 400，不再存在「登录即可订阅任意数据」的面）。

### 8.3 同一路由承载「查看」与「导出」 —— 已在批次 E 解决
- `GET /api/v1/audit/events`：带导出参数时语义为导出（`audit:export`，高风险）。
- `GET /api/v1/report/download`、`GET /api/v1/metrics/export`：GET 但语义为导出。
需在批次 A 明确约定：**按参数在 handler 内二次判断权限**，或**拆分为独立路由**。倾向于后者（路由即契约，便于审计与测试），但会改变 API 面，需你确认。

### 8.4 方法 ≠ 权限等级
`POST /api/v1/alert-pipeline/preview` 为只读试算，权限点为 `alerts:read`；`POST /api/v1/notify/test` 会对外发信，权限点为 `notify:write`（高风险）。映射表以**语义**而非 HTTP 方法为准，实施时不要机械按方法推导。

### 8.5 大屏与品牌配置的可见性
本设计将 `GET /api/v1/screen/config` 归入 `system:config`（配置类）。若希望普通登录用户也能读取大屏配置，可降为 `dashboard:read`；`GET /api/v1/ui/settings` 已由白名单匿名可读，保持不变。

### 8.6 前端守卫覆盖面
`web/src/router/index.js` 目前仅 `system/users`、`system/roles` 两条路由带 `meta.perm`。随各批次上线需同步补齐，否则出现「菜单可见、点击 403」的体验问题。

### 8.7 不影响面
`/api/v1/report`、`/api/v1/agent/check`、`/install/*`、`/bin/*` 面向 Agent，走 `X-Agent-Secret`，不纳入本次授权改造。

### 8.8 聚合类数值未按资源范围过滤（已知限制，批次 C 遗留）

- `/api/v1/middleware/overview` 的 Summary 卡片数值来自跨节点聚合查询（`mwAggregateLatest`）；
- `/api/v1/middleware/nginx/access/summary` 的全局统计（总请求数 / 状态码分布 / Top URI / Top IP）来自 `nginxaccess.Window` 的窗口聚合。

两者都**不携带节点维度**，因此未按资源范围过滤：受限用户可看到全局聚合值，但看不到范围外节点 / 实例的明细。
如需彻底隔离，需为这两类聚合引入按节点分组的 label matcher（改动面较大，建议批次 E 完成后按需评估）。

## 9. 验收标准

| 项 | 标准 |
|---|---|
| 功能 | 表中每条非公开、非「自身」路由：无权限 → 403；有权限 → 200；越范围 → 过滤后返回或 403 |
| 兼容 | `auth.enabled=false` 时全部路由 200，与改造前逐条一致 |
| 兼容 | 单管理员迁移场景下全部路由 200 |
| 测试 | `go test ./internal/server/api/... ./internal/server/auth/...` 全绿；新增测试覆盖率：表内路由 100% 有对应用例 |
| 审计 | 403 均产生权限拒绝审计记录（复用 `RecordPermissionDenied`） |
| 前端 | 各批同步补齐 `meta.perm`，无「可见但 403」入口 |

---

## 附：统计

| 项 | 数量 |
|---|---|
| 路由总数 | **136**（批次 E 新增 `GET /api/v1/audit/export`；F3 新增 `/healthz`、`/readyz`、`GET /api/v1/self/status`；D4 新增 3 条告警处置接口；A3 新增 3 条数据保留接口） |
| 公开接口 | 9（`login`、`report`、`agent/check`、`/healthz`、`/readyz`、`install/*`、`bin/*`、`/`、`GET ui/settings`） |
| 无需权限点（登录即可 / 自身资源） | 6（`version`、`auth-info`、`logout`、`auth/me` ×2、`change-password`） |
| 已生效（现状 `authz`） | 14（`users:manage` 8 + `roles:manage` 3 + `roles:read` 3） |
| **需校验项** | **107** = 136 − 9 − 6 − 14（B1 实施覆盖 100 条；F3 新增 1 条 `dashboard:read`；D4 新增 3 条 `alerts:write`；A3 新增 3 条 `system:config`） |
| 新增权限点 | 2（`dashboard:write`、`system:config`） |
| 高风险权限点 | 8（现状） |

分批校验：23（B）+ 13（C）+ 26（D）+ 38（E，含新增的 audit/export）= 100 ✓

**五个子批次已全部完成**（A 基础设施、B 主机与指标、C 中间件、D 告警与通知、E 安全系统与其余），即路线图 `docs/refactor-plan.md` 第 4 节「批次二 → B1（实施）」已交付。

### 残留事项

- 8.6（前端守卫）与 8.8（聚合口径）为**已知限制**，非阻塞；
- 8.5 大屏与品牌配置的权限归属（`system:config`）如与预期不符可单独调整；
- 后续新增路由必须同步挂 `permit` / `permitNode`，并在本表登记。
