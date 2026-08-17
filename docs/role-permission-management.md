# 角色权限管理（RBAC）设计文档

> 适用范围：Nebula Monitor Server（单机 / `standalone` 模式）+ Web 管理台。
> 本文档为设计与实施规格，供后端、前端、测试与运维依据使用。
> 面向终端用户的操作说明见 `README.md` 的「角色权限管理」章节。

---

## 1. 背景与目标

### 1.1 现状

- **认证**：`internal/server/api/auth.go` 提供单账号登录。配置位于 `server.yaml` 的 `auth.username` / `auth.password`（密码使用 `internal/server/crypto` 的带盐哈希，明文不落盘）。
- **会话**：登录成功返回 24 小时无状态 HMAC Token；通过 `Cookie`（`nebula_monitor_session`）或 `Authorization: Bearer` 传递；`AuthMiddleware` 校验签名与过期。
- **当前 Token 载荷**：`base64(username:exp).sig`，**不包含角色、范围、会话版本**，因此无法做即时失效与细粒度授权。
- **审计**：`internal/server/audit` 记录登录与管理写请求，支持按用户/路径/分类筛选与 CSV 导出；敏感请求体不直接记录。
- **节点分组**：`Node.Group` 字段已存在，节点可按分组聚合，为「资源范围」控制提供基础。
- **高风险入口**：系统升级、Agent 安装信息（含长期密钥）、Agent 升级、fail2ban、通知密钥、用户/角色变更、审计导出等接口已存在，但仅有「是否开启 Server 登录」这一层保护，无角色与范围区分。

### 1.2 目标

将单一管理员账号扩展为 **用户 + 角色 + 权限点 + 资源范围** 四层模型：

1. 用户登录后只能访问被授权的功能。
2. 可按「节点分组」限制主机、告警、安全事件与运维操作的可见/可操作范围。
3. 高风险操作需专属权限、二次确认并写入审计。
4. 兼容现有单机部署与登录认证机制；首次升级自动迁移首个管理员，关闭认证时维持现有兼容行为。

### 1.3 非目标（本期不做）

数据库持久化、LDAP/OIDC、多 Server 共享会话、ABAC、临时授权 / 审批流、组织 / 租户隔离。

---

## 2. 术语

| 术语 | 说明 |
| --- | --- |
| 用户（User） | 可登录的主体，绑定一个或多个角色，拥有资源范围。 |
| 角色（Role） | 权限点 + 资源范围的集合；分内置角色与自定义角色。 |
| 权限点（Permission） | 形如 `domain:action` 的细粒度能力标识，如 `nodes:read`、`system:upgrade`。 |
| 资源范围（Scope） | 允许访问的节点分组 ID 集合；`global` 表示全部资源。 |
| 会话版本（TokenVersion） | 用户级计数器；Token 携带，校验时比对，用于即时失效。 |
| 内置角色（Builtin Role） | 系统预置、不可删除、权限固定的角色。 |

---

## 3. 总体架构

### 3.1 请求处理链

```
HTTP 请求
  │
  ▼
AuthMiddleware          # 校验 Token 签名/过期；写入 *AuthenticatedUser（含角色、权限、范围、TokenVersion）
  │  (authCfg.Enabled=false 时直接放行，兼容无认证模式)
  ▼
AuthorizationMiddleware # 按路由权限映射校验权限点；返回 403
  │
  ▼
资源范围过滤            # 列表/详情/批量操作按 user.Scope 过滤节点分组
  │
  ▼
业务 Handler            # 只声明所需权限，调用范围过滤辅助函数
  │
  ▼
audit.Store             # 记录管理写操作、权限拒绝、高风险操作
```

### 3.2 模块划分

| 模块 | 文件 | 职责 |
| --- | --- | --- |
| 模型 | `internal/server/auth/model.go` | User / Role / Permission 类型与校验规则。 |
| 存储 | `internal/server/auth/store.go` | 加载、原子保存、缓存、备份、迁移。 |
| 策略 | `internal/server/auth/policy.go` | 权限集合判断、节点/分组范围判断、批量过滤、高风险规则。 |
| 迁移 | `internal/server/auth/migration.go` | 单管理员 → 首个超级管理员的幂等迁移。 |
| 认证扩展 | `internal/server/api/auth.go` | 扩展 Token 载荷、用户状态与 TokenVersion 校验。 |
| 审计扩展 | `internal/server/api/audit.go` | 补充权限拒绝 / 用户角色变更 / 高风险操作审计。 |
| 授权接口 | `internal/server/api/permission_api.go` | 用户、角色、权限目录、会话管理接口。 |
| 路由绑定 | `internal/server/api/*` 路由注册 | 为现有接口绑定权限点与范围校验。 |

**设计原则（深度 / 局部性）**：当前各 handler 直接调用 `AuthenticatedUser(r)` 且无角色判断，属于浅层透传。本方案将其深化为一个 `auth` 包——小接口（`HasPermission`、`CanAccessNode`、`FilterNodes`）封装权限展开、范围匹配与缓存刷新，使业务 handler 不再理解角色内部结构，权限逻辑集中在单一模块，便于测试与演进。

---

## 4. 数据模型

### 4.1 User

```go
type User struct {
    Username     string    `yaml:"username" json:"username"`
    DisplayName  string    `yaml:"display_name" json:"display_name"`
    PasswordHash string    `yaml:"password_hash" json:"-"` // 复用 crypto.HashPassword / VerifyPassword
    Roles        []string  `yaml:"roles" json:"roles"`
    Scope        Scope     `yaml:"scope" json:"scope"`
    Status       string    `yaml:"status" json:"status"` // "enabled" | "disabled"
    TokenVersion int64     `yaml:"token_version" json:"token_version"` // 失效控制
    CreatedBy    string    `yaml:"created_by" json:"created_by"`
    CreatedAt    time.Time `yaml:"created_at" json:"created_at"`
    LastLoginAt  *time.Time `yaml:"last_login_at,omitempty" json:"last_login_at,omitempty"`
    LastLoginIP  string    `yaml:"last_login_ip,omitempty" json:"last_login_ip,omitempty"`
}
```

### 4.2 Scope（资源范围）

```go
type Scope struct {
    Mode   string   `yaml:"mode" json:"mode"`   // "global" | "restricted"
    Groups []string `yaml:"groups" json:"groups"` // 允许访问的分组 ID；Mode=restricted 时生效
}
```

**关键语义**：`Mode=restricted` 且 `Groups` 为空 ⇒ **无任何资源权限**，绝不扩大为全部资源。

### 4.3 Role

```go
type Role struct {
    Name        string   `yaml:"name" json:"name"`
    Builtin     bool     `yaml:"builtin" json:"builtin"`
    Description string   `yaml:"description" json:"description"`
    Permissions []string `yaml:"permissions" json:"permissions"`
    ScopeMode   string   `yaml:"scope_mode" json:"scope_mode"`     // "global" | "restricted"
    ScopeGroups []string `yaml:"scope_groups" json:"scope_groups"` // 该角色默认范围（用户可继承/收窄）
    CreatedBy   string   `yaml:"created_by,omitempty" json:"created_by,omitempty"`
}
```

### 4.4 校验规则

- 用户名：3–32 位，`^[a-zA-Z0-9_.-]+$`，唯一。
- 密码：至少 8 位，含大小写 + 数字（或特殊字符）；创建 / 重置走服务端哈希，明文不落盘、不进审计。
- 用户至少绑定一个角色；启用用户才允许登录。
- 自定义角色不能授予超出创建者自身范围的节点范围；仅超级管理员可创建 `global` 角色。
- 不能删除 / 禁用最后一个启用的超级管理员。
- 不能移除自身最后一个权限管理能力（避免锁死）。
- 内置角色不可删除、权限不可改写（可复制为自定义角色再调整）。

---

## 5. 认证演进

### 5.1 Token 载荷扩展

```
base64(username:exp:tokenVersion).sig
```

- `tokenVersion` 取自用户当前 `TokenVersion`。
- 校验时除签名 / 过期外，额外比对 `user.TokenVersion == token.tokenVersion`；不一致 ⇒ 401（会话已失效）。
- `AuthenticatedUser` 由返回 `string` 改为返回 `*auth.Principal`（含 Username、Roles、Permissions、Scope、TokenVersion），供授权层使用。

### 5.2 会话失效控制

| 触发动作 | 处理 |
| --- | --- |
| 管理员禁用用户 | 用户 `TokenVersion++`；后续请求 401。 |
| 用户修改密码 | 自己 `TokenVersion++`；其他会话失效（强制重新登录）。 |
| 主动注销会话 | 当前单机无集中会话表，采用「注销 = 使当前 TokenVersion 失效」语义（即等效于改密后的失效）。 |
| 删除 / 禁用自己 | 禁止（需其他管理员操作）。 |

> 说明：无状态 Token 本身无法「精准踢掉某一个历史 Token」。本期以「用户级 TokenVersion」实现用户维度即时失效，满足绝大多数运维场景；精细到单设备的会话吊销留待后续「集中会话管理」。

### 5.3 用户状态与限流

- 禁用用户即使 Token 未过期，校验直接失败。
- 复用现有按源 IP 的登录失败限流；新增按用户名的失败计数，锁定后可配置时长。

### 5.4 持久化与迁移

- 新增独立数据文件 `users.yaml`（与 `server.yaml` 解耦，避免破坏现有告警 / 节点配置）。
- 写入采用「临时文件 + 备份 + 原子替换」（`os.Rename`），写前校验、写后刷新内存缓存。
- `auth.migration`：启动时若 `users.yaml` 不存在且 `auth.username` 已配置 ⇒ 创建首个超级管理员（用户名 / 密码哈希取自 `server.yaml`），并写回 `users.yaml`；**幂等**，重复启动不重复创建。
- 迁移后 `server.yaml` 的 `auth.username/password` 保留为「种子来源」，但运行态以 `users.yaml` 为准；提供 `migrate` 开关可关闭自动迁移以回滚。

### 5.5 认证关闭兼容

- `auth.enabled=false` 时维持现有行为：所有接口放行，`Principal` 退化为内置「匿名超级用户」语义（与现状一致），但 Web 端隐藏「用户与权限」入口并提示生产风险。
- `auth.enabled=true` 时，所有非公开接口必须经过认证 + 授权。

---

## 6. 授权 Seam

### 6.1 路由权限映射

集中维护一张 `routePerms` 表：

```go
type routePerm struct {
    Method   string
    Pattern  string // 支持路径参数占位，如 /api/v1/nodes/{id}
    Perm     string // 所需权限点
    ScopeRes bool   // 是否涉及节点资源范围
}
```

示例（节选）：

| 方法 | 路径 | 权限点 | 范围 |
| --- | --- | --- | --- |
| GET | `/api/v1/nodes` | `nodes:read` | 是 |
| POST | `/api/v1/nodes` | `nodes:write` | 否 |
| POST | `/api/v1/alert-rules` | `alerts:write` | 否 |
| POST | `/api/v1/notify-channels` | `notify:write` | 否 |
| GET | `/api/v1/agent/install-info` | `agent:secret:read` | 是 |
| POST | `/api/v1/agent/upgrade` | `agent:upgrade` | 是 |
| POST | `/api/v1/system/upgrade/upload` | `system:upgrade` | 否 |
| POST | `/api/v1/security/defense/*` | `security:write` | 是 |
| GET | `/api/v1/audit/events` | `audit:read` | 否 |
| GET | `/api/v1/audit/export` | `audit:export` | 否 |
| GET | `/api/v1/permissions/catalog` | `roles:read` | 否 |
| POST | `/api/v1/users` | `users:manage` | 否 |

### 6.2 中间件与辅助函数

- `AuthorizationMiddleware`：根据 `routePerms` 匹配当前路由，调用 `policy.HasPermission(principal, perm)`；无权限返回 `403`。
- `RequiresPermission(perm, scopeRes)`：handler 级包装器，供无法静态映射的动态路由使用。
- `policy.CanAccessNode(principal, group)`：判断某节点分组是否在范围内（`global` 放行；`restricted` 需命中 `Groups`）。
- `policy.FilterNodes(principal, nodes)`：列表接口服务端过滤，仅返回范围内节点。
- 批量操作（升级 / 删除）逐项调用 `CanAccessNode`，越权项返回部分失败明细。

### 6.3 高风险操作保护

以下操作除对应权限点外，还需：

1. 前端二次确认弹窗（展示影响范围）；
2. 后端在审计中记录操作人、目标资源、时间；
3. 涉及密钥 / 升级的接口额外记录「谁、对哪些节点」。

| 操作 | 权限点 | 审计分类 |
| --- | --- | --- |
| 系统升级上传 / 应用 | `system:upgrade` | `system` |
| Agent 安装信息（含密钥）查看 | `agent:secret:read` | `agent` |
| Agent 单 / 批量升级 | `agent:upgrade` | `agent` |
| fail2ban 启停 / 重载 | `security:write` | `security` |
| 通知密钥修改 | `notify:write` | `notify` |
| 用户 / 角色变更 | `users:manage` / `roles:manage` | `auth` |
| 审计导出 | `audit:export` | `audit` |

### 6.4 自定义角色范围约束

- 创建自定义角色时，`ScopeGroups` 必须是创建者自身 `Scope.Groups` 的子集（超级管理员除外）。
- 分配用户角色时，若用户已有多个角色，取其范围并集；任一角色为 `global` 则用户为 `global`。

---

## 7. 权限点清单

按业务域划分（动作：`read` / `write` / `export` / `manage` / `upgrade` / `secret:read`）：

| 域 | 权限点 |
| --- | --- |
| 仪表盘 / 概览 | `dashboard:read` |
| 主机 / 节点 | `nodes:read`、`nodes:write` |
| 节点分组 | `groups:read`、`groups:write` |
| 告警规则 | `alerts:read`、`alerts:write` |
| 通知渠道 | `notify:read`、`notify:write` |
| 静默 / 维护 | `silence:read`、`silence:write` |
| 拨测 | `probe:read`、`probe:write` |
| 报告 | `report:read`、`report:export` |
| 数据导出 | `metrics:export` |
| 中间件 | `middleware:read`、`middleware:write` |
| 安全中心 | `security:read`、`security:write` |
| Agent 运维 | `agent:read`、`agent:upgrade`、`agent:secret:read` |
| 系统升级 | `system:upgrade` |
| 审计 | `audit:read`、`audit:export` |
| 用户管理 | `users:manage` |
| 角色管理 | `roles:manage` |
| 权限目录 | `roles:read` |

---

## 8. 默认角色与权限矩阵

| 权限点 | 超级管理员 | 运维管理员 | 告警管理员 | 安全管理员 | 只读用户 | 审计用户 |
| --- | --- | --- | --- | --- | --- | --- |
| `dashboard:read` | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| `nodes:read` | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| `nodes:write` | ✓ | ✓ | — | — | — | — |
| `groups:read/write` | ✓ | ✓ | — | — | — | — |
| `alerts:read/write` | ✓ | — | ✓ | — | ✓(读) | — |
| `notify:read/write` | ✓ | — | ✓ | — | — | — |
| `silence:read/write` | ✓ | — | ✓ | — | — | — |
| `probe:read/write` | ✓ | ✓ | — | — | — | — |
| `report:read/export` | ✓ | ✓ | ✓ | — | ✓(读) | — |
| `metrics:export` | ✓ | ✓ | — | — | — | — |
| `middleware:read/write` | ✓ | ✓ | — | — | ✓(读) | — |
| `security:read/write` | ✓ | — | — | ✓ | — | — |
| `agent:read` | ✓ | ✓ | — | ✓ | — | — |
| `agent:upgrade` | ✓ | ✓ | — | — | — | — |
| `agent:secret:read` | ✓ | — | — | ✓ | — | — |
| `system:upgrade` | ✓ | — | — | — | — | — |
| `audit:read` | ✓ | — | — | ✓ | — | ✓ |
| `audit:export` | ✓ | — | — | ✓ | — | ✓ |
| `users:manage` | ✓ | — | — | — | — | — |
| `roles:manage` | ✓ | — | — | — | — | — |
| `roles:read` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

> 「资源范围」：内置角色除「只读 / 审计用户」可选 `restricted` 外，其余默认 `global`；自定义角色由创建者指定范围。

---

## 9. 节点分组范围实现

- 列表接口（`nodes`、`alerts`、`security/events` 等）在服务端调用 `FilterNodes` / 等效分组过滤，返回仅范围内数据。
- 详情 / 单节点写 / 删除接口在进入业务前调用 `CanAccessNode(node.Group)`；越权返回 `403`。
- 批量升级 / 删除：请求体携带节点 ID 列表，逐项校验，返回 `{succeeded:[], denied:[{id, reason}]}`。
- 前端不依赖隐藏菜单实现安全；菜单隐藏仅为体验，服务端为唯一权威。

---

## 10. API 契约

### 10.1 用户管理

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/users` | `users:manage` | 用户列表（含角色、范围、状态、最近登录）。 |
| POST | `/api/v1/users` | `users:manage` | 创建用户（密码服务端哈希）。 |
| GET | `/api/v1/users/{username}` | `users:manage` 或本人 | 详情。 |
| PUT | `/api/v1/users/{username}` | `users:manage` 或本人（受限字段） | 修改角色 / 范围 / 状态 / 显示名。 |
| POST | `/api/v1/users/{username}/reset-password` | `users:manage` 或本人 | 重置 / 修改密码（本人需旧密码）。 |
| POST | `/api/v1/users/{username}/disable` | `users:manage` | 禁用（TokenVersion++）。 |
| DELETE | `/api/v1/users/{username}` | `users:manage` | 删除（保护最后超级管理员）。 |

### 10.2 角色管理

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/roles` | `roles:read` | 角色列表（区分内置 / 自定义）。 |
| POST | `/api/v1/roles` | `roles:manage` | 创建自定义角色（范围受创建者约束）。 |
| GET | `/api/v1/roles/{name}` | `roles:read` | 详情（权限矩阵 + 范围 + 关联用户数）。 |
| PUT | `/api/v1/roles/{name}` | `roles:manage` | 修改自定义角色。 |
| DELETE | `/api/v1/roles/{name}` | `roles:manage` | 删除自定义角色（须无用户绑定）。 |

### 10.3 权限目录与当前身份

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/permissions/catalog` | `roles:read` | 全部权限点（按域分组）。 |
| GET | `/api/v1/auth/me` | 登录即可 | 返回当前 Principal（用户名、角色、权限、范围）。 |

### 10.4 会话管理

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/v1/sessions/me` | 登录即可 | 当前会话与最近登录信息。 |
| POST | `/api/v1/auth/logout` | 登录即可 | 注销（当前用户 TokenVersion++）。 |

### 10.5 错误码

| 状态码 | 含义 |
| --- | --- |
| 401 | 未登录 / Token 过期 / 会话已失效。 |
| 403 | 已登录但无权限点或超出资源范围。 |
| 409 | 用户名已存在 / 删除最后超级管理员 / 删除仍绑定用户的角色。 |
| 422 | 请求体校验失败（密码强度、范围超限等）。 |

---

## 11. 测试矩阵

### 11.1 单元

- `policy.HasPermission`：内置 / 自定义角色、权限并集、无权限。
- `policy.CanAccessNode`：`global` 放行、`restricted` 命中 / 未命中、空范围拒绝。
- `FilterNodes`：批量过滤正确性。
- `migration`：幂等（重复启动不重复创建）、从 `server.yaml` 取种子、缺少配置时行为。
- Token 校验：签名、过期、`TokenVersion` 不一致失效。
- 约束：最后超级管理员不可删 / 禁；自定义角色范围不超过创建者。

### 11.2 集成（复用 `internal/server/api/*_test.go`）

- 未授权访问受保护接口返回 403。
- 范围外节点列表不含越权节点；详情返回 403。
- 批量升级部分越权返回 `denied` 明细。
- 高风险操作写入对应审计分类。
- 禁用用户后旧 Token 401。
- 认证关闭时维持现状兼容。

---

## 12. 迁移与回滚

### 12.1 升级迁移

1. 部署新版本（含 `users.yaml` 读写与 `migration`）。
2. 首次启动自动把 `server.yaml` 的 `auth.username/password` 迁移为首个超级管理员。
3. 原管理员用原密码登录；建议立即创建运维 / 只读等角色并分配。

### 12.2 回滚

- 关闭 `migration` 开关并恢复 `server.yaml` 的 `auth` 配置即可回退到单管理员模式（运行态以 `server.yaml` 为准）。
- `users.yaml` 每次写入前自动备份（`.bak`），异常可手动恢复。

### 12.3 故障恢复

- `users.yaml` 损坏 / 缺失且 `auth.username` 存在 ⇒ 自动重建首个超级管理员（幂等）。
- `users.yaml` 损坏且无种子 ⇒ 启动告警，保留只读降级，需运维介入。

---

## 13. 安全边界

- 不记录密码、Token、Agent Secret、通知密钥、完整上传内容。
- 权限拒绝审计包含：用户、权限点、路径、资源标识、结果。
- 所有敏感接口服务端强制校验，前端隐藏仅为体验。
- 高风险操作二次确认 + 审计双重保护。
- 默认 `auth.enabled=false` 时 Web 明确提示生产环境风险。

---

## 14. 前端设计（页面与交互）

### 14.1 入口与守卫

- 新增「系统设置 → 用户与权限」一级入口（Sidebar 按 `roles:read` / `users:manage` 显示）。
- `router` 增加 `meta.perm` / `meta.scopeRes`；全局守卫未授权时跳登录或 403 页。
- `http.js` 已处理 401；新增 403 统一提示「暂无访问权限」。
- 新增 `auth store`：登录后拉取 `/auth/me`，缓存角色 / 权限 / 范围，供按钮级 `v-permission` 指令与菜单过滤使用。

### 14.2 页面结构（企业级管理台风格）

- **用户列表区**：用户名、显示名、角色标签、范围摘要、状态、最近登录、操作菜单（编辑 / 重置密码 / 禁用 / 删除）。
- **用户编辑抽屉**：基本信息、角色分配、节点范围树选择、启用状态、密码重置。
- **角色列表区**：内置 / 自定义区分，权限数量、范围、关联用户数。
- **角色编辑区**：按业务域折叠的权限矩阵（查看 / 创建 / 修改 / 删除 / 执行 / 导出）。
- **节点范围选择器**：分组树 + 节点数量摘要；明确「无范围 = 无资源权限」。
- **高风险确认弹窗**：系统升级、Agent 密钥、fail2ban、权限变更展示风险说明与影响范围。
- **会话管理区**：当前会话与最近登录，支持注销。
- **空 / 错误态**：无权限显示「暂无访问权限」；403 保留上下文并提供返回入口，不泄露资源存在性。

### 14.3 视觉规范

- 主色 `#2563EB` 系；背景 `#F5F7FA` / `#FFFFFF`；功能色 绿/黄/红/紫。
- 字体 PingFang SC；标题 20/600，正文 14/400。
- 克制微动效：列表淡入、抽屉滑入、风险色提示；权限标签与范围摘要降低复杂度。

---

## 15. 分阶段实施

### 阶段一（MVP，本期）

- `auth` 包（model/store/policy/migration）。
- `AuthMiddleware` 扩展 + `AuthorizationMiddleware` + 路由权限映射。
- 内置 6 角色、权限点、节点分组范围。
- 用户 / 角色 / 权限目录 / 会话管理 API。
- 高风险接口授权 + 审计。
- 前端用户 / 角色 / 范围 / 会话页面与守卫。
- 首个管理员迁移、备份回滚。

### 阶段二（后续）

- 数据库持久化、外部身份源（LDAP / OIDC）。
- 组织 / 租户隔离、按标签 / 主机属性授权（ABAC）。
- 审批式高风险操作、集中会话管理、临时授权。

---

## 16. 开放决策点

1. **TokenVersion 粒度**：本期用户级即时失效是否满足？是否需要单设备会话表？
2. **自定义角色默认范围**：新建自定义角色默认 `global` 还是继承创建者范围？（建议继承创建者范围，防越权放大。）
3. **审计用户可见性**：审计用户是否可查看其他用户操作记录？（建议仅可看审计，不可看业务数据。）
4. **迁移后 `server.yaml` 处理**：是否清空 `auth.username/password` 以避免混淆？（建议保留为种子但加注释，运行态以 `users.yaml` 为准。）

---

## 17. 实现状态（截至 2026-08-17）

已完成**后端 RBAC 核心与 API**（编译通过、单测通过、无回归）：

- `internal/server/auth/`：`model.go`（用户/角色/权限点/范围、6 内置角色、权限目录）、`policy.go`（权限展开、范围判断、批量过滤、高风险集合）、`store.go`（加载/原子写/备份/缓存/用户角色 CRUD/范围约束）、`migration.go`（单管理员幂等迁移）、`auth_test.go`（覆盖展开、范围、禁用、最后超管保护、迁移幂等、改密失效）。
- `internal/server/api/auth.go`：Token 载荷加入 `tokenVersion`；`AuthMiddleware` 解析 `Principal` 并校验启用/会话版本；登录/改密/注销走 `authStore`（单管理员模式保留兼容）；新增 `authz` 授权包装器。
- `internal/server/api/permission_api.go`：用户/角色/权限目录/当前身份接口（`/api/v1/auth/me`、`/api/v1/users`、`/api/v1/roles`、`/api/v1/permissions/catalog`）。
- `internal/server/config/config.go`：新增 `usersFile` 与 `migrateSingleAdmin`；`cmd/server/main.go`：启动构建 store、迁移、注入全局 `SetAuthStore` 与 API。
- `internal/server/api/audit.go`：新增 `RecordPermissionDenied`。

**尚未实现（后续阶段）**：

- 现有业务 handler（节点升级、Agent 密钥、fail2ban、通知、系统升级等）按权限点与节点范围的逐项服务端校验（当前仅权限管理接口受 `authz` 保护；其余高风险接口的保护需在各自 handler 内调用 `Principal(r)` 与 `policy.*` 过滤）。
- 前端「用户与权限」页面（设计稿见 `docs/permission-console-mock.html`）、菜单守卫与 `auth/me` 加载。
- 单设备会话表、审计用户对业务数据的可见性边界细化。

> 说明：范围约束规则（自定义角色范围不超过创建者）已在 `store.CreateRole` 实现；「资源范围为空=无资源权限」在 `policy.Scope.ContainsGroup` 体现。
