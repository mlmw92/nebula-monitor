// buffer.go 实现内存环形缓冲：Edge 断连期间请求入队，恢复后重放。
//
// 缓冲为环形数组，满时丢弃最旧请求并计数 DroppedTotal。
// 重放时按 FIFO 顺序重新投递到隧道，期间新请求继续入队（重放与新请求并发）。
package proxy

import "sync"

// bufferedReq 是缓冲中的待转发请求。
type bufferedReq struct {
	Frame *Frame        // 原始 data 帧
	Done  chan *Frame   // 响应回传 channel（与实时转发共用同一签名）
}

// RingBuffer 是内存环形缓冲。
type RingBuffer struct {
	mu      sync.Mutex
	buf     []*bufferedReq
	size    int
	head    int // 下一个写入位置
	count   int // 当前元素数
	metrics *Metrics
}

// NewRingBuffer 创建指定容量的环形缓冲。
func NewRingBuffer(size int, metrics *Metrics) *RingBuffer {
	if size <= 0 {
		size = 1000
	}
	return &RingBuffer{
		buf:     make([]*bufferedReq, size),
		size:    size,
		metrics: metrics,
	}
}

// Push 入队一个请求。缓冲满时丢弃最旧请求并计数。
// 返回该请求的响应 channel（调用方阻塞等待，或在 cancel 时被唤醒）。
func (b *RingBuffer) Push(f *Frame) *bufferedReq {
	req := &bufferedReq{Frame: f, Done: make(chan *Frame, 1)}
	b.mu.Lock()
	if b.count == b.size {
		// 满：丢弃最旧（head 位置即最旧元素）
		old := b.buf[b.head]
		if old != nil && b.metrics != nil {
			b.metrics.DroppedTotal.Add(1)
		}
		if old != nil {
			close(old.Done) // 唤醒等待方（收到 nil 表示被丢弃）
		}
		b.buf[b.head] = req
		b.head = (b.head + 1) % b.size
	} else {
		pos := (b.head + b.count) % b.size
		b.buf[pos] = req
		b.count++
	}
	if b.metrics != nil {
		b.metrics.BufferDepth.Store(int64(b.count))
	}
	b.mu.Unlock()
	return req
}

// Drain 取出当前所有缓冲请求（用于重连后重放）。返回后缓冲清空。
func (b *RingBuffer) Drain() []*bufferedReq {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*bufferedReq, 0, b.count)
	for i := 0; i < b.count; i++ {
		pos := (b.head + i) % b.size
		if b.buf[pos] != nil {
			out = append(out, b.buf[pos])
			b.buf[pos] = nil
		}
	}
	b.count = 0
	b.head = 0
	if b.metrics != nil {
		b.metrics.BufferDepth.Store(0)
	}
	return out
}

// Len 返回当前缓冲元素数。
func (b *RingBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}
