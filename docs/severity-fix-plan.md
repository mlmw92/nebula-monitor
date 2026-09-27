# 严重缺陷修复方案（2026-09 代码审查产出）

对应 2026-09 全仓代码审查确认的 9 条「严重」发现。每条含根因、代码级修复、测试建议与风险。全部发现（严重 9 / 一般 ~62 / 建议 60+）的完整清单见审查会话记录；本文只覆盖严重级。

按建议 PR 分组排列，组内按紧迫度排序。

---

## PR-1：告警送达可靠性（改 3 处，互相独立可拆）

### 1.1 钉钉/飞书/企业微信 errcode 不解析 → 告警静默丢失

- **位置**：`internal/server/alert/notifier.go:554-572`（`postJSON`）
- **根因**：只检查 HTTP 状态码。三家机器人签名错误/关键词不匹配/限流时返回 `200 + {"errcode":≠0}`（飞书为 `{"code":≠0}`），全部被当作成功。
- **修复**：`postJSON` 解析响应体并按渠道语义判定：

```go
// postJSON 向指定 URL POST JSON，并校验响应状态与业务码。
// 钉钉/企业微信成功为 {"errcode":0}，飞书为 {"code":0}；HTTP 200 但业务码非 0 视为失败。
func postJSON(rawURL string, body interface{}) error {
    // ... 原有 marshal / client.Post 不变 ...
    if resp.StatusCode >= 300 { /* 原逻辑 */ }
    var biz struct {
        Errcode int    `json:"errcode"`
        Code    int    `json:"code"`
        Errmsg  string `json:"errmsg"`
        Msg     string `json:"msg"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&biz); err != nil {
        slog.Warn("通知渠道响应体解析失败（按成功处理）", "err", err)
        return nil // 兼容自定义 webhook 网关返回非 JSON
    }
    if biz.Errcode != 0 {
        return fmt.Errorf("通知渠道业务错误 errcode=%d errmsg=%s", biz.Errcode, biz.Errmsg)
    }
    if biz.Code != 0 {
        return fmt.Errorf("通知渠道业务错误 code=%d msg=%s", biz.Code, biz.Msg)
    }
    return nil
}
```

注意：errcode/code 缺省为 0——若某渠道两者都无字段（纯自定义 webhook），Decode 出零值视为成功，与现状兼容。失败会经由既有 `slog.Warn("通知发送失败")` 路径可见。
- **测试**：`notifier_test.go`（如无则新建）用 `httptest.Server` 返回 200+errcode=310000（钉钉签名错误码）断言返回 error；返回 200+errcode=0 断言 nil。
- **附带（同 PR）**：飞书 `timestamp` 改为字符串（`notifier.go:704`，`body["timestamp"] = strconv.FormatInt(ts, 10)`）——飞书官方要求字符串类型，当前数值类型可能导致加签模式全量静默失败，与本条叠加。

### 1.2 引擎 `notifiers` 数据竞争（两处未持锁读）

- **位置**：`internal/server/alert/engine.go:904`（`notifyEscalation`）、`:1650`（`notify`）
- **根因**：`SetNotifiers`（`:1691`）持 `e.mu` 写切片，其余 4 处读取持锁，唯独这两处裸读。**不能给 `notify` 补锁**：`evaluate()` 持 `e.mu` 经 `fire()` 调用 `notify()`，补锁即死锁。
- **修复**：copy-on-write 原子快照，读写全部去锁化：

```go
type Engine struct {
    // ...
    notifiers atomic.Pointer[[]Notifier] // 替换原 []Notifier 字段
}

func (e *Engine) notifiersSnapshot() []Notifier {
    if ns := e.notifiers.Load(); ns != nil {
        return *ns
    }
    return nil
}

// SetNotifiers 热加载通知器列表（copy-on-write，读方无锁）。
func (e *Engine) SetNotifiers(ns []Notifier) {
    cp := make([]Notifier, len(ns))
    copy(cp, ns)
    e.notifiers.Store(&cp)
}
```

6 个读取点（`:904 :1289 :1586 :1623 :1650 :1954`）统一改为 `ns := e.notifiersSnapshot()`，并删除其中的 `e.mu.Lock/Unlock` 快照包裹（`:1289 :1586 :1623 :1954`）。
- **测试**：现有 `go test -race ./internal/server/alert/` 加一条「并发 SetNotifiers × notify」的压测用例。
- **风险**：低。切片内容本就视为不可变（结构体值快照）。

---

## PR-2：Agent 侧隧道安全与稳定（`internal/agent/proxy`，一次升级 Agent 二进制生效）

### 2.1 Edge 客户端 TLS 校验被 `InsecureSkipVerify` 完全关闭

- **位置**：`internal/agent/proxy/tls.go:30`
- **根因**：注释错误理解 Go 语义——`InsecureSkipVerify=true` 时 `RootCAs` 不参与校验，任何证书都被接受，Edge→Hub 方向服务端认证失效（可 MITM 全部上报流量）。
- **修复**：保留「IP 直连、无主机名校验」的部署兼容，但恢复**证书链校验**——用 `VerifyPeerCertificate` 手动按 CA 验链：

```go
return &tls.Config{
    Certificates: []tls.Certificate{cert},
    MinVersion:   tls.VersionTLS13,
    // 不校验主机名（网闸两侧按 IP 直连、证书 CN 不含 IP），
    // 但必须校验证书链由配置的 CA 签发——InsecureSkipVerify=true 会使
    // RootCAs 完全失效，因此链校验在回调里手动完成。
    InsecureSkipVerify: true, //nolint:gosec // 主机名跳过；链校验见 VerifyPeerCertificate
    VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
        if len(rawCerts) == 0 {
            return errors.New("对端未出示证书")
        }
        certs := make([]*x509.Certificate, len(rawCerts))
        for i, raw := range rawCerts {
            c, err := x509.ParseCertificate(raw)
            if err != nil {
                return fmt.Errorf("解析对端证书失败: %w", err)
            }
            certs[i] = c
        }
        opts := x509.VerifyOptions{Roots: caPool, Intermediates: x509.NewCertPool()}
        for _, c := range certs[1:] {
            opts.Intermediates.AddCert(c)
        }
        if _, err := certs[0].Verify(opts); err != nil {
            return fmt.Errorf("对端证书链校验失败: %w", err)
        }
        return nil
    },
}
```

同时删除客户端配置里无意义的 `ClientCAs` 字段、修正 `:27-29` 的错误注释。
- **兼容性**：现有「`--tls-auto` 生成的同 CA 证书」部署校验必然通过，无需换证书；自签野 CA 的错误部署会从「静默不安全」变为「连接失败并给出明确原因」——这正是期望行为，升级说明里注明。
- **测试**：单测用两个自建 CA 各签一套证书，断言「同 CA 通过、异 CA 拒绝」。

### 2.2 connpool 关闭竞态 → 向已关闭 channel 发送 panic

- **位置**：`internal/agent/proxy/connpool.go:88-114`
- **根因**：`send` 解锁后 `tc.write <- f`，`close` 解锁后 `close(tc.write)`——检查与动作非原子。
- **修复**：**永不关闭数据 channel**，用独立的一次性 `done` channel 广播关闭态：

```go
type tunConn struct {
    mu     sync.Mutex
    closed bool
    done   chan struct{} // close(done) 广播关闭；只关这一次
    write  chan *Frame   // 永不 close，仅容量缓冲
    // ...
}

func (tc *tunConn) send(f *Frame) error {
    select {
    case <-tc.done:
        return errors.New("连接已关闭")
    default:
    }
    select {
    case tc.write <- f:
        return nil
    case <-tc.done:
        return errors.New("连接已关闭")
    default:
        return errors.New("连接写队列满")
    }
}

func (tc *tunConn) close() {
    tc.mu.Lock()
    if tc.closed {
        tc.mu.Unlock()
        return
    }
    tc.closed = true
    close(tc.done) // 唯一的 close 点，幂等由 closed 守卫
    tc.mu.Unlock()
    _ = tc.conn.Close()
}
```

`writeLoop` 从 channel 读帧处改为 `select { case f := <-tc.write: ...; case <-tc.done: return }`。
- **附带（同 PR）**：`Remove` 的 `metrics.ConnActive.Add(-1)` 改为仅在「从池中实际移除」时执行一次（当前双路径重复 -1 导致 `proxy_conn_active` 失真）；`Acquire` 按注释实现真正的轮询（原子递增游标取模）。
- **测试**：`-race` 下并发 send/close 压测；断言多次 Remove 后 `ConnActive` 不为负。

---

## PR-3：部署脚本紧急修复（用户可立即自行打补丁）

### 3.1 uninstall 默认删数据，与文档相反

- **位置**：`deploy/uninstall.sh:53`
- **修复**：`PURGE=0`（默认保留），`--purge` 改为 `PURGE=1`，`--keep-data` 保留为显式别名；帮助文案「默认即如此」删除。同步改 `build/release.sh:152,162-163` 生成的 install.sh 帮助文案（当前文案恰好与实现相反，改完实现后文案即恢复正确）。**发版前必须改，一个字符级的失误就是数据丢失。**

### 3.2 `server.yaml` / `agent.yaml` 0644 含密钥

- **位置**：`deploy/install-server.sh:731`（全文无 chmod）、`deploy/agent-install.sh:480-486` 与 `:1542-1556`
- **修复**：两处 `cat > xxx.yaml` 之后追加 `chmod 600 "$CONFIG_DIR/server.yaml"`（agent 侧同理）；`CONFIG_DIR`/`DATA_DIR` 的 `mkdir -p` 后加 `chmod 700`（server 侧 `DATA_DIR` 含审计/安全事件，建议 `750`）。已在用的存量部署可在升级脚本里补一次 `chmod 600 /etc/monitor-{server,agent}/*.yaml`。

---

## PR-4：升级链路路径穿越

- **位置**：`internal/server/upgrade/upgrade.go:766`（`backupConfigs`）、`version.go:19-43`
- **根因**：manifest `Version` 未经 sanitize 拼进备份文件路径；`parseSemanticVersion` 对预发布段（`1.2.3-xxx` 的 xxx）不做字符校验；`RollbackTo` 完全不走版本校验。
- **修复**（三层纵深，各自独立生效）：
  1. `backupConfigs` 内 `version` 改用 `sanitizeVersion(version)`（与同文件归档路径 `:587/:624/:652` 对齐）；
  2. `parseSemanticVersion` 对 `pre` 段加白名单校验：`^[0-9A-Za-z.\-]+$` 且不含 `..`，不合法即报错（SemVer 规范本就限定预发布段字符集，这是把规范落实）；
  3. `RollbackTo` 补上与 `Apply` 相同的 `applying` 互斥守卫（顺带修复「并发替换二进制」的一般级问题），并对 `mf.Version` 落盘前一律 `sanitizeVersion`。
- **测试**：`version_test.go` 加 `1.0.0-../../evil` 断言解析报错；`upgrade_test.go` 加 manifest Version 含 `../` 时备份路径不逃逸 `Dir` 的断言。

---

## PR-5：remote_write 阻塞放大（最小改造，保持同步语义）

- **位置**：`internal/server/storage/writer.go:157-190`、`vm.go:208`
- **根因**：上报 handler 同步调 `Write`，3 次重试 × 单次 10s（客户端超时被 `max(写超时,查询超时)` 抬高），TSDB 故障时每条上报阻塞 ~30s，接收面整体被拖垮。
- **修复**（不动调用方语义，先止血）：
  1. **写与查分离 HTTP Client**：`vm.go` 为 writer 单独建 `&http.Client{Timeout: writeTimeout}`（querier 保持现状），消除 `maxDuration(wt, qt)`；
  2. **总预算超时**：`Write` 入口 `ctx, cancel := context.WithTimeout(context.Background(), writeTimeout*2)`，各次 `Do` 改 `http.NewRequestWithContext`，退避 sleep 改 `select { <-ctx.Done() }`——TSDB 不可达时单条上报最多阻塞 `2×wt`（默认 10s）而非 30s；
  3. 重试从 3 次降为 2 次（5xx 才重试的现状不变，4xx 仍立即失败）。
- **后续（独立 PR，不阻塞本项）**：上报路径改异步有界队列 + 批量 flush（README 的 Server「无状态」语义不变，仅写盘路径解耦）；该改动影响「上报失败回 500」的契约，需与 Agent 侧丢弃语义一起设计。
- **测试**：`httptest.Server` 模拟挂起（`time.Sleep` 超过 writeTimeout），断言 `Write` 在 `2×wt` 内返回。

---

## PR-6：NodeView 时间轴修复（前端，一行级）

- **位置**：`web/src/components/NodeView.vue:702,967,1001,1022`
- **修复**：删除 4 处 `/ 1e6`，直用 `pt.timestamp`（毫秒，与全仓其余图表及后端 `querier.go` 一致）；删除 `:962` 的错误注释「时间戳由纳秒转为毫秒」。
- **测试**：手动验证主机详情页任一时序图 X 轴为真实时间；如有多-chart 组件测试，加时间戳透传断言。

---

## 建议的发版顺序

1. **PR-3（部署脚本）** 最先：改动最小、风险最低、且 uninstall 数据丢失是当前线上版本就有的行为——越早修，越少人有暴露窗口。
2. **PR-1（告警送达）**：errcode 修复直接决定「告警系统是否可信」。
3. **PR-2（Agent 隧道）**：需要升级全部 Edge/Hub 节点二进制，随下一版本 Agent 发布。
4. **PR-4 / PR-5 / PR-6**：可并入同版本。

每个 PR 附一条审查报告中的对应描述作为 PR body 背景；提交信息建议 `fix(security):` / `fix(alert):` / `fix(deploy):` 前缀，遵循仓库提交规范。
