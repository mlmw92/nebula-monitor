// connpool.go 实现 Edge 到 Hub 的隧道连接池管理。
//
// Edge 维护多条到 Hub 的 TLS 长连接（默认 2 条），并发转发请求避免单连接阻塞。
// 连接池负责按需建连、健康检查（心跳超时）、空闲回收、故障剔除。
package proxy

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// tunConn 封装一条隧道连接及其读写控制。
type tunConn struct {
	conn   net.Conn          // 底层 TLS 连接
	write  chan *Frame        // 待发送帧写入通道（写入侧 goroutine 串行化）
	closed bool
	mu     sync.Mutex
}

// connPool 管理 Edge 到 Hub 的多条隧道连接。
type connPool struct {
	target  string            // Hub 地址 host:port
	tlsCfg  interface{}       // *tls.Config（用 interface 避免 import 循环，实际由调用方传入）
	size    int
	conns   []*tunConn
	mu      sync.Mutex
	metrics *Metrics
	stopCh  chan struct{}
}

// ErrPoolEmpty 连接池无可用连接。
var ErrPoolEmpty = errors.New("连接池无可用连接")

// newConnPool 创建连接池。tlsCfg 为 *tls.Config。
func newConnPool(target string, tlsCfg interface{}, size int, metrics *Metrics) *connPool {
	if size <= 0 {
		size = 2
	}
	return &connPool{
		target:  target,
		tlsCfg:  tlsCfg,
		size:    size,
		metrics: metrics,
		stopCh:  make(chan struct{}),
	}
}

// dial 建立一条到 Hub 的 TLS 隧道连接。
// 由调用方提供 dialFn 以解耦 tls.Config 类型依赖。
func (p *connPool) dial(dialFn func() (net.Conn, error)) (*tunConn, error) {
	conn, err := dialFn()
	if err != nil {
		return nil, err
	}
	tc := &tunConn{
		conn:  conn,
		write: make(chan *Frame, 256),
	}
	// 启动写入 goroutine：串行化单连接上的帧写入，避免并发写冲突
	go tc.writeLoop()
	p.metrics.ConnActive.Add(1)
	slog.Info("隧道连接建立", "target", p.target, "local", conn.LocalAddr(), "remote", conn.RemoteAddr())
	return tc, nil
}

// writeLoop 串行化写帧到底层连接。
func (tc *tunConn) writeLoop() {
	for {
		select {
		case f, ok := <-tc.write:
			if !ok {
				return
			}
			if err := EncodeFrame(tc.conn, f); err != nil {
				slog.Warn("隧道写帧失败，连接将关闭", "err", err)
				tc.close()
				return
			}
		}
	}
}

// send 向连接发送一帧。连接已关闭时返回错误。
func (tc *tunConn) send(f *Frame) error {
	tc.mu.Lock()
	if tc.closed {
		tc.mu.Unlock()
		return errors.New("连接已关闭")
	}
	tc.mu.Unlock()
	select {
	case tc.write <- f:
		return nil
	default:
		return errors.New("连接写队列满")
	}
}

// close 关闭连接。
func (tc *tunConn) close() {
	tc.mu.Lock()
	if tc.closed {
		tc.mu.Unlock()
		return
	}
	tc.closed = true
	tc.mu.Unlock()
	close(tc.write)
	_ = tc.conn.Close()
}

// isClosed 返回连接是否已关闭。
func (tc *tunConn) isClosed() bool {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.closed
}

// Acquire 获取一条可用连接（轮询）。无可用连接时返回 ErrPoolEmpty。
func (p *connPool) Acquire() (*tunConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		if !c.isClosed() {
			return c, nil
		}
	}
	return nil, ErrPoolEmpty
}

// Add 添加一条已建立的连接到池。
func (p *connPool) Add(c *tunConn) {
	p.mu.Lock()
	p.conns = append(p.conns, c)
	p.mu.Unlock()
}

// Remove 移除并关闭指定连接。
func (p *connPool) Remove(target *tunConn) {
	p.mu.Lock()
	for i, c := range p.conns {
		if c == target {
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			break
		}
	}
	p.mu.Unlock()
	target.close()
	p.metrics.ConnActive.Add(-1)
}

// ActiveCount 返回活跃连接数。
func (p *connPool) ActiveCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.conns {
		if !c.isClosed() {
			n++
		}
	}
	return n
}

// CloseAll 关闭池中所有连接。
func (p *connPool) CloseAll() {
	close(p.stopCh)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.close()
	}
	p.conns = nil
	p.metrics.ConnActive.Store(0)
}

// readLoop 在一条连接上持续读帧，分发给 handler。
// 读到错误（连接断开）时返回，由调用方触发重连。
func readLoop(conn net.Conn, handler func(*Frame), onClose func()) {
	defer onClose()
	for {
		f, err := DecodeFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("隧道读帧失败", "err", err)
			}
			return
		}
		handler(f)
	}
}

// heartbeater 在一条连接上周期发送心跳帧。
func heartbeater(tc *tunConn, interval time.Duration, stopCh <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			if err := tc.send(&Frame{Type: FramePing}); err != nil {
				return
			}
		}
	}
}
