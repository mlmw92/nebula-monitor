package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
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

// TestTemplatesAPI_Presets 预设接口：返回开箱模板供「从预设创建」，
// 且预设必须自洽（元信息齐全、不预设生效分组——分组由用户按自己环境选择）。
func TestTemplatesAPI_Presets(t *testing.T) {
	a := newTemplateTestAPI(t)
	rec := do(a, http.MethodGet, "/api/v1/middleware/templates/presets", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("预设接口应 200，got %d（body=%s）", rec.Code, rec.Body.String())
	}
	var resp struct {
		Presets []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Desc   string `json:"desc"`
			Note   string `json:"note"`
			Config struct {
				ID     string   `json:"id"`
				Kind   string   `json:"kind"`
				Groups []string `json:"groups"`
				Rules  struct {
					Keep string `json:"keep"`
				} `json:"rules"`
			} `json:"config"`
		} `json:"presets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应解析失败：%v", err)
	}
	if len(resp.Presets) < 4 {
		t.Fatalf("预设数量 %d 偏少（至少覆盖 4 个常见中间件）", len(resp.Presets))
	}
	for _, p := range resp.Presets {
		if p.ID == "" || p.Title == "" || p.Desc == "" || p.Note == "" {
			t.Errorf("预设 %s 元信息不完整（前端下拉与提示直接展示它们）", p.ID)
		}
		if p.Config.ID != p.ID || p.Config.Kind != "prometheus-exporter" {
			t.Errorf("预设 %s 的配置与元信息不一致：%+v", p.ID, p.Config)
		}
		// 预设必须留空生效分组：分组取决于用户环境，预设里写死会让「点一下就建好」变成建错
		if len(p.Config.Groups) != 0 {
			t.Errorf("预设 %s 不应预设生效分组：%v", p.ID, p.Config.Groups)
		}
		if p.Config.Rules.Keep == "" {
			t.Errorf("预设 %s 应给出 keep 收窄到该中间件指标族（否则会带入 exporter 自身的 go_*/process_*）", p.ID)
		}
	}
}

// execTemplateJSON 生成一个护栏类（exec）模板请求体。
func execTemplateJSON(id string) string {
	return `{"id":"` + id + `","kind":"exec","groups":["default"],` +
		`"targets":[{"instance":"local","command":"/bin/echo","args":["1"]}],` +
		`"rules":{"metrics":[{"name":"v","pattern":"(\\d+)"}]}}`
}

// jdbcTemplateJSON 生成 jdbc 模板请求体；withAuth=false 时不给凭据（模拟「读视图回填后保存」）。
func jdbcTemplateJSON(addr string, withAuth bool) string {
	auth := ""
	if withAuth {
		auth = `,"auth":{"basic":{"user":"monitor","password":"secret"}}`
	}
	return `{"id":"bizdb","kind":"jdbc","driver":"mysql","groups":["default"],` +
		`"targets":[{"instance":"db1","addr":"` + addr + `","database":"appdb"` + auth + `}],` +
		`"rules":{"metrics":[{"name":"rows","query":"SELECT 1"}]}}`
}

// TestTemplatesAPI_IneffectiveNodes 护栏类模板必须告诉用户「有多少节点没放行」：
// 未放行的节点**收不到**这个模板，不提示的话用户只能看到「配了却没数据」。
func TestTemplatesAPI_IneffectiveNodes(t *testing.T) {
	a := newTemplateTestAPI(t)
	// 两个采集节点：一个放行 exec，一个没放行；另有一个代理节点（不执行模板，不应计入）
	a.nodeMgr.Register(&model.ReportPayload{Node: "n1", Group: "default",
		Capabilities: &model.ClientCapability{Templates: true, TemplateKinds: []string{"exec"}}})
	a.nodeMgr.Register(&model.ReportPayload{Node: "n2", Group: "default",
		Capabilities: &model.ClientCapability{Templates: true}})
	a.nodeMgr.Register(&model.ReportPayload{Node: "hub1", Group: "default", Mode: model.ModeHub})

	if rec := do(a, http.MethodPost, "/api/v1/middleware/templates", execTemplateJSON("job")); rec.Code != http.StatusOK {
		t.Fatalf("新建 exec 模板失败：%d %s", rec.Code, rec.Body.String())
	}

	rec := do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	var resp struct {
		Templates []struct {
			ID               string `json:"id"`
			Kind             string `json:"kind"`
			IneffectiveNodes int    `json:"ineffectiveNodes"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(resp.Templates) != 1 {
		t.Fatalf("应有 1 个模板，got %+v", resp.Templates)
	}
	if got := resp.Templates[0].IneffectiveNodes; got != 1 {
		t.Fatalf("应统计出 1 个节点未放行（n2；代理节点不计），got %d", got)
	}

	// 网络取数类任何节点都能执行 → 恒为 0
	if rec := do(a, http.MethodPost, "/api/v1/middleware/templates", templateJSON("mq")); rec.Code != http.StatusOK {
		t.Fatalf("新建 prometheus 模板失败：%d", rec.Code)
	}
	rec = do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	for _, tpl := range resp.Templates {
		if tpl.ID == "mq" && tpl.IneffectiveNodes != 0 {
			t.Fatalf("网络取数类不应有未放行节点，got %d", tpl.IneffectiveNodes)
		}
	}
}

// TestTemplatesAPI_UpdatePreservesAuth 凭据永不回显，因此编辑保存时入参里没有 auth。
// 服务端必须按目标身份把原凭据补回——否则「编辑一次就把库密码静默清空」，
// 而下轮采集的失败原因看起来像连不上库，比直接报错难查得多。
func TestTemplatesAPI_UpdatePreservesAuth(t *testing.T) {
	a := newTemplateTestAPI(t)
	if rec := do(a, http.MethodPost, "/api/v1/middleware/templates", jdbcTemplateJSON("10.0.0.5:3306", true)); rec.Code != http.StatusOK {
		t.Fatalf("新建失败：%d %s", rec.Code, rec.Body.String())
	}

	// 读视图只暴露 hasAuth（凭据本身不回显）
	rec := do(a, http.MethodGet, "/api/v1/middleware/templates", "")
	var resp struct {
		Templates []struct {
			Targets []struct {
				Addr    string `json:"addr"`
				HasAuth bool   `json:"hasAuth"`
			} `json:"targets"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(resp.Templates) != 1 || len(resp.Templates[0].Targets) != 1 || !resp.Templates[0].Targets[0].HasAuth {
		t.Fatalf("读视图应给出 hasAuth=true（但不含凭据本身）：%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("读视图绝不能回显凭据")
	}

	// 编辑：入参不带 auth（模拟前端回填后保存）
	if rec := do(a, http.MethodPut, "/api/v1/middleware/templates/bizdb", jdbcTemplateJSON("10.0.0.5:3306", false)); rec.Code != http.StatusOK {
		t.Fatalf("更新失败：%d %s", rec.Code, rec.Body.String())
	}
	got, ok := a.templates.Get("bizdb")
	if !ok || got.Targets[0].Auth == nil || got.Targets[0].Auth.Basic == nil {
		t.Fatalf("原凭据应被保留，got %+v", got.Targets[0].Auth)
	}
	if got.Targets[0].Auth.Basic.Password != "secret" {
		t.Fatalf("凭据内容应原样保留，got %q", got.Targets[0].Auth.Basic.Password)
	}

	// 目标身份变了就不搬凭据：把 A 的密码悄悄用到 B 上，比丢掉它更糟。
	// 结果不是「保存后没有凭据」，而是**保存被拒**并要求重新给凭据——
	// 用户当场就知道：要么补凭据，要么确认自己确实换了目标。
	rec = do(a, http.MethodPut, "/api/v1/middleware/templates/bizdb", jdbcTemplateJSON("10.0.0.9:3306", false))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "auth.basic.user") {
		t.Fatalf("换了目标（addr 变了）又没给凭据，应被拒绝并说明缺什么，got %d %s", rec.Code, rec.Body.String())
	}
	kept, _ := a.templates.Get("bizdb")
	if kept.Targets[0].Addr != "10.0.0.5:3306" || kept.Targets[0].Auth == nil {
		t.Fatalf("被拒的更新不应改动已存配置，got %+v", kept.Targets[0])
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
