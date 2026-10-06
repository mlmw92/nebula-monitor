package logship

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// 上行侧的语义要点：请求形态正确（路径/密钥/内容），
// 以及**限速与每日上限是正常结果**（计数但不报错），只有真正的失败才是错误。

func lines(n int) []model.LogLine {
	out := make([]model.LogLine, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.LogLine{Ts: 1, Text: "error: x"})
	}
	return out
}

func TestShipper_SendsBatchWithSecret(t *testing.T) {
	var got model.LogBatch
	var path, secret, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, secret, ctype = r.URL.Path, r.Header.Get("X-Agent-Secret"), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(model.LogAppendResult{Accepted: len(got.Lines)})
	}))
	defer srv.Close()

	origin := &model.LogOrigin{Namespace: "nebula-demo", Pod: "web-1", Container: "app"}
	res, err := New(srv.URL, "s3cret", "n1", "default").Sink()(context.Background(), "podlog", origin, lines(2))
	if err != nil {
		t.Fatalf("不应报错：%v", err)
	}
	if path != "/api/v1/logs" {
		t.Fatalf("应上传到 /api/v1/logs，got %q", path)
	}
	if secret != "s3cret" || ctype != "application/json" {
		t.Fatalf("请求头不符：secret=%q content-type=%q", secret, ctype)
	}
	if got.Node != "n1" || got.Source != "podlog" || len(got.Lines) != 2 {
		t.Fatalf("请求体不符：%+v", got)
	}
	// 容器身份必须随批次上行：中心靠它把日志行标到 Pod 资产上，丢了就只剩"来自某台机器"。
	if got.Origin == nil || got.Origin.Namespace != "nebula-demo" || got.Origin.Pod != "web-1" || got.Origin.Container != "app" {
		t.Fatalf("容器身份应随批次上行，got %+v", got.Origin)
	}
	if res.Dropped != 0 {
		t.Fatalf("全部接收时不应有丢弃：%+v", res)
	}
}

// TestShipper_RateLimitedIsNormalResult 限速是**正常结果**：
// 若当成错误处理，日志会持续刷「上传失败」，把真正的问题淹掉。
func TestShipper_RateLimitedIsNormalResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	res, err := New(srv.URL, "", "n1", "").Sink()(context.Background(), "applog", nil, lines(2))
	if err != nil {
		t.Fatalf("限速不算上传失败：%v", err)
	}
	if res.Dropped != 2 || res.Reason != "rate" {
		t.Fatalf("应记为 rate 丢弃 2 行，got %+v", res)
	}
}

// TestShipper_ServerErrorIsError 5xx / 网络问题才是「上传失败」（记为 reason=unreachable）。
func TestShipper_ServerErrorIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "", "n1", "").Sink()(context.Background(), "applog", nil, lines(1)); err == nil {
		t.Fatal("5xx 应作为错误返回")
	}
	// 连不上的地址也应是错误
	if _, err := New("http://127.0.0.1:1", "", "n1", "").Sink()(context.Background(), "applog", nil, lines(1)); err == nil {
		t.Fatal("连接失败应作为错误返回")
	}
}

// TestShipper_PropagatesServerDropReason 服务端的丢弃原因（如每日上限）要原样带回，
// 这样 Agent 产出的指标里能看到「为什么少了几行」。
func TestShipper_PropagatesServerDropReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.LogAppendResult{Accepted: 1, Dropped: 2, Reason: "dailyCap"})
	}))
	defer srv.Close()

	res, err := New(srv.URL, "", "n1", "").Sink()(context.Background(), "applog", nil, lines(3))
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 2 || res.Reason != "dailyCap" {
		t.Fatalf("应带回服务端的丢弃原因，got %+v", res)
	}
}

// TestShipper_UnparseableResponseCountsAsAccepted 响应体不解析时按「已接收」处理：
// 日志已经落盘了，再记一遍丢弃只会让指标失真。
func TestShipper_UnparseableResponseCountsAsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	res, err := New(srv.URL, "", "n1", "").Sink()(context.Background(), "applog", nil, lines(2))
	if err != nil || res.Dropped != 0 {
		t.Fatalf("应视为全部接收，got %+v err=%v", res, err)
	}
}
