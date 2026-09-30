package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 忽略：从台账隐藏但不停止采集；列表默认不返回，`ignored=only` 可下钻；恢复后回到列表。
func TestRoutes_AssetIgnoreAndRestore(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	id := strconv.FormatInt(host.ID, 10)
	p := restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1")

	// 缺写权限 → 403
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"),
		http.MethodPost, "/api/v1/assets/"+id+"/ignore", `{"reason":"x"}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:write 应 403，实际 %d", denied.Code)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/assets/"+id+"/ignore", `{"reason":"已下线，采集未清理"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("忽略应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ignored"] != true || body["ignoreReason"] != "已下线，采集未清理" {
		t.Fatalf("忽略标记与理由应回显：%v", body)
	}

	// 默认列表应少一条（g1 范围内 web-01 被隐藏后只剩它上面的 redis 实例）；
	// ignored=only 则只剩被忽略的那条。
	def := httptest.NewRecorder()
	mux.ServeHTTP(def, reqWith(p, http.MethodGet, "/api/v1/assets", ""))
	if decodeBody(t, def)["total"].(float64) != 1 {
		t.Fatalf("默认列表应隐藏已忽略资产：%s", def.Body.String())
	}
	only := httptest.NewRecorder()
	mux.ServeHTTP(only, reqWith(p, http.MethodGet, "/api/v1/assets?ignored=only", ""))
	onlyBody := decodeBody(t, only)
	if onlyBody["total"].(float64) != 1 {
		t.Fatalf("ignored=only 应只剩 1 条：%s", only.Body.String())
	}

	// 摘要：已忽略单独计数，且不计入总数
	sum := httptest.NewRecorder()
	mux.ServeHTTP(sum, reqWith(p, http.MethodGet, "/api/v1/assets/summary", ""))
	if decodeBody(t, sum)["ignored"].(float64) != 1 {
		t.Fatalf("摘要应有 ignored=1：%s", sum.Body.String())
	}

	restored := httptest.NewRecorder()
	mux.ServeHTTP(restored, reqWith(p, http.MethodPost, "/api/v1/assets/"+id+"/restore", ""))
	if restored.Code != http.StatusOK || decodeBody(t, restored)["ignored"] != false {
		t.Fatalf("恢复应 200 且 ignored=false，实际 %d（%s）", restored.Code, restored.Body.String())
	}
}

// 彻底删除：采集资产 409（提示改用忽略），纯人工资产可删。
func TestRoutes_AssetPurge(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"assets:write"}, "g1")

	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	conflict := httptest.NewRecorder()
	mux.ServeHTTP(conflict, reqWith(p, http.MethodPost,
		"/api/v1/assets/"+strconv.FormatInt(host.ID, 10)+"/purge", ""))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("采集资产应 409（该用忽略），实际 %d", conflict.Code)
	}
	if !strings.Contains(conflict.Body.String(), "忽略") {
		t.Fatalf("409 的说明应提示改用忽略：%s", conflict.Body.String())
	}

	manual, _, err := svc.Apply(asset.Observation{
		TypeKey: asset.TypeHost, NaturalKey: "manual-01", Name: "手工台账设备", Node: "web-01",
		Source: asset.SourceManual, Attrs: map[string]string{"vendor": "Dell"},
	})
	if err != nil {
		t.Fatalf("准备人工资产失败: %v", err)
	}
	okRec := httptest.NewRecorder()
	mux.ServeHTTP(okRec, reqWith(p, http.MethodPost,
		"/api/v1/assets/"+strconv.FormatInt(manual.ID, 10)+"/purge", ""))
	if okRec.Code != http.StatusOK {
		t.Fatalf("纯人工资产应可删除，实际 %d（%s）", okRec.Code, okRec.Body.String())
	}
	if decodeBody(t, okRec)["purged"] != true {
		t.Fatalf("应回执 purged=true：%s", okRec.Body.String())
	}
	if _, ok, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "manual-01"}); ok {
		t.Fatal("删除后应查不到")
	}
}

// 标签：写接口需 assets:write，响应回填最新标签，可按标签筛选；空键被拒。
func TestRoutes_AssetLabels(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	host, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	id := strconv.FormatInt(host.ID, 10)
	p := restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1")

	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"),
		http.MethodPut, "/api/v1/assets/"+id+"/labels", `{"labels":{"env":"prod"}}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:write 应 403，实际 %d", denied.Code)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPut, "/api/v1/assets/"+id+"/labels",
		`{"labels":{"env":"prod","team":"sre"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("写标签应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	labels, _ := decodeBody(t, rec)["labels"].(map[string]any)
	if labels["env"] != "prod" || labels["team"] != "sre" {
		t.Fatalf("响应应回填标签：%v", labels)
	}

	filtered := httptest.NewRecorder()
	mux.ServeHTTP(filtered, reqWith(p, http.MethodGet, "/api/v1/assets?label=env:prod", ""))
	if decodeBody(t, filtered)["total"].(float64) != 1 {
		t.Fatalf("按标签筛选应剩 1 条：%s", filtered.Body.String())
	}

	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, reqWith(p, http.MethodPut, "/api/v1/assets/"+id+"/labels", `{"labels":{" ":"x"}}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("空标签键应 400，实际 %d", bad.Code)
	}

	// 非法 ignored 取值必须明确报错，而不是静默按默认处理
	badParam := httptest.NewRecorder()
	mux.ServeHTTP(badParam, reqWith(p, http.MethodGet, "/api/v1/assets?ignored=bogus", ""))
	if badParam.Code != http.StatusBadRequest {
		t.Fatalf("非法 ignored 取值应 400，实际 %d", badParam.Code)
	}
}
