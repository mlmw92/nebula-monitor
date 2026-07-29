# Nebula Monitor · 服务器监控系统

基于 Go 的 C/S 架构主机监控：轻量 Agent 部署于被监控节点，定时 HTTP 上报；无状态 Server 通过 Prometheus `remote_write` 写入、PromQL 读取时序库（**默认 VictoriaMetrics**，可对接 Mimir/Cortex/Thanos），提供多节点分组管理、Web 仪表盘（实时 + 历史趋势）、数据查询 API 与阈值告警（邮件 / Webhook）。

> **设计要点**：Server 完全无状态，时序库独立持久化，重启不丢数据；时序库与 Server 可分机部署。

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
- Agent 接入授权（启用后需携带 `X-Agent-Secret`，否则 401）
- Server 自带 CDN 分发：安装脚本 `/install/agent-install.sh`，二进制 `/bin/linux/{arch}/agent`
- 离线安装包：`deploy/install-server.sh` / `agent-install.sh` / `install-tsdb.sh`
- 交叉编译 `build/cross-compile.sh`：linux amd64/arm64/arm 共 6 个二进制
- 前端构建 `build/build-web.sh`：Vue 3 + Vite，产物部署到 `/etc/monitor-server/web`

**告警**

- 阈值规则：`>` `>=` `<` `<=` `==` `!=` + 持续时长（For）+ firing/resolved 状态机 + 事件去重
- 通知渠道：邮件（SMTP）、Webhook

**前端**

- 登录、总览、主机列表（Agent 版本低于服务端时显示红点）、主机详情、告警列表、规则新增/编辑、分组管理
- **系统升级**：Web 上传 upgrade 包 → 解析版本 → 立即升级（备份+替换+重启）/ 回滚 + 升级历史；Agent 不主动推送，由管理员在主机列表手动触发
- 升级按钮提交后 15 秒冷却（显示"请等待 Ns"并禁用），防止 server 重启期间重复点击

### 路线图（未实现）

- **中间件监控**：Redis / MySQL / PostgreSQL / MongoDB / Kafka / Nginx / Docker / Kubernetes
- **更多通知渠道**：钉钉 / 飞书 / 企业微信 / Slack / Telegram
- **告警增强**：静默 / 维护期、抑制与分组
- **可观测性增强**：自定义仪表盘、指标自动发现、历史数据导出
- **多用户与权限**：SSO 登录、角色权限管理

---

## 快速开始

### 首次部署（full 包）

下载 full 包（首次部署用），解压后 `sudo ./install.sh` 进入交互菜单：

```bash
curl -fsSL -O https://github.com/<org>/<repo>/releases/download/v<VERSION>/nebula-monitor-v<VERSION>-full.tar.gz
tar -xzf nebula-monitor-v<VERSION>-full.tar.gz
cd nebula-monitor-v<VERSION>-full
sudo ./install.sh

# 菜单选项：【1】安装 server  【2】安装 agent  【3】安装 VictoriaMetrics
#          【4】卸载 server  【5】卸载 agent  【6】卸载 VictoriaMetrics  【7】卸载全部
```

### 非交互 / 自动化

```bash
# 安装 server（对接已有时序库）
sudo ./install.sh server --yes --tsdb-addr http://<tsdb>:8428

# 安装 agent（被监控节点，从 Server CDN 拉取二进制）
sudo ./install.sh agent --yes --server http://<server>:8080 [--secret <KEY>]

# 独立安装 VictoriaMetrics
sudo ./install.sh vm --yes

# 卸载（默认保留数据；加 --purge 清数据）
sudo ./install.sh uninstall --all --yes
```

### 直接调用底层脚本

`install.sh` 内部就是按选项调用 `deploy/` 下的脚本，适合嵌入 CI：

```bash
# 部署时序库
sudo bash deploy/install-tsdb.sh --yes [--backend victoriametrics|mimir|cortex|thanos] [--docker]

# 部署 Server
sudo bash deploy/install-server.sh --yes --tsdb-addr http://<tsdb>:8428

# 被监控节点安装 Agent（从 Server CDN 拉）
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- --server http://<server>:8080 [--secret <KEY>]

# 卸载
sudo bash deploy/uninstall.sh --all --yes
```

---

## 升级

### Server 升级（Web 端推荐）

在仪表盘「系统升级」页上传 `nebula-monitor-v<VERSION>-upgrade.tar.gz`，自动解析 `manifest.json` 显示新版本信息（版本号、各组件架构/大小/SHA256），点击「立即升级」完成备份 → 替换 → 重启。同时也支持 `--upgrade` 脚本方式：

```bash
sudo bash deploy/install-server.sh --upgrade --tsdb-addr http://<tsdb>:8428
```

### Agent 升级

> **升级顺序**：Agent 自升级由 Agent 自身向 Server CDN 拉取二进制，**必须先升级 Server 到目标版本**，否则 Agent 会下载到旧版本始终不更新。

Server 升级后 CDN 里的 Agent 二进制已刷新。在「主机列表」点击「升级」按钮，或重跑安装命令即可覆盖并重启：

```bash
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- --server http://<server>:8080 [--secret <KEY>]
```

服务名统一为 `monitor-agent`（systemd）；重跑即可覆盖二进制并重启，适合批量升级。

### 时序库升级

```bash
sudo bash deploy/install-tsdb.sh --upgrade --vm-package victoria-metrics-linux-amd64-v<NEW>.tar.gz
```

---

## 构建与发布

仓库根目录**不包含**编译产物（产物统一在 `dist/`，已 `.gitignore`）。

### 本地构建

```bash
bash build/cross-compile.sh        # 编译 6 个二进制 → dist/artifacts/bin/
bash build/build-web.sh            # 构建前端 → dist/artifacts/web/
bash build/fetch-packages.sh       # 下载 node + vm → dist/artifacts/packages/（可选）
```

产物路径：

```
dist/artifacts/bin/server/linux/{amd64,arm64,arm}/server
dist/artifacts/bin/agent/linux/{amd64,arm64,arm}/agent
dist/artifacts/web/index.html + assets/
```

> `CGO_ENABLED=0` 纯静态，无需目标机 C 工具链。前端构建产物由 `build-web.sh` 平铺拷贝到 `dist/artifacts/web/`，部署时由 `install-server.sh` 拷到 `/etc/monitor-server/web`。开发态：`cd web && npm run dev`（:5173，/api 与 /ws 代理到 :8080）。

### 生成发布包

```bash
bash build/release.sh

# 产出：
#   dist/release/nebula-monitor-v<VERSION>-full.tar.gz      首次部署，含全部资源
#   dist/release/nebula-monitor-v<VERSION>-upgrade.tar.gz   增量升级，含 bin + web
```

| 包 | 用途 | 包含 |
|---|---|---|
| `-full` | 首次部署 / 全量升级 | bin + web + deploy + packages + install.sh + README + VERSION + SHA256SUMS |
| `-upgrade` | Web 端增量升级 | bin/server + bin/agent + web + VERSION + manifest.json + SHA256SUMS + UPGRADE.md |

### GitHub Actions 自动发布

打 `vX.Y.Z` tag 推送后，`.github/workflows/release.yml` 自动：

```
checkout → 校验 VERSION 与 tag 一致 → setup-go/node →
cross-compile.sh → build-web.sh → fetch-packages.sh → release.sh →
上传双包到 GitHub Release
```

---

## 配置说明

### Server（`server.yaml`）

| 字段 | 说明 |
|------|------|
| `mode` | `standalone` |
| `listen` | HTTP 监听地址，如 `:8080` |
| `tsdb.backend` | 时序库后端：`victoriametrics`(默认) / `mimir` / `cortex` / `thanos` / `prometheus` / `custom` |
| `tsdb.addr` | 时序库基址，如 `http://10.0.0.10:8428` |
| `tsdb.queryAddr` | 可选：查询基址（与写入端口不同时，如 Thanos/Cortex） |
| `alert` | 告警引擎（enabled / rulesFile / evalInterval） |
| `notify` | 邮件 / Webhook 渠道 |
| `agentAuth.enabled` | 启用 Agent 接入授权（默认 false） |
| `agentAuth.secret` | 授权密钥；启用且留空时启动自动生成 |
| `agentBinDir` | Agent 二进制分发目录（自带 CDN） |
| `agentScriptPath` | Agent 安装脚本路径（`/install/agent-install.sh`） |

#### 时序库后端

| `tsdb.backend` | 写入路径 | 说明 |
|------|------|------|
| `victoriametrics`（默认） | `/api/v1/write` | 单二进制，推荐 |
| `mimir` | `/api/v1/push` | Grafana Mimir |
| `cortex` | `/api/v1/push` | Cortex（写入 `:9009` / 查询 `:8080`） |
| `thanos` | `/api/v1/receive` | Thanos Receive（写入 `:19291` / 查询 `:9090`） |
| `prometheus` | `/api/v1/receive` | 经 remote_write receiver |
| `custom` | 由 `tsdb.writePath` 指定 | 任意 PromQL 时序库 |

> 非 PromQL 时序库（InfluxDB/TimescaleDB）不在支持范围。

### Agent（`agent.yaml`）

| 字段 | 说明 |
|------|------|
| `serverURL` | Server 接收地址 |
| `node` | 节点名（默认 hostname） |
| `group` | 分组 |
| `secret` | 接入授权密钥（与 Server 一致） |
| `interval` | 采集间隔（秒） |
| `collectors` | 采集项开关（cpu/memory/disk/network/process/load） |

---

## API

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
| POST | `/api/v1/system/upgrade/upload` | 上传升级包（multipart） |
| GET | `/api/v1/system/upgrade/current` | 当前待应用升级包 |
| POST | `/api/v1/system/upgrade/apply` | 立即应用 |
| POST | `/api/v1/system/upgrade/rollback` | 回滚到最近备份 |
| GET | `/api/v1/system/upgrade/history` | 升级历史 |

---

## 目录结构

```
cmd/{agent,server}        Agent / Server 入口
internal/                 业务代码（model / agent / server）
web/                      Vue 3 + Vite 前端源码
build/                    构建脚本
  cross-compile.sh          交叉编译 Go 二进制 → dist/artifacts/bin/
  build-web.sh              构建前端 → dist/artifacts/web/
  fetch-packages.sh         下载第三方依赖 → dist/artifacts/packages/
  release.sh                组装 full + upgrade tarball
deploy/                   安装/部署脚本
  install-tsdb.sh           时序库安装
  install-server.sh         Server 安装
  agent-install.sh          Agent 安装
  uninstall.sh              卸载
dist/                     编译产物（不入库）
  artifacts/                本地与发布用中间产物
    bin/{server,agent}/linux/<arch>/      编译后的二进制
    web/index.html + assets/              前端构建产物
    packages/                             第三方依赖
  release/                 release.sh 产出的 tarball
    nebula-monitor-v<VERSION>-full.tar.gz
    nebula-monitor-v<VERSION>-upgrade.tar.gz
```

---

## 许可证

内部使用，遵循项目约定。
