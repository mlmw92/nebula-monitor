package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/audit"
	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/security"
)

// matrixTestAPI 造一个带安全基线存储的 API（节点夹具来自 scopeTestAPI：web-01/web-02 在 g1，db-01 在 g2）。
func matrixTestAPI(t *testing.T) (*API, *security.Store) {
	t.Helper()
	a, _ := assetTestAPI(t)
	st := security.New(filepath.Join(t.TempDir(), "security_store.json"))
	a.security = st
	return a, st
}

// seedBaseline 灌一份某节点的基线（只关心 key/pass/name 三个字段，其余与 Agent 上报一致）。
func seedBaseline(t *testing.T, st *security.Store, node string, score float64, items ...model.SecurityBaselineItem) {
	t.Helper()
	st.Ingest(node, nil, &model.SecurityBaseline{
		Node: node, Score: score, CheckedAt: 1791353871785, Items: items,
	})
}

func item(key, name string, pass bool) model.SecurityBaselineItem {
	return model.SecurityBaselineItem{Key: key, Name: name, Pass: pass}
}

// exportMatrix 跑一次导出并解析出 CSV（去掉 BOM 后）。
func exportMatrix(t *testing.T, mux *http.ServeMux, p *auth.Principal) [][]string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(p, http.MethodGet, "/api/v1/security/baselines/export", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("导出应 200，实际 %d（%s）", rec.Code, rec.Body.String())
	}
	raw := strings.TrimPrefix(rec.Body.String(), "\xEF\xBB\xBF")
	rows, err := csv.NewReader(strings.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("解析导出 CSV 失败: %v", err)
	}
	return rows
}

// rowOf 按第一列（节点）取一行。
func rowOf(t *testing.T, rows [][]string, node string) []string {
	t.Helper()
	for _, r := range rows[1:] {
		if r[0] == node {
			return r
		}
	}
	t.Fatalf("导出里没有节点 %s 的行：%v", node, rows)
	return nil
}

// 矩阵的形状：身份列 + 检查项并集（按 key 排序）+ 三态。
//
// 这条用例的核心是最后两格：**空 ≠ 未通过**——db-01 没上报 firewall_enabled，
// 那一格必须是空，而不是"未通过"（把"没采到"记成"不合规"，据此开整改单就是错单）。
func TestComplianceMatrixExport_ShapeAndThreeStates(t *testing.T) {
	a, st := matrixTestAPI(t)
	seedBaseline(t, st, "web-01", 60,
		item("ssh_root_login", "SSH root 登录已禁用", true),
		item("firewall_enabled", "防火墙已启用", false))
	seedBaseline(t, st, "db-01", 80, item("ssh_root_login", "SSH root 登录已禁用", false))
	mux := newRoutesMux(a)

	rows := exportMatrix(t, mux, globalPrincipal("security:read", "security:export"))
	if len(rows) != 3 {
		t.Fatalf("应 1 行表头 + 2 行数据，实际 %d 行：%v", len(rows), rows)
	}
	wantHeader := []string{"节点", "显示名", "IP", "评分", "检查时间",
		"防火墙已启用(firewall_enabled)", "SSH root 登录已禁用(ssh_root_login)"}
	if strings.Join(rows[0], "|") != strings.Join(wantHeader, "|") {
		t.Fatalf("表头不符：\n实际 %v\n期望 %v", rows[0], wantHeader)
	}

	web := rowOf(t, rows, "web-01")
	if web[3] != "60" {
		t.Fatalf("评分应原样带出（60），实际 %q", web[3])
	}
	if web[4] != formatCSVTime(1791353871785) {
		t.Fatalf("检查时间格式不符：%q", web[4])
	}
	if web[5] != "未通过" || web[6] != "通过" {
		t.Fatalf("web-01 三态不符：firewall=%q ssh=%q", web[5], web[6])
	}

	db := rowOf(t, rows, "db-01")
	if db[5] != "" {
		t.Fatalf("db-01 没上报 firewall_enabled，该格必须为空（空 ≠ 未通过），实际 %q", db[5])
	}
	if db[6] != "未通过" {
		t.Fatalf("db-01 的 ssh_root_login 应未通过，实际 %q", db[6])
	}
}

// 导出集合必须与列表接口**完全一致**：两处各写一遍范围过滤，迟早会出现
// "界面上看得见、导出里没有"或者更糟的"导出越权"。
func TestComplianceMatrixExport_MatchesBaselinesListUnderScope(t *testing.T) {
	a, st := matrixTestAPI(t)
	seedBaseline(t, st, "web-01", 60, item("ssh_root_login", "SSH root 登录已禁用", true))
	seedBaseline(t, st, "db-01", 80, item("ssh_root_login", "SSH root 登录已禁用", false))
	mux := newRoutesMux(a)

	// 列表接口（同一身份）看到的节点集合
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(restrictedPrincipal([]string{"security:read", "security:export"}, "g1"),
		http.MethodGet, "/api/v1/security/baselines", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，实际 %d", rec.Code)
	}
	body := decodeBody(t, rec)
	listed := map[string]bool{}
	for _, b := range body["baselines"].([]any) {
		listed[b.(map[string]any)["node"].(string)] = true
	}

	rows := exportMatrix(t, mux, restrictedPrincipal([]string{"security:read", "security:export"}, "g1"))
	exported := map[string]bool{}
	for _, r := range rows[1:] {
		exported[r[0]] = true
	}
	if len(listed) != len(exported) {
		t.Fatalf("导出与列表的节点集合不一致：列表 %v，导出 %v", listed, exported)
	}
	for n := range listed {
		if !exported[n] {
			t.Fatalf("列表里有 %s，导出里没有：%v", n, exported)
		}
	}
	if exported["db-01"] {
		t.Fatalf("g1 身份不该导出 g2 的 db-01：%v", exported)
	}
}

// 导出是独立权限点：只有 security:read 的人不能导（与 assets:export / audit:export 同一约定）。
func TestComplianceMatrixExport_RequiresExportPermission(t *testing.T) {
	a, st := matrixTestAPI(t)
	seedBaseline(t, st, "web-01", 60, item("ssh_root_login", "SSH root 登录已禁用", true))
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("security:read"),
		http.MethodGet, "/api/v1/security/baselines/export", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺 security:export 应 403，实际 %d（%s）", rec.Code, rec.Body.String())
	}
}

// 没有基线数据时**明确拒绝**，不给"只有表头的空文件"——那种文件看起来像"全部合规"。
func TestComplianceMatrixExport_RejectsEmptyData(t *testing.T) {
	a, _ := matrixTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("security:export"),
		http.MethodGet, "/api/v1/security/baselines/export", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("没有数据应 400，实际 %d", rec.Code)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "还没有安全基线数据") {
		t.Fatalf("报文应说明原因，实际 %s", msg)
	}
}

// 安全能力未启用（未注入 store）时同样明确拒绝，且报文与"没数据"区分开。
func TestComplianceMatrixExport_RejectsWhenSecurityDisabled(t *testing.T) {
	a := scopeTestAPI(t)
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("security:export"),
		http.MethodGet, "/api/v1/security/baselines/export", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未启用应 400，实际 %d", rec.Code)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "未启用") {
		t.Fatalf("报文应说明未启用，实际 %s", msg)
	}
}

// 上限判定（边界）：差一台机器、差一格就该拒绝——这类上限最该被测到的是边界本身。
func TestMatrixLimitError_Boundaries(t *testing.T) {
	if msg := matrixLimitError(maxMatrixHosts, 100); msg != "" {
		t.Fatalf("%d 台应通过，实际被拒：%s", maxMatrixHosts, msg)
	}
	if msg := matrixLimitError(maxMatrixHosts+1, 100); msg == "" {
		t.Fatalf("%d 台应被拒", maxMatrixHosts+1)
	}
	// 台数没超但格子超了：同样拒绝（矩阵是乘积，只看台数会漏）
	if msg := matrixLimitError(maxMatrixHosts, maxMatrixCells/maxMatrixHosts); msg != "" {
		t.Fatalf("正好 %d 格应通过，实际被拒：%s", maxMatrixCells, msg)
	}
	if msg := matrixLimitError(maxMatrixHosts, maxMatrixCells/maxMatrixHosts+1); msg == "" {
		t.Fatalf("超过 %d 格应被拒", maxMatrixCells)
	}
}

// 超限走 HTTP 时是 400 且报文说明规模（用"格子超限"造数据：50 台 × 4001 项，
// 只写 50 次存储文件，比造 2001 台机器快得多）。
func TestComplianceMatrixExport_RejectsOverCellLimit(t *testing.T) {
	a, st := matrixTestAPI(t)
	// 键必须**唯一**：重复键会被并集合并掉，列数上不去，这条用例就变成一句空话
	// （第一版就是 26×26 循环生成键，只得到 676 列，于是"超限"根本没发生）。
	items := make([]model.SecurityBaselineItem, 0, 4001)
	for i := 0; i < 4001; i++ {
		items = append(items, item(fmt.Sprintf("check_%04d", i), "项", true))
	}
	for i := 0; i < 50; i++ {
		seedBaseline(t, st, fmt.Sprintf("web-%02d", i), 100, items...)
	}
	mux := newRoutesMux(a)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, reqWith(globalPrincipal("security:export"),
		http.MethodGet, "/api/v1/security/baselines/export", ""))
	if rec.Code != http.StatusBadRequest {
		// 不打印 body：失败时那是一个几十万字符的 CSV，日志会被它淹没。
		t.Fatalf("超过格子上限应 400，实际 %d（body 长度 %d）", rec.Code, rec.Body.Len())
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "超过单次导出上限") {
		t.Fatalf("报文应说明规模与上限，实际 %s", msg)
	}
}

// 导出要留痕：事后要能回答"谁在什么时候把整份合规结论导走了"。
func TestComplianceMatrixExport_WritesAuditRecord(t *testing.T) {
	a, st := matrixTestAPI(t)
	seedBaseline(t, st, "web-01", 60, item("ssh_root_login", "SSH root 登录已禁用", true))
	mux := newRoutesMux(a)

	exportMatrix(t, mux, globalPrincipal("security:export"))

	events, _, err := a.audit.Query(audit.QueryFilter{Limit: 50})
	if err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	for _, e := range events {
		if e.Category == "security" && e.Action == "export" {
			if !strings.Contains(e.Detail, "1 台 × 1 项") {
				t.Fatalf("审计详情应带规模，实际 %q", e.Detail)
			}
			return
		}
	}
	t.Fatalf("没有找到合规矩阵导出的审计记录：%+v", events)
}
