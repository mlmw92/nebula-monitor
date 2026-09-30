# 容器管理操作由 Agent 执行，复用上报响应下发指令

Status: proposed

Kubernetes 凭据（Kubeconfig/Token）**只存 Agent 本地、不上报 Server**，且既有结构 `model.K8sInstance` 不含凭据字段——这是既有实现明确声明并遵守的安全边界。因此 Server 无法直连 apiserver，容器管理操作只能由 Agent 执行。决定：新增 `Ops` 指令（kind 如 `container.workloads/pods/describe/events/logs`，`container.exec` 后置），**复用既有"随 `POST /api/v1/report` 响应下发指令"的通道**（与 `command:"upgrade"`、`defense` 同构），并沿用能力协商（`Capabilities`）与执行回执（`DefenseResult` 同构的 `opsResult`）与超时作废（`Take/ExpireOverdue`）机制；默认只读，写操作需四道护栏。

## Considered Options

- **Server 直连 apiserver**：需要把凭据上报到 Server，直接破坏既有凭据边界 → 拒绝。
- **Server↔Agent 新建长连接/隧道**：新增协议、鉴权与穿透复杂度，且与网闸（edge/hub）部署冲突；而既有响应内下发链路已被升级与 fail2ban 两条业务验证 → 拒绝。
- **集成 Rancher / KubeSphere**：重量级平台（自身需资源与运维），与"内网离线 + 轻量单二进制"冲突；且官方 `kubernetes/dashboard` 已归档、`KubeOperator` 已归档、`eipwork/kuboard(-v3)` 仓库 404 → 拒绝。
- **自研轻量只读管理面 + 响应内下发（选定）**：信息架构参考 `kubernetes-sigs/headlamp`（Apache-2.0），只借设计不抄代码。

## Consequences

- **接口语义必须是异步任务**：指令随上报响应下发，单次往返至少一个上报周期（默认 15s）。前端按"提交 → 任务 ID → 轮询/WS 推送"实现，不能按"同步代理"设计。
- 四道护栏缺一不可：本机护栏（agent.yaml 开关，默认只开只读）→ 能力协商（Agent 声明支持的 kind）→ 中心授权与审计（`container:read`/`container:exec`）→ 超时作废；`container.describe` 返回的 YAML 必须脱敏（Secret/token 等）。
- 与集中日志的职责边界必须固化：历史检索走集中日志；按需拉取走 `container.logs` 且**不进 Server 存储**；需要长期保留则引导配置 `logSources`。
