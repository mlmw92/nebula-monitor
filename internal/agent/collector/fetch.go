package collector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultFetchTimeout 是拉取 exporter /metrics、Nginx stub_status 等 HTTP 内容的default
// 客户端超时。它是单次请求的自身上界；请求同时受采集任务级 ctx（collectTimeout）约束，
// 两者谁先到就按谁结束。
const defaultFetchTimeout = 8 * time.Second

// fetchMetrics 以 ctx 拉取 rawURL 的响应体。
//
// 相比改造前分散在各采集器内的 `client.Get`，统一在此处做三件事：
//   - 用 http.NewRequestWithContext 构造请求，任务超时/取消时立即中止在途请求；
//   - 校验 HTTP 状态码（非 200 一律视为失败，避免把错误页面当作指标文本解析）；
//   - 统一默认超时（defaultFetchTimeout）。
//
// client 为 nil 时使用默认客户端；调用方需要自定义 Transport（如自签证书）时自行传入。
func fetchMetrics(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: defaultFetchTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exporter 返回状态码 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// fetchMetricsText 是 fetchMetrics 的字符串版本，便于直接交给 Prometheus 文本解析函数。
func fetchMetricsText(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	body, err := fetchMetrics(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
