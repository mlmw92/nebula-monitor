// Package logship 把 Agent 采集到的日志批量上传到 Server（C2 集中日志）。
//
// 与指标上报刻意走两个接口：日志体积大、可以丢、按周期批量；指标小而必须可靠。
package logship

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// DefaultTimeout 是单次上行超时。
//
// 刻意短于采集间隔：上传失败要尽快让位给下一轮，而不是把采集任务拖到超时。
const DefaultTimeout = 10 * time.Second

// Shipper 是日志上行器。
type Shipper struct {
	url    string
	secret string
	node   string
	group  string
	client *http.Client
}

// New 创建上行器。serverURL 与指标上报同一地址（形如 http://10.0.0.1:8080）。
func New(serverURL, secret, node, group string) *Shipper {
	return &Shipper{
		url:    strings.TrimRight(serverURL, "/") + "/api/v1/logs",
		secret: secret,
		node:   node,
		group:  group,
		client: &http.Client{Timeout: DefaultTimeout},
	}
}

// Sink 返回可直接交给日志采集器的接收函数。
func (s *Shipper) Sink() model.LogSink {
	return func(ctx context.Context, source string, origin *model.LogOrigin, lines []model.LogLine) (model.LogSinkResult, error) {
		return s.send(ctx, source, origin, lines)
	}
}

// send 上传一批日志。
//
// 返回值的语义（决定 Agent 记哪个丢弃原因）：
//   - 限速（429）→ Dropped + reason=rate，**不是错误**：服务端在按规则限流，
//     若当成失败处理，日志里会持续刷「上传失败」，把真正的问题淹掉；
//   - 其它非 2xx / 网络错误 → error（记为 reason=unreachable）；
//   - 2xx → 采用服务端回报的 accepted/dropped（每日上限导致的丢弃也是正常结果）。
func (s *Shipper) send(ctx context.Context, source string, origin *model.LogOrigin, lines []model.LogLine) (model.LogSinkResult, error) {
	// 容器身份（origin）只在 podLogs 来源上非空；批次级携带，见 model.LogBatch 的说明。
	body, err := json.Marshal(model.LogBatch{Node: s.node, Group: s.group, Source: source, Lines: lines, Origin: origin})
	if err != nil {
		return model.LogSinkResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return model.LogSinkResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.secret != "" {
		req.Header.Set("X-Agent-Secret", s.secret)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return model.LogSinkResult{}, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return model.LogSinkResult{Dropped: len(lines), Reason: "rate"}, nil
	case resp.StatusCode != http.StatusOK:
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return model.LogSinkResult{}, fmt.Errorf("server 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var res model.LogAppendResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&res); err != nil {
		// 响应体解析失败不影响「服务端已接收」这个事实：按全部接受处理，
		// 否则会因一个格式问题把已落盘的日志再记一遍丢弃。
		return model.LogSinkResult{}, nil
	}
	return model.LogSinkResult{Dropped: res.Dropped, Reason: res.Reason}, nil
}
