package storage

import (
	"bytes"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang/snappy"

	"github.com/nebula/monitor/internal/model"
)

// TestPromTimeSeriesEncode_WireFormat 逐字节锁定 remote_write 的 protobuf 编码。
//
// 手写 wire 格式一旦回归（字段号/类型/字节序写错），时序库会静默拒收或把样本写到错误的
// label 上，且不会在本仓库任何地方报错——因此这里用手算的期望字节做硬断言，而不是只断言
// 「能解压」。
func TestPromTimeSeriesEncode_WireFormat(t *testing.T) {
	ts := promTimeSeries{
		labels:  []promLabel{{name: "__name__", value: "cpu_usage"}},
		samples: []promSample{{timestampMs: 1700000000000, value: 1.5}},
	}
	var buf bytes.Buffer
	ts.encode(&buf)

	// 期望结构（对齐 prompb.WriteRequest / TimeSeries / Label / Sample）：
	//   field1(timeseries, wire2) len=0x29=41
	//     field1(labels, wire2)      len=0x15=21
	//       field1(name,  wire2) len=8  "__name__"
	//       field2(value, wire2) len=9  "cpu_usage"
	//     field2(samples, wire2)     len=0x10=16
	//       field1(value,     wire1/fixed64) 1.5 → 0x3FF8000000000000（小端）
	//       field2(timestamp, wire0/varint)  1700000000000
	want := "0a29" +
		"0a15" +
		"0a08" + hex.EncodeToString([]byte("__name__")) +
		"1209" + hex.EncodeToString([]byte("cpu_usage")) +
		"1210" +
		"09" + "000000000000f83f" +
		"10" + "80d095ffbc31"

	got := hex.EncodeToString(buf.Bytes())
	if got != want {
		t.Fatalf("protobuf 编码与期望不一致\n got = %s\nwant = %s", got, want)
	}
	if len(buf.Bytes()) != 43 {
		t.Fatalf("编码长度 = %d，want 43", len(buf.Bytes()))
	}
}

// TestPutVarint 覆盖 varint 的跨字节边界（0、127、128 与多字节大数）。
func TestPutVarint(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "00"},
		{1, "01"},
		{127, "7f"},
		{128, "8001"},
		{300, "ac02"},
		{1700000000000, "80d095ffbc31"},
	} {
		var buf bytes.Buffer
		putVarint(&buf, tc.in)
		if got := hex.EncodeToString(buf.Bytes()); got != tc.want {
			t.Errorf("putVarint(%d) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// TestLabelSetKey_OrderIndependent 标签集 key 必须与 map 迭代顺序无关，
// 否则同一序列会被拆成多个 TimeSeries（不同轮的编码还不稳定）。
func TestLabelSetKey_OrderIndependent(t *testing.T) {
	a := map[string]string{"__name__": "cpu_usage", "node": "web-01", "env": "prod"}
	b := map[string]string{"env": "prod", "node": "web-01", "__name__": "cpu_usage"}
	if labelSetKey(a) != labelSetKey(b) {
		t.Fatalf("标签集 key 依赖迭代顺序: %s vs %s", labelSetKey(a), labelSetKey(b))
	}
	if labelSetKey(map[string]string{"node": "web-01"}) == labelSetKey(map[string]string{"node": "web-02"}) {
		t.Fatal("不同标签值不应产生相同 key")
	}
}

// countTopLevelTimeSeries 按 wire 格式遍历顶层字段，统计 TimeSeries 个数。
// 这是测试内的独立解析实现，用于校验 Write 的分组合并行为。
func countTopLevelTimeSeries(t *testing.T, b []byte) int {
	t.Helper()
	n := 0
	for i := 0; i < len(b); {
		if b[i] != 0x0a { // WriteRequest.timeseries = field1, wire2
			t.Fatalf("偏移 %d 处的顶层 tag = %#x，want 0x0a", i, b[i])
		}
		i++
		// 读 varint 长度
		var l uint64
		var shift uint
		for {
			if i >= len(b) {
				t.Fatal("长度 varint 截断")
			}
			c := b[i]
			i++
			l |= uint64(c&0x7f) << shift
			if c < 0x80 {
				break
			}
			shift += 7
		}
		if i+int(l) > len(b) {
			t.Fatalf("字段长度 %d 超出缓冲区", l)
		}
		i += int(l)
		n++
	}
	return n
}

// TestWrite_GroupsByLabelSetAndPostsSnappy 端到端校验：同一标签集合并为一个 TimeSeries、
// 请求头正确、body 可被 snappy 解压（用测试内的独立解析器统计 TimeSeries 数）。
func TestWrite_GroupsByLabelSetAndPostsSnappy(t *testing.T) {
	var bodies [][]byte
	var hdrs []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		hdrs = append(hdrs, r.Header.Clone())
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := &PromStorage{writeURL: srv.URL, httpClient: srv.Client(), writeClient: srv.Client(), writeTimeout: 5 * time.Second}
	err := s.Write([]model.Metric{
		{Name: "cpu_usage", Node: "web-01", Timestamp: 1000, Value: 1},
		{Name: "cpu_usage", Node: "web-01", Timestamp: 2000, Value: 2},                                        // 同标签集 → 合并
		{Name: "cpu_usage", Node: "web-01", Timestamp: 3000, Value: 3, Labels: map[string]string{"cpu": "0"}}, // 不同标签集
		{Name: "mem_used_percent", Node: "web-01", Timestamp: 4000, Value: 4},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	if len(bodies) != 1 {
		t.Fatalf("请求次数 = %d，want 1（4 条样本应合并为一次 POST）", len(bodies))
	}
	if ct := hdrs[0].Get("Content-Type"); ct != "application/x-protobuf" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if ce := hdrs[0].Get("Content-Encoding"); ce != "snappy" {
		t.Fatalf("Content-Encoding = %q", ce)
	}

	raw, err := snappy.Decode(nil, bodies[0])
	if err != nil {
		t.Fatalf("snappy 解压失败: %v", err)
	}
	if n := countTopLevelTimeSeries(t, raw); n != 3 {
		t.Fatalf("TimeSeries 个数 = %d，want 3（cpu 两条合并、cpu{cpu=0}、mem 各一个）", n)
	}
	for _, want := range []string{"cpu_usage", "mem_used_percent", "web-01", "__name__"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Fatalf("编码结果缺少 %q", want)
		}
	}
}

// TestWrite_EmptyMetricsIsNoop 空批次不应发起任何请求。
func TestWrite_EmptyMetricsIsNoop(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := &PromStorage{writeURL: srv.URL, httpClient: srv.Client(), writeClient: srv.Client(), writeTimeout: 5 * time.Second}
	if err := s.Write(nil); err != nil {
		t.Fatalf("Write(nil): %v", err)
	}
	if calls != 0 {
		t.Fatalf("空批次发起了 %d 次请求", calls)
	}
}

// TestWrite_DoesNotRetryOn4xx 4xx 属请求格式错误，应立即返回且不重试。
func TestWrite_DoesNotRetryOn4xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	s := &PromStorage{writeURL: srv.URL, httpClient: srv.Client(), writeClient: srv.Client(), writeTimeout: 5 * time.Second}
	err := s.Write([]model.Metric{{Name: "cpu_usage", Node: "web-01", Timestamp: 1000, Value: 1}})
	if err == nil {
		t.Fatal("4xx 应返回错误")
	}
	if calls != 1 {
		t.Fatalf("4xx 请求次数 = %d，want 1（不重试）", calls)
	}
}

// TestWrite_RetriesOn5xx 5xx 应重试一次，第二次成功后返回 nil（退避 200ms）。
func TestWrite_RetriesOn5xx(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := &PromStorage{writeURL: srv.URL, httpClient: srv.Client(), writeClient: srv.Client(), writeTimeout: 5 * time.Second}
	if err := s.Write([]model.Metric{{Name: "cpu_usage", Node: "web-01", Timestamp: 1000, Value: 1}}); err != nil {
		t.Fatalf("第二次应成功: %v", err)
	}
	if calls != 2 {
		t.Fatalf("请求次数 = %d，want 2", calls)
	}
}
