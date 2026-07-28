package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// promResponse 是 VM PromQL 接口返回结构。
type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// vector: 单点；matrix: 多点的字符串时间
			Value []interface{} `json:"value"`
			// matrix 使用 Values
			Values [][]interface{} `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// QueryRange 按时间范围查询。入参 start/end/step 单位统一为毫秒
// （前端 Date.now() 与告警存储 UnixMilli() 均传毫秒），此处转换为
// VictoriaMetrics 期望的秒级时间戳与步长。
func (s *PromStorage) QueryRange(node, name string, labels map[string]string, start, end, step int64) ([]model.Series, error) {
	stepSec := step / 1000
	if stepSec < 1 {
		stepSec = 1
	}
	expr, err := buildExpr(node, name, labels)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("query", expr)
	q.Set("start", strconv.FormatInt(start/1000, 10))
	q.Set("end", strconv.FormatInt(end/1000, 10))
	q.Set("step", strconv.FormatInt(stepSec, 10))

	u := s.queryRangeURL + "?" + q.Encode()
	resp, err := s.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("query_range 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("query_range 返回 %d: %s", resp.StatusCode, string(body))
	}
	return parsePromResult(body)
}

// QueryLatest 查询某节点某指标最近一个样本点。
func (s *PromStorage) QueryLatest(node, name string, labels map[string]string) (*model.Point, error) {
	series, err := s.QueryInstant(node, name, labels)
	if err != nil {
		return nil, err
	}
	if len(series) == 0 || len(series[0].Points) == 0 {
		return nil, nil
	}
	return &series[0].Points[len(series[0].Points)-1], nil
}

// QueryInstant 即时查询，返回所有匹配序列（向量结果）。
func (s *PromStorage) QueryInstant(node, name string, labels map[string]string) ([]model.Series, error) {
	expr, err := buildExpr(node, name, labels)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("query", expr)
	u := s.queryURL + "?" + q.Encode()
	resp, err := s.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("query 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("query 返回 %d: %s", resp.StatusCode, string(body))
	}
	return parsePromResult(body)
}

// QueryAllLatest 对单一指标跨所有节点做即时查询（不限定 node），返回完整序列集合。
// 用于主机列表一次性拉取全部节点的某项指标，再在调用方按 node 标签聚合。
func (s *PromStorage) QueryAllLatest(name string, labels map[string]string) ([]model.Series, error) {
	if !isValidMetricName(name) {
		return nil, fmt.Errorf("invalid metric name: %q", name)
	}
	// 无标签时直接用 metric name（不加大括号，避免某些 PromQL 实现不支持空匹配器）
	expr := name
	if len(labels) > 0 {
		expr += "{"
		first := true
		for k, v := range labels {
			if !isValidLabelName(k) {
				return nil, fmt.Errorf("invalid label name: %q", k)
			}
			if !isValidLabelValue(v) {
				return nil, fmt.Errorf("invalid label value for %s: %q", k, v)
			}
			if !first {
				expr += ","
			}
			expr += k + "=" + quotePromQLValue(v)
			first = false
		}
		expr += "}"
	}
	q := url.Values{}
	q.Set("query", expr)
	u := s.queryURL + "?" + q.Encode()
	resp, err := s.httpClient.Get(u)
	if err != nil {
		return nil, fmt.Errorf("query 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("query 返回 %d: %s", resp.StatusCode, string(body))
	}
	return parsePromResult(body)
}

// parsePromResult 解析 PromQL 返回为 Series 列表。
func parsePromResult(body []byte) ([]model.Series, error) {
	var pr promResponse
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("解析时序库返回失败: %w", err)
	}
	if pr.Status != "success" {
		return nil, fmt.Errorf("时序库返回状态异常: %s", pr.Status)
	}
	out := make([]model.Series, 0, len(pr.Data.Result))
	for _, r := range pr.Data.Result {
		ser := model.Series{Labels: r.Metric, Points: nil}
		rows := r.Values
		if len(rows) == 0 && len(r.Value) == 2 {
			rows = [][]interface{}{r.Value}
		}
		for _, row := range rows {
			if len(row) != 2 {
				continue
			}
			ts, v, ok := parseSample(row[0], row[1])
			if !ok {
				continue
			}
			ser.Points = append(ser.Points, model.Point{Timestamp: ts, Value: v})
		}
		out = append(out, ser)
	}
	return out, nil
}

// parseSample 解析 [timestamp, value]，timestamp 可为字符串/数字，value 为字符串/数字。
func parseSample(tsRaw, vRaw interface{}) (int64, float64, bool) {
	// value
	var value float64
	switch v := vRaw.(type) {
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, 0, false
		}
		value = f
	case float64:
		value = v
	default:
		return 0, 0, false
	}
	// timestamp（秒，可能为字符串或数字）
	var tsSec float64
	switch t := tsRaw.(type) {
	case string:
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, 0, false
		}
		tsSec = f
	case float64:
		tsSec = t
	default:
		return 0, 0, false
	}
	return int64(tsSec * float64(time.Second)), value, true
}

// Close 释放资源（时序库为远端服务，无需特殊处理）。
func (s *PromStorage) Close() error {
	return nil
}

// postJSON 通用 POST JSON 辅助（预留扩展）。
func postJSON(client *http.Client, urlStr string, payload interface{}) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(urlStr, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
