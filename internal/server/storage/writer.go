package storage

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
	"unsafe"

	"github.com/golang/snappy"

	"github.com/nebula/monitor/internal/model"
)

// 以下结构为 Prometheus remote_write 协议中 WriteRequest 的最小 protobuf 表示，
// 手写编码以避免引入整个 github.com/prometheus/prometheus 仓库。

// promLabel 对应 prompb.Label。
type promLabel struct {
	name  string
	value string
}

// promSample 对应 prompb.Sample（timestamp 为毫秒）。
type promSample struct {
	timestampMs int64
	value       float64
}

// promTimeSeries 对应 prompb.TimeSeries。
type promTimeSeries struct {
	labels  []promLabel
	samples []promSample
}

// ---- protobuf wire 编码 ----

func putVarint(buf *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

func putTag(buf *bytes.Buffer, fieldNum int, wireType int) {
	putVarint(buf, uint64((fieldNum<<3)|wireType))
}

// appendVarintField 追加一个 varint 字段（wire type 0）。
func appendVarintField(buf *bytes.Buffer, fieldNum int, v uint64) {
	putTag(buf, fieldNum, 0)
	putVarint(buf, v)
}

// appendFixed64Field 追加一个 64 位定点字段（wire type 1，用于 float64）。
func appendFixed64Field(buf *bytes.Buffer, fieldNum int, v uint64) {
	putTag(buf, fieldNum, 1)
	buf.WriteByte(byte(v))
	buf.WriteByte(byte(v >> 8))
	buf.WriteByte(byte(v >> 16))
	buf.WriteByte(byte(v >> 24))
	buf.WriteByte(byte(v >> 32))
	buf.WriteByte(byte(v >> 40))
	buf.WriteByte(byte(v >> 48))
	buf.WriteByte(byte(v >> 56))
}

// appendBytesField 追加一个长度前缀的字节字段（wire type 2）。
func appendBytesField(buf *bytes.Buffer, fieldNum int, b []byte) {
	putTag(buf, fieldNum, 2)
	putVarint(buf, uint64(len(b)))
	buf.Write(b)
}

func (l promLabel) encode(buf *bytes.Buffer) {
	var inner bytes.Buffer
	appendBytesField(&inner, 1, []byte(l.name))
	appendBytesField(&inner, 2, []byte(l.value))
	appendBytesField(buf, 1, inner.Bytes())
}

func (s promSample) encode(buf *bytes.Buffer) {
	var inner bytes.Buffer
	// prompb.Sample: field1=value(double/fixed64), field2=timestamp(int64/varint)
	appendFixed64Field(&inner, 1, float64ToUint64(s.value))
	appendVarintField(&inner, 2, uint64(s.timestampMs))
	appendBytesField(buf, 2, inner.Bytes())
}

func (ts promTimeSeries) encode(buf *bytes.Buffer) {
	var inner bytes.Buffer
	for _, l := range ts.labels {
		l.encode(&inner)
	}
	for _, s := range ts.samples {
		s.encode(&inner)
	}
	appendBytesField(buf, 1, inner.Bytes())
}

func float64ToUint64(f float64) uint64 {
	return *(*uint64)(unsafe.Pointer(&f))
}

// Write 将一批指标编码为 remote_write 并 POST 到时序库。
// 同一标签集（指标名+node+group+附加标签）的样本合并为一个 TimeSeries。
func (s *PromStorage) Write(metrics []model.Metric) error {
	if len(metrics) == 0 {
		return nil
	}
	groups := map[string]*promTimeSeries{}
	order := []string{}

	for _, m := range metrics {
		labels := map[string]string{
			"__name__": m.Name,
			"node":     m.Node,
		}
		for k, v := range m.Labels {
			// node 标签由 Metric.Node 决定，避免被 Labels 覆盖导致查询错位
			if k == "node" {
				continue
			}
			labels[k] = v
		}
		key := labelSetKey(labels)
		ts, ok := groups[key]
		if !ok {
			ts = &promTimeSeries{}
			// 标签按 key 排序保证编码稳定
			keys := make([]string, 0, len(labels))
			for k := range labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				ts.labels = append(ts.labels, promLabel{name: k, value: labels[k]})
			}
			groups[key] = ts
			order = append(order, key)
		}
		ts.samples = append(ts.samples, promSample{
			timestampMs: m.Timestamp,
			value:       m.Value,
		})
	}

	var body bytes.Buffer
	for _, k := range order {
		groups[k].encode(&body)
	}

	compressed := snappy.Encode(nil, body.Bytes())
	const maxAttempts = 3
	backoff := 200 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}
		req, err := http.NewRequest(http.MethodPost, s.writeURL, bytes.NewReader(compressed))
		if err != nil {
			return fmt.Errorf("构造 remote_write 请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-protobuf")
		req.Header.Set("Content-Encoding", "snappy")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			lastErr = err // 网络错误，重试
			continue
		}
		if resp.StatusCode/100 == 2 {
			resp.Body.Close()
			return nil
		}
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// 4xx 为请求格式错误，不重试
		if resp.StatusCode/100 == 4 {
			return fmt.Errorf("remote_write 返回 %d: %s", resp.StatusCode, string(msg))
		}
		// 5xx（含 VM 短暂不可用）重试
		lastErr = fmt.Errorf("remote_write 返回 %d: %s", resp.StatusCode, string(msg))
	}
	return fmt.Errorf("remote_write 失败(已重试 %d 次): %w", maxAttempts, lastErr)
}

// labelSetKey 为标签集生成稳定 key。
func labelSetKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte('&')
	}
	return b.String()
}
