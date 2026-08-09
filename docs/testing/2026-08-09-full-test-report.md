# Nebula Monitor 全面测试计划与执行报告

- 测试日期：2026-08-09
- 代码版本：工作区当前版本（`VERSION` 已有用户修改，未纳入本次变更）
- 范围：Go Server、Agent、代理隧道、TSDB 适配、告警、拨测、升级、报告、Vue Web 与部署配置。
- 结论：构建和现有自动化测试通过；不满足上线安全门槛。必须先处理 SEC-01～SEC-04，并在具备 Linux、Docker、真实 TSDB 与浏览器的隔离环境完成本文的集成、负载、稳定性测试。

## 1. 测试环境、方法与限制

| 项目 | 实际值 |
|---|---|
| 主机 | Windows，PowerShell，工作区 `E:\codebuddy_project\nebula-monitor` |
| Go / Node | 以项目锁定依赖和本机工具链执行 |
| 可用服务 | 未安装 Docker；未提供可连接的 TSDB、SMTP、Webhook 或受监控中间件 |
| 自动化 | `go test ./...`、`go vet ./...`、`npm run build`、代码安全审查 |
| 未执行原因 | `go test -race` 需要 CGO（当前 `CGO_ENABLED` 未启用）；`govulncheck` 未安装；npm registry 不实现 audit API；无可运行的容器/TSDB 环境，因此未伪造 HTTP 响应时间、吞吐量或资源数据 |

“通过”只表示本地实际执行且成功；“待执行”不是通过，也不是缺陷结论。

## 2. 功能测试计划、用例与测试数据

### 2.1 Server、认证与节点

| ID | 场景 / 测试数据 | 预期 |
|---|---|---|
| FUN-A01 | `POST /api/v1/login`：正确、错误、空字段、格式错误 JSON；同 IP 连续 6 次错误密码 | 正确登录取得 24h token；错误为 401；第 6 次为 429 |
| FUN-A02 | 无 token、篡改 token、过期 token、Cookie token、Bearer token 访问每个 `/api/v1/*` 写接口与 `/ws` | 写/读接口均 401；有效 token 可访问；WebSocket 不可匿名订阅 |
| FUN-A03 | 变更密码：旧密码错误、空新密码、长密码（256 字符）、成功后用新旧密码登录 | 分别返回 401/400/成功；旧密码失效；配置中仅存密码哈希 |
| FUN-A04 | Agent 上报：正确/错误 `X-Agent-Secret`，空 node，空指标，200/2000/10000 个指标 | 鉴权失败 401；字段校验 400；合法数据写 TSDB、更新心跳、保留分组 |
| FUN-A05 | 节点列表、详情、改分组/显示名、删除、批量升级；名称包含 Unicode、空格、`../`、超长 256 字符 | 合法名称正确持久化；非法/不存在目标返回 4xx，不能越权读写文件 |

### 2.2 指标、监控与前端交互

| ID | 场景 / 测试数据 | 预期 |
|---|---|---|
| FUN-M01 | CPU、内存、磁盘、网络、进程、负载采集；0、负值、NaN、极大值、空标签 | 合法值展示；无效数据被拒绝或安全降级；页面无崩溃 |
| FUN-M02 | `query/latest`、`query/range`：1 分钟、7 天、反向时间、跨度>7天、step=0/负数 | 合法请求返回正确时序；非法/超限返回 400，不触发 TSDB 大查询 |
| FUN-M03 | Redis、MySQL、PostgreSQL、Nginx、Kafka、Docker、RocketMQ、K8s、MongoDB、FastDFS：各一在线、一离线实例 | 十类 Tab 的概览、列表、详情、角色/集群聚合和空态均正确 |
| FUN-M04 | Web：登录/退出、菜单路由、分页筛选、抽屉、刷新、窄屏 375px、常见桌面 1440px、慢网/接口500 | 路由守卫生效；加载/错误/空态明确；无未处理 JS 异常 |
| FUN-M05 | 数据大屏：10/20/30/60 秒刷新、切换主机/中间件/Nginx Tab、请求延迟大于刷新间隔 | 不重复轮询；最后一次请求结果不被旧请求覆盖；卸载时关闭定时器/WS |

### 2.3 告警、通知、拨测、报告与配置

| ID | 场景 / 测试数据 | 预期 |
|---|---|---|
| FUN-O01 | 阈值六运算符、for=0/正值、firing→resolved、主机离线、服务离线、主从切换、集群无主/多主 | 状态转换、去重、级别、标签与事件历史正确 |
| FUN-O02 | 单次静默、跨天周期静默、维护窗口、抑制规则、分组等待/间隔、升级通知 | 命中规则时只抑制通知；事件留痕；结束后恢复通知 |
| FUN-O03 | 邮件 SMTP、Webhook、钉钉、飞书、企业微信：成功、超时、4xx/5xx、恶意 URL | 成功可追踪；失败重试/错误可观测；不泄露密码/签名 |
| FUN-O04 | HTTP/HTTPS/TCP/ICMP 拨测：成功、DNS 失败、超时、证书 30/7/0 天；任务 CRUD | 指标、状态和证书告警正确；输入限制生效 |
| FUN-O05 | 日/周/月报告生成、下载、历史；通知/大屏/UI/仪表盘/地理库配置保存后重启 | 内容和时间范围正确；持久化、热加载和回退符合预期 |

### 2.4 代理、升级、部署恢复

| ID | 场景 / 测试数据 | 预期 |
|---|---|---|
| FUN-P01 | collect、edge、hub；正确 CA、错误 CA、过期证书、断网 1/10/60 分钟、缓冲区满 | mTLS 鉴权；断线指数重连；缓冲策略符合配置；恢复后顺序和丢失数可观测 |
| FUN-P02 | 升级包：正确包、损坏 tar、路径穿越、超 500MB、错误 checksum、同/低版本、回滚 | 上传/预检拒绝异常包；升级可原子回滚；审计记录完整 |
| FUN-P03 | Docker/脚本冷启动、TSDB 不可用、磁盘满、Server 被重启、Agent 版本不同 | 服务有健康状态、重试与明确告警；数据与配置不被破坏 |

## 3. 性能与稳定性计划

在独立压测环境（Server 2 vCPU/4GiB、VictoriaMetrics 2 vCPU/4GiB、Linux Agent）运行，监控 Server/TSDB 的 CPU、RSS、GC、文件句柄、网络、磁盘 IOPS、错误率与 p50/p95/p99。

| 阶段 | 工作负载 | 时长 | 通过标准 |
|---|---|---:|---|
| 基线 | 10 Agent，每 15 秒各 200 指标；20 并发读 | 15 分钟 | 成功率≥99.9%，读 p95≤500ms，上报 p95≤1s |
| 负载 | 100/500 Agent，每 15 秒各 200 指标；100 并发读/WS | 每档 20 分钟 | 成功率≥99.5%，读 p95≤1s，上报 p95≤2s；无持续内存增长 |
| 压力 | 从 500 Agent 每 5 分钟增加 250，至错误率>1%或资源饱和 | 最长 30 分钟 | 记录拐点、最大稳定吞吐、限流/恢复行为；不得数据损坏 |
| 稳定性 | 500 Agent、50 并发读、20 WS；每 6h 注入一次 TSDB 30s 不可用 | 72 小时 | 可用性≥99.9%，重连后无崩溃/句柄泄漏；积压和丢失有量化 |

建议使用 k6/vegeta 对 API 与 WebSocket 施压、Prometheus node_exporter/cAdvisor 采集资源。每档保存：请求数、RPS、p50/p95/p99、错误分类、Server/TSDB CPU/RSS/GC/IO、指标写入延迟、节点在线数。

## 4. 实际执行结果

| 编号 | 命令 / 检查 | 状态 | 结果 |
|---|---|---|---|
| EXE-01 | `go test ./...` | 通过 | 5 个已有测试包通过；其余 22 个包无测试文件 |
| EXE-02 | `go vet ./...` | 通过 | 未输出诊断 |
| EXE-03 | `npm run build` | 通过（有告警） | 2,326 个模块转换，10.14s；存在大于 500kB 的 bundle 告警，最大约 1.79MB（gzip 588kB） |
| EXE-04 | `go test -race ./...` | 未执行 | 环境返回“race requires cgo”；应在 Linux CI 以 `CGO_ENABLED=1` 执行 |
| EXE-05 | 覆盖率 | 不达标 | 仅 agent/server crypto 约 80%，report 35%，upgrade 6.6%；大多数核心 API、receiver、storage、proxy、dialtest 为 0% |
| EXE-06 | `govulncheck ./...` | 未执行 | 本机未安装；应接入 CI 并生成 SBOM/依赖漏洞报告 |
| EXE-07 | `npm audit --omit=dev` | 未得结果 | 配置的 npmmirror 未实现 audit API；切至支持 audit 的官方 registry 或 SCA 工具 |
| EXE-08 | 负载/压力/72h稳定性 | 未执行 | 没有 Docker、TSDB、外部中间件与受控 Linux Agent；故不报告虚假的响应时间、吞吐或资源占用 |

## 5. 安全测试结果与修复建议

| ID | 风险 | 证据 | 修复建议 |
|---|---|---|---|
| SEC-01（高） | Cookie 未设置 `Secure`、`SameSite`，且所有写接口接受 Cookie 自动认证，缺少 CSRF 防护 | `internal/server/api/auth.go` 登录 Cookie 仅设置 HttpOnly/Path/MaxAge；没有 Origin/CSRF token 校验 | Cookie 设置 `Secure=true`（HTTPS 部署）、`SameSite=Strict/Lax`；对 Cookie 会话的 POST/PUT/DELETE 校验 Origin 与同步 CSRF token。Bearer API 可例外。 |
| SEC-02（高） | 密码使用快速 salted SM3，不具备抗离线爆破的工作因子 | `internal/server/crypto/crypto.go` 直接 SM3(salt\|\|password) | 新密码改为 Argon2id（或 bcrypt，明确成本参数）；版本化存储格式，登录时渐进迁移；最低 12 字符与泄露密码检查。 |
| SEC-03（高） | Agent 凭据可用内置、可预测的默认主密钥加密，等同于明文保护 | `internal/agent/crypto/crypto.go` 的 `defaultKey`；空 `cryptoKey` 自动采用 | 禁止生产环境空密钥启动；密钥由 OS secret manager/KMS 或权限 0600 文件注入；轮换与密文版本号。 |
| SEC-04（高） | Edge TLS 客户端设置 `InsecureSkipVerify=true`，跳过 Hub 主机名校验 | `internal/agent/proxy/tls.go` | 默认严格验证 SAN/ServerName；为 IP 证书设置 ServerName 或自定义 `VerifyConnection` 且精确校验证书 SAN/指纹；仅受控开发环境可显式绕过。 |
| SEC-05（中） | 无角色/权限模型；一个有效 token 可执行升级、上传、删除节点、改通知等高危操作 | `AuthMiddleware` 仅验证 token，无角色/操作人上下文 | 最少分 viewer/operator/admin；高危操作二次确认/短时 re-auth；记录操作者、来源、请求 ID 的审计日志。 |
| SEC-06（中） | 登录失败限流在内存且以 RemoteAddr 为准；重启可清空，反向代理部署时无法正确识别来源 | `auth.go` 的全局 map 与 `clientIP` | 在可信代理前提下解析 X-Forwarded-For；使用共享限流或反向代理限流；对用户名+IP 指数退避，并定期清理 map。 |
| SEC-07（中） | 敏感通信取决于部署：默认 Server/Agent 示例为 HTTP；TSDB、Webhook、SMTP 配置可能走明文 | 默认 `ServerURL` 与 `ListenAndServe`，配置可指定 HTTP | 生产反向代理强制 TLS/HSTS；Agent/Server 支持 TLS 或仅私网 mTLS；拒绝生产 HTTP 敏感端点；SMTP 强制 STARTTLS/TLS。 |
| SEC-08（低） | 前端首包与地图 chunk 过大，弱网下可能影响首屏可用性 | 构建输出最大 1.79MB，另有 1.01MB world map chunk | 路由和地图按需加载，拆分 ECharts，设置性能预算并在 CI 阻断回归。 |

### 注入与 XSS 审查结论

- SQL 注入：未发现应用侧拼接 SQL；项目主要使用 PromQL。PostgreSQL 唯一 `fmt.Sprintf` 的列名来自代码固定枚举，仍应添加白名单单元测试。
- PromQL 注入：指标、标签名和值有校验及转义路径；需用 FUN-M02 的恶意值（`\"} or vector(1)`、换行、超长字符串）做集成回归。
- XSS：静态搜索未发现 `v-html`/`innerHTML`；仍应在节点名、分组、告警/通知/仪表盘文本输入 `<img src=x onerror=alert(1)>`，验证 Vue 默认转义和所有导出 HTML 的上下文编码。
- 认证：已有 HMAC token、HttpOnly Cookie、登录失败限流、WebSocket middleware 鉴权；上述 SEC-01、02、05、06 仍需修复。

## 6. 上线门禁与后续动作

1. 修复 SEC-01～SEC-04，新增 API/认证/代理 TLS 的单元和集成测试；将核心服务端覆盖率提升至至少 70%，upgrade/receiver/proxy 至少 80%。
2. 在 Linux CI 开启 `CGO_ENABLED=1 go test -race ./...`、`govulncheck ./...`、前端 SCA、SAST、secret 扫描和 SBOM。
3. 用隔离环境完成第 3 节各档压测和 72 小时稳定性；把实测指标填入本报告后再做容量结论。
4. 上线前复测所有高危接口的未认证、越权、CSRF、上传、路径穿越、TLS 和审计日志场景。
