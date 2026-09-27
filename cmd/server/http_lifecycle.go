package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// defaultShutdownTimeout 是信号驱动的优雅退出宽限期：在途请求可在该时间内完成，
// 超时后强制 Close 释放连接，避免进程无法停止。
const defaultShutdownTimeout = 10 * time.Second

// newHTTPServer 构造中心服务端的 HTTP 服务器，带读头与空闲超时以抵御慢速连接。
//
// 刻意保持 WriteTimeout = 0：net/http 的 WriteTimeout 是「请求开始读时即设定」的绝对
// 期限，会截断「算得久、写得晚」的普通响应（大范围导出 / CSV 之类）。它并不是 WebSocket
// 的保护手段——连接升级时 net/http 会 hijack 并清零连接读写期限（SetDeadline(time.Time{})），
// 所以 hijacked 长连接本就不受 WriteTimeout 约束。上传/上报体积仍由各业务上限控制。
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       time.Minute,
	}
}

// serveUntilStopped 在 listener 上提供 HTTP 服务，直到 ctx 被取消或服务自身退出，
// 并以生产宽限期执行优雅停止。返回 nil 表示按信号正常停止，非 nil 表示监听/服务异常。
func serveUntilStopped(ctx context.Context, srv *http.Server, listener net.Listener, closeClients func()) error {
	return serveUntilStoppedWithTimeout(ctx, srv, listener, closeClients, defaultShutdownTimeout)
}

// serveUntilStoppedWithTimeout 是 serveUntilStopped 的可注入超时版本，便于测试。
//
// 语义：
//   - Serve 先返回（监听被关闭、地址被占用等）：视为异常停止，返回该错误；
//     但 http.ErrServerClosed 属正常收尾，返回 nil。
//   - ctx 取消：先断开 Hub 长连接（closeClients），再以 timeout 为界调用 Shutdown
//     排空在途 HTTP 请求；超时则 Close 强制释放连接，函数不挂死。
//
// 为什么先断 Hub 再 Shutdown：WebSocket 是 hijacked 连接，Shutdown 既不等待也不关闭
// 它们，先断开不影响在途 HTTP 请求的排空；反之若放在 Shutdown 之后，排空窗口内
// （无卡住请求约 1ms，有卡死请求最长 timeout）Hub 的推送协程仍在持续查询 TSDB。
//
// closeClients 在所有退出分支恰好调用一次。
func serveUntilStoppedWithTimeout(ctx context.Context, srv *http.Server, listener net.Listener, closeClients func(), timeout time.Duration) error {
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	select {
	case err := <-serveErr:
		if closeClients != nil {
			closeClients()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	// 收到停止信号：先断开 Hub 长连接，避免推送协程在下面的排空窗口里继续查 TSDB。
	if closeClients != nil {
		closeClients()
	}

	// 再有界优雅退出，允许常规 HTTP 请求完成，但不无限等待长连接。
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// 宽限期内未能排空（例如卡住的请求）：强制关闭，避免停机挂住。
		_ = srv.Close()
	}

	// Shutdown/Close 之后 Serve 必然返回；消费掉结果避免 goroutine 泄漏。
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
