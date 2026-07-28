// Package reporter 实现 Agent 上报逻辑：批量 + 指数退避重试。
package reporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// ReportResponse 是 Server 对上报的响应。command 非空时表示有待执行指令（如 upgrade）。
type ReportResponse struct {
	Status  string `json:"status"`
	Command string `json:"command,omitempty"`
}

// Reporter 发送上报请求到 Server。
type Reporter struct {
	serverURL  string
	node       string
	group      string
	secret     string
	labels     map[string]string
	httpClient *http.Client
	maxRetries int
}

// New 创建 Reporter。secret 为接入授权密钥（与 Server agentAuth.secret 一致，可为空）。
func New(serverURL, node, group, secret string, labels map[string]string) *Reporter {
	return &Reporter{
		serverURL:  serverURL,
		node:       node,
		group:      group,
		secret:     secret,
		labels:     labels,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		maxRetries: 5,
	}
}

// ReportFull 上报完整 payload（含主机信息），返回 Server 响应（可能含待执行指令）。
func (r *Reporter) ReportFull(p model.ReportPayload) (ReportResponse, error) {
	return r.sendWithRetry(p)
}

func (r *Reporter) sendWithRetry(p model.ReportPayload) (ReportResponse, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return ReportResponse{}, err
	}
	url := r.serverURL
	if len(url) == 0 {
		return ReportResponse{}, fmt.Errorf("serverURL 为空")
	}
	// 确保路径以 /api/v1/report 结尾
	target := url
	if len(target) > 0 && target[len(target)-1] != '/' {
		target += "/"
	}
	target += "api/v1/report"

	var lastErr error
	var resp ReportResponse
	backoff := time.Second
	for attempt := 0; attempt < r.maxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
		req, e := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
		if e != nil {
			lastErr = e
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if r.secret != "" {
			req.Header.Set("X-Agent-Secret", r.secret)
		}
		httpResp, e := r.httpClient.Do(req)
		if e != nil {
			lastErr = e
			slog.Warn("上报失败，将重试", "attempt", attempt+1, "err", e)
			continue
		}
		if httpResp.StatusCode/100 == 2 {
			// 解析响应体（可能含 command 指令）
			if e := json.NewDecoder(httpResp.Body).Decode(&resp); e != nil {
				// 解析失败不影响上报成功状态
				resp = ReportResponse{Status: "ok"}
			}
			httpResp.Body.Close()
			return resp, nil
		}
		httpResp.Body.Close()
		lastErr = fmt.Errorf("上报返回 %d", httpResp.StatusCode)
		// 4xx 不重试
		if httpResp.StatusCode/100 == 4 {
			return ReportResponse{}, lastErr
		}
	}
	slog.Error("上报超过重试上限，丢弃本批数据", "node", r.node, "err", lastErr)
	return ReportResponse{}, lastErr
}
