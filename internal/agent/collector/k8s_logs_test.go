package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// fakeLogAPIServer 提供一个只实现 Pod 日志子资源的假 apiserver。
// handler 收到的查询串与路径通过返回值交给调用方断言。
func fakeLogAPIServer(t *testing.T, body string, status int, seen *url.Values) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/namespaces/default/pods/web-1/log", func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			q := r.URL.Query()
			*seen = q
		}
		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func logsCollector(srv *httptest.Server) *K8sCollector {
	return NewK8sCollector("worker-01", []model.K8sInstanceConfig{{
		Name: "dev-k8s", APIServer: srv.URL, Token: "test-token",
	}})
}

// 日志必须打到 apiserver 的 /log 子资源上，且行数/时间窗/容器三个参数如实下发。
func TestK8sQueryLogsUsesLogSubresource(t *testing.T) {
	var seen url.Values
	srv := fakeLogAPIServer(t, "line-1\nline-2\n", 0, &seen)
	res, err := logsCollector(srv).QueryLogs(context.Background(), "dev-k8s", "default", "web-1", "app", 200, 3600)
	if err != nil {
		t.Fatalf("查询日志失败：%v", err)
	}
	if got := seen.Get("tailLines"); got != "200" {
		t.Fatalf("tailLines 应下发 200，实际 %q", got)
	}
	if got := seen.Get("sinceSeconds"); got != "3600" {
		t.Fatalf("sinceSeconds 应下发 3600，实际 %q", got)
	}
	if got := seen.Get("container"); got != "app" {
		t.Fatalf("container 应下发 app，实际 %q", got)
	}
	if len(res.Columns) != 1 || res.Columns[0] != "日志" {
		t.Fatalf("日志结果应为单列文本：%+v", res.Columns)
	}
	if len(res.Rows) != 2 || res.Rows[0][0] != "line-1" || res.Rows[1][0] != "line-2" {
		t.Fatalf("行内容不符：%+v", res.Rows)
	}
	if res.Total != 2 || res.Truncated {
		t.Fatalf("2 行未达上限，不应标截断：total=%d truncated=%v", res.Total, res.Truncated)
	}
	if !strings.Contains(res.Notice, "最近 200 行") || !strings.Contains(res.Notice, "时间窗 3600 秒") {
		t.Fatalf("说明应交代范围与限制：%q", res.Notice)
	}
}

// 行数打满即视为"更早的还有"：apiserver 不告诉我们上面还有多少，只能如实标记。
func TestK8sQueryLogsMarksTruncatedAtTailLimit(t *testing.T) {
	lines := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		lines = append(lines, "line-"+strconv.Itoa(i))
	}
	srv := fakeLogAPIServer(t, strings.Join(lines, "\n")+"\n", 0, nil)
	res, err := logsCollector(srv).QueryLogs(context.Background(), "dev-k8s", "default", "web-1", "", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("行数打满必须标截断，否则使用者会以为日志就到这里了")
	}
	if !strings.Contains(res.Notice, "更早的日志未拉取") {
		t.Fatalf("截断必须在说明里讲清楚：%q", res.Notice)
	}
	// 未指定容器时如实说明取的是第一个容器，不假装"就是这个容器的日志"
	if !strings.Contains(res.Notice, "第一个容器") {
		t.Fatalf("未指定容器时应如实说明：%q", res.Notice)
	}
}

// 正文超过字节预算时必须截断并标记，而不是把整份内容塞进回执（那会让这台机器上报失败）。
func TestK8sQueryLogsStopsAtByteBudget(t *testing.T) {
	var sb strings.Builder
	for sb.Len() < containerMaxLogBytes+4096 {
		sb.WriteString(strings.Repeat("x", 200))
		sb.WriteString("\n")
	}
	srv := fakeLogAPIServer(t, sb.String(), 0, nil)
	res, err := logsCollector(srv).QueryLogs(context.Background(), "dev-k8s", "default", "web-1", "", 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("超出字节预算必须标截断")
	}
	// 被切断的半行不能留在结果里：留着会让人读到半截内容却以为是完整一行。
	for _, row := range res.Rows {
		if strings.TrimSpace(row[0]) == "" || strings.HasSuffix(row[0], "…") {
			continue
		}
		if len([]rune(row[0])) != 200 {
			t.Fatalf("不应留下被截断的半行：%q", row[0])
		}
	}
}

// apiserver 报错要如实变成错误（含状态码），不能返回一份空日志让人以为"没有日志"。
func TestK8sQueryLogsSurfacesAPIServerError(t *testing.T) {
	srv := fakeLogAPIServer(t, "", http.StatusNotFound, nil)
	_, err := logsCollector(srv).QueryLogs(context.Background(), "dev-k8s", "default", "web-1", "", 200, 0)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("应把 apiserver 的 404 如实报出，实际 %v", err)
	}
}

// 集群标识不存在时给出可选清单，而不是含糊地失败。
func TestK8sQueryLogsUnknownCluster(t *testing.T) {
	srv := fakeLogAPIServer(t, "x\n", 0, nil)
	_, err := logsCollector(srv).QueryLogs(context.Background(), "other", "default", "web-1", "", 200, 0)
	if err == nil || !strings.Contains(err.Error(), "dev-k8s") {
		t.Fatalf("应列出可选集群，实际 %v", err)
	}
}
