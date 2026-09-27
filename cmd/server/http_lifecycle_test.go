package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewHTTPServer 断言超时字段：读头与空闲必须有界，全局写超时必须保持 0。
// 一旦设置 WriteTimeout，WebSocket 长连接会在写超时后被服务端单方面切断。
func TestNewHTTPServer(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, 期望 5s", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout != time.Minute {
		t.Errorf("IdleTimeout = %v, 期望 1m", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, 必须为 0（否则 WebSocket 长连接会被切断）", srv.WriteTimeout)
	}
	if srv.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q, 期望透传监听地址", srv.Addr)
	}
	if srv.Handler == nil {
		t.Error("Handler 为空，未透传")
	}
}

// closeCounter 以原子计数记录 closeClients 被调用的次数。
type closeCounter struct{ n int32 }

func (c *closeCounter) fn() func()   { return func() { atomic.AddInt32(&c.n, 1) } }
func (c *closeCounter) count() int32 { return atomic.LoadInt32(&c.n) }

// waitSignalled 等待信号 channel 关闭（或超时失败）。
func waitSignalled(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatalf("等待 %s 超时", what)
	}
}

// waitListenerClosed 轮询直到 addr 不再接受连接，用以确认 Shutdown 已开始关闭监听。
func waitListenerClosed(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Shutdown 后监听 %s 仍未关闭", addr)
}

// waitListening 轮询直到 addr 可建立 TCP 连接（证明服务已在监听）。
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("服务未在 %s 上开始监听", addr)
}

// TestServeUntilStoppedGraceful 覆盖正常路径：ctx 取消触发有界 Shutdown，
// 在途请求在 Shutdown 期间被放行并按 200 完整返回，函数返回 nil，closeClients 恰好一次。
//
// 为避免依赖「服务端 close 后客户端仍能读到缓冲」这一平台相关行为（Windows 上
// close 可能丢弃发送缓冲而发 RST），handler 在写完并 Flush 响应后继续持有连接，
// 等客户端把响应读完才释放，因此关连接的时机不影响读取结果。
func TestServeUntilStoppedGraceful(t *testing.T) {
	gate := make(chan struct{})    // 释放 handler 去写响应
	release := make(chan struct{}) // 响应写完后继续持有连接
	entered := make(chan struct{})
	var wroteOK atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-gate // 阻塞到测试取消 ctx：保证取消时该请求仍在处理中
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err == nil {
			wroteOK.Store(true)
		}
		w.(http.Flusher).Flush()
		<-release // 客户端读取期间保持连接活跃，Shutdown 必须继续等待
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	addr := ln.Addr().String()
	srv := newHTTPServer(addr, h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cc := &closeCounter{}
	done := make(chan error, 1)
	go func() { done <- serveUntilStopped(ctx, srv, ln, cc.fn()) }()

	type result struct {
		status int
		body   string
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, rerr := http.Get("http://" + addr + "/")
		if rerr != nil {
			resCh <- result{err: rerr}
			return
		}
		defer resp.Body.Close()
		body, berr := io.ReadAll(resp.Body)
		if berr != nil {
			resCh <- result{err: berr}
			return
		}
		resCh <- result{status: resp.StatusCode, body: string(body)}
	}()

	waitSignalled(t, entered, "请求到达 handler")

	// 取消 ctx 触发优雅退出；请求此刻仍在 handler 中阻塞。
	cancel()
	// 监听已关闭即说明 Shutdown 已经开始：此后放行的请求属于「Shutdown 期间的在途请求」。
	waitListenerClosed(t, addr)

	close(gate)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("Shutdown 未等待在途请求完成，请求失败: %v", r.err)
		}
		if r.status != http.StatusOK {
			t.Fatalf("状态码 = %d, 期望 200", r.status)
		}
		if r.body != "ok" {
			t.Fatalf("响应体 = %q, 期望 %q", r.body, "ok")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("请求在 Shutdown 期间挂死")
	}

	// 此刻 serveUntilStopped 必须仍在等待（连接未释放），否则说明它没有等待在途请求。
	select {
	case serr := <-done:
		t.Fatalf("Shutdown 未等待在途请求即返回: err=%v", serr)
	default:
	}

	close(release)

	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("信号导致的正常关闭应返回 nil, 得到 %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilStopped 未返回")
	}

	if !wroteOK.Load() {
		t.Error("handler 未能写出响应")
	}
	if got := cc.count(); got != 1 {
		t.Fatalf("closeClients 调用次数 = %d, 期望恰好 1", got)
	}
}

// TestServeUntilStoppedClosesClientsBeforeDrain 断言 I-1 的顺序：收到信号后必须
// 先断开 Hub 长连接，再 Shutdown 排空在途 HTTP 请求。否则排空窗口内推送协程仍在查 TSDB。
//
// 判定方式：closeClients 回调被调用时，在途请求的 handler 必须尚未返回
// （此时它正阻塞在 release 上），即关闭动作发生在「排空之前」。
func TestServeUntilStoppedClosesClientsBeforeDrain(t *testing.T) {
	gate := make(chan struct{})
	release := make(chan struct{})
	entered := make(chan struct{})
	var handlerReturned atomic.Bool
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-gate
		w.Header().Set("Content-Length", "2")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		w.(http.Flusher).Flush()
		<-release
		handlerReturned.Store(true)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := newHTTPServer(ln.Addr().String(), h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var closeCount atomic.Int32
	var closedBeforeDrain atomic.Bool
	called := make(chan struct{})
	closeClients := func() {
		if closeCount.Add(1) == 1 {
			closedBeforeDrain.Store(!handlerReturned.Load())
			close(called)
		}
	}

	done := make(chan error, 1)
	go func() { done <- serveUntilStopped(ctx, srv, ln, closeClients) }()

	go func() {
		resp, rerr := http.Get("http://" + ln.Addr().String() + "/")
		if rerr == nil {
			resp.Body.Close()
		}
	}()

	waitSignalled(t, entered, "请求到达 handler")
	cancel()

	// closeClients 应立刻被调用；此刻 handler 仍阻塞在 release 上，排空尚未发生。
	// 若它迟迟不来，说明实现把它排在了 Shutdown 之后——此时先放行请求收尾，再明确报错。
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		close(gate)
		close(release)
		<-done
		t.Fatal("closeClients 未在 Shutdown 排空之前被调用：WS 推送会在停机窗口内继续查询 TSDB")
	}
	close(gate)
	close(release)

	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("信号导致的正常关闭应返回 nil, 得到 %v", serr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilStopped 未返回")
	}

	if !closedBeforeDrain.Load() {
		t.Error("closeClients 在在途请求排空之后才被调用：WS 推送会在停机窗口内继续查询 TSDB")
	}
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("closeClients 调用次数 = %d, 期望恰好 1", got)
	}
}

// TestServeUntilStoppedShutdownTimeoutForcesClose 覆盖 Shutdown 超时：
// 卡住的请求不会让函数挂死，超时后 Close 强制释放连接。
func TestServeUntilStoppedShutdownTimeoutForcesClose(t *testing.T) {
	entered := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // 永不主动返回：模拟无法在宽限期内结束的请求
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := newHTTPServer(ln.Addr().String(), h)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cc := &closeCounter{}
	done := make(chan error, 1)
	go func() {
		done <- serveUntilStoppedWithTimeout(ctx, srv, ln, cc.fn(), 50*time.Millisecond)
	}()

	go func() {
		resp, rerr := http.Get("http://" + ln.Addr().String() + "/")
		if rerr == nil {
			resp.Body.Close()
		}
	}()

	waitSignalled(t, entered, "请求到达 handler")
	cancel()

	start := time.Now()
	select {
	case serr := <-done:
		if serr != nil {
			t.Fatalf("超时强制关闭后仍属信号关闭，应返回 nil, 得到 %v", serr)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Shutdown 超时后未及时返回，耗时 %v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilStopped 在 Shutdown 超时后挂死")
	}

	if got := cc.count(); got != 1 {
		t.Fatalf("closeClients 调用次数 = %d, 期望恰好 1", got)
	}
}

// TestServeUntilStoppedListenerClosedExternally 覆盖监听异常：外部关闭 listener
// 不是正常停止，必须返回非 nil 错误，closeClients 仍恰好调用一次。
func TestServeUntilStoppedListenerClosedExternally(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := newHTTPServer(ln.Addr().String(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	// 该路径不依赖 ctx 取消：用 Background 保证只有监听错误能让函数返回。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cc := &closeCounter{}
	done := make(chan error, 1)
	go func() { done <- serveUntilStopped(ctx, srv, ln, cc.fn()) }()

	// 等端口确实在服务，避免「Serve 尚未开始就关闭」的空跑。
	waitListening(t, ln.Addr().String())

	if cerr := ln.Close(); cerr != nil {
		t.Fatalf("关闭 listener 失败: %v", cerr)
	}

	select {
	case serr := <-done:
		if serr == nil {
			t.Fatal("监听被外部关闭应返回非 nil 错误")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntilStopped 未在监听关闭后返回")
	}

	if got := cc.count(); got != 1 {
		t.Fatalf("closeClients 调用次数 = %d, 期望恰好 1", got)
	}
}
