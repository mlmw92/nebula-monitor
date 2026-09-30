package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/ops"
)

// opsTestAPI 在 scopeTestAPI 的基础上接入下行操作通道（临时任务文件）。
// 夹具沿用既有分组：web-01/web-02 → g1，db-01 → g2。
func opsTestAPI(t *testing.T) (*API, *ops.Service) {
	t.Helper()
	a := scopeTestAPI(t)
	svc := ops.NewService(ops.NewStore(filepath.Join(t.TempDir(), "ops_tasks.json")))
	a.SetOpsService(svc)
	return a, svc
}

// 权限：看要 ops:read，下发要 ops:exec（高风险），两者不能互相顶替。
func TestRoutes_OpsPermissions(t *testing.T) {
	a, _ := opsTestAPI(t)
	mux := newRoutesMux(a)

	// 完全没有权限 → 两个接口都拒绝
	none := restrictedPrincipal([]string{"nodes:read"}, "g1")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/ops/actions", ""},
		{http.MethodPost, "/api/v1/ops/tasks", `{"node":"web-01","kind":"node.diagnostics"}`},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(none, tc.method, tc.path, tc.body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s 无权限应 403，实际 %d", tc.method, tc.path, rec.Code)
		}
	}

	// 只有 ops:read：能看目录，不能下发
	readOnly := restrictedPrincipal([]string{"ops:read"}, "g1")
	okRec := httptest.NewRecorder()
	mux.ServeHTTP(okRec, reqWith(readOnly, http.MethodGet, "/api/v1/ops/actions", ""))
	if okRec.Code != http.StatusOK {
		t.Fatalf("有 ops:read 应可看目录，实际 %d", okRec.Code)
	}
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(readOnly, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"web-01","kind":"node.diagnostics"}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("只有 ops:read 不应能下发，实际 %d", denied.Code)
	}
}

// 下发的主流程：节点未声明能力 → 409 并说明原因；声明后 → 201；列表能查到。
func TestRoutes_OpsCreateAndList(t *testing.T) {
	a, svc := opsTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"ops:read", "ops:exec"}, "g1")

	// 1) 节点没声明任何动作：必须是 409 而不是 400——参数没问题，问题在目标机器
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"web-01","kind":"node.diagnostics","reason":"排障"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("未声明能力应 409，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "guards.ops") && !strings.Contains(rec.Body.String(), "尚未声明") {
		t.Fatalf("409 应说明原因与下一步：%s", rec.Body.String())
	}

	// 2) 参数不合法：400（改参数即可）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"web-01","kind":"svc.status","params":{"unit":"--now"}}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法参数应 400，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 3) 未知动作：400，且应列出可选动作
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"web-01","kind":"svc.explode"}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "未知动作") {
		t.Fatalf("未知动作应 400 且说明可选值，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	// 4) 声明能力后创建成功
	svc.Store().SaveCaps("web-01", []string{ops.KindNodeDiagnostics, ops.KindSvcStatus})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"web-01","kind":"node.diagnostics","reason":"排障"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("应创建成功，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	task, _ := body["task"].(map[string]any)
	if task == nil || task["state"] != "queued" {
		t.Fatalf("响应应含任务且状态为 queued：%v", body)
	}
	// 操作者来源 IP 必须落库（审计要求）；操作者名由认证中间件注入，
	// 本用例直接构造 Principal，因此只断言 IP。
	if task["operatorIP"] == "" {
		t.Fatalf("应记录操作者来源 IP：%v", task)
	}
	if task["title"] == "" || task["readOnly"] != true {
		t.Fatalf("响应应附带动作标题与只读标记：%v", task)
	}

	// 5) 列表（资源范围内）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/ops/tasks", ""))
	tasks, _ := decodeBody(t, rec)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("应查到 1 条任务，实际 %d（%s）", len(tasks), rec.Body.String())
	}
}

// 资源范围：范围外的节点必须与"不存在"完全一致（否则会变成节点存在性探测面）。
func TestRoutes_OpsNodeScope(t *testing.T) {
	a, svc := opsTestAPI(t)
	mux := newRoutesMux(a)
	g1 := restrictedPrincipal([]string{"ops:read", "ops:exec"}, "g1")

	// db-01 属 g2：下发 → 404
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(g1, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"db-01","kind":"node.diagnostics"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("范围外节点应 404（与不存在一致），实际 %d", rec.Code)
	}
	// 不存在的节点也是 404，两者不可区分
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(g1, http.MethodPost, "/api/v1/ops/tasks",
		`{"node":"no-such-node","kind":"node.diagnostics"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的节点应 404，实际 %d", rec.Code)
	}

	// 列表：别的分组创建的任务对 g1 不可见（直接往 store 里塞一条 db-01 的任务）
	svc.Store().SaveCaps("db-01", []string{ops.KindNodeDiagnostics})
	svc.Store().SaveCaps("web-01", []string{ops.KindNodeDiagnostics})
	if _, err := svc.Create("db-01", ops.KindNodeDiagnostics, nil, "other", "", ""); err != nil {
		t.Fatalf("准备环境失败: %v", err)
	}
	if _, err := svc.Create("web-01", ops.KindNodeDiagnostics, nil, "ops1", "", ""); err != nil {
		t.Fatalf("准备环境失败: %v", err)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(g1, http.MethodGet, "/api/v1/ops/tasks", ""))
	tasks, _ := decodeBody(t, rec)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("g1 只应看到 web-01 的任务，实际 %d（%s）", len(tasks), rec.Body.String())
	}
	// 按范围外节点查询列表 → 404（不能用它探测别人的节点）
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(g1, http.MethodGet, "/api/v1/ops/tasks?node=db-01", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("按范围外节点查询应 404，实际 %d", rec.Code)
	}
}

// 动作目录带上节点参数时，要能说清"这台机器放行了什么"。
func TestRoutes_OpsActionsWithNode(t *testing.T) {
	a, svc := opsTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"ops:read"}, "g1")

	svc.Store().SaveCaps("web-01", []string{ops.KindNodeDiagnostics})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/ops/actions?node=web-01", ""))
	body := decodeBody(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d", rec.Code)
	}
	support, _ := body["nodeSupport"].([]any)
	if len(support) != 1 || support[0] != ops.KindNodeDiagnostics {
		t.Fatalf("应返回该节点的可执行清单，实际 %v", body["nodeSupport"])
	}
	if body["nodeSupportKnown"] != true {
		t.Fatalf("有声明时应标记为已知：%v", body)
	}
	actions, _ := body["actions"].([]any)
	if len(actions) == 0 {
		t.Fatalf("应返回动作目录：%v", body)
	}
	// 范围外节点 → 404
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/ops/actions?node=db-01", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("范围外节点应 404，实际 %d", rec.Code)
	}
}
