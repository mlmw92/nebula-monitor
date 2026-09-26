package logship_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/agent/collector"
	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/agent/logship"
	"github.com/nebula/monitor/internal/model"
	serverconfig "github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/node"
	"github.com/nebula/monitor/internal/server/receiver"
)

// TestEndToEnd_CollectShipStore 走一遍 C2 的数据路径：
// 日志文件 → 增量采集（模式过滤）→ 上传（带接入密钥）→ Server 校验并落盘。
//
// 为什么要有这条跨包用例：三段的单测各自全绿，不代表接起来能通——
// 签名/字段/命中路径这类问题只在集成处暴露（本项目在批次二就踩过一次）。
func TestEndToEnd_CollectShipStore(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "app.log")
	if err := os.WriteFile(logPath, []byte("INFO all good\nerror: boom\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Server 侧：真实存储器 + 真实鉴权 + 真实处理器
	store := logstore.New(filepath.Join(root, "logroot"), 0)
	mgr := node.New(filepath.Join(root, "nodes.json"), time.Minute)
	recv := receiver.New(nil, mgr, serverconfig.AgentAuthConfig{Enabled: true, Secret: "s3cret"}, nil, nil, nil, nil)
	recv.SetLogStore(store, 0, 0)
	srv := httptest.NewServer(http.HandlerFunc(recv.HandleLogs))
	defer srv.Close()

	// Agent 侧：真实采集器 + 真实上行器
	src := config.LogSourceConfig{
		ID:       "applog",
		Paths:    []string{logPath},
		Patterns: []config.LogPattern{{Name: "err", Regex: "error"}},
	}
	c := collector.NewLogCollector("n1", []config.LogSourceConfig{src}, filepath.Join(root, "offsets.json"))
	c.SetSink(logship.New(srv.URL, "s3cret", "n1", "default").Sink())

	// 第一轮：**首次见到该文件 → 从文件尾开始，不回溯历史**（上线时的默认行为）。
	// 这里刻意按真实生命周期走「先起采集、后写日志」，而不是先写日志再采集：
	// 后者会把「首次不回溯」这条决策绕过去，而这正是实机验证抓出来过的缺口。
	first := c.CollectCtx(context.Background())
	if m, ok := metricBy(first, "applog_log_lines_total"); !ok || m.Value != 0 {
		t.Fatalf("首次采集不应上传历史行（从文件尾开始），got %+v", first)
	}
	if m, ok := metricBy(first, "applog_log_up"); !ok || m.Value != 1 {
		t.Fatalf("文件可读时 up 应为 1（否则会被误判成路径/权限问题），got %+v", first)
	}

	// 业务继续写日志 → 第二轮应只上传命中模式的那一条
	fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("error: boom\nINFO still fine\n"); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()

	ms := c.CollectCtx(context.Background())
	if m, ok := metricBy(ms, "applog_log_lines_total"); !ok || m.Value != 1 {
		t.Fatalf("采集侧应成功上传 1 条（追加的 2 行中只有 1 行命中模式），got %+v", ms)
	}
	if _, ok := metricBy(ms, "applog_log_dropped_total"); ok {
		t.Fatalf("这条链路上不应有任何丢弃，got %+v", ms)
	}

	// Server 侧：确实按 来源/日期/节点 分片落了盘，且内容正确
	path := filepath.Join(root, "logroot", "applog", time.Now().Format("2006-01-02"), "n1.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("服务端应落盘到 %s：%v", path, err)
	}
	if !strings.Contains(string(data), "error: boom") {
		t.Fatalf("落盘内容不符：%q", string(data))
	}
	if strings.Contains(string(data), "INFO still fine") {
		t.Fatalf("未命中模式的行不该上传：%q", string(data))
	}
	if strings.Contains(string(data), "INFO all good") {
		t.Fatalf("启动前已存在的历史行不该上传（首次从文件尾开始）：%q", string(data))
	}

	// 第三轮：没有新行时不应重复上传（偏移已推进）
	ms2 := c.CollectCtx(context.Background())
	if m, ok := metricBy(ms2, "applog_log_lines_total"); !ok || m.Value != 0 {
		t.Fatalf("无新行时不应重复上传，got %+v", ms2)
	}
}

// metricBy 取指定名字的指标（第一个）。
func metricBy(ms []model.Metric, name string) (model.Metric, bool) {
	for _, m := range ms {
		if m.Name == name {
			return m, true
		}
	}
	return model.Metric{}, false
}
