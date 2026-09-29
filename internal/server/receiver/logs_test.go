package receiver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"github.com/nebula/monitor/internal/server/logstore"
	"github.com/nebula/monitor/internal/server/node"
)

// 集中日志上行（C2）：鉴权、体积上限、限速、来源校验、落盘。
//
// 这些用例守的是「日志通道有边界」：没有认证就没有通道；没有体积/速率/每日上限，
// 一个坏 Agent 或一次日志洪峰就能把 Server 打爆或把盘写满。

func newLogsReceiver(t *testing.T, root string, maxPerDay, bodyLimit, rateBps int64) (*Receiver, string) {
	t.Helper()
	dir := t.TempDir()
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	r := New(nil, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)
	r.SetLogStore(logstore.New(root, maxPerDay), bodyLimit, rateBps)
	return r, dir
}

func logBody(t *testing.T, b model.LogBatch) string {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func postLogs(r *Receiver, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/logs", strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.HandleLogs(rec, req)
	return rec
}

func sampleBatch(source string, n int) model.LogBatch {
	lines := make([]model.LogLine, 0, n)
	for i := 0; i < n; i++ {
		lines = append(lines, model.LogLine{Ts: time.Now().UnixMilli(), Text: "error: x"})
	}
	return model.LogBatch{Node: "n1", Source: source, Lines: lines}
}

// TestHandleLogs_StoresAndReportsAccepted 正常路径：落盘并回执接受行数。
func TestHandleLogs_StoresAndReportsAccepted(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 0)
	rec := postLogs(r, logBody(t, sampleBatch("applog", 2)), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，got %d（%s）", rec.Code, rec.Body.String())
	}
	var res model.LogAppendResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Accepted != 2 || res.Dropped != 0 {
		t.Fatalf("应回执接受 2 条，got %+v", res)
	}
	// 真的落盘了
	path := filepath.Join(root, "applog", time.Now().Format("2006-01-02"), "n1.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("应落盘到预期路径：%v", err)
	}
}

// TestHandleLogs_DisabledWithoutStore 未注入存储器 = 该能力关闭，明确回 503
// （与「不配置 logSources 的 Agent」恰好对称：谁都不该悄悄半开）。
func TestHandleLogs_DisabledWithoutStore(t *testing.T) {
	dir := t.TempDir()
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	r := New(nil, mgr, config.AgentAuthConfig{}, nil, nil, nil, nil)
	rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用应回 503，got %d", rec.Code)
	}
}

// TestHandleLogs_RequiresAgentSecret 启用接入授权后，缺失/错误的密钥一律 401。
func TestHandleLogs_RequiresAgentSecret(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	mgr := node.New(filepath.Join(dir, "nodes.json"), time.Minute)
	r := New(nil, mgr, config.AgentAuthConfig{Enabled: true, Secret: "s3cret"}, nil, nil, nil, nil)
	r.SetLogStore(logstore.New(root, 0), 0, 0)

	if rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("无密钥应 401，got %d", rec.Code)
	}
	if rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), map[string]string{"X-Agent-Secret": "wrong"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密钥应 401，got %d", rec.Code)
	}
	if rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), map[string]string{"X-Agent-Secret": "s3cret"}); rec.Code != http.StatusOK {
		t.Fatalf("正确密钥应通过，got %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestHandleLogs_RejectsBadSource 来源名会被用作路径的一段，必须先校验格式。
func TestHandleLogs_RejectsBadSource(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 0)
	for _, bad := range []string{"../escape", "App-Log", "", "a/b"} {
		if rec := postLogs(r, logBody(t, sampleBatch(bad, 1)), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("来源名 %q 应 400，got %d", bad, rec.Code)
		}
	}
}

// TestHandleLogs_RejectsUndeclaredSource 节点**已声明**过来源清单后，清单之外的来源被拒。
func TestHandleLogs_RejectsUndeclaredSource(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 0)
	// 节点上报能力：只配了 applog
	r.nodeMgr.Register(&model.ReportPayload{Node: "n1", Group: "default",
		Capabilities: &model.ClientCapability{LogSources: []string{"applog"}}})

	if rec := postLogs(r, logBody(t, sampleBatch("otherlog", 1)), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("未声明的来源应 403，got %d", rec.Code)
	}
	if rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), nil); rec.Code != http.StatusOK {
		t.Fatalf("已声明的来源应通过，got %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestHandleLogs_UnknownNodeIsAccepted 节点未知（还没上报过能力）时放行：
// Agent 可能先发日志、后上报，卡住会把启动后的首轮日志整批打回。
func TestHandleLogs_UnknownNodeIsAccepted(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 0)
	if rec := postLogs(r, logBody(t, sampleBatch("applog", 1)), nil); rec.Code != http.StatusOK {
		t.Fatalf("未知节点应放行，got %d", rec.Code)
	}
}

// TestHandleLogs_RateLimited 速率超限回 429（Agent 侧记为 reason=rate，而不是上传失败）。
func TestHandleLogs_RateLimited(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 1) // 1 字节/秒
	big := model.LogBatch{Node: "n1", Source: "applog", Lines: []model.LogLine{
		{Text: strings.Repeat("x", 4096)},
	}}
	rec := postLogs(r, logBody(t, big), nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超出速率应 429，got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("限速响应应带 Retry-After，便于 Agent 侧退避")
	}
}

// TestHandleLogs_BodyTooLarge 请求体超限回 413（日志是外部输入，必须先设上限）。
func TestHandleLogs_BodyTooLarge(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 64, 0) // 64 字节上限
	rec := postLogs(r, logBody(t, sampleBatch("applog", 50)), nil)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限应 413，got %d（%s）", rec.Code, rec.Body.String())
	}
}

// TestHandleLogs_MethodNotAllowed 只接受 POST（与上报接口同一约定）。
func TestHandleLogs_MethodNotAllowed(t *testing.T) {
	root := t.TempDir()
	r, _ := newLogsReceiver(t, root, 0, 0, 0)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/logs", nil)
	rec := httptest.NewRecorder()
	r.HandleLogs(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("非 POST 应 405，got %d", rec.Code)
	}
}
