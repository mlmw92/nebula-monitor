package api

import (
	"encoding/json"
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

// 批量下发：逐节点结论；只有一台能下发也要成功，一台都不能下发才 409。
func TestRoutes_OpsBatchCreate(t *testing.T) {
	a, svc := opsTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"ops:read", "ops:exec"}, "g1")
	// web-01 放行只读；web-02 刻意不声明任何能力（模拟旧 Agent / 护栏全关）
	svc.Store().SaveCaps("web-01", []string{ops.KindNodeDiagnostics, ops.KindSvcStatus})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/ops/tasks/batch",
		`{"nodes":["web-01","web-02","db-01","no-such-node"],"kind":"node.diagnostics","reason":"批量排障"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("部分成功应 201，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	batch, _ := decodeBody(t, rec)["batch"].(map[string]any)
	if batch["created"].(float64) != 1 || batch["failed"].(float64) != 3 || batch["total"].(float64) != 4 {
		t.Fatalf("应为 4 目标 / 1 成功 / 3 失败：%v", batch)
	}
	if batch["batchId"] == "" {
		t.Fatalf("应返回批次号：%v", batch)
	}
	// 逐节点失败原因必须可区分：范围外与不存在都报「不存在」（不可用作探测面），
	// 未声明能力给出护栏/版本提示
	raw, _ := json.Marshal(batch["items"])
	joined := string(raw)
	if strings.Count(joined, "节点不存在") != 2 {
		t.Fatalf("范围外与不存在都应报「节点不存在」：%s", joined)
	}
	if !strings.Contains(joined, "尚未声明") {
		t.Fatalf("未声明能力的节点应给出可操作提示：%s", joined)
	}

	// 全部不可下发 → 409（不能报成部分成功）
	allFail := httptest.NewRecorder()
	mux.ServeHTTP(allFail, reqWith(p, http.MethodPost, "/api/v1/ops/tasks/batch",
		`{"nodes":["web-02","db-01"],"kind":"node.diagnostics"}`))
	if allFail.Code != http.StatusConflict {
		t.Fatalf("全部不可下发应 409，实际 %d（%s）", allFail.Code, allFail.Body.String())
	}

	// 权限：只有 ops:read 不能下发
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"ops:read"}, "g1"), http.MethodPost,
		"/api/v1/ops/tasks/batch", `{"nodes":["web-01"],"kind":"node.diagnostics"}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("只有 ops:read 应 403，实际 %d", denied.Code)
	}

	// 批量能力查询：范围外的节点不出现在 nodeCaps 里
	capsRec := httptest.NewRecorder()
	mux.ServeHTTP(capsRec, reqWith(p, http.MethodGet, "/api/v1/ops/actions?nodes=web-01,db-01,no-such", ""))
	nodeCaps, _ := decodeBody(t, capsRec)["nodeCaps"].(map[string]any)
	if len(nodeCaps) != 1 {
		t.Fatalf("nodeCaps 只应含范围内的存在节点：%v", nodeCaps)
	}
	if _, ok := nodeCaps["web-01"]; !ok {
		t.Fatalf("web-01 应在 nodeCaps 里：%v", nodeCaps)
	}

	// 按批次查询任务
	listRec := httptest.NewRecorder()
	mux.ServeHTTP(listRec, reqWith(p, http.MethodGet, "/api/v1/ops/tasks?batchId="+batch["batchId"].(string), ""))
	tasks, _ := decodeBody(t, listRec)["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("按批次应查到 1 条任务，实际 %d", len(tasks))
	}
}

// 取消与删除：取消只对排队中生效；删除只对已结束生效；范围外的任务一律「不存在」。
func TestRoutes_OpsCancelAndDelete(t *testing.T) {
	a, svc := opsTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"ops:read", "ops:exec"}, "g1")
	svc.Store().SaveCaps("web-01", []string{ops.KindNodeDiagnostics})
	svc.Store().SaveCaps("db-01", []string{ops.KindNodeDiagnostics})

	batch := httptest.NewRecorder()
	mux.ServeHTTP(batch, reqWith(p, http.MethodPost, "/api/v1/ops/tasks/batch",
		`{"nodes":["web-01"],"kind":"node.diagnostics"}`))
	batchID, _ := decodeBody(t, batch)["batch"].(map[string]any)["batchId"].(string)

	// 排队中的任务不能删（先取消或等结束）
	queued := httptest.NewRecorder()
	mux.ServeHTTP(queued, reqWith(p, http.MethodGet, "/api/v1/ops/tasks?batchId="+batchID, ""))
	taskID, _ := decodeBody(t, queued)["tasks"].([]any)[0].(map[string]any)["id"].(string)
	delActive := httptest.NewRecorder()
	mux.ServeHTTP(delActive, reqWith(p, http.MethodDelete, "/api/v1/ops/tasks/"+taskID, ""))
	if delActive.Code != http.StatusConflict {
		t.Fatalf("删除排队中任务应 409，实际 %d（%s）", delActive.Code, delActive.Body.String())
	}

	// 取消 → 成功；再取消 → 失败项
	cancel1 := httptest.NewRecorder()
	mux.ServeHTTP(cancel1, reqWith(p, http.MethodPost, "/api/v1/ops/tasks/cancel", `{"ids":["`+taskID+`"]}`))
	res, _ := decodeBody(t, cancel1)["result"].(map[string]any)
	if cancel1.Code != http.StatusOK || res["cancelled"].(float64) != 1 {
		t.Fatalf("取消应成功：%d %s", cancel1.Code, cancel1.Body.String())
	}
	cancel2 := httptest.NewRecorder()
	mux.ServeHTTP(cancel2, reqWith(p, http.MethodPost, "/api/v1/ops/tasks/cancel", `{"ids":["`+taskID+`"]}`))
	res2, _ := decodeBody(t, cancel2)["result"].(map[string]any)
	if res2["cancelled"].(float64) != 0 || res2["failed"].(float64) != 1 {
		t.Fatalf("重复取消应记为失败项：%s", cancel2.Body.String())
	}

	// 已结束的记录可以删
	del := httptest.NewRecorder()
	mux.ServeHTTP(del, reqWith(p, http.MethodDelete, "/api/v1/ops/tasks/"+taskID, ""))
	if del.Code != http.StatusOK {
		t.Fatalf("删除已取消的记录应成功，实际 %d（%s）", del.Code, del.Body.String())
	}

	// 范围外的任务：查/取消/删都按「不存在」处理
	other, err := svc.Create("db-01", ops.KindNodeDiagnostics, nil, "other", "", "")
	if err != nil {
		t.Fatalf("准备环境失败: %v", err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/ops/tasks/" + other.ID, ""},
		{http.MethodDelete, "/api/v1/ops/tasks/" + other.ID, ""},
		{http.MethodPost, "/api/v1/ops/tasks/cancel", `{"ids":["` + other.ID + `"]}`},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, tc.method, tc.path, tc.body))
		if tc.method == http.MethodPost {
			// 批量取消逐条给结果：范围外的条目必须报「任务不存在」且不计入成功
			res, _ := decodeBody(t, rec)["result"].(map[string]any)
			if res["cancelled"].(float64) != 0 {
				t.Fatalf("范围外任务不应被取消：%s", rec.Body.String())
			}
			continue
		}
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s 范围外应 404，实际 %d", tc.method, tc.path, rec.Code)
		}
	}
	// 那条范围外的任务必须仍然存在（不能因为别人的请求被动过）
	if t2, ok := svc.Task(other.ID); !ok || t2.State != "queued" {
		t.Fatalf("范围外任务不应被改动：%+v ok=%v", t2, ok)
	}
}
