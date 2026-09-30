# 一体化运维平台开源项目调研对比报告

日期：2026-09-30
用途：为「在现有监控平台上演进为一体化运维平台」提供选型依据；本报告只给结论与证据，不含实现方案（实现见同目录总体设计件与模块详设件）。

## 一、检索口径与数据来源

**检索方式**（全部经本机代理 `http://127.0.0.1:10808`，取证时间 2026-09-30）：

1. GitHub 搜索 API 按 star 排序取各方向头部项目：`https://api.github.com/search/repositories?q=<方向>&sort=stars&order=desc`
2. GitHub 仓库 API 逐个取元数据（star / license / language / pushed_at / archived / description / default_branch）
3. 一手文档直取：VictoriaLogs 官方文档、headlamp 与 nightingale 官方 README、JackDB/NetBox 官方文档、蓝鲸配置平台官方文档站

**可复现命令**（PowerShell，代理地址按需替换）：

```powershell
$p='http://127.0.0.1:10808'; $h=@{'User-Agent'='research'}
# 按 star 排序检索
$u='https://api.github.com/search/repositories?q=cmdb&sort=stars&order=desc&per_page=7'
(Invoke-RestMethod -Proxy $p -Headers $h -Uri $u).items | Select-Object full_name,stargazers_count,
  @{n='license';e={$_.license.spdx_id}},language,@{n='pushed';e={$_.pushed_at.ToString('yyyy-MM-dd')}}
# 单仓库元数据
Invoke-RestMethod -Proxy $p -Headers $h -Uri 'https://api.github.com/repos/TencentBlueKing/bk-cmdb' |
  Select-Object full_name,stargazers_count,default_branch,description,archived,@{n='license';e={$_.license.spdx_id}}
# 一手文档（网页）
curl.exe -s -x $p --max-time 40 'https://docs.victoriametrics.com/victorialogs/'
```

**取证限制（如实记录）**：

- Firecrawl MCP 不可达（`connect ECONNREFUSED 35.245.250.27:443`）；平台自带 `web_fetch` 对多数文档站超时（10s）。因此文档类证据改由代理直取 HTML/README。
- `bk-cmdb` 仓库 README 原文未取到（`main` 404、`master` 空响应），其能力描述来自**蓝鲸官方文档站**与**镜像 README 摘要**，已在下方逐条标注来源类型。
- `eipwork/kuboard-v3`、`eipwork/kuboard` 均返回 404，无法确认当前开源状态。

## 二、分方向对比

> 表中 star 数与最近 push 时间为 2026-09-30 快照；`许可` 列取自仓库声明的 SPDX（`NOASSERTION` 表示仓库未声明标准 SPDX，需人工确认条款）。

### 2.1 资产与配置（CMDB / 资产台账）

| 项目 | ★ | 许可 | 语言 | 最近 push | 官方定位（仓库描述原文） |
|---|---|---|---|---|---|
| netbox-community/netbox | 21620 | Apache-2.0 | Python | 2026-09-29 | The premier source of truth powering network automation. Open source under Apache 2. |
| ccfos/nightingale | 13316 | Apache-2.0 | Go | 2026-09-23 | Nightingale is to monitoring and alerting what Grafana is to visualization. |
| openspug/spug | 11085 | AGPL-3.0 | JavaScript | 2026-09-28 | Spug is a lightweight agent-free automatic operation and maintenance platform designed for small and medium-sized enterprises… (集成主机管理/批量执行/在线终端/文件管理/应用发布部署/流水线/在线任务计划/配置中心/监控/告警) |
| glpi-project/glpi | 6405 | GPL-3.0 | PHP | 2026-09-30 | GLPI is a Free Asset and IT Management Software package… |
| TencentBlueKing/bk-cmdb | 5755 | NOASSERTION | Go | 2026-09-15 | 蓝鲸智云配置平台(BlueKing CMDB) |
| opendevops-cn/opendevops | 4103 | GPL-3.0 | Python | 2026-04-12 | 企业多混合云、全球一站式 DevOps、自动化运维、完全开源的云管理平台 |
| welliamcao/OpsManage | 3595 | GPL-2.0 | Python | 2024-06-15 | 自动化运维平台: 代码及应用部署CI/CD、资产管理CMDB、计划任务管理平台… |
| guohongze/adminset | 3454 | GPL-2.0 | Python | 2025-03-16 | 自动化运维平台：CMDB、CD、DevOps、资产管理、任务编排… |
| nautobot/nautobot | 1616 | Apache-2.0 | Python | 2026-09-29 | （NetBox 派生演进，同许可） |

**可引用的一手设计依据**：

- NetBox 官方文档 Introduction：「Careful consideration has been given to the data model to ensure that it can accurately reflect a real-world network. For instance, **IP addresses are assigned not to devices, but to specific interfaces attached to a device**, and an interface may have multiple IP addresses assigned to it.」（来源：NetBox 官方文档 Introduction 页镜像）
- NetBox 官方文档 Planning：「NetBox has **purpose-built models** for racks, devices, cables, IP prefixes, VLANs, and so on.」
- 蓝鲸官方文档站（bk.tencent.com）：「蓝鲸配置平台是一款**面向应用的 CMDB**……通过提供配置管理服务，以**数据和模型相结合映射应用间的关系**，保证数据的准确和一致性；并以整合的思路推进，最终面向应用消费。」
- bk-cmdb README 摘要（来自 GitHub/Gitee 镜像，**二手转述，待核**）：「提供全新**自定义模型管理**……随时新增模型和关联关系……新增更多符合场景需要的新功能：**机器数据快照、数据自动发现、变更事件主动推送**、更加精细的权限管理、可拓展的业务拓扑。」
- nightingale 官方 README：其工具集按对象划分，可见其对象模型包含 `targets`、`busi_groups`、`datasource`、`alerts`、`mutes`、`notify_rules`、`metrics`、`logs`、`dashboards`、`users`、`roles` 等（原文列举 13 个 toolset / 74 个工具）；同时明确「Nightingale itself does not provide monitoring data collection capabilities. We recommend using **Categraf** as the collector」。

### 2.2 容器与 Kubernetes 管理

| 项目 | ★ | 许可 | 语言 | 最近 push | 状态/官方定位 |
|---|---|---|---|---|---|
| k3s-io/k3s | 34070 | Apache-2.0 | Go | 2026-09-28 | 轻量 Kubernetes 发行版 |
| aquasecurity/trivy | 38145 | Apache-2.0 | Go | 2026-09-29 | 漏洞/配置/镜像扫描 |
| rancher/rancher | 25942 | Apache-2.0 | Go | 2026-09-29 | Complete container management platform |
| kubesphere/kubesphere | 17059 | NOASSERTION | Go | 2026-07-15 | The container platform tailored for Kubernetes multi-cloud, datacenter, and edge management |
| **kubernetes-retired/dashboard** | 15415 | Apache-2.0 | Go | 2026-01-21 | General-purpose web UI for Kubernetes clusters —— **已归档（ARCHIVED）** |
| kubernetes-sigs/headlamp | 7352 | Apache-2.0 | TypeScript | 2026-09-29 | A Kubernetes web UI that is fully-featured, user-friendly and extensible |
| karmada-io/karmada | 5710 | Apache-2.0 | Go | 2026-09-28 | Multi-Cloud, Multi-Cluster Kubernetes Orchestration |
| **KubeOperator/KubeOperator** | 4975 | Apache-2.0 | Go | 2024-07-02 | **已归档（ARCHIVED）** |
| kubewall/kubewall | 1937 | Apache-2.0 | TypeScript | 2026-09-29 | Kubernetes Dashboard for Multi-Cluster Management |
| eipwork/kuboard(-v3) | — | — | — | — | **仓库 404，开源状态无法确认** |

**可引用的一手依据**：

- headlamp 官方 README 特性原文：「Vendor-independent / generic Kubernetes UI」「Works **in-cluster**, or locally as a desktop app」「**Multi-cluster**」「Extensible through **plugins**」「**UI controls reflecting user roles (no deletion/update if not allowed)**」；项目已迁入 Kubernetes SIG UI（`kubernetes-sigs` 组织），并有 terminal（exec）截图。
- `kubernetes-retired/dashboard`：仓库已归档（最近 push 2026-01-21），说明**官方通用面板路线不再演进**。

### 2.3 日志与可观测

| 项目 | ★ | 许可 | 语言 | 最近 push | 官方定位 |
|---|---|---|---|---|---|
| SigNoz/signoz | 32251 | NOASSERTION | TypeScript | 2026-09-30 | Open-source, OpenTelemetry-native observability |
| grafana/loki | 28975 | **AGPL-3.0** | Go | 2026-09-30 | Like Prometheus, but for logs. |
| apache/skywalking | 24967 | Apache-2.0 | Java | 2026-09-30 | APM, Application Performance Monitoring System |
| vectordotdev/vector | 22643 | MPL-2.0 | Rust | 2026-09-30 | A high-performance observability data pipeline. |
| openobserve/openobserve | 22201 | **AGPL-3.0** | TypeScript | 2026-09-30 | …alternative to Datadog, Splunk, and Elasticsearch with **140x lower storage costs and single binary deployment**. |
| opensearch-project/OpenSearch | 13791 | Apache-2.0 | Java | 2026-09-30 | 社区版 ES 分支 |
| quickwit-oss/quickwit | 11688 | Apache-2.0 | Rust | 2026-09-29 | Cloud-native OSS search engine for observability |
| Graylog2/graylog2-server | 8150 | NOASSERTION | Java | 2026-09-30 | Free and open log management |
| deepflowio/deepflow | 4286 | Apache-2.0 | Go | — | eBPF Observability - Distributed Tracing and Profiling |
| grafana/alloy | 3567 | Apache-2.0 | Go | 2026-09-30 | Promtail 继任者（采集/转换代理） |
| **VictoriaMetrics/VictoriaLogs** | 2331 | Apache-2.0 | Go | 2026-09-30 | Fast and easy to use database for logs, which can efficiently handle terabytes of logs |

**可引用的一手依据（VictoriaLogs 官方文档）**：

- 资源效率（原文）：「It is resource-efficient and fast. It uses **up to 30x less RAM and up to 15x less disk space** than other solutions such as **Elasticsearch and Grafana Loki**. See these benchmarks…」
- 部署形态：文档导航同时提供 **Single-node version** 与 **Cluster version**；生态包含 `vlagent`、`vlogscli`、Grafana/Perses/Bindplane/Logchef 集成，以及 **Alerting with Logs**、**Retention**（含「Retention by disk space usage / Absolute disk space limit / Percentage-based disk space limit」）。
- 查询语言：**LogsQL**（文档含「Convert Loki Queries to VictoriaLogs Queries」「SQL to LogsQL」）。
- 与现有平台的天然契合点：同属 VictoriaMetrics 生态，官方导航页即与 VictoriaMetrics 并列展示（同一套 Quick start / 单机-集群形态叙述），且其文档导航中出现 **Headlamp Kubernetes** 集成条目。

### 2.4 监控告警（与"现在的监控平台"对照）

| 项目 | ★ | 许可 | 语言 | 最近 push | 官方定位 |
|---|---|---|---|---|---|
| louislam/uptime-kuma | 91978 | MIT | JavaScript | 2026-09-30 | A fancy self-hosted monitoring tool |
| netdata/netdata | 80748 | GPL-3.0 | Go | 2026-09-30 | The fastest path to AI-powered full stack observability, even for lean teams. |
| grafana/grafana | 77001 | AGPL-3.0 | TypeScript | 2026-09-30 | The open and composable observability and data visualization platform |
| prometheus/prometheus | 66312 | Apache-2.0 | Go | 2026-09-29 | The Prometheus monitoring system and time series database. |
| apache/hertzbeat | 7412 | Apache-2.0 | Java | 2026-09-30 | An AI-powered next-generation open source real-time observability system. |
| ccfos/nightingale | 13316 | Apache-2.0 | Go | 2026-09-23 | Nightingale is to monitoring and alerting what Grafana is to visualization. |
| zabbix/zabbix | 6427 | AGPL-3.0 | Go Template | 2026-09-30 | Real-time monitoring of IT components and services… |

**与现有平台的对照结论**（依据本仓库 `README.md` 与 `CONTEXT.md`，非推测）：

- 现有平台已覆盖：主机监控、10+ 中间件监控（直连 + exporter 双模式）、拨测、阈值告警（静默/维护窗口/升级/抑制/分组/收敛/处置）、安全监测中心、集中日志、巡检报告、智能分析、网闸代理、RBAC + 资源范围、离线包与 Web 升级。
- 与头部项目相比**不落后的部分**：告警状态机与处置协作（firing/Ack 两套）、网闸穿透、离线交付、国密加密——这些在 uptime-kuma/netdata 等轻量方案中通常缺失或很弱。
- **明显缺口**：无资产模型与关系（对照 NetBox/bk-cmdb）、无日志索引与结构化检索（对照 Loki/VictoriaLogs/OpenObserve）、无容器管理操作面（对照 headlamp/Rancher）、无链路追踪（对照 SkyWalking/DeepFlow）、无自动化执行/发布/工单/PAM（对照 Spug/JumpServer/bk-job）。

### 2.5 自动化执行与发布

| 项目 | ★ | 许可 | 语言 | 最近 push | 说明 |
|---|---|---|---|---|---|
| openspug/spug | 11085 | AGPL-3.0 | JavaScript | 2026-09-28 | 见 2.1（含批量执行/发布部署/流水线/任务计划/配置中心） |
| sky22333/ansible-ui | 543 | GPL-3.0 | TypeScript | 2026-08-16 | 轻量 Ansible Web 面板（批量主机管理/剧本/命令/文件传输/Web 终端） |
| ops-coffee/ansible-job-platform | 83 | — | HTML | 2025-11-15 | Django + Ansible 的企业级运维自动化平台 |
| TencentBlueKing/bk-job | 866 | NOASSERTION | Java | 2026-09-28 | 蓝鲸作业平台 |
| TencentBlueKing/bk-sops | 1276 | NOASSERTION | Python | 2026-09-29 | 蓝鲸标准运维（可视化流程编排） |

**观察**：该方向**没有 Apache-2.0/MIT 的头部项目**（头部是 AGPL 的 Spug 与 NOASSERTION 的蓝鲸系列）；若要落地，宜自研（复用现有 Agent 与三道门控护栏）或仅借其交互模型。

### 2.6 权限与审计（PAM / 堡垒机）

| 项目 | ★ | 许可 | 语言 | 最近 push | 官方定位 |
|---|---|---|---|---|---|
| jumpserver/jumpserver | 31686 | GPL-3.0 | Python | 2026-09-30 | Open-source Privileged Access Management (PAM) platform…（统一访问 SSH、RDP、Kubernetes、数据库、网站、RemoteApp、VirtualApp） |
| glpi-project/glpi | 6405 | GPL-3.0 | PHP | 2026-09-30 | Free Asset and IT Management Software package（资产 + ITSM/工单） |

**观察**：PAM 方向头部项目为 **GPL-3.0**，代码级集成的许可成本高；现有平台已有审计与三处安全护栏机制，自研"高危操作审计 + 审批"路径更可控。

## 三、许可合规分析

| 许可类别 | 项目 | 结论 |
|---|---|---|
| **Apache-2.0 / MIT / MPL-2.0**（可放心借鉴，必要时可代码级复用并保留声明） | prometheus、grafana/alloy、VictoriaLogs、VictoriaMetrics、headlamp、kubewall、karmada、k3s、trivy、netbox、nautobot、nightingale、hertzbeat、quickwit、OpenSearch、skywalking、vector、deepflow、uptime-kuma | 优先从本集合选择集成或借鉴对象 |
| **AGPL-3.0**（传染性强） | grafana、loki、openobserve、netdata、spug、zabbix | **不复制代码、不做静态链接**；即便"独立部署的服务"也需法务确认分发方式 |
| **GPL-2.0 / GPL-3.0** | glpi、jumpserver、OpsManage、adminset、ansible-ui | 同上，且 PAM/ITSM 方向头部均为此类 |
| **NOASSERTION（未声明标准 SPDX）** | bk-cmdb、bk-job、bk-sops、kubesphere、signoz、graylog、KubeOperator | 条款需人工确认后才可考虑集成；默认按"只借设计思想"处理 |

**统一红线**：本项目的交付形态是内网离线包 + 自研 Agent/Server，任何引入都必须满足「可离线分发、许可可分发、不污染自研代码许可」，因此**默认策略是只借设计思想，代码级复用仅限 Apache-2.0/MIT 集合**。

## 四、逐方向「可借鉴 / 拒绝 / 与现有平台差异」

### 4.1 资产与配置

- **可借鉴（设计思想）**
  - NetBox：**数据模型精确到"关系与接口层"**（IP 属于接口而非设备）；**purpose-built models** 而非一张大表；"source of truth" 定位——资产是权威源，监控/自动化消费它，而不是被自动改写。
  - bk-cmdb：**模型-实例-关联** 三元组 + 面向应用组织资源 + **模型可自定义扩展**；`机器数据快照`、`数据自动发现`、`变更事件主动推送` 三个能力正好对应"配置快照/自动发现/变更历史"。
  - nightingale：以 `targets` + `busi_groups` 组织被监控对象，并把采集配置下发到独立采集器（Categraf）——与现有平台「Server 下发指令 + 自研 Agent 采集」的结构同构（注：现有平台的采集项模板功能已移除，此处只比结构）。
- **拒绝点**
  - Spug（AGPL）：交互模型可参考，代码不可引入；且它是"无 Agent"路线，与现有 Agent 体系不同。
  - 外部数据库依赖（NetBox 需 DB）：与"内网离线、单二进制"约束冲突，只借模型不借实现。
- **与现有平台的差异**：现有平台已有 `node`（节点）、节点分组、中间件实例，但**没有资产类型/资产实例/关联关系/变更历史**四件套；资产信息其实已在采集侧产生（主机信息/分区/进程/中间件/容器），缺的是"模型化 + 关系化 + 历史化"。

### 4.2 容器与 Kubernetes

- **可借鉴**
  - headlamp：信息架构（集群/命名空间/工作负载/事件/日志/终端分层）、**in-cluster 与桌面双形态**、**插件化扩展**、**UI 反映用户权限**（原文：no deletion/update if not allowed）——与现有 RBAC/资源范围理念一致。
  - kubewall：轻量多集群面板的取舍参考（仅 1.9k★ 但方向一致）。
  - trivy：镜像/配置扫描可作为"容器安全"扩展点（Apache-2.0）。
- **拒绝点**
  - Rancher / KubeSphere：**重量级平台**，引入后等于"平台里再套一层平台"，其组件自身需要可观资源与运维，与「内网离线 + 轻量单二进制」定位冲突；KubeSphere 许可还是 NOASSERTION。
  - `kubernetes/dashboard`：**已归档**，不可依赖。
  - KubeOperator：**已归档**；Kuboard：**仓库 404**，无法评估。
- **与现有平台的差异**：现有平台已有 k8s/docker 采集器（只产指标），**没有任何管理/排障操作面**；且现有 `K8sInstance` 结构中凭据（kubeconfig/token）**只存 Agent 本地、不上报**，因此管理操作只能由 Agent 侧执行——这是与 Rancher/KubeSphere 的根本架构差异，决定了**必须自研轻量只读管理面 + 下行操作通道**。

### 4.3 日志与可观测

- **可借鉴**
  - VictoriaLogs：单机/集群双形态、LogsQL、按磁盘用量的保留策略、官方 30x RAM / 15x 磁盘的资源对比结论、同生态（与现用 VictoriaMetrics 同厂）——是唯一能"无缝并入现有离线包与部署脚本模式"的候选。
  - Grafana Alloy：采集侧代理的取舍参考（若未来需要独立采集代理）。
- **拒绝点**
  - Loki/OpenObserve/Grafana（AGPL）、Graylog/SigNoz（NOASSERTION + 重组件）、OpenSearch（JVM 全家桶）——许可或重量级原因均不适合默认引入。
- **与现有平台的差异**：现有 `logship`+`logstore` 已有"采集-限速-上行-分片落盘-游标检索"半套，**缺结构化解析、字段检索、全文索引与外部后端**；因此日志方向的正确做法是"增强现有实现 + 抽象出后端接口（默认自研、可选 VictoriaLogs）"，而不是替换。

### 4.4 监控告警

- **可借鉴**：Prometheus 生态的指标语义与 remote_write（现有平台已采用）；nightingale 的"业务组 + 订阅 + 事件流水线"组织方式；hertzbeat 的"一个平台多类型采集"验证了现有平台 10+ 中间件路线的合理性。
- **拒绝点**：uptime-kuma/netdata 定位轻量单点，与现有平台的能力面不在同一层，不构成替换关系。
- **与现有平台的差异**：告警侧（状态机/静默/抑制/收敛/升级/处置/安全事件复用）与离线交付是现有平台的**相对优势**，无需替换；缺的是"告警 → 资产/变更"的关联闭环（这正是 CMDB 的价值）。

### 4.5 自动化执行与发布

- **可借鉴**：Spug/ansible-ui 的任务编排与"剧本/命令/文件分发/Web 终端"交互模型；bk-sops 的可视化流程编排。
- **拒绝点**：该方向头部无干净许可；且"批量执行 = 高危能力"，外部平台难以复用现有的三道门控与本机护栏机制。
- **与现有平台的差异**：现有平台**已具备执行基础**（Agent 以 root 运行、已有「本机开关 + 能力协商」的护栏先例与审计），但**没有作业编排与发布能力**；建议自研并与资产模型绑定。

### 4.6 权限与审计（PAM）

- **可借鉴**：JumpServer 的"统一入口 + 会话审计 + 高危命令拦截"能力边界划分；GLPI 的工单/资产联动。
- **拒绝点**：均为 GPL 系；且现有平台已有 RBAC + 资源范围 + 审计 + 三道门控，自研"审批 + 会话审计"更贴合。
- **与现有平台的差异**：现有平台有**操作审计**，但没有"事前审批 + 会话录制/回放 + 高危命令拦截"。

## 五、对首批两个方向的选型结论

| 议题 | 结论 | 依据（本报告内） |
|---|---|---|
| 资产管理的数据模型 | 借 NetBox（关系精确到接口层、purpose-built models、source of truth）+ bk-cmdb（模型-实例-关联、快照、自动发现、变更推送）+ nightingale（target/busi_group 组织） | §2.1、§4.1 |
| 资产的采集来源 | **不新增采集器**：复用现有 Agent 已产出的主机信息、中间件实例、容器清单 | §4.1 差异段 |
| 容器管理面 | **自研轻量、默认只读**；UI 反映权限（借鉴 headlamp）；**不做** Rancher/KubeSphere 集成 | §2.2、§4.2 |
| 日志后端 | **默认增强现有 logstore 并抽出后端接口；VictoriaLogs 作为可选后端** | §2.3、§4.3 |
| 链路追踪 | **首批不做**；如需零侵入可评估 eBPF 方案（deepflow，Apache-2.0），SkyWalking 需应用侧探针 | §2.3、§4.3 |
| 自动化执行/发布、工单、PAM | 列入后续优先级，**自研并与资产模型绑定**，不引入 GPL 系外部平台 | §2.5、§2.6、§4.5、§4.6 |
| 监控告警 | **保持现状**（相对优势），只补"告警 ↔ 资产/变更"关联 | §2.4、§4.4 |

## 六、待确认事项（不得在后续设计中当作既成事实）

1. `bk-cmdb` 仓库 README 原文（网络受限已两次失败）；其"模型-实例-关联/快照/自动发现/变更推送"描述目前来自官方文档站与镜像 README 摘要，属二手转述。
2. NetBox「自定义字段」与「变更日志（change logging）」的官方段落链接尚未取到原文（本报告只引用了 Introduction/Planning/IPAM 三处可核原文）。
3. KubeSphere 的实际许可条款（仓库为 NOASSERTION，需读其 LICENSE 与版本差异）。
4. Kuboard 当前开源状态与许可（仓库 404，需其官网确认）。
5. VictoriaLogs 的**离线单机部署体积与最小资源需求**官方数值（本报告只引用了"30x/15x"相对结论与单机/集群形态）。

## 附：本次检索覆盖的方向清单（供后续复核）

资产与配置、容器/K8s、日志与可观测、监控告警、自动化执行与发布、权限与审计，共 6 个方向；功能全景（14 个领域）的逐条状态与优先级见 `docs/superpowers/specs/2026-09-30-ops-platform-capability-map.md`。
