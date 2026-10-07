package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
)

// 周期化巡检在接口层的用例。
//
// 最要紧的一条是**范围只能由服务端按身份折算**：请求体里带着 scope 一律覆盖掉。
// 否则一个受限用户只要提交一个更宽的范围，就能让定时巡检跑到自己看不到的资产上去
// （而巡检结果里带着资产名与差异项，等于把范围外的数据递到手上）。
// 用桩（而不是真调度器）来验这条契约：它直接把"服务端最后交给调度器的配置"记下来。

// stubInspectSched 记录被调用的参数，用来验证接口层的契约。
type stubInspectSched struct {
	saved     asset.InspectScheduleConfig
	changedBy string
	ranBy     []string
	ranManual []bool
	run       asset.InspectScheduleRun
}

func (s *stubInspectSched) Config() asset.InspectScheduleConfig { return s.saved }

func (s *stubInspectSched) Save(cfg asset.InspectScheduleConfig, by string) error {
	s.saved, s.changedBy = cfg, by
	return nil
}

func (s *stubInspectSched) Status() asset.InspectScheduleStatus {
	return asset.InspectScheduleStatus{Config: s.saved}
}

func (s *stubInspectSched) RunNow(by string, manual bool) asset.InspectScheduleRun {
	s.ranBy = append(s.ranBy, by)
	s.ranManual = append(s.ranManual, manual)
	return s.run
}

// principalWith 构造带指定权限点、指定用户名的身份（用户名是范围折算的输入，必须真实存在）。
func principalWith(username string, perms ...string) *auth.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return &auth.Principal{Username: username, Permissions: set}
}

// restrictedUser 写一个"节点分组 g1 + 业务标签 biz=pay"的受限用户。
func restrictedUser(t *testing.T, a *API, username string) {
	t.Helper()
	if err := a.authStore.CreateUserDirect(auth.User{
		Username: username,
		Scope: auth.Scope{
			Mode: auth.ScopeRestricted, Groups: []string{"g1"},
			AssetMode: auth.AssetScopeLimited,
			AssetLabels: []auth.AssetScope{
				{Key: auth.DefaultScopeLabelKey, Value: "pay"},
			},
		},
	}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
}

// 范围由服务端折算：请求体里提交的"更宽的范围"必须被覆盖。
func TestInspectScheduleAPIScopeComesFromIdentity(t *testing.T) {
	a, _ := assetTestAPI(t)
	restrictedUser(t, a, "ops1")
	stub := &stubInspectSched{}
	a.SetInspectScheduler(stub)

	// 请求体故意带一个"两个维度都不限"的范围（试图放宽）
	body := `{"enabled":true,"intervalHours":6,"type":"host","scope":{"nodeMode":"all","assetMode":"all"}}`
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(principalWith("ops1", "inspect:run"),
		http.MethodPut, "/api/v1/inspect/schedule", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	if stub.saved.Scope.NodeMode != asset.ScopeModeLimited {
		t.Fatalf("客户端提交的范围必须被覆盖成服务端折算的结果，实际 %#v", stub.saved.Scope)
	}
	// g1 下有 web-01 / web-02（夹具），受限身份只能拿到这两个节点
	if len(stub.saved.Scope.Nodes) != 2 {
		t.Fatalf("受限身份的节点清单应为分组 g1 的两台，实际 %#v", stub.saved.Scope.Nodes)
	}
	if stub.saved.Scope.AssetMode != asset.ScopeModeLimited || len(stub.saved.Scope.Labels) != 1 {
		t.Fatalf("业务标签维度同样按身份受限，实际 %#v", stub.saved.Scope)
	}
	// 配置里的其它字段照常生效，变更者留的是登录名
	if !stub.saved.Enabled || stub.saved.IntervalHours != 6 || stub.saved.Type != "host" {
		t.Fatalf("其余字段应原样保存，实际 %#v", stub.saved)
	}
	if stub.changedBy != "ops1" {
		t.Fatalf("应记录配置变更者，实际 %q", stub.changedBy)
	}
}

// 身份解析不到（账号被删/被停用）：**不允许保存**——保存下去等于给一份来源不明的范围授权。
func TestInspectScheduleAPISaveRejectsUnknownIdentity(t *testing.T) {
	a, _ := assetTestAPI(t)
	stub := &stubInspectSched{}
	a.SetInspectScheduler(stub)

	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(principalWith("ghost", "inspect:run"),
		http.MethodPut, "/api/v1/inspect/schedule", `{"enabled":true}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("身份解析不到应 403，实际 %d", rec.Code)
	}
	if stub.changedBy != "" {
		t.Fatalf("被拒绝的保存不该落到调度器，实际记了 %q", stub.changedBy)
	}
}

// 权限：读要 inspect:read，改与立即执行都要 inspect:run。
func TestInspectScheduleAPIPermissions(t *testing.T) {
	a, _ := assetTestAPI(t)
	a.SetInspectScheduler(&stubInspectSched{})

	cases := []struct {
		name   string
		method string
		path   string
		perms  []string
	}{
		{"读需要 inspect:read", http.MethodGet, "/api/v1/inspect/schedule", []string{"inspect:run"}},
		{"改需要 inspect:run", http.MethodPut, "/api/v1/inspect/schedule", []string{"inspect:read"}},
		{"立即执行需要 inspect:run", http.MethodPost, "/api/v1/inspect/schedule/run", []string{"inspect:read"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal(c.perms...), c.method, c.path, `{"enabled":true}`))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("状态码 = %d，期望 403（响应 %s）", rec.Code, rec.Body.String())
			}
		})
	}
}

// 未注入调度器：三个接口都 503（与"该能力未启用"的既有口径一致）。
func TestInspectScheduleAPINotEnabled(t *testing.T) {
	a := &API{}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/inspect/schedule"},
		{http.MethodPut, "/api/v1/inspect/schedule"},
		{http.MethodPost, "/api/v1/inspect/schedule/run"},
	} {
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("inspect:read", "inspect:run"), c.method, c.path, `{}`))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 状态码 = %d，期望 503", c.method, c.path, rec.Code)
		}
	}
}

// 立即执行传的是**点击者**（不是配置保存者）：范围收窄发生在调度器内部，这里固定住入参契约。
func TestInspectScheduleAPIRunPassesClicker(t *testing.T) {
	a, _ := assetTestAPI(t)
	restrictedUser(t, a, "ops1")
	stub := &stubInspectSched{}
	a.SetInspectScheduler(stub)

	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(principalWith("ops1", "inspect:run"),
		http.MethodPost, "/api/v1/inspect/schedule/run", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	if len(stub.ranBy) != 1 || stub.ranBy[0] != "ops1" || !stub.ranManual[0] {
		t.Fatalf("应立即执行并带上点击者身份，实际 by=%#v manual=%#v", stub.ranBy, stub.ranManual)
	}
}

// 本轮没执行（占用中、范围为空、身份消失）时要把原因透出来，而不是报成"跑了没差异"。
func TestInspectScheduleAPIRunSurfacesReason(t *testing.T) {
	a, _ := assetTestAPI(t)
	restrictedUser(t, a, "ops1")
	stub := &stubInspectSched{run: asset.InspectScheduleRun{Error: "范围已收窄为空，本轮不执行"}}
	a.SetInspectScheduler(stub)

	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(principalWith("ops1", "inspect:run"),
		http.MethodPost, "/api/v1/inspect/schedule/run", ""))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502（与报告调度的立即生成同一取向）", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["error"] == nil {
		t.Fatalf("响应应带原因，实际 %s", rec.Body.String())
	}
}

// InspectScopeSnapshot 是"身份 → 范围快照"的唯一出口，四种情形都要分清。
func TestInspectScopeSnapshotResolution(t *testing.T) {
	a, _ := assetTestAPI(t)
	restrictedUser(t, a, "ops1")

	// ① 未启用登录认证：没有身份与范围的概念 → 两个维度都不限（不得因此锁死业务）
	bare := &API{}
	snap, ok := bare.InspectScopeSnapshot("anyone")
	if !ok || snap.NodeMode != asset.ScopeModeAll || snap.AssetMode != asset.ScopeModeAll {
		t.Fatalf("未启用认证应返回不限范围，实际 %#v ok=%v", snap, ok)
	}

	// ② 受限身份：两个维度都受限，且清单来自身份（空清单也是受限）
	snap, ok = a.InspectScopeSnapshot("ops1")
	if !ok {
		t.Fatal("已存在的用户应能解析")
	}
	if snap.NodeMode != asset.ScopeModeLimited || len(snap.Nodes) != 2 {
		t.Fatalf("节点维度应受限到 g1 的两台，实际 %#v", snap)
	}
	if snap.AssetMode != asset.ScopeModeLimited || len(snap.Labels) != 1 || snap.Labels[0].Key != auth.DefaultScopeLabelKey {
		t.Fatalf("业务标签维度应受限，实际 %#v", snap)
	}

	// ③ 全局身份：范围来自**全局角色**（不是用户自己声明的字段）
	global, _ := assetTestAPI(t)
	if err := global.authStore.CreateUserDirect(auth.User{
		Username: "root", Roles: []string{auth.RoleSuperAdmin},
	}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	snap, ok = global.InspectScopeSnapshot("root")
	if !ok || snap.NodeMode != asset.ScopeModeAll || snap.AssetMode != asset.ScopeModeAll {
		t.Fatalf("全局角色应是不限范围，实际 %#v", snap)
	}

	// ④ 解析不到：**不是**不限，而是 ok=false（调用方据此拒绝执行）
	if _, ok := a.InspectScopeSnapshot("ghost"); ok {
		t.Fatal("账号不存在时必须 ok=false（fail-closed），不能当成不限")
	}

	// ⑤ 自己声称 global 但没有任何全局角色：按受限处理——这一条是与**授权口径一致**的判据：
	// ExpandPrincipal 明确不因用户字段就把人提升为全局（policy.go 的注释），
	// 折算若比授权本身更宽松，就成了越权旁路。
	claim, _ := assetTestAPI(t)
	if err := claim.authStore.CreateUserDirect(auth.User{
		Username: "claim", Scope: auth.Scope{Mode: auth.ScopeGlobal},
	}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	if snap, ok := claim.InspectScopeSnapshot("claim"); !ok || snap.NodeMode != asset.ScopeModeLimited {
		t.Fatalf("自声称 global 但无全局角色应按受限处理，实际 %#v ok=%v", snap, ok)
	}
}
