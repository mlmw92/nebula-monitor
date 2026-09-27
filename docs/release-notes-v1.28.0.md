# nebula-monitor v1.28.0 — 2026-09 节点分组资源范围与服务生命周期加固

本版本收口两件事：**把节点分组资源范围真正落到指标读取链路上**（此前部分接口可绕过），以及**加固 Server 的 HTTP 生命周期**（超时、优雅停机、WebSocket 连接回收）。**升级前请完整阅读下方「重要行为变化」。**

本版本改动集中在 Server 与 Web，**Agent 无需升级**（上一版 1.27.1 的 Agent 侧改动不受影响）。

## 重要行为变化（升级前必读）

- **受限用户只能看到授权节点分组内的指标**：指标浏览、主机列表、历史导出（CSV）、指标「在线状态」探测、`/nodes/latest` 聚合与实时推送（`/ws?topic=metrics`）现在统一按授权节点集合裁剪结果。行为要点：范围外与未注册（TSDB 有数据但已在 Server 注销）节点不再返回任何序列；显式指定范围外节点返回 403 并写入授权拒绝审计；不指定节点的跨节点查询仍可用，但只返回授权节点序列；受限用户没有任何授权分组时不再发起时序库查询，直接返回空结果。**若既有部署把「受限范围」当成仅隐藏菜单用，请检查这些用户实际应有的指标可见范围。**
- **矛盾的节点查询参数改为拒绝**：`?node=` 与 `?labels.node=`（或导出接口的 `labels=node=...`）、`?hostname=` 与 `?node=` 同时出现且取值不一致时返回 400。此前以某一参数优先、另一个被静默忽略——同一请求「授权校验看到的节点」与「实际查询的节点」可能不同，属本次修复的越权面。
- **查询参数形式的未注册节点返回 403**：进程 / 监听端口 / 防火墙（`?hostname=`）、实时推送（`/ws?topic=metrics&node=`）、告警列表（`?node=`、`?hostname=`）在受限身份下指定未注册节点现在一律 403 并记审计。此前可能返回 200 空结果，而「200 空结果 vs 403」本身可被用来探测某个节点名是否已注册。路径参数路由（如 `/api/v1/nodes/{name}`、`/api/v1/analysis/hosts/{name}`）保持原有语义（不存在时 404），不受影响。
- **触发测试告警改为需要 `notify:write`**：`POST /api/v1/alerts/test`（告警页「测试事件」按钮）会真实写入事件并向通知渠道发消息，此前该路由未挂权限点、任何登录用户均可调用，现改用 `notify:write`（而非 `alerts:write`，避免「能改规则就能触发对外通知」）。升级后**所有不含 `notify:write` 的角色**（含内置「运维管理员」「安全管理员」，以及此前仅授予 `alerts:write` 的自定义角色）将不再显示该按钮、直接调用接口返回 403。**运维动作**：如需保留该能力，为该角色补 `notify:write`；内置「告警管理员」与超级管理员已含该权限，不受影响。
- **WebSocket 写阻塞的客户端会被回收**：实时推送中写阻塞超过 10 秒的客户端（典型是「已连接但长期不读取」，如断网后残留的连接、只连不消费数据的脚本）现在会被服务端主动断开并回收，此前这类连接会永久占用连接并维持每秒一次的指标查询。正常浏览器订阅不受影响；若升级后经反向代理访问时实时指标频繁断开，请检查代理是否让后端写长期阻塞（读缓冲/超时配置）。
- **停机行为**：Server 收到 SIGINT/SIGTERM 后，会先断开 WebSocket、再给在途 HTTP 请求最多 10 秒完成，随后取消后台任务并退出；在宽限期内再按一次信号则立即终止。此前进程会立即退出，可能截断在途响应（包括「系统升级」接口自身的响应）。同时新增 `ReadHeaderTimeout` 5 秒、`IdleTimeout` 60 秒。

## 变更清单

| # | 变更 | 位置 |
|---|---|---|
| 1 | 指标读取统一按授权节点分组裁剪（范围查询、即时查询、导出、活跃状态、`nodes/latest`） | `server/api/metric_scope.go`、`query.go`、`export.go`、`catalog.go` |
| 2 | `node` / `labels.node` 冲突拒绝，标签不再可覆盖查询目标 | `server/api/metric_scope.go` |
| 3 | 以 `hostname` 为目标的接口改用统一目标校验（进程、监听端口、防火墙） | `server/api/auth.go`、`query.go` |
| 4 | 实时推送的查询参数目标改用 fail-closed 校验 | `server/api/ws.go` |
| 5 | 测试告警改为需要 `notify:write`；前端按钮按权限显示 | `server/api/query.go`、`web/src/components/AlertsView.vue` |
| 6 | Hub 可幂等断开连接；单客户端断线与写阻塞客户端不再残留推送协程与查询 | `server/api/ws.go` |
| 7 | HTTP 读头/空闲超时与有界优雅退出（先断 WS → 排空 → 取消后台 → 关存储） | `cmd/server/http_lifecycle.go`、`main.go` |
| 8 | CI 增加 `internal/server/api` 的 `-race` 步骤（覆盖 WS/Hub 并发路径） | `.github/workflows/ci.yml` |
| 9 | 文档同步：README 路线图与权限章节、升级公告、权限矩阵、领域术语表 | `README.md`、`CONTEXT.md`、`docs/permission-matrix.md` |

## 测试与验证

- `go test -count=1 ./...` 全部通过；`go vet ./...` 干净
- 前端 `npm test`：4 个测试文件 / 20 个用例通过；`npm run build` 成功
- 新增回归测试：指标范围与参数冲突（`metric_scope_test.go`）、hostname 目标与告警查询目标（`scope_hostname_test.go`）、Hub 关闭与断线回收（`ws_shutdown_test.go`）、HTTP 生命周期（`http_lifecycle_test.go`）、前端按钮可见性与指标跨节点查询
- 并发路径由 CI 的 `go test -race ./internal/server/api/...` 覆盖（本机若无 cgo 无法运行 `-race`）

## 升级顺序

1. **先升级 Server**（Web 端「系统升级」上传 upgrade 包，或脚本方式）。
2. Web 静态资源随 Server 升级包一并替换，无需单独操作。
3. **Agent 无需升级**：本版本未改动 Agent 采集与上报逻辑。
4. 升级后如需核对权限边界，可用一个受限范围用户登录，确认只能看到其授权分组内的主机与指标。

## 未包含（后续跟进）

- `WebSocket topic=alerts` 的告警广播尚未按客户端资源范围过滤：受限用户可能在实时通道收到范围外节点的告警内容（REST 侧 `/api/v1/alerts` 已过滤）。修法需在 Hub 内按订阅者范围过滤，另行设计。
- `DELETE /api/v1/nodes/{name}`、`/api/v1/security/defense/status|tasks/{node}` 对未注册节点仍返回 200，持有相应权限的受限用户可借此区分「节点不存在」与「属于其他分组」（无指标数据泄露）。
- 停机宽限期（10 秒）与 WS 写截止时间（10 秒）当前为固定值，后续可考虑做成配置项。
