// edge.go 实现 Edge Proxy：区 A 边界代理。
//
// Edge 启动本地 HTTP 监听口汇聚采集 Agent 上报，主动拨出 TLS 隧道到 Hub。
// 收到本地请求后，封装为 data 帧经隧道转发，阻塞等待 resp 帧回传。
// 隧道断连期间请求入环形缓冲，重连后补发。
package proxy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// Edge 是区 A 边界代理。
type Edge struct {
	cfg      edgeCfg
	metrics  *Metrics
	pool     *connPool
	router   *respRouter
	buffer   *RingBuffer
	reconn   *Reconnector
	tlsCfg   *tls.Config
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// edgeCfg 是 Edge 运行配置。
type edgeCfg struct {
	Listen     string // 本地 HTTP 监听口，如 :18080
	HubAddr    string // Hub 地址 host:port，如 10.0.0.2:8443
	TLSCert    string
	TLSKey     string
	TLSCA      string
	BufferSize int
	PoolSize   int
}

// NewEdge 创建 Edge 代理。
func NewEdge(cfg edgeCfg) (*Edge, error) {
	tlsCfg, err := buildClientTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.TLSCA)
	if err != nil {
		return nil, err
	}
	metrics := NewMetrics()
	return &Edge{
		cfg:    cfg,
		metrics: metrics,
		tlsCfg: tlsCfg,
		router: newRespRouter(),
		buffer: NewRingBuffer(cfg.BufferSize, metrics),
		reconn: NewReconnector(2*time.Second, 60*time.Second, metrics),
		stopCh: make(chan struct{}),
	}, nil
}

// Metrics 返回自监控指标快照。
func (e *Edge) Metrics() MetricsSnapshot {
	return e.metrics.Snapshot()
}

// Run 启动 Edge：先建立隧道连接池，再启动本地 HTTP 监听。
// 隧道断连时自动重连并补发缓冲。阻塞直至 ctx 取消或致命错误。
func (e *Edge) Run(ctx context.Context) error {
	e.pool = newConnPool(e.cfg.HubAddr, e.tlsCfg, e.cfg.PoolSize, e.metrics)

	// 初始建连（失败不致命：HTTP 监听照常启动，请求入缓冲等待重连后补发）
	e.connectAll()

	// 启动重连守护：池中连接数 < PoolSize 时持续尝试补连
	e.wg.Add(1)
	go e.reconnectLoop(ctx)

	// 启动本地 HTTP 监听
	srv := &http.Server{
		Addr:    e.cfg.Listen,
		Handler: http.HandlerFunc(e.handleLocal),
	}
	listener, err := net.Listen("tcp", e.cfg.Listen)
	if err != nil {
		return fmt.Errorf("Edge 监听失败: %w", err)
	}
	slog.Info("Edge 本地监听已启动", "listen", e.cfg.Listen, "hub", e.cfg.HubAddr)

	errCh := make(chan error, 1)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		err := srv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("Edge 退出中")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		e.pool.CloseAll()
		e.router.cancelAll()
		e.wg.Wait()
		return nil
	case err := <-errCh:
		e.pool.CloseAll()
		return err
	}
}

// connectAll 尝试建立到 PoolSize 条连接。
func (e *Edge) connectAll() {
	for i := 0; i < e.cfg.PoolSize; i++ {
		if err := e.connectOne(); err != nil {
			slog.Warn("初始隧道建连失败，将由重连守护补连", "err", err)
			break
		}
	}
}

// connectOne 建立一条隧道连接并启动读循环。
func (e *Edge) connectOne() error {
	conn, err := tls.Dial("tcp", e.cfg.HubAddr, e.tlsCfg)
	if err != nil {
		return fmt.Errorf("TLS 拨号失败: %w", err)
	}
	tc, err := e.pool.dial(func() (net.Conn, error) { return conn, nil })
	if err != nil {
		_ = conn.Close()
		return err
	}
	e.pool.Add(tc)
	e.reconn.Reset()

	// 心跳守护
	go heartbeater(tc, 15*time.Second, e.stopCh)

	// 读循环：处理 resp 帧与 ping 响应
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		readLoop(conn, func(f *Frame) {
			switch f.Type {
			case FrameResp:
				e.router.deliver(f)
			case FramePing:
				// Hub→Edge 方向的心跳通常不需要，回 pong 可选；这里忽略
			}
		}, func() {
			slog.Warn("隧道连接断开，触发重连", "remote", e.cfg.HubAddr)
			e.pool.Remove(tc)
		})
	}()

	// 连接建立后立即补发缓冲
	go e.replayBuffer()
	return nil
}

// reconnectLoop 持续维持连接池水位。
func (e *Edge) reconnectLoop(ctx context.Context) {
	defer e.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if e.pool.ActiveCount() < e.cfg.PoolSize {
				backoff := e.reconn.NextBackoff()
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					return
				}
				if err := e.connectOne(); err != nil {
					slog.Warn("重连失败", "err", err)
				}
			}
		}
	}
}

// replayBuffer 重连后补发缓冲请求。
func (e *Edge) replayBuffer() {
	reqs := e.buffer.Drain()
	if len(reqs) == 0 {
		return
	}
	slog.Info("补发缓冲请求", "count", len(reqs))
	for _, br := range reqs {
		go e.forward(br)
	}
}

// handleLocal 处理采集 Agent 的本地 HTTP 请求：封装为 data 帧转发，等待 resp 回传。
func (e *Edge) handleLocal(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}

	reqID := newRequestID()
	headers := make(map[string]string)
	for k, v := range r.Header {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}
	f := &Frame{
		Type:      FrameData,
		RequestID: reqID,
		Method:    r.Method,
		Path:      r.URL.Path,
		Headers:   headers,
		Body:      body,
	}

	// 无可用连接：入缓冲等待重连后补发
	if _, err := e.pool.Acquire(); err != nil {
		br := e.buffer.Push(f)
		go e.forward(br)
		// 阻塞等待（重连补发后唤醒）
		select {
		case resp := <-br.Done:
			e.writeResp(w, resp)
		case <-time.After(30 * time.Second):
			http.Error(w, "隧道不可用（缓冲超时）", http.StatusBadGateway)
		}
		return
	}

	// 有连接：直接转发
	br := &bufferedReq{Frame: f, Done: make(chan *Frame, 1)}
	e.forward(br)
	select {
	case resp := <-br.Done:
		e.writeResp(w, resp)
	case <-time.After(30 * time.Second):
		e.router.cancel(reqID)
		http.Error(w, "转发超时", http.StatusGatewayTimeout)
	}
}

// forward 将请求帧经隧道转发，并将响应投递回 br.Done。
// 同时注册到 router 等待 resp 帧回传。
func (e *Edge) forward(br *bufferedReq) {
	f := br.Frame
	pending := e.router.register(f.RequestID)

	tc, err := e.pool.Acquire()
	if err != nil {
		// 无连接：入缓冲
		e.buffer.Push(f)
		return
	}
	if err := tc.send(f); err != nil {
		e.pool.Remove(tc)
		e.metrics.DroppedTotal.Add(1)
		return
	}

	// 等待响应：deliver 时会设置 pending.resp 并 close(done)
	select {
	case <-pending.done:
		if pending.resp != nil {
			br.Done <- pending.resp
			e.metrics.ForwardTotal.Add(1)
		}
	case <-time.After(30 * time.Second):
		e.router.cancel(f.RequestID)
	}
}

// writeResp 将隧道响应帧写回本地 HTTP 客户端。
func (e *Edge) writeResp(w http.ResponseWriter, f *Frame) {
	if f == nil {
		http.Error(w, "请求被丢弃（缓冲满）", http.StatusServiceUnavailable)
		return
	}
	for k, v := range f.Headers {
		w.Header().Set(k, v)
	}
	if f.Status == 0 {
		f.Status = http.StatusOK
	}
	w.WriteHeader(f.Status)
	_, _ = w.Write(f.Body)
}

// newRequestID 生成请求 ID（16 字节十六进制，无外部依赖）。
func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}
