# nebula-monitor 中间件监控测试报告（dev-server）

- 报告日期：2026-09-30（2026-09-29 初版，本次为补齐实测后的修订版）
- 被测环境：dev-server（124.223.77.206，Ubuntu 24.04，3.6 GB 内存 + 2 GB swap，4 vCPU）
- 被测版本：Server / Agent **1.29.0**（离线包 `dist/release/nebula-monitor-v1.29.0-full.tar.gz` /
  `-upgrade.tar.gz`，含本轮集群判定与采集修复）；此前为验证修复，dev-server 上曾部署本地构建的
  server / agent 二进制（备份见 `/usr/local/bin/monitor-server.bak-dedupe`、
  `/usr/local/bin/monitor-agent.bak-1.28.8`）
- 结论：**通过**。已接入并实测 **14 类**中间件；补齐 14 条场景/阈值告警规则并逐条验证；
  本轮发现并修复 2 处缺陷（RocketMQ 实例恒离线、已恢复实例仍显示离线/告警不消）。

> 说明：「已验证」指通过 Server API、时序库原始数据或告警链路取得实测证据。

---

## 一、测试范围与方法

| 维度 | 说明 |
|---|---|
| 采集验证 | 直接查询时序库（`/api/v1/query`）核对 `<类型>_instance_up` 与核心指标是否有数据、取值是否与中间件真实状态一致 |
| 接口验证 | `GET /api/v1/middleware/<类型>/instances` 返回的实例数、在线状态、角色与真实拓扑逐一比对 |
| 告警验证 | ① 阈值规则必然触发（`> -1`）验证链路 ② 服务离线规则停/起容器实测触发与自动 resolve ③ 规则误报治理（空载恒真规则删除） |
| 资源约束 | 3.6 GB 内存不足以同时常驻全部中间件，采用**分批轮换**：验证哪一类就把该类容器拉起，验证完按需停用（停用期间离线告警会如实触发，属预期） |

## 二、各类型实测结果（14 类）

| 类型 | 实例数 | 关键证据 | 状态 |
|---|---|---|---|
| Redis | 7 | 单机 6379 + 集群 7000-7005；**集群角色识别正确**（3 master + 3 replica） | 通过 |
| MySQL | 4 | standalone 3306 + Group Replication 3307/3308/3309；角色来自 `performance_schema.replication_group_members`（1 PRIMARY + 2 SECONDARY） | 通过 |
| PostgreSQL | 1 | 5432 在线，缓存命中率 99.99% | 通过 |
| MongoDB | 1 | 27017 在线（`mongodb_up=1`），角色 STANDALONE | 通过 |
| Nginx | 1 | 80 端口 stub_status + access.log 访问分析 | 通过 |
| Docker | 18 | 容器列表与逐容器资源指标；`docker_container_up` 与容器真实状态一致 | 通过 |
| Kafka | 1 | 9092 broker 在线（容器停用期间如实转离线） | 通过 |
| RocketMQ | 1 | 5.3.1 + `apache/rocketmq-exporter`（exporter 模式）；修复后 `rocketmq_instance_up=1` | 通过（含缺陷修复） |
| FastDFS | 2 | tracker 22122 + storage 23000 均在线 | 通过 |
| RabbitMQ | 1 | 15692（rabbitmq_prometheus）在线 | 通过 |
| Elasticsearch | 1 | 7.17.28 单节点 green；`es_instance_up=1`、节点/分片指标有值 | 通过 |
| ClickHouse | 1 | 24.3，`clickhouse_instance_up=1` | 通过（含缺陷修复） |
| Nacos | 1 | v2.3.2 standalone，健康检查通过 | 通过 |
| ZooKeeper | 1 | 3.9.5，`mntr` 四字命令读数正常（standalone） | 通过（含缺陷修复） |

## 三、本轮发现并修复的缺陷

### 缺陷 1：RocketMQ exporter 模式下实例恒显示离线

- 现象：按平台文档配置 `exporterURL`（推荐模式）后，实例列表一直显示离线，尽管
  `/metrics` 返回了 78 条 `rocketmq_*` 指标。
- 根因：`collectExporter` 只做 Prometheus 文本解析，而 RocketMQ 官方 exporter **不暴露
  `rocketmq_instance_up`**；平台实例列表以该指标判定在线 → 永远离线。
- 修复：`internal/agent/collector/rocketmq.go` 在成功解析到 RocketMQ 指标时补出
  `rocketmq_instance_up=1`（原生指标存在时不重复），拉取失败/空响应仍判离线。
  回归测试：`internal/agent/collector/rocketmq_test.go`。
- 实测：修复后平台显示 `dev-rocketmq[nameserver]` 在线。

### 缺陷 2：实例已恢复，平台仍显示离线 / 离线告警不消（用户报障根因）

- 现象：中间件容器恢复后，实例列表仍显示离线、服务离线告警一直挂在 firing（此前被误记为
  "VictoriaMetrics 回看窗口导致的已知现象，约 5 分钟自愈"）。
- 证据（直接查时序库）：同一实例的 `<类型>_instance_up` 存在**两条序列**：

  ```
  clickhouse_instance_up{... role=server}                    0     ← 采集失败时写入（无 version）
  clickhouse_instance_up{... role=server, version=24.3.18.7} 1     ← 采集成功时写入
  zookeeper_instance_up{... }                                0     ← role 为空
  zookeeper_instance_up{... role=standalone, version=3.9.5}  1
  ```

- 根因：存活指标上挂了**只在采集成功时才存在**的标签（`version`、`role`、Docker 的
  `status`、`replica_of` 等）。Prometheus 按标签集区分序列 → 离线期间写的 `up=0` 与恢复后写的
  `up=1` 落在两条序列上，恢复后旧的 `up=0` 仍在即时查询回看窗口内并存：
  - 告警引擎 `evalServiceDown` 同一轮里同时拿到 `up=0` 与 `up=1`，先判离线触发、再判在线恢复，
    结果取决于 map 遍历顺序 → **告警反复抖动或一直不消**；
  - 实例接口（MySQL/PostgreSQL/MongoDB/Nginx/通用类型）以"后写覆盖/先到先得"取值 → 可能取到
    陈旧的 `up=0` → **已恢复仍显示离线**（Redis 接口此前已按时间戳去重，故现象不一致）。
- 修复（服务端，一次覆盖所有 Agent 版本，含存量 Agent 无需升级）：
  - `internal/server/alert/engine.go`：服务离线评估前按 `instance` 取**数据点时间戳最新**的序列
    （新增 `latestPerInstance`，与既有 role 聚合、Redis 接口约定一致）。
  - `internal/server/api/mw_newest.go`：新增共用助手 `newestSampleKept`；
    `middleware_generic.go`（ES/ClickHouse/Nacos/ZooKeeper/RabbitMQ）与
    `middleware_api.go`（MySQL/PostgreSQL/MongoDB/Nginx/Docker）实例列表同样改为按最新序列取值，
    且元信息（role/version）保留非空值不丢。
  - 回归测试：`internal/server/alert/instance_dedupe_test.go`。
- 实测：部署后 ClickHouse、ZooKeeper 立即由"离线"转为**在线**；`Redis 服务离线` 告警不再残留，
  停止的容器（ES/Nacos）则如实触发离线告警。

### 待修问题（本轮未改，建议排期）

| 问题 | 说明 | 建议 |
|---|---|---|
| Kafka 容器停止时无 `kafka_instance_up` | broker 不可达时采集器**不写任何样本**（其余类型会写 `up=0`），导致「Kafka 服务离线」规则在真正宕机时**永不触发** | 与其它采集器对齐：不可达时落一条 `kafka_instance_up=0`（标签集与成功路径一致） |
| Docker「服务离线」按容器判定 | 设计即每容器一条 `docker_container_up`，因此**正常运维停止一个容器**也会产生 critical 告警（本轮 5 条） | 拆分为「Docker 守护进程离线」与「容器异常退出」两条语义，或规则只作用于期望常驻的容器 |
| 存活指标标签集仍不稳定（根因未除） | 服务端已按时间戳去重，症状消除；但两条序列仍会写入时序库并驻留回看窗口 | 采集器侧：`*_up` 只带配置派生标签（node/instance/name/group/topology），role/version 仅挂在业务指标上 |

## 四、告警规则（共 40 条，0 条停用）

- 原有 26 条：主机离线、CPU/内存/磁盘/负载阈值、MySQL/Redis/Nginx/Kafka/Docker/FastDFS/
  PostgreSQL/MongoDB 服务离线、MySQL 主从切换与集群损坏、PostgreSQL 主从切换、
  Kubernetes 集群状态损坏、安全事件、各中间件连接数过高、Redis 拒绝连接/内存占用过高等。
- 本轮新增 14 条：

  | 规则 | 类型 | 条件 |
  |---|---|---|
  | Elasticsearch 服务离线 | service_down | es_instance_up <= 0 持续 3m |
  | ClickHouse 服务离线 | service_down | clickhouse_instance_up <= 0 持续 3m |
  | Nacos 服务离线 | service_down | nacos_instance_up <= 0 持续 3m |
  | ZooKeeper 服务离线 | service_down | zookeeper_instance_up <= 0 持续 3m |
  | RabbitMQ 服务离线 | service_down | rabbitmq_instance_up <= 0 持续 3m |
  | Kubernetes 集群离线 | service_down | k8s_cluster_up <= 0 持续 3m |
  | Elasticsearch 分片未分配 | threshold | es_unassigned_shards > 0 持续 2m |
  | RocketMQ 消息堆积过大 | threshold | rocketmq_message_accumulation > 10000 持续 5m |
  | MySQL 缓冲池命中率过低 | threshold | mysql_innodb_buffer_pool_hit_rate < 90 持续 5m |
  | PostgreSQL 缓存命中率过低 | threshold | postgres_cache_hit_ratio < 90 持续 5m |
  | ZooKeeper 请求积压 | threshold | zookeeper_outstanding_requests > 100 持续 5m |
  | ClickHouse 运行查询过多 | threshold | clickhouse_queries_running > 50 持续 5m |
  | MySQL 死锁 | threshold | mysql_innodb_deadlocks > 0 持续 1m |
  | PostgreSQL 死锁 | threshold | postgres_deadlocks > 0 持续 1m |

- 本轮启用 1 条：「RocketMQ 服务离线」（此前因 exporter 未就绪一直停用）。
- 本轮删除 2 条（实测为空载恒真的噪音规则）：`Redis 命中率过低`（空载实例
  `hit_rate=0` 恒真）、`Redis 内存碎片率过高`（容器内 jemalloc 碎片率普遍 1.5-3.7）。
- 规则治理结论：**阈值规则必须能区分「空载」与「异常」**，否则应改为「有流量时才判」或删除。

## 五、环境与约束

- dev-server 为 3.6 GB 内存的测试机，全量常驻（MySQL GR 三节点 + Redis 集群 6 节点 + 各类
  中间件容器）会超配。本轮采用分批轮换，当前内存占用约 2.9 GB / 可用约 0.75 GB。
- 当前**停用中**（按需拉起即可，拉起后平台会自动转为在线）：`mw-es`、`mw-nacos`、`mw-kafka`、
  `mw-etcd`、`test-nginx`。ES/Nacos 已实测通过，停用仅为让出内存。
- Kubernetes（k3s）未纳入本轮验证，需要时单独起集群接入 `k8sInstances`。
- 安全中心在测试期间产出大量真实安全事件（SSH 暴力破解、sudo 提权、反弹 shell 特征），
  属预期采集结果，不属于中间件监控范畴。

## 六、验收清单

- [x] 各类型实例接口返回预期实例数与 `up=true`（14 类，共 40 个实例中 33 在线、7 为刻意停用）
- [x] 实例角色/拓扑与真实部署一致（Redis 集群主从、MySQL GR PRIMARY/SECONDARY、MongoDB 角色）
- [x] 阈值告警可触发、恢复后自动 resolved
- [x] 服务离线告警（停容器）可触发、恢复后自动 resolved
- [x] 已恢复实例不再残留离线告警（本轮修复缺陷 2）
- [x] 各类型采集无报错（`journalctl -u monitor-agent` 仅剩刻意停用实例的预期告警）
- [ ] 实例详情抽屉趋势图人工确认（需 Web 端目视）

## 七、MySQL 集群健康判定（1.29.0 新增，含实测）

### 7.1 背景：页面与告警曾对同一实例给出两种结论

- 现象：3 个实例都是主库时，**页面显示「运行正常」**，而**「MySQL 集群状态损坏」告警判「多主（疑似脑裂）」**。
- 根因：两处各写了一套判定。前端 `web/src/components/mysql/MySQLTab.vue` 的 `clusterHealth()`
  只判「有无离线 + 主从延迟」，**从不数主库个数**；后端 `internal/server/alert/engine.go`
  的 `classifyClusterFault` 才按主库个数判多主。
- 修法：把判定收敛成一份实现（`internal/server/alert/cluster.go` 的 `ClassifyClusterFault`），
  告警引擎与页面接口（`GET /api/v1/middleware/mysql/instances` 的 `clusters` 字段）共用；
  前端只渲染后端结论，不再自行判定。

### 7.2 判定语义（模式感知 + 组视图一致性 + 成员就绪度）

单主模式下多个 PRIMARY 是脑裂，而多主模式（`group_replication_single_primary_mode=OFF`）下
全部成员都是 PRIMARY 是**正常形态**——只看「有几个主库」无法判定，必须结合模式。

| 情形 | 判定 | 依据 |
|---|---|---|
| 组视图分裂：各节点看到的成员集合不一致 | **异常** | 单主/多主都算；比数主库个数更直接（全量重启后各自引导成单成员组，角色看都是 PRIMARY） |
| 组内有成员未就绪（所有观察者一致认为非 ONLINE） | **异常** | 成员可达（up=1）但没真正参与组复制；实测多主切换后出现过长期 RECOVERING 却显示"正常" |
| 有实例不在组复制中（同组有的在组内、有的完全没有组视图） | **异常** | 组复制被停掉后角色退化为 master，主库计数看起来正常，实则已掉出集群 |
| 同组既有单主又有多主配置 | 异常 | 角色判定不可信 |
| 无主（在线成员无 PRIMARY） | 异常 | 集群无法写入 |
| 单主模式 + 多个 PRIMARY | **异常** | 脑裂（单主模式仅允许 1 个主库） |
| 多主模式 + 全部在线成员都是 PRIMARY | **正常** | 设计形态 |
| 多主模式 + 部分成员是主库 | 异常 | 模式与角色不一致 |
| 模式未知（旧版 Agent 未上报） | 沿用历史保守判定 | >1 主即异常 |

顺带修掉一个假告警：Kubernetes 等**无主从角色概念**的集群，原判定会恒定报「无主」
（`k8s_cluster_up` 不带 `role` 标签），现在无角色信息就不做主库个数判定。

新增采集指标（agent 侧，仅 GR 实例产出）：`mysql_gr_single_primary_mode`（1 单主/0 多主）、
`mysql_gr_view_size`、`mysql_gr_view_member{member=...}`（1=ONLINE/0.5=RECOVERING/0=其他）。

### 7.3 实测结果

| 场景 | 构造方式 | 平台结论 | 结果 |
|---|---|---|---|
| 单主三节点正常 | 1 PRIMARY + 2 SECONDARY ONLINE | `mode=single`、`fault=''` | ✅ 不误报 |
| **合法多主** | 停 GR → 设 `single_primary_mode=OFF` → 三节点重启入组，三节点全为 PRIMARY | `mode=multi`、`fault=''` | ✅ **不再误报脑裂** |
| 组内成员未就绪 | 多主切换后两成员长期 RECOVERING | `组内有 2 个成员未就绪（…=RECOVERING）` | ✅ 识别出降级 |
| 实例掉出集群 | 容器重启把运行时设置重置，两节点 GR 停止 | `有 2 个实例未在组复制中（…）` | ✅ 识别出掉组 |
| **组视图分裂** | 三节点各自 bootstrap 成单成员组（各自为组） | `组视图分裂：各节点看到的成员集合不一致（3307 看到 1 个成员，3308 看到 1 个）` | ✅ 识别出脑裂 |
| 恢复基线 | 重建 + 引导 + GTID 清理，1 PRIMARY + 2 SECONDARY | `mode=single`、`fault=''`，集群告警自动 resolved | ✅ |

### 7.4 实测中发现的关键坑：VictoriaMetrics 即时查询的时间戳

排查「多主判定不生效/随机」时定位到一个**影响面很广的后端行为**：

```
GET /api/v1/query?query=mysql_gr_view_member{...}   # 陈旧序列（最后样本 13:46:16）
{"value":[1790747384,"1"]}                          # 返回的时间戳 == 查询时刻（date +%s 相同）
```

VictoriaMetrics 的即时查询把返回时间戳设成**求值时刻**，而不是样本自身时间。
后果：同一实例的多条序列（标签集不同，如 up=0 与 up=1、role=master 与 role=secondary）
**时间戳完全相同**，任何「按时间戳取最新序列」的去重都会退化成「谁先返回谁生效」——
这正是「实例已恢复但告警不消／看板仍判离线」的机制性原因。

对策（本轮已实施）：改用仓库里已有的 `QueryInstantWithLookback`（range-vector 查询，
保留**真实样本时间戳**），并限定新鲜窗口 `freshSampleWindow = 90s`：

- 服务离线评估（`evalServiceDown`）：只取 90s 内的样本 + 按实例取最新时间戳
- 角色/集群成员（`latestRoleSamples`、`LoadClusterMembers`）：同上
- GR 模式与组视图：同上，并只采纳观察者**最新一轮**上报的成员

**待跟进**：`QueryAllLatest`（实例列表类接口，如 MySQL/Redis 列表的 up 与 role）仍走即时查询，
同样拿不到真实样本时间戳，其中的 `newestSampleKept` 去重同样无法生效；
建议存储层统一改为「range 查询 + 取窗口内最新样本」，一次性覆盖所有消费方。

### 7.5 待修问题（本轮未改）

| 问题 | 说明 | 建议 |
|---|---|---|
| 存活指标标签集仍不稳定（根因未除） | `role`/`version`/`replica_of`/`status` 等「仅采集成功时才存在」的标签挂在 `*_up` 上，同一实例产生多条序列。服务端已按新鲜窗口兜住症状，但序列churn仍在 | 采集器侧：`*_up` 只带配置派生标签（node/instance/name/group/topology），role/version 挂到独立指标上 |
