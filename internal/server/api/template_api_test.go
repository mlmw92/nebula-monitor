package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/templates"
)

// newTemplateTestAPI 构造注入了模板存储的 API（用真实存储 + 临时目录，避免替身掩盖落盘问题）。
func newTemplateTestAPI(t *testing.T) *API {
	t.Helper()
	a := scopeTestAPI(t)
	a.SetTemplateStore(templates.NewStore(filepath.Join(t.TempDir(), "templates.yaml")))
	return a
}

// templateJSON 生成一个最小合法模板的请求体（含 Server 侧必填的 groups）。
func templateJSON(id string) string {
	return `{"id":"` + id + `","kind":"prometheus-exporter","groups":["default"],` +
		`"targets":[{"instance":"` + id + `-01:15692","addr":"http://127.0.0.1:15692/metrics"}]}`
}

// do 以持有 middleware:read + middleware:write 的主体发起请求。
func do(a *API, method, target, body string) *httptest.ResponseRecorder {
	mux := newRoutesMux(a)
	rec := httptest.NewRecorder()
	perm := globalPrincipal("middleware:read", "middleware:write")
	mux.ServeHTTP(rec, reqWith(perm, method, target, body))
	return rec
}

func TestTemplatesAPI_CreateUpdateDelete(t *testing.T) {
	a := newTemplateTestAPI(t)

	// 新建
	rec := do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("rabbitmq"))
	if rec.Code != http.StatusOK {
		t.Fatalf("新建应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 同 id 再新建：必须 409 而不是静默覆盖（id 决定指标名前缀）
	rec = do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("rabbitmq"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("重复 id 新建应 409，got %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 列表应含 1 条与版本号
	rec = do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", rec.Code)
	}
	var listResp struct {
		Templates []struct {
			ID string `json:"id"`
		} `json:"templates"`
		Revision uint64 `json:"revision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("列表响应解析失败：%v（body=%s）", err, rec.Body.String())
	}
	if len(listResp.Templates) != 1 || listResp.Templates[0].ID != "rabbitmq" {
		t.Fatalf("列表内容不符：%s", rec.Body.String())
	}
	if listResp.Revision == 0 {
		t.Fatal("列表应返回非 0 版本号（前端据此判断是否有变更待下发）")
	}

	// 修改 id：应被拒绝（改名等价于换一套指标）
	rec = do(a, http.MethodPut, "/api/v1/middleware/templates/rabbitmq", templateJSON("othermq"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("修改 id 应 400，got %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 正常更新
	updated := `{"id":"rabbitmq","title":"RabbitMQ 集群","kind":"prometheus-exporter","groups":["mq"],"targets":[{"addr":"http://127.0.0.1:15692/metrics"}]}`
	rec = do(a, http.MethodPut, "/api/v1/middleware/templates/rabbitmq", updated)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}

	// 更新不存在的模板
	rec = do(a, http.MethodPut, "/api/v1/middleware/templates/nosuch", templateJSON("nosuch"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在的模板应 404，got %d", rec.Code)
	}

	// 删除
	rec = do(a, http.MethodDelete, "/api/v1/middleware/templates/rabbitmq", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	rec = do(a, http.MethodDelete, "/api/v1/middleware/templates/rabbitmq", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("重复删除应 404，got %d", rec.Code)
	}
}

// TestTemplatesAPI_CreateRejectsInvalid 校验失败必须返回 400 且带上具体原因
// （前端 http.js 优先展示 error 字段，用户不必「保存失败再猜」）。
func TestTemplatesAPI_CreateRejectsInvalid(t *testing.T) {
	a := newTemplateTestAPI(t)

	rec := do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("redis"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("保留前缀 id 应 400，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Error == "" {
		t.Fatalf("错误响应应带 error 字段：%s", rec.Body.String())
	}
	if !strings.Contains(resp.Error, "保留前缀") {
		t.Fatalf("错误原因应可读且指向具体规则，got %q", resp.Error)
	}

	// 非法 JSON
	rec = do(a, http.MethodPost, "/api/v1/middleware/templates", "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，got %d", rec.Code)
	}

	// 缺 groups（Server 侧要求声明生效分组，否则会在所有节点上产出 up=0）
	rec = do(a, http.MethodPost, "/api/v1/middleware/templates",
		`{"id":"rabbitmq","kind":"prometheus-exporter","targets":[{"addr":"http://127.0.0.1:15692/metrics"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 groups 应 400，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || !strings.Contains(resp.Error, "groups") {
		t.Fatalf("错误应指明缺 groups，got %s", rec.Body.String())
	}
}

// TestTemplatesAPI_Validate 仅校验接口不得产生副作用，且要报出跨条约束。
func TestTemplatesAPI_Validate(t *testing.T) {
	a := newTemplateTestAPI(t)

	rec := do(a, http.MethodPost, "/api/v1/middleware/templates/validate", templateJSON("rabbitmq"))
	if rec.Code != http.StatusOK {
		t.Fatalf("校验接口应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	var okResp struct {
		OK     bool     `json:"ok"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("校验响应解析失败：%v", err)
	}
	if !okResp.OK || len(okResp.Errors) != 0 {
		t.Fatalf("合法模板应通过：%s", rec.Body.String())
	}

	// 校验不应落盘：列表仍为空
	rec = do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	var listResp struct {
		Templates []any `json:"templates"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Templates) != 0 {
		t.Fatal("校验接口产生了副作用（写入了存储）")
	}

	// 跨条约束：先建 mq，再校验与其互为前缀的 mq_prod
	if rec := do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("mq")); rec.Code != http.StatusOK {
		t.Fatalf("新增 mq 失败：%s", rec.Body.String())
	}
	rec = do(a, http.MethodPost, "/api/v1/middleware/templates/validate", templateJSON("mq_prod"))
	if err := json.Unmarshal(rec.Body.Bytes(), &okResp); err != nil {
		t.Fatalf("校验响应解析失败：%v", err)
	}
	if okResp.OK || len(okResp.Errors) == 0 {
		t.Fatalf("与既有模板互为前缀应被报出：%s", rec.Body.String())
	}
}

// TestTemplatesAPI_NotInjected 未注入存储时：读返回空集合（前端不报错），写明确不可用。
func TestTemplatesAPI_NotInjected(t *testing.T) {
	a := scopeTestAPI(t) // 不注入模板存储

	rec := do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("未注入时列表应 200（空集合），got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "templates") {
		t.Fatalf("响应应含 templates 字段：%s", rec.Body.String())
	}

	rec = do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("rabbitmq"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未注入时写入应 503，got %d（body=%s）", rec.Code, rec.Body.String())
	}
}
