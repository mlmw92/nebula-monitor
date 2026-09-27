# Server HTTP 生命周期加固 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Server 能防止慢速请求头 / 闲置连接长期占用资源，并在 SIGINT / SIGTERM 下有限等待现有 HTTP 请求完成后退出，保留 WebSocket 服务语义。

**Architecture:** 在 `cmd/server` 内提取配置 HTTP Server 的小函数，以及可测试的 `Serve` + 信号协调函数；HTTP 只设置 `ReadHeaderTimeout` 和 `IdleTimeout`，不用全局 `WriteTimeout` 截断 WebSocket。收到信号时先停止接收新请求、尝试有界 `Shutdown`，随后取消后台任务并显式断开 Hijack 的 WS 连接；非信号监听故障为错误退出。

**Tech Stack:** Go 1.25、net/http、os/signal、gorilla/websocket。

**Spec:** `docs/superpowers/specs/2026-09-27-scope-and-server-reliability-design.md`（本计划只实现 Server 生命周期；权限与文档另见 `docs/superpowers/plans/2026-09-27-resource-scope-and-alerts.md`）。

## Global Constraints

- 不改变 `storage.Storage` 接口、不引入额外依赖，不影响 Agent 长请求与 WebSocket 长连接；不设全局 `WriteTimeout`。
- SIGINT / SIGTERM 在有界时间内结束；日志必须区分正常关闭与监听失败；`http.ErrServerClosed` 在信号引发时是正常结果。
- 当前在 `main` 分支，工作区已有未跟踪文件；不覆盖、不暂存无关文件。未经用户单独要求，不提交、不推送。

## Review Focus

1. 信号到达时有活动的 HTTP 请求：必须允许它在宽限时间内结束，不能先 cancel 后立即中断；见任务 2 测试。
2. 长时间不结束的 handler：截止时间到后仍能退出而不永久阻塞；见任务 2 测试。
3. `Serve` 在信号前因为监听错误退出：应返回错误而非将它当作正常 shutdown；见任务 2 测试。
4. 存量 WebSocket 已 Hijack、不受 `Shutdown` 管理：退出时需显式关闭连接及发送队列，避免循环泄漏；见任务 1 测试。
5. 普通 HTTP 探针与 WS 握手：读头 / 空闲超时有效但不设置导致 WS 断线的全局写超时；见任务 2 测试。

---

## File Structure

- `internal/server/api/ws.go` + 新增 `internal/server/api/ws_shutdown_test.go`：Hub 管理已升级连接的关闭，复用既有 `Register`/`Unregister` 锁与生命周期；不把关闭职责泄漏到 `cmd/server`。
- 新建 `cmd/server/http_lifecycle.go` + `http_lifecycle_test.go`：`http.Server` 参数与信号/监听停止协调，以 `Serve` 注入 listener 使测试不依赖实际 OS 信号。
- `cmd/server/main.go`：唯一启动集成点，将原有 `ListenAndServe` 改为可注入的生命周期函数；Server 退出后取消本进程后台任务。

### Task 1: Hub 可幂等断开现有 WS 连接

**Files:** Modify `internal/server/api/ws.go:60-131,266-283`; Create `internal/server/api/ws_shutdown_test.go`。

**Interfaces:** Produces `func (h *Hub) CloseClients()`，由任务 2 在停机流程中「ctx 取消之后、`srv.Shutdown` 排空之前」调用（实现期收紧：先断 WS，推送查询在排空窗口内即停止）；不可 `close(h.alertCh)`，因为告警引擎仍可能短暂调用 `BroadcastAlert`。

- [ ] **Step 1: 写失败测试**：用 `httptest.NewServer` 和 `github.com/gorilla/websocket` 建立真实 WS（或构造真实连接）；调用 `CloseClients` 两次后客户端 ReadMessage 返回关闭错误、`h.ClientCount()==0`，再次调用 `Unregister` 不 panic；让 `BroadcastAlert` 在关闭前后调用不 panic。增加 `Run(ctx)` 的取消测试（创建 ctx、启动 goroutine、cancel 后等待完成 channel）及 metrics 客户端持续连接测试（不因写超时提前断开）；不对全局 goroutine 数做脆弱断言。

```go
// 关闭后新 BroadcastAlert 不得向已注销连接写入或 panic。
h.CloseClients()
h.CloseClients()
if n := h.ClientCount(); n != 0 { t.Fatalf("残留 WS 客户端: %d", n) }
h.BroadcastAlert(model.AlertEvent{Node: "web-01"})
```

- [ ] **Step 2: 运行测试确认失败**：`go test ./internal/server/api -run TestHubCloseClients -count=1`，预期因缺少 `CloseClients` 而编译失败。
- [ ] **Step 3: 实现幂等关闭**：在 `Hub` 新增 `done chan struct{}` 和 `stopOnce sync.Once`；`Run(ctx context.Context)` 使用 `select { case <-ctx.Done(): return; case e := <-h.alertCh: ... }`，并在主入口传入后台任务 ctx。`CloseClients` 经 `stopOnce` 关闭 done、在 `h.mu` 内从 map 删除客户端并关闭各自 `send`，锁外关闭对应 `conn` 唤醒 `readPump`；`Unregister` 保持 map 存在性守卫。`pushNodeMetrics` 改为在 ticker 外层 select 监听 `h.done`（不用消费 `client.send`，它是唯一发送队列）；发送时在 `h.mu` 内确认客户端仍在 map 中再写入 `send`，避免与 `CloseClients`、`Unregister` 的 close 并发 panic。`Register` 在 Hub 已关闭时拒绝并主动关闭传入连接，避免停机竞态。不要 close `alertCh`，告警引擎仍可能短暂广播。将 `Client.writePump()` 的结束路径加 `defer c.conn.Close()`（原版只在读循环退出时关闭 `send`，写失败需促使读循环退出），避免断线客户端残留。
- [ ] **Step 4: 验证 WS 子包**：`go test ./internal/server/api -run 'TestHubCloseClients|TestWS_' -count=1`，预期 PASS；`go test ./internal/server/api -count=1`，预期 PASS。

### Task 2: HTTP 超时与有界优雅退出

**Files:** Create `cmd/server/http_lifecycle.go`, `cmd/server/http_lifecycle_test.go`; Modify `cmd/server/main.go:64-65,303-337`。

**Interfaces:** Consumes `(*api.Hub).CloseClients()` and `(*api.Hub).Run(context.Context)`；Produces `func newHTTPServer(addr string, handler http.Handler) *http.Server` 与 `func serveUntilStopped(ctx context.Context, srv *http.Server, listener net.Listener, closeClients func()) error`。`ctx` 为 `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`；唯一生产调用点位于 `main.go`，后台任务的 `runCtx` 独立于 signalCtx。

- [ ] **Step 1: 写失败测试**：`newHTTPServer` 的 `ReadHeaderTimeout==5*time.Second`、`IdleTimeout==60*time.Second`、`WriteTimeout==0`；`net.Listen("tcp", "127.0.0.1:0")` 调用 `serveUntilStopped`，用带阻塞通道的 handler 接到一个正常请求，在 `ctx` cancel 后释放 handler，确认请求返回 200 且函数最终返回 nil、`closeClients` 调用一次。另用永远阻塞的 handler 模拟超时，以可注入的 `shutdownTimeout` 测试参数（测试用 50ms，生产 10s）确认 `Shutdown` 超时后 `srv.Close()`，函数不挂死。手动关闭 listener 时 `serveUntilStopped` 必须返回非 nil，`closeClients` 仍恰好一次。不要使用真实 OS 信号避免干扰测试进程。

```go
srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
if srv.ReadHeaderTimeout != 5*time.Second || srv.IdleTimeout != time.Minute || srv.WriteTimeout != 0 { t.Fatalf("超时配置异常: %+v", srv) }
```

- [ ] **Step 2: 运行测试验证 RED**：`go test ./cmd/server -run 'TestNewHTTPServer|TestServeUntilStopped' -count=1`，预期因函数不存在编译失败。
- [ ] **Step 3: 实现并集成**：`newHTTPServer` 返回设置地址、handler、读头/空闲超时的服务器；`serveUntilStopped` 用 goroutine 执行 `srv.Serve(listener)` 并通过缓冲 channel 报告结果；信号分支调用 `srv.Shutdown(timeoutCtx)`，超时后 `srv.Close()` 释放连接；在所有退出分支调用 `closeClients()`，正常信号关闭返回 nil，异常监听返回 error。将 timeout 值作为私有变体 `serveUntilStoppedWithTimeout(..., timeout time.Duration)` 供测试，生产包装传 10s；main 以 `signal.NotifyContext` 创建 signalCtx，另创建 `runCtx, cancelRun := context.WithCancel(context.Background())` 管理后台任务，防止信号一到就终止正在完成的请求；后台循环用 runCtx。待 HTTP 已关闭且 WebSocket 连接已关闭后显式调用 `cancelRun()`，再 `store.Close()`；非 nil 错误日志记录并 `os.Exit(1)`，不要只依赖 defer（`os.Exit` 会跳过）。

```go
func newHTTPServer(addr string, handler http.Handler) *http.Server {
    return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
}
```

- [ ] **Step 4: 运行并检查边界**：`go test ./cmd/server ./internal/server/api -count=1`、`go vet ./cmd/server ./internal/server/api`、`go test -count=1 ./...`，预期全 PASS；`git diff --check`，预期无空白错误。手工静态确认 `api.MetricsMiddleware` 仍转发 `Hijack` 且没有设置全局 `WriteTimeout`；`Shutdown` 不等待 WS 连接，这些在 Task 1 显式断开。

## 交接

先完成 Hub 可关闭，再集成 Server 退出；每项先 RED 再 GREEN。与权限计划彼此独立；无需等待另一计划完成后才开始测试，但若两计划同时修改 `cmd/server` 和 `internal/server/api`，务必串行合并。未经用户提交授权不运行 `git commit`。
