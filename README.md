# Nebula Monitor

<div align="center">

**基于 Go 的 C/S 架构服务器监控系统**：轻量 Agent 部署于被监控节点，定时 HTTP 上报；无状态 Server 通过 Prometheus `remote_write` 写入、PromQL 读取时序库，提供多节点分组管理、Web 仪表盘（实时 + 历史趋势）、数据查询 API 与阈值告警。

[![Go Report Card](https://goreportcard.com/badge/github.com/mlmw92/nebula-monitor)](https://goreportcard.com/report/github.com/mlmw92/nebula-monitor)
[![License](https://img.shields.io/badge/license-Internal-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/mlmw92/nebula-monitor)](https://github.com/mlmw92/nebula-monitor/releases)
[![Platform](https://img.shields.io/badge/platform-linux--amd64%20%7C%20arm64%20%7C%20arm-lightgrey.svg)](https://github.com/mlmw92/nebula-monitor/releases)

</div>

## 特性

- **主机监控**：CPU / 内存 / 负载 / 磁盘 / 网络 / 进程 / 硬件信息采集，支持节点分组与离线告警
- **多时序库后端**：默认 VictoriaMetrics，可对接 Mimir / Cortex / Thanos / Prometheus（`remote_write`）
- **中间件监控**：Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / Kubernetes / MongoDB / FastDFS 十类组件（直连 + exporter 双模式）
- **Web 仪表盘**：实时数据、历史趋势、数据大屏、Nginx 访问分析（含请求来源地理分布）
- **服务拨测**：HTTP / HTTPS / TCP / ICMP 拨测，含 SSL 证书到期告警
- **阈值告警**：邮件 / Webhook / 钉钉 / 飞书 / 企业微信，支持静默、维护窗口、告警升级、告警抑制与分组
- **网闸代理模式**：Edge / Hub mTLS 隧道穿透网闸，单端口、断线重连、内存缓冲
- **一键部署与升级**：Server + Agent 离线 / 在线安装，Web 端系统升级与独立 IP 地理库热更新

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

> **网闸场景**：两个网区经网闸隔离时，可在两侧各部署一个代理模式 Agent（Edge/Hub）构成受控 TLS 隧道，使采集 Agent 的上报数据穿透网闸到达 Server。详见下文「网闸代理部署」章节。

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
- **网闸代理模式（v1.13.0+）**：Agent 二进制支持 `mode=collect|edge|hub` 三种运行模式；edge/hub 构成网闸双侧 TLS 隧道，mTLS 双向校验，单端口穿透；含连接池、断线重连（指数退避）、内存缓冲（断连期间请求入队、恢复后补发）、自监控指标（`proxy_*`）
- **添加主机引导（Web）**：「主机列表 → 添加主机」抽屉，按场景生成安装方式；直连场景自动生成含密钥的一行命令；网闸代理场景支持 TLS 证书「自动生成（--tls-auto）」或手动指定，生成 Hub/Edge 两侧安装命令与 agent.yaml 模板

**告警**

- 阈值规则：`>` `>=` `<` `<=` `==` `!=` + 持续时长（For）+ firing/resolved 状态机 + 事件去重
- 告警抑制与分组：抑制按告警依赖关系减少重复通知（源告警 firing 时抑制同标签目标告警）；分组按标签聚合通知（group_wait 首次等待 + group_interval 汇总）；均支持 Web 热生效配置（详见「告警中心」章节）
- 场景化规则：内置「主机离线 / 中间件服务离线 / 数据库主从切换 / 集群状态损坏」四类场景规则类型，无需关心底层指标细节即可配置常见故障告警
- 规则静默：支持按规则设置静默开关 + 截止时间（到期自动解除），静默期间跳过评估
- 周期静默时段：按星期 + 时间区间（支持跨天）设置重复静默时段，与单次静默、全局维护窗口构成三层静默
- 告警升级策略：告警持续未恢复超过设定时长后升级级别 / 切换通知渠道，并按间隔重复提醒
- 维护窗口：全局维护期，期间抑制所有告警通知
- 通知渠道：邮件（SMTP，多收件人）、Webhook（多地址）、钉钉、飞书、企业微信（自定义机器人 Webhook，均支持配置多个群，钉钉/飞书支持加签，钉钉/企业微信支持 @ 多人）

**前端**

- 登录、总览、主机列表（Agent 版本低于服务端时显示红点）、主机详情（含端口状态区块）、告警列表、规则新增/编辑（含静默设置）、分组管理
- **中间件监控**：独立一级菜单，Tab 布局（一种中间件一个 Tab）。已实现 Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / Kubernetes / MongoDB / FastDFS 十个 Tab：统计概览卡片 + 实例列表表格 + 实例详情抽屉（多趋势图）。
- **数据大屏**：`/screen` 全屏自适应数据分析视图，三大板块 Tab 切换：主机监控（CPU/内存/磁盘/网络实时仪表盘 + 集群趋势 + 主机健康列表）、中间件监控（8 类组件健康度总览 + 关键参数趋势 + 实例下钻）、Nginx 分析（访问量/流量趋势、状态码分布、Top URI/Top IP 排行、请求来源地理分布中国/世界地图热力散点与动线）；顶部 KPI 指标卡、底部实时告警滚动区，模块显隐可配置并持久化。主机 Tab 已支持离线主机整行高亮标识、磁盘集群均值（仅统计在线主机）与磁盘读写 IOPS、load1/5/15、网络丢包率、TCP 重传率展示；上述新增指标（磁盘 IOPS、网络丢包率、TCP 重传率、load5/load15）需先将各节点 Agent 升级到相同版本并重分发二进制后方可采集，未升级前对应字段显示为空或 0。
- **服务拨测**：拨测任务管理页面（新增/编辑/删除/启用切换），实时展示拨测结果（在线状态/延迟/证书到期）。
- **巡检报告**：报告生成页面（日报/周报/月报选择 + 即时生成 + 下载 + 历史记录）。
- **系统升级**：Web 上传 upgrade 包 → 解析版本 → 立即升级（备份+替换+重启）/ 切换到指定版本 + 升级历史；Agent 不主动推送，由管理员在主机列表手动触发。页面另设独立的「IP 地理库」入口，可单独上传 ip2region 库文件即时生效，不重启服务、不影响其他组件
- 升级按钮提交后 15 秒冷却（显示"请等待 Ns"并禁用），防止 server 重启期间重复点击
- **Agent 部署引导**：`/setup` 页生成直连安装命令 + 网闸代理向导 + 代理节点状态表

**告警场景与规则配置**

告警中心在阈值规则之外，内置四类开箱即用的场景规则。进入「告警中心 → 规则」点击「新建规则」，选择规则类型并在「从模板新建」中可一键载入对应模板（阈值、主机离线、各中间件离线、主从切换、集群损坏）。下文说明各类场景的**触发条件、告警级别、通知方式、处理建议**以及通用配置项。

**1. 主机离线**

- 触发条件：节点心跳超时，服务端将其标记为 `offline`（离线持续时长可配，默认 5 分钟）即触发。该检测直接读取节点内存状态，**不依赖指标上报**，因此主机宕机、Agent 进程退出、网络中断均可覆盖。
- 告警级别：默认「紧急」。
- 通知方式：规则通知渠道（默认全部已启用渠道）。
- 处理建议：
  1. 检查该主机是否宕机、断电或网络分区；
  2. 登录主机确认 Agent 进程（`monitor-agent`）是否运行，必要时执行 `systemctl status monitor-agent` / `systemctl restart monitor-agent`；
  3. 检查 Agent 到 Server 的连通性（端口、防火墙、网闸代理）；
  4. 若为主动维护，请在维护窗口或静默时段内操作。

**2. 中间件 / 服务离线**

- 触发条件：基于各中间件存活指标 `*_instance_up`（如 `mysql_instance_up`、`redis_instance_up`、`nginx_instance_up`、`kafka_instance_up`、`docker_container_up`、`k8s_cluster_up`），指标值为 `0` 表示离线（判定阈值可配，默认 `<= 0.5`），持续时长可配（默认 3 分钟）。仅覆盖该中间件实例可达性，主机整体离线由「主机离线」规则兜底。
- 告警级别：默认「紧急」。
- 通知方式：规则通知渠道。
- 处理建议：
  1. 登录对应节点，确认中间件进程是否存活（`systemctl status <服务>` / 端口监听）；
  2. 查看中间件自身错误日志与资源占用（内存、句柄、磁盘）；
  3. 检查 Agent 采集是否异常（采集超时、认证失败），必要时在中间件 Tab 查看实例详情；
  4. 若为计划内重启，请在维护窗口或静默时段内操作。

**3. 数据库主从切换**

- 触发条件：监测数据库实例的 `role` 标签（`PRIMARY/SECONDARY` 或 `master/slave`，来自 `*_instance_up` 的 role 标签）变化。检测到角色切换即触发事件型告警（下个评估周期角色稳定后自动恢复，重复切换会重复触发）。支持 `cluster`（MySQL Group Replication / InnoDB Cluster）与 `replication`（PostgreSQL 流复制等）拓扑。无需改动 Agent，复用时序库既有的 role 标签。
- 告警级别：默认「警告」（切换可能在故障或运维时触发，需重点关注但非必然故障）。
- 通知方式：规则通知渠道。
- 处理建议：
  1. 确认切换是计划内运维还是故障导致（如原主库宕机）；
  2. 检查新主库健康度、复制状态与延迟；
  3. 确认客户端连接配置是否已指向新主库，应用流量是否恢复正常；
  4. 若为脑裂风险（旧主库未真正下线），按集群官方流程隔离并重新加入。

**4. 集群状态损坏**

- 触发条件：按集群 / 分组聚合各实例角色，出现「无主」（缺少 PRIMARY / 主库）或「多主」（检测到多于一个 PRIMARY，疑似脑裂）即告警（持续时长可配，默认 2 分钟）。Kubernetes 集群可基于 `k8s_cluster_up` 判定。
- 告警级别：默认「紧急」。
- 通知方式：规则通知渠道。
- 处理建议：
  1. 无主：检查网络分区、仲裁 / 多数派是否存活，按集群官方流程选主或强制恢复；
  2. 多主（脑裂）：立即停止写入、隔离异常节点，确认数据一致性后再重新加入集群；
  3. 通过中间件 Tab 的实例列表查看各节点真实角色与复制状态；
  4. 恢复后确认告警自动解除；如反复出现，应排查节点间网络与时钟。

**通用配置项**

- 自定义阈值：阈值规则可调整指标、运算符、阈值与持续时长；服务离线规则可调整离线判定阈值；其余场景的持续时长、级别、应用范围（全部 / 指定主机、支持分组）均可配置。
- 静默时段（三层）：① 规则级单次静默（开关 + 截止时间，到期自动解除）；② 规则级周期静默时段（按星期 + 时间区间，支持跨天，如工作日 02:00–06:00）；③ 全局维护窗口（抑制所有告警通知，事件仍记录）。三层互不冲突，命中任一即跳过评估或通知。
- 告警升级策略：开启后，告警持续未恢复超过「升级时间」（分钟）即升级——可提升告警级别（如警告→紧急）、切换为「升级渠道」通知，并按「重复提醒」间隔（分钟，0 为不重复）持续提醒。升级通知同样受维护窗口与静默约束。建议对「主机离线」「集群状态损坏」等紧急场景配置较短升级时间（如 10–15 分钟）与重复提醒。
- **告警抑制（Inhibition）**：当存在满足条件的「源告警」处于 firing 状态时，自动抑制与之等价标签相同的「目标告警」的通知，避免告警风暴；被抑制的告警事件仍会正常记录并在告警列表标记「已抑制」。规则可配置：源/目标的范围（规则 ID 包含、告警级别、指标正则）与「需相同标签」（如 `host`、`instance`），命中后目标告警不发送通知。源告警恢复后，此前被抑制的告警若不再满足抑制条件会自动补发通知。配置入口：「告警中心 → 高级设置 → 抑制与分组 → 抑制规则」，保存后热生效，无需重启。
- **告警分组（Grouping）**：将相同分组标签的告警合并为一组，在首个告警到达后等待「首次等待（group_wait）」再发送，之后每「重复间隔（group_interval）」汇总一次，减少通知频率。分组标签可多选（如 `name`、`host`、`severity`）。开启入口同上「告警分组」开关，保存后热生效。分组仅影响通知聚合方式，不改变告警事件本身。

> 抑制 / 分组与三层静默、维护窗口的区别：静默与维护窗口按「时间 / 范围」跳过评估或通知；抑制按「告警间的依赖关系」减少重复通知；分组按「标签」聚合通知。四者互补，可同时生效。

**中间件监控（Agent 采集）**

| 指标 | 说明 |
|------|------|
| Redis | 支持单机/主从/哨兵/集群四种部署模式；Agent 内置直连（RESP 协议）+ Prometheus exporter 双采集模式；实例密码仅存 Agent 本地不上报；上报 redis_instance_up 存活状态 + 20+ 核心指标（连接数/内存/OPS/命中率/键空间/复制延迟/哨兵/集群等） |
| MySQL | `go-sql-driver/mysql` 直连 `SHOW GLOBAL STATUS` / `SHOW SLAVE STATUS` + Prometheus exporter 双采集模式；密码仅存本地；支持 standalone / replication / cluster 拓扑（cluster 指 MySQL Group Replication / InnoDB Cluster，多节点多主，按相同 name 分组展示）；22 项核心指标（QPS/TPS/连接数/InnoDB 缓冲池/慢查询/主从延迟/复制状态等） |
| PostgreSQL | `lib/pq` 直连 `pg_stat_database` / `pg_stat_replication` 等系统视图 + exporter 双模式；密码仅存本地；20 项指标（连接数/事务提交回滚/缓存命中率/死锁/复制延迟/WAL 写入量等） |
| Nginx | HTTP GET `stub_status` 页面解析 + VTS exporter 双模式；10 项指标（活跃连接/接受/处理/请求数/读写等待） |
| Kafka | `sarama` AdminClient 直连（Broker/Topic/ConsumerGroup）+ JMX exporter 双模式；22 项指标（Broker 吞吐/ISR 收缩/Topic 分区/Consumer Lag/请求队列等） |
| Docker | Docker Engine API（`/var/run/docker.sock` Unix Socket）自动发现容器 + stats 资源采集；16 项指标（容器 CPU/内存/网络/磁盘 IO/运行状态/重启次数/镜像数） |
| RocketMQ | 推荐 **exporter 模式**（RocketMQ Prometheus Exporter 拉取 `/metrics`，4.x/5.x 通用）；内置 HTTP API 直连模式仅在你环境将 `/rocketmq/httpapi/` 以标准 HTTP 暴露时可用（标准 5.x 的 NameServer/Proxy 为二进制协议，直连会 EOF）；18 项指标（Broker TPS/消息积压量/消费延迟/生产者消费者 TPS/今日生产消费总数等） |
| Kubernetes | 标准库 net/http 直连 apiserver REST（kubeconfig/token 认证，不引入 client-go）+ kube-state-metrics/metrics-server exporter 双模式；基于标准 K8s API，兼容标准 K8s 与 k3s；采集单元为整个集群；集群健康/Node 状态与资源/Deployment・StatefulSet・DaemonSet 副本就绪/Pod 总数与异常统计；凭据仅存 Agent 本地不上报 |
| MongoDB | 官方 Go Driver 直连（执行 `serverStatus` / `dbStats` / `hello` / `replSetGetStatus`）+ mongodb_exporter 双模式；密码仅存本地（经 `authSource` 认证）；支持 standalone / replicaset / sharded 拓扑；指标含连接数/内存/ OPS（insert·query·update·delete·command）/ 库数据·存储·对象·索引大小 / 副本集角色（PRIMARY·SECONDARY·ARBITER·MONGOS）/ 复制延迟等 |
| FastDFS | 社区 fastdfs_exporter 拉取 `/metrics` + TCP 端口存活探测双模式；exporter 指标含 Group/Storage 数量、在线 Storage、总/空闲/已用空间、Trunk 空闲、磁盘读写、网络收发等；无 exporter 时仅提供 tracker（22122）/ storage（23000）端口存活状态 |

**服务拨测**

- HTTP / HTTPS / TCP / ICMP 四种拨测类型
- 定时调度器，支持自定义检测间隔与超时
- SSL/TLS 证书到期时间检测：HTTPS 任务自动读取对端证书剩余天数
- **SSL 证书过期告警**：HTTPS 任务按「证书预警(天)/证书告警(天)」两个阈值触发告警（默认 ≤30 天「警告」、≤7 天「紧急」，已过期视为「紧急」）；证书更新后自动恢复；持续未恢复时每日重复提醒；沿用任务的「通知渠道」并受全局维护窗口约束。阈值在任务编辑表单的 HTTPS 类型下配置。
- 任务 CRUD API + Web 可视化管理页面
- 拨测结果指标 `dial_test_up` / `dial_test_latency` / `dial_test_cert_expiry` 写入时序库

**端口监控**

- Agent 内置 TCP 端口存活检测，可配置目标端口列表
- 上报 `port_up` / `port_latency` 指标
- Web 主机详情页端口状态区块（在线/离线/延迟展示）

**巡检报告**

- 日报 / 周报 / 月报 HTML 模板渲染
- 主机资源趋势（CPU/内存/磁盘）+ SLA 可用性统计
- Web 端报告生成/下载/历史查询

### 路线图（未实现）

- **更多中间件**：MariaDB、RabbitMQ 等
- **可观测性增强**：自定义仪表盘、指标自动发现、历史数据导出
- **多用户与权限**：SSO 登录、角色权限管理
- **代理增强**：磁盘缓冲（长时间断网容灾）、请求批量合并（Server 减负）、主备双实例故障切换

**数据大屏（主机 Tab）未实现项**

- **主机列表行内历史趋势对照**：各主机行内暂无迷你趋势图（sparkline），也不支持下钻查看 24 小时历史曲线，仅展示实时值。
- **磁盘读写 await 延迟**：主机磁盘监控暂未展示读写 await（平均 I/O 等待）延迟。当前采集端依赖的系统库（gopsutil）未暴露该字段，需更换采集方式后方可支持；已实现的磁盘指标为容量/已用/使用率与读写 IOPS。

---

## 目录

- [特性](#特性)
- [架构](#架构)
- [功能清单](#功能清单)
- [快速开始](#快速开始)
- [升级](#升级)
- [构建与发布](#构建与发布)
- [配置说明](#配置说明)
- [网闸代理部署](#网闸代理部署)
- [API](#api)
- [目录结构](#目录结构)
- [贡献](#贡献)
- [问题反馈](#问题反馈)
- [许可证](#许可证)

## 快速开始

### 首次部署（full 包）

下载 full 包（首次部署用），解压后 `sudo ./install.sh` 进入交互菜单：

> 最新发布包见 [GitHub Releases](https://github.com/mlmw92/nebula-monitor/releases)。

```bash
VERSION=1.17.27
curl -fsSL -O https://github.com/mlmw92/nebula-monitor/releases/download/v${VERSION}/nebula-monitor-v${VERSION}-full.tar.gz
tar -xzf nebula-monitor-v${VERSION}-full.tar.gz
cd nebula-monitor-v${VERSION}-full
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

### 切换到指定版本

每次通过 Web 端上传的升级包都会被自动归档（保留最近 5 个版本，可在 `server.yaml` 的 `upgrade.archiveKeep` 调整）。在「系统升级」页「升级历史」表格中，凡是仍存在于归档且非当前运行版本的记录，其行尾都带有「切换」按钮：点击后服务端会解压该版本的归档包并按其 `manifest.json` 重新执行备份 → 替换 Server/Web/Agent → 重启，使运行版本变为该归档版本（既可切换回旧版本，也可从旧版本切换回较新的版本）。切换同样会生成备份，因此可继续向后切换或回退。当前运行版本对应的「切换」按钮为禁用状态。

> 归档包位于升级目录下的 `archive/` 子目录，记录保存在 `archive.json`；归档文件缺失的版本不会在列表中展示。

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

### IP 地理库更新

Nginx 访问分析与数据大屏的「请求来源地理分布」依赖 IP 地理库（ip2region v4，IPv4）。程序内置一份库，可在不重新打包、不升级系统的前提下单独更新：

1. 进入仪表盘「系统升级」页，找到底部的「IP 地理库」区块；
2. 准备新的 `.xdb` 文件：从 ip2region 官方仓库（https://github.com/lionsoul2014/ip2region ）的 `data` 目录获取最新的 `ip2region.xdb`（需为 v4 格式），保留 `.xdb` 后缀即可上传；
3. 上传该文件（拖拽或点击）；
4. 上传后服务端会先校验文件格式与样本 IP 查询结果，通过后立即热加载生效，无需重启 `monitor-server`，也不影响 Server / Web / Agent 组件。

该区块同时提供：

- **样本查询**：展示固定样本 IP 的解析结果，用于确认库是否正常工作；
- **测试查询**：输入任意 IP 查看解析出的国家 / 省份 / 城市；
- **恢复程序内置库**：删除已上传的库文件，回退到随程序发布的版本。

库文件存放路径由 `server.yaml` 的 `geoipFile` 指定，默认 `/var/lib/monitor-server/geoip/ip2region_v4.xdb`。该文件缺失或损坏时自动使用内置库，不影响服务启动。

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
| `notify` | 邮件 / Webhook / 钉钉 / 飞书 / 企业微信渠道（**仅首次初始化用**；运行时以独立 `notifyFile` 为准，可在 Web 后台「通知配置」页修改） |
| `notifyFile` | 通知配置独立文件路径（默认 `/etc/monitor-server/notify.yaml`）；Web 端修改写入此处并热加载 |
| `agentAuth.enabled` | 启用 Agent 接入授权（默认 false） |
| `agentAuth.secret` | 授权密钥；启用且留空时启动自动生成 |
| `agentBinDir` | Agent 二进制分发目录（自带 CDN） |
| `agentScriptPath` | Agent 安装脚本路径（`/install/agent-install.sh`） |
| `dialtestFile` | 拨测任务配置文件（默认 `/etc/monitor-server/dialtest.yaml`） |
| `auth.password` | Web 登录密码。存储为**国密 SM3 加盐哈希**（形如 `sm3:<salt>:<hash>`）；旧明文配置在 Server 启动时自动迁移为哈希并写回，无需手动处理。可在 Web 端「系统设置 → 修改密码」更改。 |
| `reportDir` | 报告存储目录（默认 `/var/lib/monitor-server/reports`） |

> **Web 通知配置**：登录后在「通知配置」页可视化配置邮件 / Webhook / 钉钉 / 飞书 / 企业微信，支持多收件人、多群（多 Webhook 地址）、@ 成员；保存即写入独立 `notifyFile` 并热加载，无需重启。敏感字段（SMTP 密码、机器人加签密钥）读取时脱敏，留空表示不修改。

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
| `mode` | 运行模式：`collect`(默认,采集) \| `edge`(网闸区A边界代理) \| `hub`(网闸区B边界代理)；详见「网闸代理部署」 |
| `serverURL` | Server 接收地址（collect 模式）；Edge 模式填 Hub 的 HTTPS 地址；Hub 模式填真实 Server 地址 |
| `node` | 节点名（默认 hostname） |
| `group` | 分组 |
| `secret` | 接入授权密钥（与 Server 一致） |
| `interval` | 采集间隔（秒）；代理模式下用于自监控指标上报周期 |
| `proxy` | 代理模式配置（mode=edge/hub 时生效），见下表 |
| `collectors` | 采集项开关（cpu / memory / disk / network / process / load / redis / mysql / postgres / nginx / nginxLog / kafka / docker / rocketmq / k8s / mongodb / fastdfs / port） |
| `redisInstances` | Redis 实例连接配置列表（数组，密码仅存本地不上报） |
| `mysqlInstances` | MySQL 实例连接配置列表（数组，密码仅存本地不上报） |
| `postgresInstances` | PostgreSQL 实例连接配置列表（数组，密码仅存本地不上报） |
| `mongoInstances` | MongoDB 实例连接配置列表（数组，密码仅存本地不上报） |
| `fastdfsInstances` | FastDFS 实例连接配置列表（数组，无密码） |
| `nginxInstances` | Nginx 实例连接配置列表（数组） |
| `kafkaInstances` | Kafka 实例连接配置列表（数组） |
| `dockerInstances` | Docker 实例连接配置列表（数组） |
| `rocketmqInstances` | RocketMQ 实例连接配置列表（数组） |
| `k8sInstances` | Kubernetes 集群连接配置列表（数组，kubeconfig/token 仅存本地不上报） |
| `portChecks` | TCP 端口存活检测列表（数组），如 `["80","443","3306"]`，开启 `collectors.port` 后生效 |

**代理模式配置（`proxy` 字段，mode=edge/hub 时生效）**

| 字段 | 模式 | 说明 |
|------|------|------|
| `listen` | edge/hub | 监听地址（Edge 默认 `:18080` 本地汇聚口；Hub 默认 `:8443` TLS 监听口） |
| `hubAddr` | edge | Hub 地址 `host:port`（如 `10.0.0.2:8443`），Edge 主动拨出 TLS 隧道至此 |
| `serverURL` | hub | 真实 Server 地址（如 `http://127.0.0.1:8080`），Hub 转发请求至此 |
| `tlsCert` | edge/hub | TLS 证书文件路径（mTLS 双向校验） |
| `tlsKey` | edge/hub | TLS 私钥文件路径 |
| `tlsCa` | edge/hub | CA 证书文件路径（用于校验对端证书） |
| `bufferSize` | edge | 断连期间内存缓冲条数，默认 1000；满时丢弃最旧请求并计数 |
| `poolSize` | edge | 到 Hub 的并发隧道连接数，默认 2 |

#### 通过命令一键配置（推荐）

安装 Agent 后，在任何被监控节点上执行以下命令，按交互引导填写实例即可（无需手编 yaml）：

```bash
# 在线方式（从 Server CDN 拉取脚本）
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- redis
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- mysql
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- postgres
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- nginx
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- kafka
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- rocketmq
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- k8s
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- mongodb
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- fastdfs

# 或直接调用本机已安装的脚本
bash /etc/monitor-agent/agent-install.sh redis
bash /etc/monitor-agent/agent-install.sh mysql
bash /etc/monitor-agent/agent-install.sh postgres
bash /etc/monitor-agent/agent-install.sh nginx
bash /etc/monitor-agent/agent-install.sh kafka
bash /etc/monitor-agent/agent-install.sh rocketmq
bash /etc/monitor-agent/agent-install.sh k8s
bash /etc/monitor-agent/agent-install.sh mongodb
bash /etc/monitor-agent/agent-install.sh fastdfs
```

向导会引导你逐个填写对应中间件的实例信息（别名、地址、账号/密码、拓扑类型等），完成后自动写入 `agent.yaml`、开启对应的 `collectors` 开关并重启 Agent。**密码仅存本机，不上报 Server。**

> 该命令仅修改配置并重启 Agent，不安装/覆盖二进制。可多次执行以更新实例列表（会覆盖对应中间件的 `xxxInstances` 段）；不同中间件子命令对应的配置段相互独立。

#### 手动编辑 YAML（高级）

也可直接编辑 `/etc/monitor-agent/agent.yaml`，字段说明如下：

```yaml
serverURL: "http://10.0.0.1:8080"
node: "web-01"
group: "default"
interval: 15

collectors:
  cpu: true
  memory: true
  disk: true
  network: true
  process: true
  load: true
  redis: true                    # ← 总开关，必须开启

redisInstances:
  # 1) 单机（无需指定 db，监控命令为实例级，与具体库无关）
  - name: "redis-standalone"
    addr: "127.0.0.1:6379"
    password: "yourpassword"     # 仅存本地，不上报 Server
    topology: "standalone"

  # 2) 主从（master / slave 各配一条 standalone，分别填各自地址）
  - name: "redis-master"
    addr: "127.0.0.1:6379"
    password: "yourpassword"
    topology: "standalone"
  - name: "redis-slave"
    addr: "127.0.0.1:6380"
    password: "yourpassword"
    topology: "standalone"

  # 3) 哨兵（addr 填任一哨兵节点，sentinelName 填 master 名，Agent 自动发现并采集 master）
  - name: "redis-sentinel"
    addr: "127.0.0.1:26379"
    password: "yourpassword"
    topology: "sentinel"
    sentinelName: "mymaster"

  # 4) 集群（addr 填任一集群节点，Agent 自动遍历全部 master 并采集）
  - name: "redis-cluster"
    addr: "127.0.0.1:7000"
    password: "yourpassword"
    topology: "cluster"

  # 5) Prometheus exporter 模式（exporterURL 填 /metrics，不走直连）
  - name: "redis-exporter"
    addr: "127.0.0.1:6379"
    password: ""
    topology: "standalone"
    exporterURL: "http://127.0.0.1:9121/metrics"
```

**Redis 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名，Web 展示用 |
| `addr` | 是 | 地址 `host:port`（直连为 Redis 地址；sentinel 为哨兵地址；cluster 为任一节点；exporter 为实例地址） |
| `password` | 否 | 认证密码，`json:"-"` 标记，**仅存 Agent 本地，绝不通过网络上报**，Web 端不可见；存储为**国密 SM4 加密**（形如 `enc:<base64>`），旧明文配置保持兼容、按明文使用，无需手动处理 |
| `db` | 否 | 预留字段，当前采集为实例级指标，与具体 DB 无关，**无需填写** |
| `topology` | 是 | `standalone` \| `replication` \| `sentinel` \| `cluster` |
| `sentinelName` | 哨兵必填 | sentinel 模式监控的 master 名称 |
| `exporterURL` | exporter 必填 | Prometheus exporter 的 `/metrics` URL；**一旦填写即走 exporter 拉取模式，忽略直连** |

#### 采集高可用：Agent 冗余部署（避免单点故障）

中间件采集依赖部署在某台机器上的 Agent。**如果只装了一台 Agent，它所在的服务器宕机或进程退出，则该 Agent 负责采集的所有 Redis 实例（含整个集群）监控数据全部中断**——Redis 本身照常运行，只是监控出现盲区（前端显示"未采集"/离线，该 Agent 节点被 Server 标为 offline）。

Redis 集群入口节点的单点问题已由 Agent 内置的入口故障转移解决（`collectCluster` 在配置入口不可达时，自动改用上次成功发现的其他存活节点作为入口）。但 **Agent 这台机器本身挂了没有任何内部机制能补**，需要冗余部署来消除：

**方案：双 Agent 同 `node` 名兜底**

1. 在另一台**独立**的机器上安装 Agent（建议与 Redis 节点分离，避免同机共损）；
2. 两台 Agent 的 `agent.yaml` 使用**完全相同的 `node` 名**（如都叫 `redis-monitor`），并配置**相同的 `redisInstances`**（含同一个集群入口）；
3. 两台 Agent 各自独立采集、独立上报。指标按 `node|instance` 聚合，前端把这台 node 下的 Redis 实例合并显示为一份，**不会翻倍**；
4. 任一 Agent 存活，Server 看到的都是同一个 node，数据不断；一台宕机，另一台无缝兜底。

> 关键点：**两个 Agent 的 `node` 名必须相同**。若配成不同名，同一 Redis 集群会在前端出现两份（两个 node 下各一份），造成重复展示。

**进阶：keepalived VIP**
如需更"生产级"的单主漂移，可在两台机器上用 keepalived 漂一个 VIP，两台 Agent 的 `node` 均配置为 VIP 对应的主机名，平时一主一备，主机宕机后 VIP 漂到备机，Server 视角节点不变（本质仍是上面的同 `node` 名方案，只是用 VIP 保证同一时刻只有一个主在写，避免双写）。

---

**MySQL 配置示例**

```yaml
collectors:
  mysql: true                     # ← 总开关，必须开启

mysqlInstances:
  - name: "mysql-master"
    addr: "127.0.0.1:3306"
    user: "monitor"
    password: "yourpassword"      # 仅存本地，不上报 Server
    topology: "standalone"        # standalone | replication | cluster

  - name: "mysql-cluster"         # Group Replication / InnoDB Cluster 多节点，name 相同即归为一组
    addr: "127.0.0.1:3306"
    user: "monitor"
    password: "yourpassword"
    topology: "cluster"

  - name: "mysql-exporter"
    addr: "127.0.0.1:3306"
    user: "monitor"
    password: ""
    topology: "standalone"
    exporterURL: "http://127.0.0.1:9104/metrics"
```

**MySQL 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名 |
| `addr` | 是 | MySQL 地址 `host:port` |
| `user` | 是 | 采集账号（建议授予 `PROCESS`、`REPLICATION CLIENT` 权限） |
| `password` | 否 | 密码，`json:"-"` 标记，仅存本地不上报 |
| `topology` | 是 | `standalone` \| `replication` \| `cluster`（cluster 指 MySQL Group Replication / InnoDB Cluster，多节点多主；同集群各节点取相同 `name` 即在前端「实例拓扑」以集群分组展示；agent-install.sh 向导暂仅提供 standalone / replication，配置 cluster 需手动编辑 agent.yaml） |
| `exporterURL` | exporter 选填 | 填写后走 mysqld_exporter 拉取模式 |

---

**PostgreSQL 配置示例**

```yaml
collectors:
  postgres: true

postgresInstances:
  - name: "pg-primary"
    addr: "127.0.0.1:5432"
    database: "postgres"
    user: "monitor"
    password: "yourpassword"      # 仅存本地，不上报 Server
    sslMode: "disable"            # disable | require | verify-ca | verify-full
    topology: "standalone"        # standalone | replication

  - name: "pg-exporter"
    addr: "127.0.0.1:5432"
    database: "postgres"
    user: "monitor"
    password: ""
    sslMode: "disable"
    topology: "standalone"
    exporterURL: "http://127.0.0.1:9187/metrics"
```

**PostgreSQL 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名 |
| `addr` | 是 | PostgreSQL 地址 `host:port` |
| `database` | 是 | 连接的数据库名 |
| `user` | 是 | 采集账号 |
| `password` | 否 | 密码，`json:"-"` 标记，仅存本地不上报 |
| `sslMode` | 否 | SSL 模式，默认 `disable` |
| `topology` | 是 | `standalone` \| `replication` |
| `exporterURL` | exporter 选填 | 填写后走 postgres_exporter 拉取模式 |

---

**Nginx 配置示例**

```yaml
collectors:
  nginx: true
  nginxLog: true   # 访问日志采集开关，用于数据大屏 Nginx 分析板块

nginxInstances:
  - name: "nginx-01"
    addr: "127.0.0.1:80"
    statusPath: "/nginx_status"   # stub_status 路径，默认 /nginx_status
    accessLog: "/var/log/nginx/access.log"   # access.log 路径；填写后采集访问日志
    logFormat: "combined_timed"   # 日志格式：combined（默认）| combined_timed（含 $request_time）

  - name: "nginx-vts"
    addr: "127.0.0.1:80"
    statusPath: "/nginx_status"
    exporterURL: "http://127.0.0.1:9913/metrics"   # VTS exporter
```

**Nginx 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名 |
| `addr` | 是 | Nginx 监听地址 `host:port` |
| `statusPath` | 否 | `stub_status` 路径，默认 `/nginx_status` |
| `exporterURL` | exporter 选填 | 填写后走 nginx-vts-exporter 拉取模式 |
| `accessLog` | 否 | access.log 文件路径；填写后按行增量采集访问日志，用于数据大屏 Nginx 分析板块（请求来源地理分布、状态码分布、Top URI/Top IP）。留空则不采集 |
| `logFormat` | 否 | access.log 格式：`combined`（默认，对应 Nginx 默认 log_format）或 `combined_timed`（追加 `$request_time`，可统计平均响应时间）。需与实际日志格式一致，否则解析失败会跳过该行 |

**Nginx access.log 配置说明**

大屏的请求来源地理分布、状态码分布、Top URI/Top IP 等分析依赖 access log 解析，需同时满足以下条件：

1. 在 Agent 配置的 `collectors` 中开启 `nginxLog: true`，否则不会创建访问日志采集器，仅配置 `accessLog` 路径不会生效。
2. Nginx 的 `log_format` 与 Agent 的 `logFormat` 保持一致。启用响应时间统计时，Nginx 侧可配置：

```nginx
http {
    log_format combined_timed '$remote_addr - - [$time_local] "$request" $status $body_bytes_sent $request_time';
    server {
        access_log /var/log/nginx/access.log combined_timed;
    }
}
```

3. 日志文件的读取权限：Agent 进程需能读取该文件（如运行用户为 root，或将该用户加入日志目录所属组）。
4. 修改配置后重启 Agent 生效：`systemctl restart monitor-agent`。首次采集从文件末尾开始增量跟踪，历史行不计入统计。

---

**Kafka 配置示例**

```yaml
collectors:
  kafka: true

kafkaInstances:
  - name: "kafka-cluster"
    addr: "127.0.0.1:9092"        # 任一 Broker 地址
    version: "2.8.0"              # 用于展示的版本号
    # exporterURL: "http://127.0.0.1:9308/metrics"  # 可选 JMX exporter
```

**Kafka 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名（集群名） |
| `addr` | 是 | 任一 Broker 地址 `host:port` |
| `version` | 否 | Kafka 版本号，仅展示用 |
| `exporterURL` | exporter 选填 | 填写后走 kafka-exporter 拉取模式 |

---

**Docker 配置示例**

```yaml
collectors:
  docker: true

dockerInstances:
  - name: "local-docker"
    addr: "unix:///var/run/docker.sock"   # 本地 Unix Socket
    # 远程 Docker 可填 tcp://10.0.0.1:2375（需开启 TCP 监听）
```

**Docker 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名 |
| `addr` | 是 | Docker Daemon 地址，本地用 `unix:///var/run/docker.sock`；远程用 `tcp://host:2375` |

---

**RocketMQ 采集模式说明**

Agent 支持两种采集模式，但**强烈建议使用 exporter 模式**：

- **exporter 模式（推荐，必选用于 RocketMQ 5.x）**：Agent 通过 `exporterURL` 拉取一个 RocketMQ Prometheus Exporter 暴露的 `/metrics`，兼容 RocketMQ 4.x / 5.x，无需 NameServer 开启任何 HTTP 接口。
- **HTTP API 直连模式（不推荐，5.x 不可用）**：Agent 直接向 `addr`（NameServer）发起 HTTP GET 到 `/rocketmq/httpapi/...` 端点。但标准 RocketMQ 的 NameServer（9876）与 Proxy（默认 8080）均只跑二进制 `Remoting` 协议、**不提供标准 HTTP 管理 API**，直连会得到 `EOF` 报错且拿不到数据。该模式仅在你的环境通过反向代理/定制把 `/rocketmq/httpapi/` 以真正 HTTP 形式暴露时才可用。

> 一句话：**RocketMQ 5.x 请务必配置 `exporterURL` 走 exporter 模式**；只填 `addr` 不填 `exporterURL` 在 5.x 上必然失败（日志报 `RocketMQ 集群信息获取失败 ... EOF`）。

**RocketMQ 配置示例（exporter 模式，推荐）**

```yaml
collectors:
  rocketmq: true

rocketmqInstances:
  - name: "rocketmq-cluster"
    addr: "127.0.0.1:9876"                       # NameServer 地址（仍必填，用于标识）
    exporterURL: "http://127.0.0.1:5557/metrics" # 指向 RocketMQ exporter 的 /metrics
```

**用户侧操作步骤（exporter 模式）**

1. 部署一个 RocketMQ Prometheus Exporter（与 Agent 同机或网络可达），把 NameServer 地址指过去：
   ```bash
   docker run -d --name rocketmq-exporter -p 5557:5557 \
     -e ROCKETMQ_NAMESRV_ADDR=127.0.0.1:9876 \
     masteryourtech/rocketmq-exporter   # 或 apache/rocketmq-exporter
   ```
   > 若使用独立部署的 NameServer 集群，把 `ROCKETMQ_NAMESRV_ADDR` 改为 `ns1:9876;ns2:9876`。
2. 在 Agent 的 `agent.yaml` 中按上面的示例填好 `exporterURL`（同时保留 `addr`）。
3. 重启 Agent（必须）：
   ```bash
   systemctl restart monitor-agent   # 或离线包：/etc/monitor-agent/monitor-agent restart
   ```
4. 查看采集日志确认无报错、且不再出现 `EOF`：
   ```bash
   journalctl -u monitor-agent -f | grep -i rocketmq
   ```
5. Web 端「中间件监控 → RocketMQ」Tab 查看概览卡片、实例列表与详情抽屉趋势图。

**RocketMQ 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 实例别名（集群名） |
| `addr` | 是 | NameServer 地址 `host:port`（用于实例标识；5.x 下不用于 HTTP 直连） |
| `exporterURL` | **5.x 必填** | 指向 RocketMQ exporter 的 `/metrics` 地址，填写后走 exporter 拉取模式 |

---

**Kubernetes 配置示例**

```yaml
collectors:
  k8s: true

k8sInstances:
  # 方式①：kubeconfig 文件认证
  - name: "prod-cluster"
    kubeconfig: "/root/.kube/config"   # 仅存本地不上报
    insecureTLS: true                  # 自签名证书跳过校验
    metricsServer: true                # 启用 metrics-server 采集节点 CPU/内存
  # 方式②：apiServer + ServiceAccount Token
  - name: "staging-cluster"
    apiServer: "https://10.0.0.2:6443"
    token: "eyJhbGciOi..."             # 仅存本地不上报
    insecureTLS: true
    metricsServer: false
    # exporterURL: "http://127.0.0.1:8080/metrics"  # 可选 kube-state-metrics
```

**MongoDB 配置示例**

```yaml
collectors:
  mongodb: true

mongoInstances:
  - name: "prod-mongo"              # 副本集多成员请使用相同 name 归为同一副本集
    addr: "10.0.0.10:27017"
    user: "monitor"                 # 无认证时留空
    password: "****"                # 仅存本地不上报
    authSource: "admin"             # 认证库
    database: "appdb"               # 采集 dbStats 的库；留空则不采集库存储/对象统计
    topology: "replicaset"          # standalone / replicaset / sharded
    # exporterURL: "http://127.0.0.1:9216/metrics"  # 可选；填了走 exporter，不再直连
```

**MongoDB 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `mongoInstances[].name` | 是 | 实例别名（replicaset 多成员请使用相同 `name` 归为同一副本集） |
| `mongoInstances[].addr` | 是 | MongoDB 地址 `host:port` |
| `mongoInstances[].user` | 否 | 认证用户名；无认证留空 |
| `mongoInstances[].password` | 否 | 密码，仅存 Agent 本地不上报 |
| `mongoInstances[].authSource` | 否 | 认证库（如 `admin`） |
| `mongoInstances[].database` | 否 | 采集 `dbStats` 的库名；留空则不采集库存储/对象统计 |
| `mongoInstances[].topology` | 否 | `standalone` / `replicaset` / `sharded`；影响角色判定展示 |
| `mongoInstances[].exporterURL` | 否 | mongodb_exporter 的 `/metrics` 地址；填了走 exporter，不再直连 |

> **提示**：MongoDB 直连模式下建议为监控账号授予 `clusterMonitor` 角色（副本集）或 `readAnyDatabase`，以保证 `serverStatus` / `dbStats` / `replSetGetStatus` 可读取。

**FastDFS 配置示例**

```yaml
collectors:
  fastdfs: true

fastdfsInstances:
  - name: "tracker-1"
    role: "tracker"                 # tracker / storage
    addr: "10.0.0.20:22122"
    group: ""                       # tracker 留空
    # exporterURL: "http://127.0.0.1:9300/metrics"  # 建议；完整指标依赖 fastdfs_exporter
  - name: "storage-1"
    role: "storage"
    addr: "10.0.0.21:23000"
    group: "group1"
```

**FastDFS 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `fastdfsInstances[].name` | 是 | 实例别名 |
| `fastdfsInstances[].role` | 是 | `tracker` / `storage` |
| `fastdfsInstances[].addr` | 是 | FastDFS 地址 `host:port`（tracker 22122 / storage 23000） |
| `fastdfsInstances[].group` | 否 | storage 所属 group 名称；tracker 留空 |
| `fastdfsInstances[].exporterURL` | 否 | fastdfs_exporter 的 `/metrics` 地址；完整空间/IO/Storage 状态依赖它，未配置时仅做端口存活探测 |

> **提示**：FastDFS 无标准直连接口，建议部署社区 `fastdfs_exporter` 并填写 `exporterURL` 以获取 Group/Storage 数量、空间使用、磁盘读写、网络收发等完整指标；仅做 TCP 存活探测时只能看到在线/离线状态。

**Kubernetes 字段说明**

| 字段 | 必填 | 说明 |
|------|------|------|
| `name` | 是 | 集群别名 |
| `apiServer` | 条件 | apiserver 地址 `https://host:6443`；留空则从 kubeconfig 取 |
| `kubeconfig` | 条件 | kubeconfig 文件路径（与 apiServer+token 二选一，仅存本地不上报） |
| `token` | 条件 | ServiceAccount Bearer Token（配 apiServer 使用，仅存本地不上报） |
| `insecureTLS` | 选填 | 跳过 apiserver 证书校验（默认 false） |
| `metricsServer` | 选填 | 启用 metrics-server 采集 Node/Pod CPU/内存使用率（默认 false） |
| `exporterURL` | exporter 选填 | 填写后走 kube-state-metrics /metrics 拉取模式 |

> 认证仅支持 token / client-cert 两种；exec/plugin（如云厂商 IAM 鉴权）不支持，需要时用 token 模式替代。
>
> **兼容标准 K8s 与 k3s**：采集基于标准 apiserver REST API，与发行版无关。k3s 使用时注意两点：
> - kubeconfig 默认在 `/etc/rancher/k3s/k3s.yaml`（非 `/root/.kube/config`），配置 `kubeconfig` 字段填此路径；
> - k3s 默认内置 metrics-server，直接开 `metricsServer: true` 即可采集节点 CPU/内存使用率；
> - k3s kubeconfig 内 server 默认为 `https://127.0.0.1:6443`，若 Agent 与 k3s 不同机，需用 `apiServer` 字段覆盖为节点真实地址。
>
> **k3s 配置示例**：
> ```yaml
> k8sInstances:
>   - name: "k3s-cluster"
>     kubeconfig: "/etc/rancher/k3s/k3s.yaml"
>     insecureTLS: true      # k3s 默认自签名证书
>     metricsServer: true    # k3s 内置 metrics-server
> ```

**部署与验证**

> 以下以 Redis 为例，其余中间件步骤完全一致，仅替换对应的 `collectors` 开关与实例段，并在 Web 端进入对应的中间件 Tab 查看。

1. 编辑 Agent 配置 `agent.yaml` 加入上述对应中间件的配置；
2. 重启 Agent（必须，中间件采集逻辑依赖 1.2.0+ 二进制）：
   ```bash
   systemctl restart monitor-agent   # 或离线包：/etc/monitor-agent/monitor-agent restart
   ```
3. 查看采集日志确认无报错：
   ```bash
   journalctl -u monitor-agent -f | grep -iE 'redis|mysql|postgres|nginx|kafka|docker|rocketmq|k8s'
   ```
4. Web 端左侧菜单「中间件监控」→ 对应中间件 Tab（Redis / MySQL / PostgreSQL / Nginx / Kafka / Docker / RocketMQ / Kubernetes）查看：概览卡片、实例列表、实例详情抽屉（多趋势图）。

> **版本一致性**：中间件监控涉及 Agent（采集）与 Server（实例聚合 + API）两端改动，两端须同时升级到同一版本（≥ 1.2.0），否则对应 Tab 无数据。
> **密码安全**：各中间件 `password` 仅在 Agent 本地用于直连，`json:"-"` 标记，不上报、不入库、Web 不可见；磁盘上以**国密 SM4 加密**形式存储（旧明文配置保持兼容，无需手动处理）。Server 登录密码以**国密 SM3 加盐哈希**存储，被攻破时无法反推原密码。

---

## 网闸代理部署

当两个网区经网闸隔离、仅有有限开放端口时，采集 Agent 无法直接访问 Server。此时在网闸两侧各部署一个代理模式 Agent（Edge/Hub）构成受控 TLS 隧道，即可穿透网闸。

### 架构

```
区 A（被监控区）                          网闸                区 B（监控中心区）
┌───────────────────────────┐            ┌────────┐      ┌──────────────────────────────┐
│ 采集 Agent（mode=collect） │            │ 仅开放 │      │  Hub Proxy（mode=hub）         │
│   └─上报→ Edge Proxy(本地) │──TLS隧道──▶│ TCP    │───▶│   └─转发→ Server(HTTP)          │
│  Edge Proxy（mode=edge）   │◀─回程通道──│ 8443   │◀───│                                │
└───────────────────────────┘            └────────┘      └──────────────────────────────┘
```

- **采集 Agent**：部署在被监控节点，`serverURL` 指向区 A 的 Edge 本地口（如 `http://127.0.0.1:18080`）
- **Edge Proxy**：区 A 边界，监听本地口汇聚采集 Agent 上报，主动拨出 TLS 隧道到 Hub
- **Hub Proxy**：区 B 边界，TLS 监听口接收 Edge 隧道，还原请求转发至真实 Server
- **Server**：无感知，收到 Hub 转发的请求与直连无异，复用现有 `/api/v1/report` 等接口，**无需改造**

### 端口规划与网闸策略

| 项目 | 规划 |
|------|------|
| 网闸开放端口 | 仅 1 个 `TCP 8443`（隧道端口） |
| 协议 | 隧道外层 TLS 1.3（mTLS 双向校验）；内层复用现有 HTTP |
| 源/目的约束 | 源=区A Edge IP，目的=区B Hub IP，端口 8443，其他一律拒绝 |
| 本地端口 | Edge 本地汇聚口 `18080`（仅区 A 内可达）；Hub 转发至 Server `8080` |
| 安全 | mTLS 双向证书校验，网闸即使放行也看不到业务数据；沿用 `X-Agent-Secret` 鉴权 |

### Web 引导部署（推荐）

登录后进入「主机列表」→「添加主机」抽屉，按场景选择：

1. **直连场景**：自动生成 `curl|bash` 一行命令（已含密钥），一键复制执行
2. **网闸场景**：切换「网闸代理」，分别填写 Hub / Edge 的监听地址等参数；TLS 证书默认「自动生成（推荐）」，点击后自动生成 Hub/Edge 安装命令与 agent.yaml 模板；也可切到「手动指定」自行填写证书路径
3. 控制台支持手动部署（见下文），代理节点状态可在「中间件/代理状态」等处查看

### 手动部署（命令行）

**1. 生成 TLS 证书（网闸两侧共用同一 CA）**

推荐**自动生成**：安装命令加 `--tls-auto`，脚本在 `/etc/monitor-agent/certs/` 生成自签 CA + Hub/Edge 节点证书，无需公网 CA、无需手动准备 openssl 命令。

```bash
# 区 B Hub 节点（自动生成证书）
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode hub --listen :8443 --server http://127.0.0.1:8080 --tls-auto --yes [--secret <KEY>]

# 把证书目录复制到对端（保证两端 ca.crt 一致，mTLS 才能校验通过）
scp -r /etc/monitor-agent/certs/ <区A Edge>:/etc/monitor-agent/certs/

# 区 A Edge 节点（脚本检测到已有 ca.crt 会复用，不再生成新 CA）
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode edge --listen :18080 --hub-addr <HUB_IP>:8443 --tls-auto --yes [--secret <KEY>]
```

如需**手动生成**（可选），在任一 Linux 用 openssl 生成一套 CA + 两套证书，分别放到 Hub / Edge 主机的 `/etc/monitor-agent/certs/`：

```bash
# CA
openssl genrsa -out ca.key 2048
openssl req -new -x509 -key ca.key -out ca.crt -days 3650 -subj "/CN=nebula-proxy-ca"

# Hub 证书
openssl genrsa -out hub.key 2048
openssl req -new -key hub.key -out hub.csr -subj "/CN=hub"
openssl x509 -req -in hub.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out hub.crt -days 3650

# Edge 证书
openssl genrsa -out edge.key 2048
openssl req -new -key edge.key -out edge.csr -subj "/CN=edge"
openssl x509 -req -in edge.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out edge.crt -days 3650
```

**2. 区 B 部署 Hub**

```bash
# 自动生成证书
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode hub --listen :8443 --server http://127.0.0.1:8080 --tls-auto --yes [--secret <KEY>]

# 或手动指定证书
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode hub --listen :8443 --server http://127.0.0.1:8080 \
  --tls-cert /etc/monitor-agent/certs/hub.crt \
  --tls-key /etc/monitor-agent/certs/hub.key \
  --tls-ca /etc/monitor-agent/certs/ca.crt --yes [--secret <KEY>]
```

服务名 `monitor-proxy-hub`。

**3. 网闸开放端口**

在网闸配置中开放 `TCP 8443`：源 IP = 区 A 的 Edge 主机 IP，目的 IP = 区 B 的 Hub 主机 IP。

**4. 区 A 部署 Edge**

```bash
# 自动生成证书
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode edge --listen :18080 --hub-addr <HUB_IP>:8443 --tls-auto --yes [--secret <KEY>]

# 或手动指定证书
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --mode edge --listen :18080 --hub-addr <HUB_IP>:8443 \
  --tls-cert /etc/monitor-agent/certs/edge.crt \
  --tls-key /etc/monitor-agent/certs/edge.key \
  --tls-ca /etc/monitor-agent/certs/ca.crt --yes [--secret <KEY>]
```

服务名 `monitor-proxy-edge`。

**5. 区 A 采集 Agent**

普通采集 Agent 安装时 `--server` 指向 Edge 本地口：

```bash
curl -fsSL http://<server>:8080/install/agent-install.sh | bash -s -- \
  --server http://<EDGE_IP>:18080 --yes [--secret <KEY>]
```

### 代理模式 agent.yaml 示例

**Edge（区 A 边界）**

```yaml
mode: "edge"
node: "edge-proxy"
group: "proxy"
secret: "<KEY>"
interval: 15
serverURL: "https://10.0.0.2:8443"   # Hub 地址（用于 Edge 上报自监控指标）

proxy:
  listen: ":18080"
  hubAddr: "10.0.0.2:8443"
  tlsCert: "/etc/monitor-agent/certs/edge.crt"
  tlsKey: "/etc/monitor-agent/certs/edge.key"
  tlsCa: "/etc/monitor-agent/certs/ca.crt"
  bufferSize: 1000
  poolSize: 2
```

**Hub（区 B 边界）**

```yaml
mode: "hub"
node: "hub-proxy"
group: "proxy"
secret: "<KEY>"
interval: 15
serverURL: "http://127.0.0.1:8080"   # 真实 Server

proxy:
  listen: ":8443"
  tlsCert: "/etc/monitor-agent/certs/hub.crt"
  tlsKey: "/etc/monitor-agent/certs/hub.key"
  tlsCa: "/etc/monitor-agent/certs/ca.crt"
  serverURL: "http://127.0.0.1:8080"
```

### 自监控指标

代理模式启动后周期上报以下指标（带 `node`/`mode` 标签），可在「Agent 部署」页或主机详情查看：

| 指标 | 说明 |
|------|------|
| `proxy_conn_active` | 当前活跃隧道连接数 |
| `proxy_forward_total` | 累计成功转发的请求数 |
| `proxy_dropped_total` | 累计丢弃的请求数（缓冲满或超时） |
| `proxy_reconnect_total` | 累计重连次数 |
| `proxy_buffer_depth` | 当前缓冲深度（Edge 断连期间） |

### 故障排查

```bash
# 查看代理服务状态
systemctl status monitor-proxy-edge
systemctl status monitor-proxy-hub

# 查看日志（隧道连接/断连/重连/鉴权失败）
journalctl -u monitor-proxy-edge -f
journalctl -u monitor-proxy-hub -f

# 常见问题：
# 1. 隧道连不上：检查网闸端口是否开放、TLS 证书是否由同一 CA 签发
# 2. 鉴权失败：检查 --secret 是否与 Server agentAuth.secret 一致
# 3. 数据丢失：调大 bufferSize，或检查网络抖动时长
```

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
| GET | `/api/v1/middleware/redis/instances` | Redis 实例列表（聚合最新状态与指标） |
| GET | `/api/v1/middleware/mysql/instances` | MySQL 实例列表（聚合最新状态与指标） |
| GET | `/api/v1/middleware/postgres/instances` | PostgreSQL 实例列表 |
| GET | `/api/v1/middleware/nginx/instances` | Nginx 实例列表 |
| GET | `/api/v1/middleware/kafka/instances` | Kafka 实例列表 |
| GET | `/api/v1/middleware/docker/instances` | Docker 实例列表 |
| GET | `/api/v1/middleware/rocketmq/instances` | RocketMQ 实例列表 |
| GET | `/api/v1/middleware/k8s/instances` | Kubernetes 集群列表（集群聚合 + Node/异常 Pod 明细） |
| GET | `/api/v1/middleware/mongodb/instances` | MongoDB 实例列表（聚合最新状态与指标） |
| GET | `/api/v1/middleware/fastdfs/instances` | FastDFS 实例列表（聚合最新状态与指标） |
| GET | `/api/v1/middleware/overview` | 中间件健康度总览（10 类组件实例数 / 在线数 / 告警数聚合） |
| GET | `/api/v1/middleware/nginx/access/summary` | Nginx 访问日志汇总（总请求 / 速率 / 状态码分布 / Top URI / Top IP） |
| GET | `/api/v1/middleware/nginx/access/geo?scope=cn\|world` | 请求来源地理分布（来源热力点 / 部署点 / 动线） |
| GET | `/api/v1/alerts?state=active` | 告警事件（含 `suppressed` / `suppressedBy` 抑制状态） |
| GET/POST/PUT/DELETE | `/api/v1/rules` | 告警规则 CRUD |
| GET/PUT | `/api/v1/inhibit` | 告警抑制规则查询 / 全量更新（热生效） |
| GET/PUT | `/api/v1/grouping` | 告警分组配置查询 / 更新（热生效） |
| GET | `/ws?topic=metrics&node=` | 实时指标（WebSocket） |
| GET | `/ws?topic=alerts` | 告警广播（WebSocket） |
| POST | `/api/v1/system/upgrade/upload` | 上传升级包（multipart） |
| GET | `/api/v1/system/upgrade/current` | 当前待应用升级包 |
| POST | `/api/v1/system/upgrade/apply` | 立即应用 |
| GET | `/api/v1/system/upgrade/archive` | 已归档（可切换）版本列表 |
| POST | `/api/v1/system/upgrade/rollback-to` | 切换到指定归档版本 |
| GET | `/api/v1/system/upgrade/history` | 升级历史 |
| GET/POST/PUT/DELETE | `/api/v1/dialtest/tasks` | 拨测任务 CRUD |
| GET | `/api/v1/dialtest/latest` | 最近拨测结果 |
| POST | `/api/v1/report/generate` | 生成巡检报告 |
| GET | `/api/v1/report/download` | 下载报告 HTML |
| GET | `/api/v1/report/history` | 报告历史列表 |
| GET/PUT | `/api/v1/maintenance` | 维护窗口查看/设置 |
| GET | `/api/v1/install-info` | Agent 安装信息（serverURL + 一行命令 + 代理配置模板） |
| GET | `/api/v1/agent/check` | Agent 接入鉴权预检（走 X-Agent-Secret，不受登录 token 影响） |
| GET | `/api/v1/proxy/status` | 代理节点状态（Edge/Hub 自监控指标聚合） |

---

## 目录结构

```
cmd/{agent,server}        Agent / Server 入口
internal/                 业务代码（model / agent / server）
  agent/
    collector/              各采集器（host / redis / mysql / postgres / nginx / kafka / docker / rocketmq / k8s / port）
    config/                 Agent 配置（含 mode/ProxyConfig 代理模式字段）
    proxy/                  代理模式核心包（tunnel/edge/hub/connpool/reconnector/buffer/monitor/tls）
    reporter/               Agent 上报逻辑
  server/
    api/                    REST / WebSocket / 中间件聚合 / 代理状态接口
web/                      Vue 3 + Vite 前端源码
  src/components/           SetupView（Agent 部署引导页）+ 各业务页面
build/                    构建脚本
  cross-compile.sh          交叉编译 Go 二进制 → dist/artifacts/bin/
  build-web.sh              构建前端 → dist/artifacts/web/
  fetch-packages.sh         下载第三方依赖 → dist/artifacts/packages/
  release.sh                组装 full + upgrade tarball
deploy/                   安装/部署脚本
  install-tsdb.sh           时序库安装
  install-server.sh         Server 安装
  agent-install.sh          Agent 安装（含 --mode edge/hub 代理模式）
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

## 贡献

欢迎参与本项目开发。

- **问题反馈**：请通过 [GitHub Issues](https://github.com/mlmw92/nebula-monitor/issues) 提交 Bug 报告或功能建议；提交前请先检索是否已有相同或类似问题。
- **代码贡献**：Fork 本仓库后从 `main` 分支切出特性分支，完成修改并自测（建议本地执行 `bash build/release.sh` 验证可正常出包）后提交 Pull Request，并在描述中说明改动目的与验证方式。
- **提交规范**：提交信息建议以 `feat:` / `fix:` / `chore:` / `docs:` 等前缀开头，一句话概括本次改动。

## 问题反馈

使用中遇到问题，可先在本文档对应章节（部署、配置、网闸代理、故障排查）查找答案。仍无法解决时，请提供以下信息以便定位：

1. 版本号（`VERSION` 文件或 Web 仪表盘「系统升级」页当前版本）；
2. 部署方式（full 包安装 / 脚本安装）与组织架构（直连 / 网闸代理）；
3. 关键日志（`journalctl -u monitor-server` / `journalctl -u monitor-agent`）；
4. 复现步骤与预期 / 实际表现。

---

## 许可证

本项目为内部使用，遵循项目约定。如需对外分发或二次开发，请先与维护者确认授权范围。
