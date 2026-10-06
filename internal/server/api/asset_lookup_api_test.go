package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// lookupPath 拼出跨页联动用的精确查询地址（容器页就是这么拼的）。
func lookupPath(typeKey, key string) string {
	return "/api/v1/assets/lookup?type=" + typeKey + "&key=" + key
}

// 跨页联动（容器页的 Pod → 台账资产）拿到的只有集群/命名空间/名字，
// 拼出的自然键必须精确命中：命中要给出资产，没命中必须明确 404。
func TestHandleAssetLookupByNaturalKey(t *testing.T) {
	a, svc := assetTestAPI(t)
	cluster := "https://10.0.0.9:6443"
	key := asset.PodNaturalKey(cluster, "default", "web-7d9f-abc")
	seedAsset(t, svc, asset.TypePod, key, "web-7d9f-abc", "web-01", map[string]string{"phase": "Running"})

	rec := httptest.NewRecorder()
	a.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), lookupPath(asset.TypePod, key)))
	if rec.Code != http.StatusOK {
		t.Fatalf("命中应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	item, _ := body["asset"].(map[string]any)
	if item == nil || item["naturalKey"] != key || item["typeKey"] != asset.TypePod || item["node"] != "web-01" {
		t.Fatalf("返回的资产不符：%v", body)
	}
}

// 未命中必须 404（而不是返回一条模糊匹配到的别的资产）：
// 点进一条不相干的记录比"没找到"更糟——用户会以为看的就是这个 Pod。
func TestHandleAssetLookupMissIsNotFound(t *testing.T) {
	a, svc := assetTestAPI(t)
	cluster := "https://10.0.0.9:6443"
	seedAsset(t, svc, asset.TypePod, asset.PodNaturalKey(cluster, "default", "web-1"), "web-1", "web-01", nil)

	for _, tc := range []struct {
		name string
		path string
	}{
		{"同命名空间但名字不同", lookupPath(asset.TypePod, asset.PodNaturalKey(cluster, "default", "web-2"))},
		{"同名字但命名空间不同", lookupPath(asset.TypePod, asset.PodNaturalKey(cluster, "other", "web-1"))},
		{"同名字但集群不同", lookupPath(asset.TypePod, asset.PodNaturalKey("https://10.0.0.10:6443", "default", "web-1"))},
		{"类型不同", lookupPath(asset.TypeHost, asset.PodNaturalKey(cluster, "default", "web-1"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), tc.path))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("应 404，实际 %d（%s）", rec.Code, rec.Body.String())
			}
		})
	}
}

// 范围外资产按「不存在」返回：联动入口不能变成范围探测工具。
func TestHandleAssetLookupRespectsScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	cluster := "https://10.0.0.9:6443"
	key := asset.PodNaturalKey(cluster, "default", "db-0")
	// db-01 属 g2：g1 的受限用户查它必须 404
	seedAsset(t, svc, asset.TypePod, key, "db-0", "db-01", nil)

	rec := httptest.NewRecorder()
	a.handleAssetLookup(rec, assetReq(restrictedPrincipal([]string{"assets:read"}, "g1"), lookupPath(asset.TypePod, key)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("范围外应 404，实际 %d（%s）", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	a.handleAssetLookup(rec, assetReq(restrictedPrincipal([]string{"assets:read"}, "g2"), lookupPath(asset.TypePod, key)))
	if rec.Code != http.StatusOK {
		t.Fatalf("范围内应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// 参数缺失与未启用资产能力：给出可操作的错误，而不是空对象。
func TestHandleAssetLookupValidationAndDisabled(t *testing.T) {
	a, _ := assetTestAPI(t)
	for _, target := range []string{
		"/api/v1/assets/lookup",
		"/api/v1/assets/lookup?type=pod",
		"/api/v1/assets/lookup?key=x",
	} {
		rec := httptest.NewRecorder()
		a.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), target))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", target, rec.Code)
		}
	}

	disabled := &API{}
	rec := httptest.NewRecorder()
	disabled.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), lookupPath(asset.TypePod, "x")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未启用资产能力应 503，实际 %d", rec.Code)
	}
}

// 容器类资产允许直接给身份三元组：前端不该知道自然键的拼法（拼错只会命中别的资产）。
func TestHandleAssetLookupByContainerIdentity(t *testing.T) {
	a, svc := assetTestAPI(t)
	cluster := "https://10.0.0.9:6443"
	podKey := asset.PodNaturalKey(cluster, "default", "web-1")
	seedAsset(t, svc, asset.TypePod, podKey, "web-1", "web-01", nil)
	wlKey := asset.WorkloadNaturalKey(cluster, "default", "deployment", "web")
	seedAsset(t, svc, asset.TypeWorkload, wlKey, "web", "web-01", nil)

	cases := []struct {
		name   string
		target string
		key    string
	}{
		{"Pod 身份", "/api/v1/assets/lookup?type=pod&cluster=" + cluster + "&namespace=default&name=web-1", podKey},
		{"工作负载身份", "/api/v1/assets/lookup?type=workload&cluster=" + cluster + "&namespace=default&kind=deployment&name=web", wlKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), tc.target))
			if rec.Code != http.StatusOK {
				t.Fatalf("应 200，实际 %d（%s）", rec.Code, rec.Body.String())
			}
			item, _ := decodeBody(t, rec)["asset"].(map[string]any)
			if item == nil || item["naturalKey"] != tc.key {
				t.Fatalf("命中的资产不符：%v", item)
			}
			// 视图里必须带解好的容器身份，供反向联动（资产 → 容器页）直接使用
			ident, _ := item["container"].(map[string]any)
			if ident == nil || ident["cluster"] != cluster || ident["namespace"] != "default" {
				t.Fatalf("容器身份未解出：%v", item["container"])
			}
		})
	}

	// 身份缺一块 → 400（拼不出键就明确报错，不要静默按空值查）
	for _, target := range []string{
		"/api/v1/assets/lookup?type=pod&namespace=default&name=web-1",
		"/api/v1/assets/lookup?type=pod&cluster=" + cluster + "&name=web-1",
	} {
		rec := httptest.NewRecorder()
		a.handleAssetLookup(rec, assetReq(globalPrincipal("assets:read"), target))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应 400，实际 %d", target, rec.Code)
		}
	}
}

// 真实路由表：缺 assets:read 拿不到；且 /lookup 不会被 /{id} 抢先匹配。
func TestRoutes_AssetLookupPermissionAndRouting(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	cluster := "https://10.0.0.9:6443"
	key := asset.PodNaturalKey(cluster, "default", "web-1")
	seedAsset(t, svc, asset.TypePod, key, "web-1", "web-01", nil)

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(globalPrincipal("assets:export"), http.MethodGet, lookupPath(asset.TypePod, key), ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:read 应 403，实际 %d", denied.Code)
	}

	ok := httptest.NewRecorder()
	mux.ServeHTTP(ok, reqWith(globalPrincipal("assets:read"), http.MethodGet, lookupPath(asset.TypePod, key), ""))
	if ok.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d（%s）", ok.Code, ok.Body.String())
	}
	// 命中 /{id} 的表现是 400「资产 ID 非法」，用它反证路由没被 {id} 抢先
	if strings.Contains(ok.Body.String(), "资产 ID") {
		t.Fatalf("lookup 被 /assets/{id} 抢先匹配了：%s", ok.Body.String())
	}
}
