# NebulaEye · 服务器监控系统

基于 Go 的 C/S 架构主机监控。轻量 Agent 部署在被监控节点采集指标并定时经 HTTP 上报；
无状态 Server 以二进制 + systemd（或 Docker）部署，通过 Prometheus `remote_write` 写入、
PromQL 读取时序库（**默认 VictoriaMetrics**，可对接 Mimir/Cortex/Thanos 等），提供多节点分组管理、
Web 仪表盘（实时 + 历史趋势）、数据查询 API 与阈值告警（邮件 / Webhook）。

> 设计要点：**Server 完全无状态**，时序库独立持久化，重启不丢数据；时序库与 Server 可分机部署。

---

## 架构

```
Agent(linux/amd64|arm64|arm) --HTTP 上报--> Server(二进制+systemd / Docker)
                                        |
                                        |-- remote_write / PromQL --> 时序库(VictoriaMetrics，可分机)
                                        |-- REST / WebSocket --> Web 仪表盘(磁盘读取)
                                        |-- 告警引擎 --> 邮件 / Webhook
```

---

## 功能清单

### 已实现

**主机监控（Agent 采集）**

| 指标 | 说明 |
|------|------|
| CPU | 使用率、逻辑核心数、CPU 型号 |
| 内存 | 使用率、总量、已用、可用 |
| 负载 | load1 / load5 / load15 |
| 磁盘 | 各分区容量/已用/使用率；真实磁盘汇总使用率（过滤 tmpfs/overlay 等虚拟挂载） |
| 网络 | 各网卡收发速率（跳过 lo） |
| 进程 | 进程总数 + 资源占用 Top10 |
| 主机信息 | OS / Arch / IP、CPU 型号、内存/磁盘大小、分区表 |

**架构与服务端**

- Server + Agent 架构；Agent 走 Prometheus `remote_write`，后端时序库可切换（VictoriaMetrics / Mimir / Cortex / Thanos / Prometheus）
- 节点管理 + 分组（Group）
- Agent 接入授权（`agentAuth` 启用后需携带 `X-Agent-Secret`，否则 401）
- 安装分发：Server 自带 CDN 路由 `/install/agent-install.sh` 与 `/bin/linux/{arch}/agent`
- 离线安装脚本：`deploy/install-server.sh` 自动探测 `dist/artifacts/packages` 与 `dist/artifacts/bin`（旧 `offline/` 兼容）
- 交叉编译 `build/cross-compile.sh`：linux amd64/arm64/arm 的 server/agent
- 前端构建 `build/build-web.sh`：Vue 3 + Vite 构建并平铺到 `dist/artifacts/web/`

**告警**

- 阈值规则：`>` `>=` `<` `<=` `==` `!=` + 持续时长（For）+ firing/resolved 状态机 + 事件去重
- 通知渠道：邮件（SMTP）、Webhook

**前端**

- 登录、总览、主机列表（含分组）、主机详情（硬件信息 + 分区表 + 指标图表）、告警列表、规则新增/编辑、分组管理
- **系统升级**：上传 upgrade 包 → 解析版本 → 立即升级（备份+替换 server/web+同步 agent 到自带 CDN+重启）/ 回滚 / 历史；**Agent 不主动推送**，由管理员在主机列表手动点击
- **主机列表 Agent 版本提示**：某节点 Agent 版本低于服务端版本时，「Agent 版本」列显示红点，提示需要升级
- **升级按钮防重复**：「系统升级」页「立即升级」提交后按钮进入 15 秒冷却（显示"请等待 Ns"并禁用），避免 server 重启期间被重复点击

**系统升级**

- **Web 端一键升级**：在仪表盘「系统升级」页上传 `nebula-monitor-v*-upgrade.tar.gz`，自动解析 `manifest.json` 显示新版本信息（版本号、Server 架构/大小、Web 大小、Agent 架构、SHA256），点击「立即升级」完成备份→替换→重启并写历史。提交后按钮进入 15 秒冷却（显示"请等待 Ns"并禁用），防止 server 重启期间重复点击；升级历史「操作」列显示「升级 / 回滚」、「结果」列显示「成功 / 失败」（中文化）。
- **Agent CDN 同步**：apply 时把新 agent（amd64/arm64/arm）复制到 server 自带 CDN（`agentBinDir/agent/linux/<arch>/agent`），**不主动推送到主机**；管理员到「主机列表」点升级按钮触发。
- **回滚**：一键回到最近一次备份（替换 server + web + 重启）。
- **包格式约定**：`manifest.json` 声明每个组件的 source/target/action/sha256，由 `build/release.sh` 自动生成并自校验一致性。

### 路线图（未实现，暂未排期）

> 暂未开发，列为将来任务。

- **中间件监控**
  - Redis：连接数 / 内存 / 命中率 / 命令速率 / 慢查询 / 主从状态
  - MySQL / MariaDB：连接数 / QPS / 慢查询 / 缓冲池命中率 / 主从延迟
  - PostgreSQL：连接数 / 事务 / 缓存命中率 / 复制延迟
  - MongoDB：连接数 / 操作数 / 内存 / 复制状态
  - 消息队列：Kafka / RabbitMQ / RocketMQ
  - Nginx：连接数 / 请求速率
  - 容器与编排：Docker / Kubernetes
- **更多通知渠道**：钉钉 / 飞书 / 企业微信 / Slack / Telegram
- **告警增强**：静默 / 维护期、告警抑制与分组
- **可观测性增强**：自定义仪表盘、指标自动发现、历史数据导出
- **多用户与权限**：SSO 登录、角色权限管理

---

## 快速部署

下载 **full 包**（首次部署用，已含全部资源），解压后 `sudo ./install.sh` 进入 7 选项交互菜单：

```bash
# 1. 下载 full 包（GitHub Release）
curl -fsSL -O https://github.com/<org>/<repo>/releases/download/v${VERSION}/nebula-monitor-v${VERSION}-full.tar.gz

# 2. 解压
tar -xzf nebula-monitor-v${VERSION}-full.tar.gz
cd nebula-monitor-v${VERSION}-full

# 3. 一键入口（默认交互菜单；非 root 会自动 sudo 提权）
sudo ./install.sh

# 安装菜单：
#   【1】安装 server
#   【2】安装 agent
#   【3】安装 VictoriaMetrics
#   【4】卸载 server
#   【5】卸载 agent
#   【6】卸载 VictoriaMetrics
#   【7】卸载全部
```

`install.sh` 内部按选项调用 `deploy/install-server.sh` / `deploy/install-tsdb.sh` / `deploy/agent-install.sh` / `deploy/uninstall.sh`。

### 非交互 / 自动化

```bash
# 安装 server（对接已有时序库）
sudo ./install.sh server --yes --tsdb-addr http://<tsdb>:8428

# 安装 agent（在被监控节点）
sudo ./install.sh agent --yes --server http://<server>:8080 [--secret <KEY>]

# 独立安装 VictoriaMetrics
sudo ./install.sh vm --yes

# 卸载（默认保留数据；加 --purge 才删数据）
sudo ./install.sh uninstall --all --yes
sudo ./install.sh uninstall --server --purge --yes
```

### 准备 dist/artifacts/ 资源（自建发布包时）

`install.sh` 默认从包内 `bin/`、`web/`、`packages/` 读取资源，**用户无需关心**。
仅当你在仓库根目录自建发布包（开发/CI 维护者）才需要准备：

1. `dist/artifacts/bin/server/linux/<arch>/server` — Server 离线二进制
2. `dist/artifacts/bin/agent/linux/<arch>/agent` — Agent 离线二进制
3. `dist/artifacts/web/index.html` + `assets/` — 前端构建产物
4. `dist/artifacts/packages/node-v<版本>-linux-<arch>.tar.xz` — Node（可选，已有预构建 web 时可不放）
5. `dist/artifacts/packages/victoria-metrics-linux-<arch>-<版本>.tar.gz` — VictoriaMetrics 二进制包

生成全部产物（需 Go + Node 18+）：

```bash
bash build/cross-compile.sh        # 生成 dist/artifacts/bin/{server,agent}/linux/<arch>/
bash build/build-web.sh            # 生成 dist/artifacts/web/
bash build/fetch-packages.sh       # 下载 node + victoria-metrics 到 dist/artifacts/packages/
```

> `fetch-packages.sh` 是幂等的（已存在则跳过），便于 CI 重复使用。包文件名需符合上述规律，
> 脚本按 `uname -m` 自动匹配；同一 arch 有多个会列出供确认，也可用 `--node-package` /
> `--vm-package` 显式指定。

### 高级：直接调用底层脚本

`install.sh` 内部就是按选项调用 `deploy/` 下的脚本。如果你想跳过交互、嵌入 CI 或对单步做自定义，也可以直接调：

```bash
# 部署时序库（VictoriaMetrics / Mimir / Cortex / Thanos）
sudo bash deploy/install-tsdb.sh [--yes] [--backend victoriametrics|mimir|cortex|thanos] [--docker]
sudo bash deploy/install-tsdb.sh --upgrade --vm-package victoria-metrics-linux-amd64-v<NEW>.tar.gz

# 部署 Server（对接时序库）
sudo bash deploy/install-server.sh --yes --tsdb-addr http://<tsdb>:8428
sudo bash deploy/install-server.sh --upgrade   # 升级已有部署（保留 server.yaml）

# 安装 Agent（被监控节点，从 Server 自带 CDN 拉）
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- --server http://<server>:8080 [--secret <KEY>]
sudo bash deploy/agent-install.sh --yes --server http://<server>:8080

# 卸载（默认保留数据；--purge 连数据一起清）
sudo bash deploy/uninstall.sh --all --yes
sudo bash deploy/uninstall.sh --server --purge --yes

# Docker 方式
docker build -f deploy/docker/Dockerfile.server -t monitor-server:latest .
docker compose -f deploy/docker/docker-compose.yml up -d
```

---

## 升级

各脚本支持 `--upgrade`：强制覆盖二进制、保留已有配置（备份为 `.bak.<时间戳>`）、重启服务。

### 时序库升级

```bash
# 1. 把新版本 VM 包放进 dist/artifacts/packages/（或用 --vm-package 指定）
# 2. 升级（覆盖二进制，数据目录保留不动）
sudo bash deploy/install-tsdb.sh --upgrade --vm-package victoria-metrics-linux-amd64-v<新版本>.tar.gz
```

### Server 升级

```bash
# 1. 准备新二进制：bash build/cross-compile.sh（或放新产物到 --dist 目录）
# 2. 升级（覆盖 monitor-server，刷新 Agent CDN，保留 server.yaml）
sudo bash deploy/install-server.sh --upgrade --tsdb-addr http://<时序库IP>:8428
```

升级后原有 `server.yaml` 会被保留（备份为 `server.yaml.bak.<时间戳>`），systemd 单元会更新并重启服务。

### Agent 升级

> **升级顺序**：Agent 自升级由 Agent 自身向 Server 自带 CDN 拉取二进制，因此**必须先将 Server 升级到目标版本**（Web 上传 upgrade 包），否则 Agent 会下载到旧版本、始终不更新。完整链路：Server 升级 → 在主机列表点「升级」或重跑 `agent-install.sh` → Agent 二进制与 Server 同源、服务名统一为 `monitor-agent`。

Server `--upgrade` 后 CDN 里的 Agent 二进制已刷新，各节点**重跑一行安装命令**即自动覆盖旧 Agent 并重启：

```bash
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- --server http://<server>:8080 [--secret <SECRET>]
```

> `agent-install.sh` 本身无"已安装跳过"逻辑，重跑即覆盖二进制 + 重启 `monitor-agent.service`，适合批量升级。

---

## 发布与分发

仓库根目录**不包含**任何编译产物（产物统一在 `dist/artifacts/` 与 `dist/release/`，已 `.gitignore`）。
将版本变更推送到 GitHub 后打 `vX.Y.Z` tag，会自动构建并发布到 GitHub Release。

### 本地构建产物

```bash
bash build/cross-compile.sh        # 编译 6 个二进制到 dist/artifacts/bin/
bash build/build-web.sh            # 构建前端到 dist/artifacts/web/
bash build/fetch-packages.sh       # 下载第三方依赖到 dist/artifacts/packages/（可选）
```

### 生成发布包（一键）

```bash
bash build/release.sh
# 输出：
#   dist/release/nebula-monitor-v{VERSION}-full.tar.gz     首次部署用，含全部资源
#   dist/release/nebula-monitor-v{VERSION}-upgrade.tar.gz  增量升级用，仅含 bin + web
```

| 包 | 用途 | 包含 |
|---|---|---|
| `-full` | 首次部署 / 全量升级 | bin + web + deploy + packages + install.sh + README + VERSION + SHA256SUMS |
| `-upgrade` | 增量升级（含 agent + manifest） | bin/server + bin/agent + web + VERSION + manifest.json + SHA256SUMS + UPGRADE.md |

### GitHub Actions 自动发布

打 `vX.Y.Z` tag 推送后，`.github/workflows/release.yml` 自动跑下列流程并把两个 tarball
作为 Release 资产上传：

```
checkout → 校验 VERSION 与 tag 一致 → setup-go/node →
cross-compile.sh → build-web.sh → fetch-packages.sh → release.sh →
action-gh-release 上传双包
```

要求：仓库 Actions 拥有 `contents: write` 权限（默认即如此）。

---

## 从源码构建

仓库**不含**预编译二进制（编译产物统一在 `dist/artifacts/`，已 `.gitignore`）。每台部署机或开发机
按需本地编译即可：

```bash
bash build/cross-compile.sh
```

产物：

```
dist/artifacts/bin/server/linux/{amd64,arm64,arm}/server
dist/artifacts/bin/agent/linux/{amd64,arm64,arm}/agent
```

> `CGO_ENABLED=0` 纯静态，无需目标机 C 工具链。server 编译不依赖 `web/dist`（前端由 server 从磁盘读取）。
> 前端 Vue 3 + Vite：`cd web && npm install && npm run build` 产出 `web/dist/`，再由 `build/build-web.sh`
> 平铺拷贝到 `dist/artifacts/web/`，部署时由 `install-server.sh` 拷到 `/etc/monitor-server/web`。
> 开发态：`cd web && npm run dev`（:5173，/api 与 /ws 代理到 :8080）。

---

## 配置说明

### Server（`server.yaml`，由 install-server.sh 生成）

| 字段 | 说明 |
|------|------|
| `mode` | `standalone` |
| `listen` | HTTP 监听地址，如 `:8080` |
| `tsdb.backend` | 时序库后端：`victoriametrics`(默认) / `mimir` / `cortex` / `thanos` / `prometheus` / `custom` |
| `tsdb.addr` | 时序库基址，如 `http://10.0.0.10:8428` |
| `tsdb.queryAddr` | 可选：查询基址（与写入端口不同时，如 Thanos/Cortex） |
| `alert` | 告警引擎（enabled / rulesFile / evalInterval） |
| `notify` | 邮件 / Webhook 渠道（敏感配置不写日志） |
| `agentAuth.enabled` | 启用 Agent 接入授权（默认 `false`） |
| `agentAuth.secret` | 授权密钥；`enabled: true` 且留空时启动自动生成 |
| `agentBinDir` | Agent 二进制分发目录（自带 CDN） |
| `agentScriptPath` | Agent 安装脚本路径（`/install/agent-install.sh`） |

#### 时序库后端

Server 写入走 `remote_write`、读取走 PromQL，兼容 PromQL 生态时序库：

| `tsdb.backend` | 写入路径 | 说明 |
|------|------|------|
| `victoriametrics`（默认） | `/api/v1/write` | 单二进制，推荐 |
| `mimir` | `/api/v1/push` | Grafana Mimir |
| `cortex` | `/api/v1/push` | Cortex（写入 `:9009` / 查询 `:8080`） |
| `thanos` | `/api/v1/receive` | Thanos Receive（写入 `:19291` / 查询 `:9090`） |
| `prometheus` | `/api/v1/receive` | 经 remote_write receiver |
| `custom` | 由 `tsdb.writePath` 指定 | 任意 PromQL 时序库 |

> 非 PromQL 时序库（InfluxDB/TimescaleDB）不在支持范围。

#### Agent 接入授权

启用 `agentAuth` 后，Agent 上报须携带 `X-Agent-Secret` 头（与 `secret` 一致），否则 401。
`install-server.sh` 的 `--agent-auth` 步骤会自动生成密钥并在摘要给出带 `--secret` 的安装命令。

### Agent（`agent.yaml`，由 agent-install.sh 生成）

| 字段 | 说明 |
|------|------|
| `serverURL` | Server 接收地址 |
| `node` | 节点名（默认 hostname） |
| `group` | 分组 |
| `secret` | 接入授权密钥（与 Server 一致） |
| `interval` | 采集间隔（秒） |
| `collectors` | 采集项开关（cpu/memory/disk/network/process/load） |

---

## API 速览

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/nodes` | 节点列表 |
| GET | `/api/v1/nodes/{name}` | 节点详情 |
| PUT | `/api/v1/nodes/{name}/group` | 修改分组 |
| DELETE | `/api/v1/nodes/{name}` | 移除节点 |
| GET/POST/DELETE | `/api/v1/groups` | 分组管理 |
| GET | `/api/v1/query/range?node=&metric=&start=&end=&step=` | 历史范围查询 |
| GET | `/api/v1/query/latest?node=&metric=` | 最新点 |
| GET | `/api/v1/processes?node=` | 进程 TOP |
| GET | `/api/v1/alerts?state=active` | 告警事件 |
| GET/POST/PUT/DELETE | `/api/v1/rules` | 告警规则 CRUD |
| GET | `/ws?topic=metrics&node=` | 实时指标（WebSocket） |
| GET | `/ws?topic=alerts` | 告警广播（WebSocket） |
| POST | `/api/v1/system/upgrade/upload` | 上传升级包（multipart, field `file`） |
| GET  | `/api/v1/system/upgrade/current` | 当前待应用升级包 |
| POST | `/api/v1/system/upgrade/apply` | 立即应用（备份+替换+重启） |
| POST | `/api/v1/system/upgrade/rollback` | 回滚到最近备份 |
| GET  | `/api/v1/system/upgrade/history` | 升级历史 |

---

## 目录结构

```
cmd/{agent,server}       Agent / Server 入口
internal/                业务代码（model / agent / server）
web/                     Vue 3 + Vite 前端源码（dist/ 由 build-web.sh 处理）
build/                   构建脚本
  cross-compile.sh         交叉编译 Go 二进制到 dist/artifacts/bin/
  build-web.sh             构建前端 Vue → dist/artifacts/web/
  fetch-packages.sh        下载第三方依赖（node/vm）到 dist/artifacts/packages/
  release.sh               组装 full + upgrade 两个 tarball
  VERSION-UPGRADE.md       版本升级流程
deploy/                  安装/部署脚本（不产出二进制）
  install-tsdb.sh          时序库安装（独立，可分机）
  install-server.sh        Server 安装（对接时序库）
  agent-install.sh         Agent 安装
  uninstall.sh             卸载（默认保留数据，--purge 清数据）
  docker/                  可选容器化部署
dist/                    编译产物（不入库）
  artifacts/               本地与发布用产物
    bin/{server,agent}/linux/<arch>/       编译后的二进制
    web/index.html + assets/               前端构建产物
    packages/                              第三方依赖（node / vm）
  release/                 release.sh 产出的 tarball
    nebula-monitor-v{VERSION}-full.tar.gz
    nebula-monitor-v{VERSION}-upgrade.tar.gz
.github/workflows/       GitHub Actions
  release.yml              打 v* tag 自动发布双包到 GitHub Release
```

---

## 许可证

内部自用，遵循项目约定。
