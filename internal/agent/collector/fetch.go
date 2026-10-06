package collector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// exporterHealth 判定 exporter 模式下一个实例是否真的健康。
//
// HTTP 200 只代表**抓取成功**，不代表被监控对象可用：exporter 会在目标实例宕机时
// 照常返回 200，把 `*_up 0` 写在正文里。因此判定顺序是：
//
//  1. 正文里出现任一候选存活指标（平台目录名 `<mw>_instance_up`，或第三方 exporter
//     惯用名如 `mysql_up` / `pg_up` / `nginx_up` / `redis_up`）→ 以它的值决定；
//  2. 否则只要解析到本类型业务指标，就认为实例可达（有些 exporter 不暴露 up 指标）；
//  3. 空正文、仅注释、全是无关指标 → 不健康（绝不默认在线）。
//
// 存活指标在**原始文本**上查找，而不是在按前缀过滤后的指标里：postgres_exporter 的
// `pg_up` 不带 `postgres_` 前缀，按前缀过滤后就找不到了。
func exporterHealth(text string, hasBusinessMetrics bool, upNames ...string) bool {
	if value, ok := firstMetricValue(text, upNames); ok {
		return value > 0.5
	}
	return hasBusinessMetrics
}

// firstMetricValue 在 Prometheus 文本中查找首个命中的指标值（跳过注释与空行）。
//
// 命中顺序有优先级：**平台目录名 `<mw>_instance_up` 优先于第三方 exporter 惯用名
// （`mysql_up` / `pg_up` / `nginx_up` …）**。两者可能同时出现在同一份 exposition 里
// （exporter 抓多个 target、或本平台 receiver 合成过同名序列），此时平台口径描述的是
// 「我们配置的这个实例」，而上游 `*_up` 可能来自别的 target——优先采信前者，
// 否则 `mysql_up 1` 会把 `mysql_instance_up 0` 覆盖成在线。
// 同一优先级内仍是「首个出现的序列」生效。
func firstMetricValue(text string, names []string) (float64, bool) {
	if len(names) == 0 {
		return 0, false
	}
	const noMatch = 2
	rank := make(map[string]int, len(names))
	for _, name := range names {
		r := 1
		if strings.HasSuffix(name, "_instance_up") {
			r = 0
		}
		if old, ok := rank[name]; !ok || r < old {
			rank[name] = r
		}
	}
	best, bestRank := 0.0, noMatch
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, value, ok := parsePromLine(line)
		if !ok {
			continue
		}
		r, hit := rank[name]
		if !hit || r >= bestRank {
			continue
		}
		best, bestRank = value, r
		if bestRank == 0 {
			break // 平台口径已命中，不必再看
		}
	}
	return best, bestRank != noMatch
}

// safeExporterTarget 只保留 scheme 与 host：exporter URL 允许携带 userinfo、query
// token，甚至把凭据放在 path 里（如 /push/<token>/metrics），这些都不该进日志。
func safeExporterTarget(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "<invalid>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path, u.RawPath, u.Opaque = "", "", ""
	return u.String()
}

func safeExporterError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

// fetchMetricsText 是 fetchMetrics 的字符串版本，便于直接交给 Prometheus 文本解析函数。
func fetchMetricsText(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	body, err := fetchMetrics(ctx, client, rawURL)
	if err != nil {
		return "", err
	}
	return string(body), nil
}
