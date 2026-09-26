package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/alert"
)

func newPipelineAPI(t *testing.T) *API {
	t.Helper()
	store := alert.NewPipelineStore(filepath.Join(t.TempDir(), "alert_pipeline.yaml"))
	return &API{pipeline: store}
}

// doJSON 调用 handler 并解析 JSON 响应（前端依赖 body.error 展示校验文案，故一律要求 JSON）。
func doJSON(t *testing.T, h http.HandlerFunc, method, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/alert-pipeline", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)

	out := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是合法 JSON: %v / %s", err, rec.Body.String())
		}
	}
	return rec.Code, out
}

// TestPipelineAPI_GetReturnsEmptyConfig 未配置时 GET 返回空配置而非报错。
func TestPipelineAPI_GetReturnsEmptyConfig(t *testing.T) {
	a := newPipelineAPI(t)
	code, body := doJSON(t, a.handlePipelineGet, http.MethodGet, "")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if v, ok := body["relabels"].([]any); ok && len(v) != 0 {
		t.Fatalf("期望空 relabels，实际 %v", v)
	}
}

// TestPipelineAPI_PutSavesAndEchoesConfig 保存成功返回归一化后的配置，且再次 GET 能读到（热生效）。
func TestPipelineAPI_PutSavesAndEchoesConfig(t *testing.T) {
	a := newPipelineAPI(t)
	cfg := `{"relabels":[{"op":"set","target":"team","value":"sre"}],"templates":[{"name":"t1","template":"X{{.Node}}"}]}`

	code, body := doJSON(t, a.handlePipelinePut, http.MethodPut, cfg)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%v", code, body)
	}
	if body["status"] != "ok" {
		t.Fatalf("status = %v", body["status"])
	}

	code, body = doJSON(t, a.handlePipelineGet, http.MethodGet, "")
	if code != http.StatusOK {
		t.Fatalf("get code = %d", code)
	}
	if relabels, ok := body["relabels"].([]any); !ok || len(relabels) != 1 {
		t.Fatalf("relabels 未持久化: %v", body["relabels"])
	}
}

// TestPipelineAPI_PutRejectsInvalidConfig 非法配置返回 400，且 error 文案可被前端直接展示。
func TestPipelineAPI_PutRejectsInvalidConfig(t *testing.T) {
	a := newPipelineAPI(t)
	code, body := doJSON(t, a.handlePipelinePut, http.MethodPut, `{"relabels":[{"op":"explode","source":"a"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "未知操作") {
		t.Fatalf("error 文案缺失或不可读: %v", body)
	}
}

// TestPipelineAPI_PreviewAppliesPendingConfig 预览按「待保存配置」试算，且不改动线上配置。
func TestPipelineAPI_PreviewAppliesPendingConfig(t *testing.T) {
	a := newPipelineAPI(t)
	payload := `{"config":{"relabels":[{"op":"set","target":"team","value":"sre"}],` +
		`"templates":[{"name":"t1","channel":"dingtalk","template":"DING:{{.Node}}"}]},` +
		`"channel":"dingtalk"}`

	code, body := doJSON(t, a.handlePipelinePreview, http.MethodPost, payload)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%v", code, body)
	}
	if msg, _ := body["message"].(string); msg != "DING:web-01" {
		t.Fatalf("message = %q，期望渠道模板已生效", msg)
	}
	if om, _ := body["originalMessage"].(string); om == "" {
		t.Fatal("缺少 originalMessage（前端用于展示回退值）")
	}
	if labels, ok := body["labels"].(map[string]any); !ok || labels["team"] != "sre" {
		t.Fatalf("labels 未包含管道注入的 team: %v", body["labels"])
	}

	if got := a.pipeline.Get(); len(got.Relabels) != 0 || len(got.Templates) != 0 {
		t.Fatalf("预览意外修改了线上配置: %+v", got)
	}
}

// TestPipelineAPI_PreviewRejectsInvalidConfig 预览携带非法配置时返回 400 与可读文案。
func TestPipelineAPI_PreviewRejectsInvalidConfig(t *testing.T) {
	a := newPipelineAPI(t)
	code, body := doJSON(t, a.handlePipelinePreview, http.MethodPost,
		`{"config":{"templates":[{"name":"bad","template":"{{.RuleName"}]},"channel":"email"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "模板") {
		t.Fatalf("error 文案缺失: %v", body)
	}
}

// TestPipelineAPI_DisabledWithoutStore 未注入管道时返回 503 与 JSON 文案（而非纯文本）。
func TestPipelineAPI_DisabledWithoutStore(t *testing.T) {
	a := &API{}
	code, body := doJSON(t, a.handlePipelinePut, http.MethodPut, `{}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", code)
	}
	if msg, _ := body["error"].(string); msg == "" {
		t.Fatalf("缺少 error 文案: %v", body)
	}
}
