package api

import (
	"encoding/csv"
	"net/http/httptest"
	"strings"
	"testing"
)

// CSV 公式注入：Excel / WPS 会把以 = + - @ 开头的单元格当**公式**执行，
// Tab 与 CR 还能用来从单元格里逃逸。这是**电子表格语义**层面的问题——
// encoding/csv 只做 CSV 语法层面的转义（逗号、引号、换行），对公式前缀一无所知。
func TestCSVCell_SanitizesFormulaPrefixes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"=cmd|'/C calc'!A0", "'=cmd|'/C calc'!A0"},
		{"=1+1", "'=1+1"},
		{"+1+1", "'+1+1"},
		{"-2+3", "'-2+3"},
		{"@SUM(A1)", "'@SUM(A1)"},
		{"\t=1+1", "'\t=1+1"},
		{"\r=1+1", "'\r=1+1"},
		// 普通值不能被改动：否则"导出的数据和界面上的不一样"会变成一个没法回答的问题。
		{"", ""},
		{"web-01", "web-01"}, // 连字符不在首位，不算前缀
		{"1.30.45", "1.30.45"},
		{"SSH root 登录已禁用", "SSH root 登录已禁用"},
		{"通过", "通过"},
		{"未通过", "未通过"},
	}
	for _, c := range cases {
		if got := csvCell(c.in); got != c.want {
			t.Errorf("csvCell(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// csvDownload 是导出类接口的统一出口：BOM + 响应头 + 表头与每一行都消毒。
func TestCSVDownload_WritesBOMHeadersAndSanitizesRows(t *testing.T) {
	rec := httptest.NewRecorder()
	csvDownload(rec, "demo-20261007-120000.csv",
		[]string{"=恶意表头", "名称"},
		func(write func([]string)) {
			write([]string{"=cmd|'/C calc'!A0", "web-01"})
			write([]string{"", "正常值"})
		})

	if rec.Code != 200 {
		t.Fatalf("状态码应 200，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type 应 text/csv，实际 %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "demo-20261007-120000.csv") {
		t.Fatalf("Content-Disposition 应带文件名，实际 %q", cd)
	}

	raw := rec.Body.String()
	if !strings.HasPrefix(raw, "\xEF\xBB\xBF") {
		// BOM 少了这三个字节，Excel 打开中文列名就是乱码——审计导出此前就漏了它。
		t.Fatalf("响应体应以 UTF-8 BOM 开头，实际开头 %q", raw[:min(6, len(raw))])
	}

	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\xEF\xBB\xBF"))).ReadAll()
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("应有 1 行表头 + 2 行数据，实际 %d 行", len(rows))
	}
	if rows[0][0] != "'=恶意表头" {
		t.Fatalf("表头也要消毒，实际 %q", rows[0][0])
	}
	if rows[1][0] != "'=cmd|'/C calc'!A0" {
		t.Fatalf("数据行的公式前缀应被中和，实际 %q", rows[1][0])
	}
	if rows[1][1] != "web-01" {
		t.Fatalf("普通值不该被改动，实际 %q", rows[1][1])
	}
	if rows[2][0] != "" {
		t.Fatalf("空单元格应保持为空，实际 %q", rows[2][0])
	}
}

func TestCSVFilename_CarriesTimestamp(t *testing.T) {
	got := csvFilename("compliance-matrix")
	if !strings.HasPrefix(got, "compliance-matrix-") || !strings.HasSuffix(got, ".csv") {
		t.Fatalf("文件名形状不符：%q", got)
	}
	if len(got) != len("compliance-matrix-20060102-150405.csv") {
		t.Fatalf("文件名应带 15 位时间戳：%q", got)
	}
}
