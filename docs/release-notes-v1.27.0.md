# nebula-monitor v1.27.0 — 2026-09 全仓代码审查安全修复

本版本集中修复 2026-09 全仓代码审查确认的 9 条严重缺陷，并包含一批一般级修复。**升级前请完整阅读下方「重要行为变化」。**

## 重要行为变化（升级前必读）

- **卸载脚本默认改为「保留数据」**：`uninstall.sh` / `install.sh uninstall` 的默认行为此前与文档相反（默认即删数据目录）。修复后**默认保留** `/var/lib/monitor-*`，需要连数据一起彻底清理请显式加 `--purge`（不可逆）。原先依赖「默认即删」做例行清理的操作需改用 `uninstall.sh --purge`。
- **配置文件权限收紧**：`server.yaml`、`agent.yaml` 落盘权限改为 `0600`（含接入密钥/登录口令/通知渠道密钥，此前 0644 全局可读），配置与数据目录分别收紧为 `700`/`750`。如有依赖「其他用户可读配置」的运维流程需相应调整；**存量部署建议手动补一次**：
  ```bash
  chmod 600 /etc/monitor-server/server.yaml /etc/monitor-agent/agent.yaml
  ```
- **网闸代理（Edge/Hub）恢复证书链校验**：此前 Edge 侧 TLS 配置实际不校验 Hub 证书（任何证书都被接受），存在被中间人劫持隧道的风险。修复后 Edge 会按两侧共享的 CA 校验证书链。使用 `--tls-auto` 或手动生成的同 CA 证书的部署**无需任何改动**；此前用野 CA/错证书的错误部署将从「静默不安全」变为「连接失败并在日志给出原因」。该变化随新版本 Agent 二进制生效，Edge/Hub 节点需重新安装/升级 Agent（先升 Server 再升 Agent）。
- **告警渠道失败不再被掩盖**：钉钉/飞书/企业微信机器人返回「HTTP 200 但业务码非 0」（签名错误、关键词不匹配、限流）时，现在会正确报错并记录「通知发送失败」日志。升级后若首次出现此类日志，请检查对应渠道的加签密钥与机器人配置——问题可能早已存在，只是此前被静默吞掉。

## 严重缺陷修复清单

| # | 修复 | 位置 |
|---|---|---|
| 1 | 钉钉/飞书/企业微信业务码校验，告警不再静默丢失；飞书加签 timestamp 改字符串 | `alert/notifier.go` |
| 2 | remote_write 快速失败（独立写客户端 + 2×写超时预算），TSDB 故障不再拖垮上报面 | `storage/` |
| 3 | 卸载脚本默认保留数据，与文档承诺对齐 | `deploy/uninstall.sh` |
| 4 | `server.yaml` / `agent.yaml` 权限收紧为 0600 | `deploy/*.sh` |
| 5 | 升级 manifest 版本号路径穿越（任意文件写入）修复 | `upgrade/` |
| 6 | Edge TLS 恢复证书链校验，消除 MITM 风险 | `agent/proxy/tls.go` |
| 7 | 连接池关闭竞态导致进程 panic 修复 | `agent/proxy/connpool.go` |
| 8 | 主机详情页时序图时间轴塌缩修复（前端） | `web/src/components/NodeView.vue` |
| 9 | 告警引擎 `notifiers` 数据竞争消除（copy-on-write 快照） | `alert/engine.go` |

另有约 20 条一般级修复随本版本发布（含飞书 timestamp 类型、升级/回退并发互斥、Do 错误分支连接泄漏等），完整清单见仓库 `docs/severity-fix-plan.md` 与 README「升级」章节。

## 升级顺序

1. **先升级 Server**（Web 端上传 upgrade 包，或脚本方式）——Agent 从 Server CDN 拉取二进制，顺序颠倒会导致 Agent 拿到旧版本。
2. **再升级 Agent**（主机列表逐台/批量升级）。
3. **Edge/Hub 节点**随 Agent 一并升级，升级后证书链校验生效。
