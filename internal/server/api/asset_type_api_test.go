package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 配置项模型接口的用例。
//
// 最要紧的一条是 **响应里不得出现属性值**：模型页是"看模型"而不是"看数据"，
// 而属性值里可能有连接串、口令、内网地址。这条用**真实存在的值**反向断言——
// 只断言"没有 value 字段"是挡不住有人把值塞进别的字段里的。

func findType(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	for _, it := range body["types"].([]any) {
		m := it.(map[string]any)
		if m["key"] == key {
			return m
		}
	}
	t.Fatalf("响应里没有类型 %q：%v", key, body["types"])
	return nil
}

// 属性值绝不出现在模型响应里；键名可以出现。
func TestAssetTypesResponseHidesValues(t *testing.T) {
	a, svc := assetTestAPI(t)
	const secret = "SHOULD_NOT_LEAK_2026"
	if _, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "web-02", Name: "web-02", Node: "web-01",
		Source: asset.SourceDiscovery,
		Attrs:  map[string]string{"dbPassword": secret, "cpuCores": "8", "up": "1"},
	}); err != nil {
		t.Fatalf("造资产失败: %v", err)
	}

	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/asset-types", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("响应里出现了属性值（模型页只看模型）：%s", rec.Body.String())
	}
	body := decodeBody(t, rec)
	host := findType(t, body, "host")

	// 键名要出现（那是模型），并且带上覆盖度与来源分布
	attrs, _ := host["attrs"].([]any)
	if len(attrs) == 0 {
		t.Fatalf("host 应有属性清单：%v", host)
	}
	var seedFound bool
	for _, it := range attrs {
		m := it.(map[string]any)
		if m["key"] == "dbPassword" {
			seedFound = true
			if m["assets"].(float64) < 1 {
				t.Fatalf("dbPassword 的覆盖数应 ≥1：%v", m)
			}
			if _, has := m["value"]; has {
				t.Fatalf("属性统计不该带 value 字段：%v", m)
			}
		}
	}
	if !seedFound {
		t.Fatalf("应列出 dbPassword 这个键：%v", attrs)
	}

	// 运行态字段：没配过时标记为"用内置默认"，并把生效值列出来
	schema, _ := host["schema"].(map[string]any)
	if schema["runtimeFieldsDefault"] != true {
		t.Fatalf("未配置时应标记 runtimeFieldsDefault=true：%v", schema)
	}
	rf, _ := schema["runtimeFields"].([]any)
	if len(rf) != 4 {
		t.Fatalf("生效的运行态字段应是内置默认 4 个，实际 %v", rf)
	}

	// 短命对象也要在模型里，并被标出来
	pod := findType(t, body, "pod")
	if pod["ephemeral"] != true {
		t.Fatalf("Pod 应标记为短命对象：%v", pod)
	}
}

// 统计按调用者的范围收窄，但**模型本身不受限**（类型与字段定义是平台配置）。
func TestAssetTypesStatsRespectScope(t *testing.T) {
	a, _ := assetTestAPI(t)

	global := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(global, reqWith(globalPrincipal("assets:read"), http.MethodGet, "/api/v1/asset-types", ""))
	globalHost := findType(t, decodeBody(t, global), "host")

	restricted := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(restricted, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"), http.MethodGet, "/api/v1/asset-types", ""))
	if restricted.Code != http.StatusOK {
		t.Fatalf("受限身份状态码 = %d，响应 %s", restricted.Code, restricted.Body.String())
	}
	restrictedHost := findType(t, decodeBody(t, restricted), "host")

	if globalHost["assets"].(float64) <= restrictedHost["assets"].(float64) {
		t.Fatalf("全局应看到不少于受限身份的资产数：%v vs %v", globalHost["assets"], restrictedHost["assets"])
	}
	// 模型（字段定义）不受范围限制：两个身份都应看到 pod 类型
	findType(t, decodeBody(t, restricted), "pod")
}

// 保存模型：权限、校验、未知类型，以及"改完立刻回读到的就是新的"。
func TestAssetTypeModelSave(t *testing.T) {
	a, _ := assetTestAPI(t)

	// 只读身份不能改（与「标杆 = 台账数据维护」同一取向）
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:read"),
		http.MethodPut, "/api/v1/asset-types/host", `{"runtimeFields":["cpuCores"]}`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("只读身份应 403，实际 %d", rec.Code)
	}

	// 有 assets:write：保存成功并回整份模型
	rec = httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:write"), http.MethodPut,
		"/api/v1/asset-types/host",
		`{"runtimeFields":["cpuCores"],"attrMeta":{"cpuCores":{"title":"CPU 核数","unit":"核"}}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("保存状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	host := findType(t, decodeBody(t, rec), "host")
	schema := host["schema"].(map[string]any)
	if schema["runtimeFieldsDefault"] != false {
		t.Fatalf("配过之后不该再标为内置默认：%v", schema)
	}
	rf, _ := schema["runtimeFields"].([]any)
	if len(rf) != 1 || rf[0] != "cpuCores" {
		t.Fatalf("生效的运行态字段应是配置的那一个，实际 %v", rf)
	}
	meta, _ := schema["attrMeta"].(map[string]any)
	if meta["cpuCores"].(map[string]any)["title"] != "CPU 核数" {
		t.Fatalf("属性说明应被保存：%v", meta)
	}

	// 自相矛盾（同一个键既是关注字段又是运行态字段）→ 400，且**不能**悄悄写进去
	rec = httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:write"), http.MethodPut,
		"/api/v1/asset-types/host", `{"runtimeFields":["env"],"focusFields":["env"]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("自相矛盾的模型应 400，实际 %d（响应 %s）", rec.Code, rec.Body.String())
	}
	// 空白键也要被明确拒（而不是悄悄丢掉）：静默丢弃会让"我明明加了"变成一个谜
	rec = httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:write"), http.MethodPut,
		"/api/v1/asset-types/host", `{"runtimeFields":["   "]}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空白键应 400，实际 %d（响应 %s）", rec.Code, rec.Body.String())
	}

	// 未知类型 → 404（与"提交内容不合法"区分开，现场才知道该往哪改）
	rec = httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:write"), http.MethodPut,
		"/api/v1/asset-types/no-such-type", `{}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知类型应 404，实际 %d（响应 %s）", rec.Code, rec.Body.String())
	}
}

// 未启用台账时两个接口都 503（与该能力的其它接口同一取向）。
func TestAssetTypesNotEnabled(t *testing.T) {
	a := &API{}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/asset-types", ""},
		{http.MethodPut, "/api/v1/asset-types/host", `{}`},
	} {
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec, reqWith(globalPrincipal("assets:read", "assets:write"), c.method, c.path, c.body))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 状态码 = %d，期望 503", c.method, c.path, rec.Code)
		}
	}
}
