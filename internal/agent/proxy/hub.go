// hub.go 实现 Hub Proxy：区 B 边界代理。
//
// Hub 启动 TLS 监听口接收 Edge 隧道连接。收到 data 帧后还原为 HTTP 请求
// 转发至真实 Server，响应封装为 resp 帧沿隧道原路返回。
// Hub 对 Server 完全透明，复用现有 /api/v1/report 等接口，Server 无需改造。
package proxy

import (
	"bytes"
	"context"
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

// Hub 是区 B 边界代理。
type Hub struct {
	cfg       hubCfg
	metrics   *Metrics
	tlsCfg    *tls.Config
	serverURL string
	client    *http.Client
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// hubCfg 是 Hub 运行配置。
type hubCfg struct {
	Listen  string // TLS 监听口，如 :8443
	TLSCert string
	TLSKey  string
	TLSCA   string
}

// NewHub 创建 Hub 代理。serverURL 为真实 Server 地址（如 http://127.0.0.1:8080）。
func NewHub(cfg hubCfg, serverURL string) (*Hub, error) {
	tlsCfg, err := buildServerTLSConfig(cfg.TLSCert, cfg.TLSKey, cfg.TLSCA)
	if err != nil {
		return nil, err
	}
	return &Hub{
		cfg:       cfg,
		metrics:   NewMetrics(),
		tlsCfg:    tlsCfg,
		serverURL: serverURL,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		stopCh: make(chan struct{}),
	}, nil
}

// Metrics 返回自监控指标快照。
func (h *Hub) Metrics() MetricsSnapshot {
	return h.metrics.Snapshot()
}

// Run 启动 Hub：监听 TLS 端口接收隧道连接，每条连接独立读循环。
func (h *Hub) Run(ctx context.Context) error {
	ln, err := tls.Listen("tcp", h.cfg.Listen, h.tlsCfg)
	if err != nil {
		return fmt.Errorf("Hub TLS 监听失败: %w", err)
	}
	slog.Info("Hub TLS 监听已启动", "listen", h.cfg.Listen, "server", h.serverURL)

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if !errors.Is(err, net.ErrClosed) {
						slog.Warn("Hub 接受连接失败", "err", err)
					}
				}
				return
			}
			h.metrics.ConnActive.Add(1)
			slog.Info("Edge 隧道连入", "remote", conn.RemoteAddr())
			h.wg.Add(1)
			go h.serveTunnel(conn)
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("Hub 退出中")
		h.wg.Wait()
		h.metrics.ConnActive.Store(0)
		return nil
	case err := <-errCh:
		_ = ln.Close()
		return err
	}
}

// serveTunnel 处理一条 Edge 隧道连接：读 data 帧 → 转发到 Server → 回传 resp 帧。
func (h *Hub) serveTunnel(conn net.Conn) {
	defer h.wg.Done()
	defer func() {
		_ = conn.Close()
		h.metrics.ConnActive.Add(-1)
	}()

	writeCh := make(chan *Frame, 256)
	// 写入 goroutine：串行化写帧
	go func() {
		for f := range writeCh {
			if err := EncodeFrame(conn, f); err != nil {
				slog.Warn("Hub 写帧失败", "err", err)
				return
			}
		}
	}()

	for {
		f, err := DecodeFrame(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("Hub 读帧失败", "err", err)
			}
			close(writeCh)
			return
		}
		switch f.Type {
		case FrameData:
			go h.handleData(f, writeCh)
		case FramePing:
			// 心跳：无需响应，连接活跃即可
		case FrameClose:
			close(writeCh)
			return
		}
	}
}

// handleData 将 data 帧还原为 HTTP 请求转发到真实 Server，响应封装为 resp 帧回传。
func (h *Hub) handleData(f *Frame, writeCh chan<- *Frame) {
	// 构造到真实 Server 的请求
	target := h.serverURL + f.Path
	req, err := http.NewRequest(f.Method, target, bytes.NewReader(f.Body))
	if err != nil {
		h.sendResp(writeCh, f.RequestID, http.StatusBadRequest, nil, "构造请求失败")
		return
	}
	for k, v := range f.Headers {
		// 跳过会被 http.Transport 自动设置的 hop-by-hop 头
		switch k {
		case "Host", "Content-Length", "Transfer-Encoding", "Connection":
		default:
			req.Header.Set(k, v)
		}
	}

	resp, err := h.client.Do(req)
	if err != nil {
		slog.Warn("Hub 转发 Server 失败", "path", f.Path, "err", err)
		h.sendResp(writeCh, f.RequestID, http.StatusBadGateway, nil, "转发失败")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.sendResp(writeCh, f.RequestID, http.StatusBadGateway, nil, "读取响应失败")
		return
	}

	respHeaders := make(map[string]string)
	for k, v := range resp.Header {
		if len(v) > 0 {
			respHeaders[k] = v[0]
		}
	}
	h.sendResp(writeCh, f.RequestID, resp.StatusCode, respHeaders, string(body))
	h.metrics.ForwardTotal.Add(1)
}

// sendResp 发送 resp 帧到隧道写入通道。
func (h *Hub) sendResp(writeCh chan<- *Frame, reqID string, status int, headers map[string]string, body string) {
	resp := &Frame{
		Type:      FrameResp,
		RequestID: reqID,
		Status:    status,
		Headers:   headers,
		Body:      []byte(body),
	}
	select {
	case writeCh <- resp:
	case <-time.After(5 * time.Second):
		slog.Warn("resp 帧投递超时，可能隧道已断", "requestId", reqID)
	}
}


