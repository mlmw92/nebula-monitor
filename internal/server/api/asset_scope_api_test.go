package api

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
	"github.com/nebula/monitor/internal/server/auth"
)

// 业务范围（资产标签维度）在接口层的用例。
//
// 重点全在**负例**：两个维度是「且」，任一维度不放行都必须看不见；而且响应要与"不存在"
// 完全一致（404 而不是 403），否则等于告诉对方"范围外有这么一条资产"。
// 另外一条同样重要：**未配置业务范围的账号行为必须逐字不变**（存量部署升级后不能失权）。

// bizPrincipal 构造"两个维度都限定"的身份：节点分组 groups + 业务标签值 values。
func bizPrincipal(perms []string, groups []string, values ...string) *auth.Principal {
	set := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	labels := make([]auth.AssetScope, 0, len(values))
	for _, v := range values {
		labels = append(labels, auth.AssetScope{Key: auth.DefaultScopeLabelKey, Value: v})
	}
	return &auth.Principal{
		Username:    "ops1",
		Permissions: set,
		Scope: auth.Scope{
			Mode: auth.ScopeRestricted, Groups: groups,
			AssetMode: auth.AssetScopeLimited, AssetLabels: labels,
		},
	}
}

// listAssetNames 走真实路由取台账列表，返回资产名（排序）与总数。
func listAssetNames(t *testing.T, a *API, p *auth.Principal) ([]string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/assets", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	names := []string{}
	if raw, ok := body["assets"].([]any); ok {
		for _, it := range raw {
			if m, ok := it.(map[string]any); ok {
				names = append(names, m["name"].(string))
			}
		}
	}
	sort.Strings(names)
	total := 0
	if v, ok := body["total"].(float64); ok {
		total = int(v)
	}
	return names, total
}

func TestAssetBusinessScope(t *testing.T) {
	a, svc := assetTestAPI(t)
	// 夹具：web-01 是 biz=pay；web-01 上的 redis 实例是 biz=risk；db-01 不打标签
	if _, err := svc.SetLabels(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"},
		map[string]string{auth.DefaultScopeLabelKey: "pay"}, nil, "admin", ""); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}
	riskRef := asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"}
	if _, err := svc.SetLabels(riskRef, map[string]string{auth.DefaultScopeLabelKey: "risk"}, nil, "admin", ""); err != nil {
		t.Fatalf("打标签失败: %v", err)
	}

	// ① 两个维度取交集：g1+g2 里的 biz=pay 只有 web-01（redis 是 risk、db-01 没标签）
	pay := bizPrincipal([]string{"assets:read"}, []string{"g1", "g2"}, "pay")
	names, total := listAssetNames(t, a, pay)
	if total != 1 || len(names) != 1 || names[0] != "web-01" {
		t.Fatalf("业务范围过滤不符：names=%v total=%d", names, total)
	}

	// ② fail-closed：限定了却没有任何选择器 ⇒ 什么都看不到（不是"不限"）
	if names, total := listAssetNames(t, a, bizPrincipal([]string{"assets:read"}, []string{"g1", "g2"})); total != 0 || len(names) != 0 {
		t.Fatalf("限定了却没有选择器应恒不可见，实际 names=%v total=%d", names, total)
	}

	// ③ 未配置业务范围 ⇒ 与以前逐字一致（只有节点分组在起作用）
	if names, total := listAssetNames(t, a, restrictedPrincipal([]string{"assets:read"}, "g1")); total != 2 || len(names) != 2 {
		t.Fatalf("未配置业务范围时不该受影响：names=%v total=%d", names, total)
	}

	risk, ok, err := svc.Get(riskRef)
	if err != nil || !ok {
		t.Fatalf("取夹具资产失败: ok=%v err=%v", ok, err)
	}
	detailCode := func(p *auth.Principal) int {
		t.Helper()
		rec := httptest.NewRecorder()
		newRoutesMux(a).ServeHTTP(rec,
			reqWith(p, http.MethodGet, "/api/v1/assets/"+strconv.FormatInt(risk.ID, 10), ""))
		return rec.Code
	}

	// ④ 同一条资产：节点在范围内、业务范围外 ⇒ 详情按「不存在」返回 404（不是 403）
	if code := detailCode(pay); code != http.StatusNotFound {
		t.Fatalf("业务范围外的资产详情应 404，实际 %d", code)
	}
	// 对照：同一身份去掉业务范围就该看得到（证明上面那个 404 是业务维度造成的）
	if code := detailCode(restrictedPrincipal([]string{"assets:read"}, "g1")); code != http.StatusOK {
		t.Fatalf("未配置业务范围时应可见，实际 %d", code)
	}

	// ⑤ 写路径同样收窄：能看见才能改，看不见的连"存在"都不该知道
	upd := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(upd, reqWith(
		bizPrincipal([]string{"assets:write"}, []string{"g1"}, "pay"),
		http.MethodPut, "/api/v1/assets/"+strconv.FormatInt(risk.ID, 10), `{"name":"改个名"}`))
	if upd.Code != http.StatusNotFound {
		t.Fatalf("业务范围外的资产写入应 404，实际 %d（%s）", upd.Code, upd.Body.String())
	}

	// ⑥ 导出与摘要必须与列表同一个集合（否则用户拿一份掺了范围外资产的表去盘点）
	exporter := bizPrincipal([]string{"assets:read", "assets:export"}, []string{"g1", "g2"}, "pay")
	exp := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(exp, reqWith(exporter, http.MethodGet, "/api/v1/assets/export", ""))
	if exp.Code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d", exp.Code)
	}
	if rows := len(csvLines(exp.Body.String())) - 1; rows != 1 {
		t.Fatalf("导出应只剩 1 条（表头之外），实际 %d", rows)
	}
	sum := httptest.NewRecorder()
	newRoutesMux(a).ServeHTTP(sum, reqWith(exporter, http.MethodGet, "/api/v1/assets/summary", ""))
	if got := int(decodeBody(t, sum)["total"].(float64)); got != 1 {
		t.Fatalf("摘要总数应与列表一致（1），实际 %d", got)
	}
}
