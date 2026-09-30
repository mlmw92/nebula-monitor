# 日志存储抽成接口，但随第二个适配器一起落地

Status: proposed

现有集中日志的自研分片落盘实现（`internal/server/logstore`）在规模上够用（有扫描预算与游标语义），但客户日志量增长后需要可替换的外部后端。决定：为日志存储定义接口 `LogStore`（方法 `Write` / `Query(model.LogQuery, Cursor)` / `Backend()`，**沿用现有方法名与模型类型**），但**不在只有一个适配器时提前抽象**——接口与新适配器同时引入。首个候选外部后端为 VictoriaLogs（Apache-2.0、单二进制、与现用 VictoriaMetrics 同厂）。

## Considered Options

- **默认引入 Loki/Grafana**（AGPL-3.0）或 **OpenObserve**（AGPL-3.0）：传染性许可，与"自研产品分发"冲突 → 拒绝。
- **Graylog/SigNoz**（NOASSERTION）或 **OpenSearch**（JVM 全家桶）：许可需人工确认且组件重量级，与"内网离线 + 轻量"定位冲突 → 拒绝。
- **不抽象、直接替换**：会丢失现有"游标 + 扫描预算 + truncated"语义（这些语义解决的是真实问题：翻页跳行、静默截断误导结论）→ 拒绝。
- **先抽象接口、暂不实现第二适配器（本轮倾向）**：收益是调用方与测试共用同一接缝；代价是假想接缝。因此**接口定义与第二适配器同时落地**，不提前。

## Consequences

- 检索语义是接口契约的一部分，不得在替换后端时丢失：游标不透明（前端原样回传）、`Truncated` 必须回传、扫描预算与 `ScannedBytes/ScannedLines/Files` 诊断保留。
- 引入外部后端属于**可选**能力：默认仍走自研落盘，客户可选择部署，不改变离线包的默认真空。
- 若将来引入，需同时纳入 `build/fetch-packages.sh`（离线依赖缓存）与 `deploy/install-tsdb.sh` 的部署脚本模式。
