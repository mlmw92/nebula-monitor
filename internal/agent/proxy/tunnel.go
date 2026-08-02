// Package proxy 实现 Agent 的网闸代理模式（edge/hub）。
//
// Edge 与 Hub 之间通过长连接多路复用 HTTP 隧道通信。隧道协议基于 JSON 帧封装，
// 每帧含类型（data/resp/ping/close）、请求 ID、可选状态码/请求头/请求体。
// 单条 TLS 连接上可并发流转多个请求（按 RequestID 多路复用匹配请求与响应）。
package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// FrameType 隧道帧类型。
type FrameType string

const (
	FrameData  FrameType = "data"  // Edge→Hub: 转发原始 HTTP 请求
	FrameResp  FrameType = "resp"  // Hub→Edge: 响应回传
	FramePing  FrameType = "ping"  // 心跳
	FrameClose FrameType = "close" // 主动关闭
)

// Frame 是隧道上传输的单个帧。
type Frame struct {
	Type      FrameType            `json:"type"`
	RequestID string               `json:"requestId"`           // 请求 ID（多路复用匹配）
	Method    string               `json:"method,omitempty"`    // data 帧的 HTTP 方法
	Path      string               `json:"path,omitempty"`      // data 帧的请求路径（如 /api/v1/report）
	Headers   map[string]string    `json:"headers,omitempty"`   // data 帧携带的原始请求头
	Status    int                  `json:"status,omitempty"`    // resp 帧的状态码
	Body      []byte               `json:"body,omitempty"`      // 请求体或响应体
}

// EncodeFrame 将帧编码为 JSON 并以长度前缀写入（4 字节大端 uint32 + JSON 体）。
// 长度前缀便于接收端按帧边界读取，避免 JSON 流式解析的歧义。
func EncodeFrame(w io.Writer, f *Frame) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("帧编码失败: %w", err)
	}
	var hdr [4]byte
	hdr[0] = byte(len(data) >> 24)
	hdr[1] = byte(len(data) >> 16)
	hdr[2] = byte(len(data) >> 8)
	hdr[3] = byte(len(data))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("写帧头失败: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("写帧体失败: %w", err)
	}
	return nil
}

// DecodeFrame 从流读取并解码一帧。
func DecodeFrame(r io.Reader) (*Frame, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	length := int(hdr[0])<<24 | int(hdr[1])<<16 | int(hdr[2])<<8 | int(hdr[3])
	if length <= 0 || length > 64*1024*1024 { // 单帧上限 64MB，防恶意大帧
		return nil, fmt.Errorf("帧长度非法: %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var f Frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("帧解码失败: %w", err)
	}
	return &f, nil
}

// pendingReq 表示一个待响应的转发请求（Edge 侧维护）。
type pendingReq struct {
	done   chan struct{}
	resp   *Frame
}

// respRouter 是 Edge 侧的响应路由表：按 RequestID 匹配等待中的请求。
// 当 Hub 通过隧道回传 resp 帧时，Edge 根据帧的 RequestID 找到对应 pendingReq 并唤醒。
type respRouter struct {
	mu       sync.Mutex
	pending  map[string]*pendingReq
}

func newRespRouter() *respRouter {
	return &respRouter{pending: make(map[string]*pendingReq)}
}

// register 注册一个待响应请求，返回等待 channel。
func (r *respRouter) register(reqID string) *pendingReq {
	p := &pendingReq{done: make(chan struct{})}
	r.mu.Lock()
	r.pending[reqID] = p
	r.mu.Unlock()
	return p
}

// deliver 投递响应帧，唤醒对应请求的等待方。返回是否命中。
func (r *respRouter) deliver(f *Frame) bool {
	r.mu.Lock()
	p, ok := r.pending[f.RequestID]
	if ok {
		delete(r.pending, f.RequestID)
	}
	r.mu.Unlock()
	if !ok {
		return false
	}
	p.resp = f
	close(p.done)
	return true
}

// cancel 取消一个待响应请求（超时或连接断开时调用）。
func (r *respRouter) cancel(reqID string) {
	r.mu.Lock()
	p, ok := r.pending[reqID]
	if ok {
		delete(r.pending, reqID)
	}
	r.mu.Unlock()
	if ok {
		close(p.done)
	}
}

// cancelAll 取消所有待响应请求（连接断开时调用）。
func (r *respRouter) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, p := range r.pending {
		close(p.done)
		delete(r.pending, id)
	}
}
