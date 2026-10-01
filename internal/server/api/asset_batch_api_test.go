package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/server/asset"
)

// 夹具（见 assetTestAPI）：web-01 → g1，db-01 → g2，另有 web-01 上的 redis 实例（属 g1）。
// 因此"限制到 g1"的用户能看到 2 条资产（web-01 + redis），看不到 db-01。

// 批量维护：逐条给结论、范围外按「资产不存在」、一条都没成功才 409。
func TestRoutes_AssetsBatchScopeAndPartialSuccess(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	web, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	db, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "db-01"})

	// 缺 assets:write → 403（批量沿用单条写的权限点，不另开一个）
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"assets:read"}, "g1"),
		http.MethodPost, "/api/v1/assets/batch", `{"ids":[1],"op":"owner","owner":"alice"}`))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:write 应 403，实际 %d", denied.Code)
	}

	// 一条范围内 + 一条范围外：部分成功照常 200
	p := restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d,%d],"op":"owner","owner":"alice"}`, web.ID, db.ID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("部分成功应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	batch, ok := decodeBody(t, rec)["batch"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 batch：%s", rec.Body.String())
	}
	if batch["ok"].(float64) != 1 || batch["failed"].(float64) != 1 {
		t.Fatalf("应 1 成功 1 失败：%v", batch)
	}
	// 范围外必须是「资产不存在」，而不是「无权限」——后者等于确认了它存在
	var outItem map[string]any
	for _, raw := range batch["items"].([]any) {
		it := raw.(map[string]any)
		if int64(it["id"].(float64)) == db.ID {
			outItem = it
		}
	}
	if outItem == nil || outItem["error"] != "资产不存在" {
		t.Fatalf("范围外资产应按「资产不存在」计入 items：%v", batch["items"])
	}
	// 范围内那条要真的改了（只回执不算改）
	got, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if got.Owner() != "alice" {
		t.Fatalf("范围内资产的责任人应已转派，实际 %q", got.Owner())
	}

	// 一条都没成功（全部范围外）→ 409，且 body 里仍带逐条原因
	all := httptest.NewRecorder()
	mux.ServeHTTP(all, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d],"op":"owner","owner":"alice"}`, db.ID)))
	if all.Code != http.StatusConflict {
		t.Fatalf("全部失败应 409（不能报成部分成功），实际 %d", all.Code)
	}
	if _, hasErr := decodeBody(t, all)["error"]; !hasErr {
		t.Fatal("409 响应应带 error 说明")
	}
}

// 批量标签与批量忽略/恢复：都以单条写方法的语义与历史记录为准。
func TestRoutes_AssetsBatchLabelsAndIgnore(t *testing.T) {
	a, svc := assetTestAPI(t)
	mux := newRoutesMux(a)
	web, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	redis, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeMiddlewareInst, NaturalKey: "redis:127.0.0.1:6379"})
	p := restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1")

	// 打标签：一次给两条写 env=prod
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d,%d],"op":"labels","labels":{"env":"prod"}}`, web.ID, redis.ID)))
	if rec.Code != http.StatusOK {
		t.Fatalf("批量打标签应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	got, _, _ := svc.Get(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"})
	if got.Labels["env"] != "prod" {
		t.Fatalf("标签应已写入，实际 %v", got.Labels)
	}
	// 标签变更同样进字段级历史（字段名带 label: 前缀）
	hist, err := svc.History(asset.Ref{TypeKey: asset.TypeHost, NaturalKey: "web-01"}, 0)
	if err != nil {
		t.Fatalf("查询历史失败: %v", err)
	}
	found := false
	for _, h := range hist {
		if h.Field == "label:env" && h.New == "prod" {
			found = true
		}
	}
	if !found {
		t.Fatal("批量打标签也应留下字段级变更记录（否则'谁把这个环境标签改了'无从回答）")
	}

	// 批量忽略：默认列表不再返回这两条
	ign := httptest.NewRecorder()
	mux.ServeHTTP(ign, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d,%d],"op":"ignore","reason":"批量下线"}`, web.ID, redis.ID)))
	if ign.Code != http.StatusOK {
		t.Fatalf("批量忽略应 200，实际 %d（%s）", ign.Code, ign.Body.String())
	}
	def := httptest.NewRecorder()
	mux.ServeHTTP(def, reqWith(p, http.MethodGet, "/api/v1/assets", ""))
	if got := decodeBody(t, def)["total"].(float64); got != 0 {
		t.Fatalf("忽略后默认列表应为 0 条（范围内只剩这两条），实际 %v", got)
	}
	only := httptest.NewRecorder()
	mux.ServeHTTP(only, reqWith(p, http.MethodGet, "/api/v1/assets?ignored=only", ""))
	if got := decodeBody(t, only)["total"].(float64); got != 2 {
		t.Fatalf("ignored=only 应有 2 条，实际 %v", got)
	}

	// 批量恢复
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d,%d],"op":"restore"}`, web.ID, redis.ID)))
	if res.Code != http.StatusOK {
		t.Fatalf("批量恢复应 200，实际 %d（%s）", res.Code, res.Body.String())
	}
	back := httptest.NewRecorder()
	mux.ServeHTTP(back, reqWith(p, http.MethodGet, "/api/v1/assets", ""))
	if got := decodeBody(t, back)["total"].(float64); got != 2 {
		t.Fatalf("恢复后应有 2 条，实际 %v", got)
	}

	// 清除责任人
	clear := httptest.NewRecorder()
	mux.ServeHTTP(clear, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%d],"op":"ownerClear"}`, web.ID)))
	if clear.Code != http.StatusOK {
		t.Fatalf("清除责任人应 200，实际 %d（%s）", clear.Code, clear.Body.String())
	}
}

// 批量维护的参数校验：**整批**的问题必须在执行前一次性拦下，
// 否则结果面板会被 N 条一模一样的失败刷屏。
func TestRoutes_AssetsBatchValidation(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)
	p := restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1")

	cases := []struct {
		name string
		body string
	}{
		{"转派但未填责任人", `{"ids":[1],"op":"owner"}`},
		{"打标签但没给任何键", `{"ids":[1],"op":"labels"}`},
		{"标签键为空", `{"ids":[1],"op":"labels","labels":{"  ":"x"}}`},
		{"同一键既写又删", `{"ids":[1],"op":"labels","labels":{"env":"prod"},"remove":["env"]}`},
		{"动作未知", `{"ids":[1],"op":"purge-all"}`},
		{"没有资产", `{"ids":[],"op":"restore"}`},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, reqWith(p, http.MethodPost, "/api/v1/assets/batch", tc.body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s：应 400，实际 %d（%s）", tc.name, rec.Code, rec.Body.String())
		}
	}

	// 超过单批上限：显式 400（静默截断会让用户以为全都改了）
	ids := make([]string, 0, maxBatchAssets+1)
	for i := 1; i <= maxBatchAssets+1; i++ {
		ids = append(ids, strconv.Itoa(i))
	}
	over := httptest.NewRecorder()
	mux.ServeHTTP(over, reqWith(p, http.MethodPost, "/api/v1/assets/batch",
		fmt.Sprintf(`{"ids":[%s],"op":"restore"}`, strings.Join(ids, ","))))
	if over.Code != http.StatusBadRequest {
		t.Fatalf("超上限应 400，实际 %d", over.Code)
	}
	if msg := over.Body.String(); !strings.Contains(msg, "分批") {
		t.Fatalf("超上限的提示应告诉用户怎么办（分批）：%s", msg)
	}
}

// 导出清单：独立权限点、只导**范围内**的全部资产（不受分页限制）、带 BOM。
func TestRoutes_AssetExport(t *testing.T) {
	a, _ := assetTestAPI(t)
	mux := newRoutesMux(a)

	// 有 assets:read / assets:write 但没有 assets:export → 403
	// （导出是"整份台账落盘"，与逐页翻看不是一个量级的动作）
	denied := httptest.NewRecorder()
	mux.ServeHTTP(denied, reqWith(restrictedPrincipal([]string{"assets:read", "assets:write"}, "g1"),
		http.MethodGet, "/api/v1/assets/export", ""))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("缺 assets:export 应 403，实际 %d", denied.Code)
	}

	p := restrictedPrincipal([]string{"assets:export"}, "g1")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/assets/export", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("Content-Type 应为 csv，实际 %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") ||
		!strings.Contains(cd, ".csv") {
		t.Fatalf("应作为附件下载并给 .csv 文件名，实际 %q", cd)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "\xEF\xBB\xBF") {
		t.Fatal("缺少 UTF-8 BOM：Excel 打开中文列名会乱码")
	}
	lines := csvLines(body)
	if !strings.Contains(lines[0], "资产ID") || !strings.Contains(lines[0], "责任人") {
		t.Fatalf("表头应含资产ID与责任人：%s", lines[0])
	}
	// g1 范围内共 2 条（web-01 + 它上面的 redis）；db-01 属 g2 → 不得出现
	if len(lines) != 3 {
		t.Fatalf("应导出 2 条范围内资产（不含范围外的 db-01），实际 %d 行数据：%v", len(lines)-1, lines)
	}
	if strings.Contains(body, "db-01") {
		t.Fatal("导出不得包含资源范围外的资产")
	}
	if !strings.Contains(body, "web-01") {
		t.Fatal("导出应包含范围内资产")
	}
	// 上报状态与来源要翻成中文（导出的表是给人看的）
	if !strings.Contains(lines[0], "上报状态") || !strings.Contains(lines[0], "来源") {
		t.Fatalf("表头应含上报状态与来源：%s", lines[0])
	}
	if !strings.Contains(lines[1], "在线") && !strings.Contains(lines[1], "归档") {
		t.Fatalf("状态列应是中文取值：%s", lines[1])
	}

	// 筛选与列表同一套参数：type=host 时只剩 1 条
	one := httptest.NewRecorder()
	mux.ServeHTTP(one, reqWith(p, http.MethodGet, "/api/v1/assets/export?type=host", ""))
	if got := len(csvLines(one.Body.String())) - 1; got != 1 {
		t.Fatalf("type=host 应导出 1 条，实际 %d", got)
	}
}

// csvLines 把响应体切成行（去掉 BOM 与尾部空行）。
//
// 按 \n 切：encoding/csv 默认用 \n 结尾（UseCRLF 默认 false），与 metrics 导出保持一致。
// 仍兼容 \r\n，免得哪天有人打开 UseCRLF 时这条助手悄悄失效。
func csvLines(body string) []string {
	body = strings.TrimPrefix(body, "\xEF\xBB\xBF")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	return strings.Split(strings.TrimRight(body, "\n"), "\n")
}
