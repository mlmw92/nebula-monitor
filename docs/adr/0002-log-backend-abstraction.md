# 日志存储抽成接口，且随第二个适配器一起落地

Status: accepted（2026-10-06 落地）

现有集中日志的自研分片落盘实现（`internal/server/logstore`）在规模上够用（有扫描预算与游标语义），但客户日志量增长后需要可替换的外部后端。决定：为日志存储定义接口 `LogStore`（方法 `Append` / `Query(model.LogQuery, Cursor)` / `Backend` / `Sources` / `FieldNames`，**沿用现有方法名与模型类型**），但**不在只有一个适配器时提前抽象**——接口与新适配器同时引入。首个候选外部后端为 VictoriaLogs（Apache-2.0、单二进制、与现用 VictoriaMetrics 同厂）。

## Considered Options

- **默认引入 Loki/Grafana**（AGPL-3.0）或 **OpenObserve**（AGPL-3.0）：传染性许可，与"自研产品分发"冲突 → 拒绝。
- **Graylog/SigNoz**（NOASSERTION）或 **OpenSearch**（JVM 全家桶）：许可需人工确认且组件重量级，与"内网离线 + 轻量"定位冲突 → 拒绝。
- **不抽象、直接替换**：会丢失现有"游标 + 扫描预算 + truncated"语义（这些语义解决的是真实问题：翻页跳行、静默截断误导结论）→ 拒绝。
- **先抽象接口、暂不实现第二适配器（本轮倾向）**：收益是调用方与测试共用同一接缝；代价是假想接缝。因此**接口定义与第二适配器同时落地**，不提前。

## Consequences

- 检索语义是接口契约的一部分，不得在替换后端时丢失：游标不透明（前端原样回传）、`Truncated` 必须回传、扫描预算与 `ScannedBytes/ScannedLines/Files` 诊断保留。
- 引入外部后端属于**可选**能力：默认仍走自研落盘，客户可选择部署，不改变离线包的默认真空。
- 若将来引入，需同时纳入 `build/fetch-packages.sh`（离线依赖缓存）与 `deploy/install-tsdb.sh` 的部署脚本模式。

## 落地记录（2026-10-06）

### 接口签名：以实际调用面为准，修正初稿

初稿写的是 `Write(entries []model.LogEntry) (dropped int, reason string, err error)`，但实际调用面（Agent 上行）用的是 `Append(model.LogBatch) (accepted, dropped int, reason string, err error)`——批次里带 `Node`/`Source`，而限额丢弃是**正常结果**（Agent 要把它记进 `log_dropped_total`），不是错误。本 ADR 自己的原则是"沿用现有方法名与模型类型"，因此接口按实际签名落地，并补上界面需要的 `Sources()` / `FieldNames()`（它们此前是具体类型上的方法，抽接口时必须一起搬，否则调用方拿不到候选列表）。

### 游标必须带后端标识（本批新增的约束）

原实现只有一种后端，游标 `{File, Offset}` 天然无歧义。引入第二个后端后，"把 A 后端的游标喂给 B 后端"会**静默跳行或重复**——而"少了几行日志"这种症状在现场极难归因。因此 `Cursor` 增加 `Backend` 字段：

- 有续读位置但无 `Backend` → 只对本地后端有效（接口引入前只有它，浏览器里存着的旧游标必须继续可用）；
- **零值游标**（无任何续读位置）表示"从头开始"，任何后端都接受；
- 不匹配 → 明确报错"游标不属于当前日志后端（后端可能已切换），请重新查询"。

### 后端能力差异（是边界，不是遗漏）

| 维度 | 自研落盘 | VictoriaLogs |
|---|---|---|
| 扫描诊断 | `ScannedBytes/ScannedLines/Files` 真实值 | 无逐文件扫描概念，**三项恒为 0**（界面只在"没命中"时展示诊断，因此不会显示成"扫了 0 个文件"的假象） |
| 单来源每日上限 | 生效（`logMaxBytesPerDay`） | 不适用：容量治理归后端（`-retentionPeriod`）。上行的**限速与请求体上限仍然生效**（在 receiver 侧） |
| 保留清理 | 平台按天分片删除（retention 的 `logsDays`） | 平台不清理也不统计；retention 会显式说明"由外部后端负责"，避免把架构选择显示成"日志未接入" |
| 分页 | 文件内**绝对字节偏移**续读（翻页不重扫） | `limit` + `offset`（每次续读重跑查询）；`end` 是**开区间**，闭区间语义由适配器 +1ms 补齐 |
| 字段 | 落盘时按 `logparse` 提取，与原文同一条 JSON | 写入时把提取结果平铺成顶层字段（供后端建索引），**读取时再按同一套规则从正文重新提取**（保证两个后端的 `Fields` 完全一致） |

### 验证方式与未覆盖边界

- **契约用例**（`internal/server/logstore/backend_contract_test.go`）：同一套断言跑两个适配器，覆盖子串/正则/精确等值/节点来源过滤/**闭区间时间**/分页不重不漏/游标跨后端拒绝/写入校验/目录候选。
- 第二个适配器跑在**按官方文档实现文档化子集**的假后端上（`victorialogs_fake_test.go`），它自带一个照文档语法写的 LogsQL 求值器——翻译错了会失败，而不是等到现场。
- **未覆盖（首次启用前必须核对）**：真实 VictoriaLogs 实例的方言兼容性、`_stream_fields` 绑定后的压缩与查询行为、集群/租户（`AccountID`/`ProjectID`）行为、同毫秒边界行的顺序稳定性。清单见 `docs/testing/2026-10-06-platform-review-delta.md` §十。
