# 运维平台功能验证矩阵（增量审查）

日期：2026-10-05；代码基线：`VERSION` = `1.30.33`（本轮变更尚未发布）。

## 使用方式和边界

- 功能清单、现有实现状态及代码入口：[`功能全景表`](../superpowers/specs/2026-09-30-ops-platform-capability-map.md) §三。下面的编号 `领域-序号` 与该表**自上而下的行顺序**一一对应，共 14 域 96 行；即使是未实现项，也给出后续验收用例，不把它们写成当前缺陷。
- 下面每行是**可执行的代表性用例设计，不表示已经执行通过**。`A` = 适合以自动化用例验证（并非声称已有覆盖）；`L` = 需要 Linux 或真实依赖；`B` = 需要浏览器交互；`N` = 功能未完成，待立项后执行。不同用例的边界数据、异常、权限和持久化均需逐步补齐；不得把 `go test ./...` 等价成逐项端到端通过。
- 清单是功能入口级，不是每个按钮、每个中间件版本组合的穷举。认证、资源范围、故障重试、脱敏、重启恢复、升级回滚应对每个**可写**入口横向套用。不得在生产环境执行破坏性的升级/回滚、服务重启、告警轰炸或压测。

## 逐项用例设计

| ID | 功能点 | 输入/步骤与预期（断言） | 验证层 |
|---|---|---|---|
| 1-01 | 主机指标 | Agent 采样 CPU/内存/负载/磁盘/网络/进程，断网重连：标签与数值匹配；重连后无假在线。 | A+L |
| 1-02 | 15 类中间件 | 逐类用直连及 exporter 正例、凭据错误/超时负例：实例状态、角色与指标一致，不回显口令。 | A+L |
| 1-03 | 指标字典/模板 | 校验目录指标均有采集来源，规则方向与变差方向一致；表单选中可正确生成规则。 | A+B |
| 1-04 | HTTP/TCP/ICMP 拨测 | 正常/拒绝连接/证书过期/超时：状态、耗时、到期天数及恢复事件准确。 | A+L |
| 1-05 | 规则类型 | 阈值六比较符及主机/中间件离线、主从切换、安全事件各造一例：firing→resolved 去重。 | A+L |
| 1-06 | 告警治理 | 静默、维护、抑制、聚合/升级时间边界：事件留存而通知按命中条件静默。 | A+L |
| 1-07 | 五种通知渠道 | SMTP/Webhook/钉钉/飞书/企业微信成功、429/500/超时：失败可定位，凭据不写日志。 | A+L |
| 1-08 | 告警处置协作 | 认领→指派→评论→关闭→重开，权限拒绝与重启：状态和评论不丢、不重复。 | A+B |
| 1-09 | 告警关联资产变更 | N：告警可跳转资产及最近变更，范围外资产不可见；当前无闭环。 | N |
| 1-10 | 值班/外部通知 | N：按轮班路由并在无人接单时升级，节假日/时区不误派。 | N |
| 2-01 | 资产自动发现 | 同一主机/中间件/K8s 清单重复上报与乱序上报：无重影，截断不误删旧资产。 | A+L |
| 2-02 | 资产台账 | 新建、筛选、编辑人工值、清除与忽略恢复：采集值不丢，汇总与列表同口径。 | A+B |
| 2-03 | 资产关系/拓扑 | 人工关系优先；解除采集关系后重报不复活，恢复可见；拓扑与影响传播尚不完整。 | A+L |
| 2-04 | 变更历史 | 属性变更/不变/删除资产：仅真变化有 diff；历史仍可查，且与操作审计按**请求 id** 双向可查（2026-10-07）。 | A+B |
| 2-05 | 人工值优先 | 采集与人工值冲突时返回两来源并以人工值生效；重置后立即回退采集值。 | A |
| 2-06 | 生命周期 | N：上线→退役→回收状态转移、成本/维保到期提醒、审计与权限。 | N |
| 2-07 | 资源范围 | 角色仅可见 A 组节点：资产列表/总数/导出/详情均不能泄露 B 组。 | A+B |
| 2-08 | 快照与差异巡检 | 首次建基线不报差异；增加/改动/缺失/偏离各造一例；运行态字段不入报告。 | A+B |
| 2-09 | 台账页 | 健康度卡片下钻、筛选/翻页、属性冲突、关系与历史三个 Tab；空态/慢响应仍可操作。 | B |
| 2-10 | 批量维护/导出 | 200 条上限、部分失败、清空责任人幂等；导出按筛选及范围全量、不受当前页影响。 | A+B |
| 2-11 | 配置项模型/关系视图 | N：模型字段校验、关系方向、环路处理与范围过滤，拓扑动态更新。 | N |
| 3-01 | 升级指令 | 旧/新 Agent、错误版本号、离线节点：仅目标节点收到指令、升级状态可见。 | A+L |
| 3-02 | 防御指令 | 仅本机授权的 fail2ban jail 执行；重复回执幂等，未放行被拒绝。 | A+L |
| 3-03 | 采集项模板 | N：历史功能已移除，不按现有能力测试；若重做需先重定需求。 | N |
| 3-04 | 远程配置 | N：灰度下发、回读校验、权限和本机同意门控、失败不覆盖原配置。 | N |
| 3-05 | 配置回滚 | N：错误配置导致启动失败时回滚到上一有效版本并留下审计。 | N |
| 3-06 | 配置中心 | N：密钥引用不能在列表/日志/响应中出现明文；离线缓存与轮换。 | N |
| 4-01 | 任务下行回执 | queued→delivered→succeeded/failed/expired；重复上报与重启无丢任务/错配 ID。 | A+L |
| 4-02 | 操作护栏 | 无 RBAC、无节点能力、本机禁用和恶意参数分别被拦，写动作需显式本机许可。 | A+L |
| 4-03 | 参数化动作/批量 | 最多 200 节点、部分成功、仅 queued 可取消；绝不允许注入任意 shell 命令。 | A+B |
| 4-04 | 在线终端 | N：当前安全边界不提供任意执行，不作为默认增强建议。 | N |
| 4-05 | 文件分发 | 文件长度/hash/目录软链逃逸检查；原子替换与备份；重试不重复副作用。 | A+L |
| 4-06 | 计划任务 | 现仅拨测调度；N：如扩展通用作业，检验时区、错过执行与同轮去重。 | N |
| 5-01 | 系统自身升级 | 有效/低版本/篡改包、Web/Server/Agent 替换失败与回滚；必须隔离 Linux 演练。 | A+L |
| 5-02 | 应用制品发布 | N：制品校验、环境隔离、审批与可追溯版本；不等同于平台自身升级。 | N |
| 5-03 | CI/CD | N：构建不可变制品、失败中止、凭据遮盖、审批后部署。 | N |
| 5-04 | 应用级回滚 | N：回滚制品与配置一致，数据库变更需明确向后兼容。 | N |
| 6-01 | 告警协作非工单 | 待处理→认领→关闭→重开，不产生虚构工单号，协作记录可追溯。 | A+B |
| 6-02 | 工单审批 | N：申请/审批/驳回/超时，审批人不能自审，高危操作不能绕过。 | N |
| 6-03 | 变更单 | N：变更窗口外禁止执行；回滚步骤/审批结果可查询。 | N |
| 6-04 | 服务目录/SLA | N：服务归属和可用性目标计算，跨月/维护时段口径一致。 | N |
| 7-01 | SSH 攻击检测 | 模拟多 IP 登录失败与合法登录：窗口计数、事件脱敏与去重。 | A+L |
| 7-02 | sudo 审计 | 模拟成功/失败提权日志：用户名和节点匹配，不收集口令。 | A+L |
| 7-03 | fail2ban | jail 不存在、白名单 IP、重复封禁、到期恢复：只执行允许的动作。 | A+L |
| 7-04 | 会话录制 | N：用户已明确暂不做；合规要求出现时再单独验收。 | N |
| 7-05 | 统一堡垒机 | N：用户已明确暂不做，避免将 SSH/RDP/K8s 入口与当前只读能力混淆。 | N |
| 7-06 | 高危参数拦截 | N：若增加参数化动作策略，验证危险目录/核心服务目标被拒或要求复核。 | N |
| 7-07 | 双人复核 | N：申请人不可批准本人任务，超时自动失效，审计记录审批链。 | N |
| 8-01 | K8s 指标 | Pod CrashLoopBackOff 但 phase=Running 时仍计异常；副本/节点指标一致。 | A+L |
| 8-02 | Docker 指标 | 容器重启/停止、API 超时：数值与 Docker API 一致，不阻塞整轮采集。 | A+L |
| 8-03 | 工作负载/详情 | 描述白名单不泄露 Secret/环境值；资源名路径穿越拒绝，原始 YAML 故意不提供。 | A+L |
| 8-04 | K8s 事件 | 命名空间过滤、200 条截断标记、倒序和 Agent 未配置能力提示。 | A+L |
| 8-05 | Pod 日志 | N：按集群/命名空间/Pod 拉取最近日志，权限、行数/时间上限、敏感内容过滤。 | N |
| 8-06 | exec 终端 | N：与无任意命令安全边界冲突，非默认升级方向。 | N |
| 8-07 | 容器日志资产联动 | N：Pod 标签关联资产与日志，仅可见授权节点及命名空间。 | N |
| 8-08 | 镜像/配置扫描 | N：漏洞数据库离线更新、误报解释、敏感配置不外传。 | N |
| 9-01 | Agent 日志采集 | 文件轮转/截断、多行合并、断网续传、速率与每日配额边界。 | A+L |
| 9-02 | 日志接收 | 错误 Agent 密钥、超 4MiB、未授权来源与重试：返回明确 4xx，隔离节点限流。 | A+L |
| 9-03 | 日志检索 | 时间/关键词/正则/节点/来源、游标与扫描预算：无漏页/越权且截断显式。 | A+L |
| 9-04 | 日志保留 | 跨日过期清理后可查新记录、旧记录删除且不会误删未过期文件。 | A+L |
| 9-05 | 日志告警 | 当前指标规则可用；N：从某条日志创建规则后须验证匹配、去重与通知。 | N |
| 9-06 | 结构化解析 | N：JSON/正则提字段，坏行不丢原文，字段名白名单和卡口基数控制。 | N |
| 9-07 | 字段索引 | N：百万行级字段筛选与范围查询性能、游标稳定、删除后索引一致。 | N |
| 9-08 | 外部日志后端 | N：断网缓存/恢复、旧本地检索兼容、跨后端分页一致。 | N |
| 10-01 | Trace/Span | N：OTel trace/span ID 关联、采样与异常大 span 限制。 | N |
| 10-02 | 调用拓扑 | N：多服务链路/环路和未采样链路的缺失提示。 | N |
| 10-03 | 慢调用关联 | N：告警定位至 trace，跨租户权限隔离。 | N |
| 11-01 | 巡检报告 | 日/周/月跨时区、无数据与缺数据：HTML 范围、下载历史一致。 | A+B |
| 11-02 | 安全基线 | 一项通过/不通过/无权限：评分分母与详情一致，失败不伪装合规。 | A+L |
| 11-03 | FIM | 首次建基线、变更、删除和权限失败：报警只含路径/哈希不含敏感文件内容。 | A+L |
| 11-04 | 配置差异巡检 | 变更前后与标杆偏差分类准确；首次不报警，重复运行无假差异。 | A+B |
| 11-05 | 周期化调度 | 报告（2026-10-06）与**配置巡检（2026-10-07）**都可按固定间隔自动跑，两处调度各自独立、默认关闭、跨重启按 `lastRunAt` 去重、启动不立即跑：造一次"到点"（间隔下限 1 小时，验证时回退 `lastRunAt`）确认只跑一轮；**巡检的范围按触发身份收窄**（保存者被降权后覆盖资产数变少；停用后不执行且写明原因）；同一时刻重复 tick 不重复执行。**（2026-10-07 实测：巡检侧 38 项断言全过，见 `../superpowers/specs/2026-10-07-inspect-schedule-design.md` §7.5；两处核对坑记在该节——探活别用需要登录的 `/api/v1/version`，手动执行会推进 `lastRunAt` 从而顶掉本应到点的那一轮）** | A+L |
| 11-06 | 合规矩阵导出 | 当前只有 HTML 报告；N：矩阵导出与筛选/权限一致。 | N |
| 12-01 | 数据大屏 | 切三 Tab、慢接口/WS 断开、自动刷新与卸载：不叠加请求且数据不倒退。 | B+L |
| 12-02 | 自定义仪表盘 | 新建/编辑/删除面板与重启：布局持久化、超权限指标不展示。 | A+B |
| 12-03 | 巡检报告页 | 生成→查看→下载→历史；失败/空态不提示成功。 | B+L |
| 12-04 | 指标 CSV | 7 天边界、反向时间和恶意标签：拒绝大查询，CSV 列与图表一致。 | A+L |
| 12-05 | 审计 CSV | 分页第 3 页、limit=1、筛选=management：导出应含筛选内前 2000 条，不能只导当前页。 | A+B |
| 12-06 | 公开状态页 | 匿名读取允许的汇总；故障不泄露主机密钥与私有地址。 | A+B |
| 12-07 | 资产/容器/日志报表 | N：同筛选口径的统计/导出，不跨越 RBAC 范围。 | N |
| 12-08 | 移动端/多语言 | N：移动端当前不考虑；i18n 属远期，勿计入发布验收。 | N |
| 13-01 | RBAC | 不同内置/自定义角色访问读写接口：前端隐藏与服务端拒绝一致。 | A+B |
| 13-02 | 节点范围 | 同名不同组/未知节点/组变更后列表、详情、任务、导出均不可越权。 | A+B |
| 13-03 | 高危权限标记 | `audit:export` 等标记在角色界面可见，但不误认为标记本身会强制二次确认。 | A+B |
| 13-04 | 审计查询 | 时间范围与 offset/total、旧筛选；旧请求晚返回不能覆盖新结果，异常不得显示假空态。 | A+B |
| 13-05 | RBAC 页面 | 改角色权限保存→重新登录→重启：菜单与接口权限一致；超级管理员全量。 | A+B |
| 13-06 | 资产范围扩展 | N：业务/资产授权范围服务端强校验，不能仅前端隐藏。 | N |
| 13-07 | 新接口权限持续纳管 | 权限目录与路由匹配，每个新增读写入口做 401/403/作用域回归。 | A+B |
| 13-08 | SSO/LDAP/OIDC | N：远期能力；登录、禁用用户、令牌撤销和断联降级需独立验收。 | N |
| 14-01 | REST API | 参数非法/错误方法/未授权/超载：稳定 4xx/5xx、错误不泄密，版本保持兼容。 | A+L |
| 14-02 | WebSocket | 匿名拒绝、认证成功、掉线/重连和慢消费者；服务退出不泄漏连接。 | A+L |
| 14-03 | 通知 Webhook | 网络错误/5xx/超时及签名不泄露，投递失败可观测。 | A+L |
| 14-04 | Agent 下载分发 | 不同架构 Agent/脚本的下载及哈希、资源权限、404 与路径穿越。 | A+L |
| 14-05 | OpenAPI | N：生成接口契约与代码一致、认证/错误码和示例可用于 SDK 测试。 | N |
| 14-06 | 第三方集成 | N：ITSM/CMDB 对接的幂等键、重试、签名与权限最小化。 | N |
| 14-07 | 外部数据出口 | N：订阅确认、积压/重放与网络分区恢复，不输出密钥。 | N |

## 本轮执行证据（只标记实际执行的范围）

| 检查 | 结果 |
|---|---|
| `go test ./...`、`go vet ./...`、`go build ./...` | 全部通过；`internal/agent/proxy`、`internal/server/instancereg`、`internal/server/nginxaccess` 等包仍显示 `no test files`。单测通过不等于真实中间件/TSDB/浏览器 E2E 通过。 |
| `npm --prefix web test`、`npm --prefix web run build` | 通过；存在 Element Plus 分页 `small` 弃用告警及 >500kB chunk 告警，最大主包约 1.80MB、world 地图约 1.01MB；未将其误写成阻断缺陷。 |
| 审计页组件 | 新增并验证旧请求竞态、仅 Bearer 会话 CSV 导出；原有首页分页/时间范围 3 项保持通过。 |
| 审计 API | 新增并验证列表 `limit/offset` 不影响导出（旧/新 CSV 路由共用实现）；后端 `audit_test.go` 验证过滤与 CSV。 |
| 本机 Chromium 交互（`vite preview` + 模拟 API） | 实际打开 `#/audit`、看见 1 条记录与总数 120、点击第 2 页看到页码 2、输入 `admin` 点查询返回第 1 页；API 是模拟响应，**未证明真实鉴权/入库/下载跨端链路**。通用 `503` 模拟产生控制台网络错误，不能解释成生产环境故障。 |
| Linux/外部依赖/故障注入 | 本轮未执行 Linux 故障注入与真实 TSDB/告警通知；既有批次 18/19 的实机验证记录见对应设计件，不冒充本轮结果。 |

### 继续验证（2026-10-05，本机 Windows）

| 对应 ID | 实际输入与命令 | 结果与边界 |
|---|---|---|
| 12-05、13-03/13-07 | `go test ./internal/server/api ./internal/server/audit -count=1`；`npm --prefix web test -- AuditView.test.js`；另执行 `go test ./internal/server/api -run 'TestRoutes_AuditExportRequiresIndependentPermission|TestHandleAuditExportIgnoresPageOffsetAndLimit|TestRoutes_AssetExport|TestMetricExportScope_' -count=1 -v` | 通过。审计导出权限拆分、分页不截断、资产/指标导出范围由本机测试覆盖；审计组件 5 项通过。只验证测试注入的身份与数据，未执行真实登录→SQLite→浏览器下载闭环。 |
| 审计迁移与留存 | `go test ./internal/server/audit -run 'TestUseSQLiteBackfillsAllEventsOnce|TestUseSQLiteKeepsCorruptFile|TestPruneByTimeAndRows' -count=1 -v` | 3 项通过：临时 SQLite 回填 2100 条、重复切换不重复导入，坏 JSON 保留现场，按时间/条数清理。未执行已部署服务的升级回滚。 |
| 9-01、9-02 | `go test ./internal/agent/logship ./internal/server/receiver -run 'TestEndToEnd_CollectShipStore|TestHandleLogs_' -count=1 -v` | 通过。临时文件与本机 HTTP 测试覆盖增量命中采集、密钥/来源/大小/限流等接收边界；未验证 Linux 轮转、跨机断线续传。 |
| 2-08、11-04 | `go test ./internal/server/asset ./internal/server/api -run 'Inspect|Snapshot|Baseline' -count=1 -v` | 通过。临时库与 API 测试覆盖初次建基线、差异分类及节点分组范围；未做浏览器操作验证。 |
| 4-05 | `go test ./internal/agent/ops ./internal/server/ops -run 'FilePush|FileStore' -count=1 -v` | 参数、哈希、长度和目录护栏等测试通过；Windows 上 5 项 POSIX 落盘/备份/软链/幂等测试明确 `SKIP`，不得写成整项通过。 |
| 14-02 | `go test ./internal/server/api -run '^(TestHub|TestWS_)' -count=1 -v`；失败项单独 `-run '^TestWS_RegisterRejectedAfterClose$' -count=3 -v`、`-count=12` | 完整定向组有 1 项失败：`TestWS_RegisterRejectedAfterClose` 在 `ws_shutdown_test.go:607` 等待 Hub 关闭后新连接关闭超时；单项重复运行通过，属于待查的间歇性失败，不视作修复或整组通过。 |
| 竞态检测 | `go test -race ./internal/server/audit ./internal/server/api` | 未运行到测试：本机 Go 报 `-race requires cgo; enable cgo by setting CGO_ENABLED=1`。需在具备 CGO 工具链的隔离环境重试，不能声称无竞态。 |

### 第二批继续验证（2026-10-06，本机 Windows）

| 对应 ID | 实际输入与命令 | 结果与边界 |
|---|---|---|
| 1-07 | `go test ./internal/server/alert -run 'TestWebhookNotifierRedacts|TestRobotNotifierRedacts' -count=1 -v` | 通过。`httptest` 与拒绝连接覆盖通用 Webhook、钉钉/飞书/企业微信共用 HTTP 路径的非 2xx、业务错误及连接错误；错误与结构化日志均不含 URL query 哨兵密钥或第三方响应正文。未向真实 SMTP/机器人发送消息，未验证真实 429 重试策略（当前实现也不自动重试）。 |
| 1-08 | `go test ./internal/server/alert ./internal/server/api ./internal/server/retention -count=1` | 通过。真实临时 SQLite 关闭后，认领写入返回错误且不污染内存缓存；真实 API 返回 500 且不回显数据库内部错误；JSON 降级落盘失败同样不虚报成功；同一请求的状态变更与附带评论只执行一次持久化写，避免部分提交；保留清理失败也不删除缓存或虚报删除数。未验证磁盘写满、网络文件系统或已部署进程重启。 |
| 1-04 | `go test ./internal/server/dialtest -count=1`；`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./internal/server/dialtest` | 通过。可注入 ICMP 边界证明不再用 TCP 7/80 冒充 ping；真实 `probeICMP` 函数级测试覆盖 IPv4/IPv6 Echo 报文、权限失败和 socket 关闭；本地 TLS 服务覆盖已过期证书仍严格校验签发链/主机名并产出负到期天数，未来生效、错误主机名及过期中间 CA 仍拒绝；Linux/amd64 纯 Go 交叉编译通过。未在 Linux raw socket、防火墙或真实证书链上执行 ICMP/HTTPS 拨测。 |
| 1-02 | `go test ./internal/agent/collector -run 'TestExporter\|TestMySQL\|TestK8s\|TestRabbitMQ\|TestOther\|TestRocketMQ' -count=1` | 通过（连续 3 次）。`httptest` 覆盖 9 类显式 exporter 及 RabbitMQ `/metrics`：空正文、仅注释、无关指标不得判在线；**同时识别上游 exporter 惯用名与平台目录名**（`mysql_up`/`pg_up`/`nginx_up`/`redis_up`/`rabbitmq_up`/`mongodb_up` 与 `<mw>_instance_up`），原生 `*_up 0` 优先判离线；无原生 up 但有本类型业务指标才回退在线；K8s 以「是否存在任意 `kube_*` 样本」判在线，兼容 metric-allowlist 收窄，且仅注释正文判离线；ExporterURL 日志只保留 scheme+host，剥离 userinfo/query/path 及 `url.Error` 中的完整 URL。仅验证 Prometheus 文本与本机 HTTP 边界；**未连接真实 15 类中间件**，各第三方 exporter 版本的实际指标名仍需在实机 `curl <exporter>/metrics` 各核一次。 |

#### 本批次未修复但已记录

| 项 | 证据与边界 |
|---|---|
| ~~exporter 模式下 6 类缺少 `<mw>_instance_up` 序列~~ **已于 2026-10-06 修复** | 原状：mysql/postgres/nginx/kafka/mongo/fastdfs 的 exporter 路径不产出平台统一存活指标（`redis`/`k8s` 由 receiver 合成，`rabbitmq`/`rocketmq` 采集器自合成），因此「中间件离线」告警（查 TSDB 的 `<mw>_instance_up`）对这 6 类不触发。**修法**：在 receiver 侧按实例元信息补出这 6 类序列（与 redis/k8s 同一形态），并按「指标名 + instance」判重——直连模式采集器已自产同名序列时不重复写，避免同一实例出现两条 label 集不同的序列。**验证**：`internal/server/receiver/instance_up_test.go` 三条用例（exporter 模式补出且离线写 0、直连模式不重复、同机多实例逐个补）。**未实机验证**：真实 exporter 部署下的告警触发闭环。 |
| `zookeeper` 测试随机失败（既有偶发，Windows 特有，非本批引入） | `internal/agent/collector/zookeeper_test.go` 的 `fakeZooKeeper`：Windows 整包 6 次有 2 次失败。`-run 'TestZooKeeper' -count=40`（**不运行**本批任何新用例）同样失败，证明与本批改动无关。诊断（临时插桩）显示服务端每次都能读满 6 字节命令并完整写出 payload 且无错误，客户端却收到 RST 或 5s 超时；尝试「读满一行」「CloseWrite+排空」「写后延迟关闭」三种关闭方式均未消除偶发，已全部回退，夹具保持原样。**2026-10-06 在 dev-server 上 `-count=200` 全过**，确认是 Windows 回环在快速连接/关闭下的行为，不影响 Linux/CI。 |
| 第二批全量回归 | `go test ./...`；`go vet ./...`；`go build ./...`；`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./...`；`npm --prefix web test`；`npm --prefix web run build` | vet、双平台构建、前端 10 文件 55 项测试与生产构建均通过。`go test ./...` 在本机**不能稳定全绿**：除上表已记录的 zookeeper 偶发外其余包全部通过（重复运行可复现同一偶发，非本批引入）。因拨测包新增对 `golang.org/x/net` 的直接导入，`go mod tidy` 仅把该依赖从 indirect 移到直接 require（`go.sum` 未变）。既有 Element Plus `small` 弃用和大 chunk 告警仍在。 |

### dev-server（Linux）复核验证（2026-10-06）

把当前工作树（含未提交改动）打包上传到 `~/nb-test` 后运行；**未改动服务器上的服务、配置、二进制或数据**，只跑测试与只读探测。

| 检查 | 命令与结果 |
|---|---|
| 构建与静态检查 | `go build ./...`、`go vet` 通过（Go 1.27.1，Ubuntu 24.04，4 vCPU）。 |
| 完整测试套件 | `go test -count=1 ./...` **全部包通过**（本机 Windows 因 zookeeper 偶发只能 1/2 通过）。 |
| ZooKeeper 偶发定性 | `go test ./internal/agent/collector -run TestZooKeeper -count=200` → 0.45s 通过。**Linux 不复现**，确认该偶发是 Windows 回环行为的夹具问题，不是产品缺陷。 |
| CI 竞态门禁（本机因缺 cgo 无法运行） | `go test -race -count=1 ./internal/agent/...` 与 `./internal/server/api/...` 均通过，含本批新增用例——复核中「新测试可能触发 TSan」的担忧不成立。 |
| 本批其余改动包的竞态 | `go test -race -count=1 ./internal/server/{alert,asset,dialtest,retention,audit}` 全部通过。 |
| 真实 RabbitMQ exporter 核对 | 机器上 `127.0.0.1:15692`（rabbitmq_prometheus）返回 200，但**不存在** `rabbitmq_up` 或 `rabbitmq_instance_up`（仅 `rabbitmq_erlang_uptime_seconds` 等）。因此该类型靠「解析到 `rabbitmq_*` 业务指标即在线」回退判定，新增的 `rabbitmq_up` 候选在此部署下不命中（无害）。 |
| 其他 exporter 核对 | 全端口扫描后，仅 15692（RabbitMQ）、8428（VictoriaMetrics）、10249（kubelet）返回 Prometheus 文本；**没有** mysqld/postgres/nginx/redis/mongodb exporter 在跑（中间件均走直连模式）。故 `mysql_up`/`pg_up`/`nginx_up`/`redis_up` 仍**未经实机 exporter 核对**——仓库内 `docs/c1-collector-templates.md:447` 列出了这些上游名字被改名为 `<mw>_instance_up`，可作旁证。 |



### 已部署 dev-server 只读探测（2026-10-05）

经 SSH 隧道访问已部署服务的本机 8080 端口；前端地址为 HTTP，未在公网明文连接上传管理员凭据。`GET /` 返回 200，公开 `GET /api/v1/status` 返回 200，匿名 `GET /api/v1/audit/events?limit=1` 与 `GET /api/v1/audit/export?category=management` 均返回 401。上述只证明匿名边界，**不证明**已登录权限、SQLite 入库、分页导出或浏览器下载。尝试登录验证时，含明文口令的命令被安全限制拒绝；未换工具/编码重试，未读取服务器密钥与数据库。已部署二进制位于 `/usr/local/bin/monitor-server`，但未读配置、未修改服务或执行升级。若要完成真实登录闭环，需获准使用不把口令写入命令/文件/输出的凭据传递方式。

### 本轮发现并修复

1. 审计导出复用列表的 `limit/offset`：在第 3 页点导出会漏掉前两页，同条件导出总数还受每页条数限制。新用例先失败（仅 2 行，预期 4 行）后修复；固定导出 2000 条上限，筛选条件保留。
2. 审计页并发查询：旧筛选慢请求在新筛选后返回会覆盖新列表与总数；新用例先失败后加请求序号保护，组件卸载也作废过期响应。
3. 审计 CSV 下载只依赖 Cookie：仅用 `Authorization: Bearer` 登录的会话浏览列表正常、点击导出却 401；新用例先失败后在下载请求携带已有令牌。
4. 巡检历史读取未按节点分组裁剪：受限用户可看到范围外运行汇总、差异项与标杆；新增逐资产运行明细，按当前资产归属重算局部结果，旧无明细记录对受限用户隐藏，并阻断范围外标杆值进入差异项。
5. 通知失败路径泄密：Webhook 完整 URL、第三方非 2xx 正文、机器人业务错误文案及连接错误可能进入日志或错误返回；新用例用哨兵密钥复现后统一只保留脱敏目标与状态/业务码。
6. 告警处置持久化失败虚报成功：SQLite/JSON 写失败时内存仍生效且 API 返回 200；改为先持久化后更新缓存，错误返回 500 且不回显数据库内部错误。附带评论与状态动作改为一次写入，保留清理也改为落盘成功后再删缓存，避免部分提交与重启复活。
7. ICMP 拨测以 TCP 7/80 代替，端口状态会被误写成主机可达；改为真实 ICMP Echo，权限不足明确失败。HTTPS 过期证书原先在握手阶段被拒绝而无到期指标；现在保持链与主机名校验，同时记录负到期天数并判定 `Up=false`。
8. 多类 exporter 只要 HTTP 200 就把实例标为在线，空/无关 exposition 或原生 up=0 会与统一实例状态矛盾；新增公共健康判定并接入 9 类 exporter 与 RabbitMQ。修正过程中发现更关键的一层：平台目录名是 `<mw>_instance_up`（由直连采集器/receiver 产出），而第三方 exporter 暴露的是 `mysql_up`/`pg_up` 等，因此存活判定改为在**原始文本**上同时识别两套名字，否则 `*_up 0` 仍会被回退分支判成在线；K8s 空 KSM 也据此判离线。

### 后续验证与约束

- 优先在隔离环境完成 RBAC/资源范围×导出、审计入库回滚、升级失败原子恢复、真实 TSDB/告警通知、Agent 断线续传、K8s 事件截断、日志限流等跨模块场景；每项记录版本、输入、预期、实际和证据。回滚及升级演练涉及服务替换，必须另行获得操作授权。
- 在 Linux 隔离节点补做 raw ICMP（IPv4/IPv6、无 CAP_NET_RAW、目标拒绝/超时）与公网/私有 CA HTTPS 证书演练；本机注入测试和交叉编译不能替代真实网络行为。
- 使用已有自动化的包/组件覆盖作为回归基础，再逐行细化数据驱动测试；对未实现项不运行“通过”测试，也不把没有测试文件当作一定有缺陷。

## 有升级价值的方向（按收益/风险排序）

1. **P0：审计可信度与操作护栏可视化。** 审计已入 SQLite，解决了 JSON 截尾但仍与业务共机同权限：增加外部只追加审计出口/失败积压可观测性；把 Agent 的 `CheckGuardRisks` 组合风险告警上报并展示，不新增任意执行。参考现有逐机本地许可，先给明确的安全状态和证据链。
2. **P1：资产→告警→任务的关联闭环。** 先把告警事件关联资产及最近配置变化，支持按业务影响定位；再接已有参数化处置任务和审批状态机（双人复核），不先上大型通用工单引擎。参考 [NetBox](https://github.com/netbox-community/netbox) 的明确对象关系建模及 [Nightingale](https://github.com/ccfos/nightingale) 的目标/告警对象组织；只借设计思路。
3. **P1：日志结构化和有界检索。** 优先 JSON/受限正则字段提取、标签基数限额、字段检索和日志→Pod/资产跳转；保留现有有界扫描作回退。达到规模瓶颈再评估 [VictoriaLogs](https://github.com/VictoriaMetrics/VictoriaLogs) 后端适配（不能仅因项目热度就替换现有链路）。
4. **P1：容器只读视图增强。** Pod 日志按时间/行数/范围限额，敏感字段继续白名单投影；先补跨工作负载、事件、指标、资产的导航。参考 [Headlamp](https://github.com/kubernetes-sigs/headlamp) 的多集群与按角色暴露操作，不照搬 exec 终端。
5. **P2：可复现发布与测试门禁。** CI 增 `go test -race`（Linux）、契约/权限矩阵、浏览器端到端和可控故障演练；为 API 发布 OpenAPI 契约；将大屏/主包按需拆分并制定体积预算。第三方项目的许可证和运维开销应独立评估，现有[调研快照](../superpowers/specs/2026-09-30-ops-platform-open-source-research.md)的 stars/状态只代表 2026-09-30。

**明确不建议**：重新引入任意脚本/在线终端、默认打开写权限、仅靠前端隐藏实现 RBAC，或用一次单测全绿宣称整个平台“全面验证通过”。
