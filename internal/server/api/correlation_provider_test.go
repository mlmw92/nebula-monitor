package api

import (
	"testing"

	"github.com/nebula/monitor/internal/server/analysis"
)

// TestCorrelationNotes_NoAnalyzer 未注入分析器时不返回结论，也不 panic。
func TestCorrelationNotes_NoAnalyzer(t *testing.T) {
	a := &API{}
	if got := a.CorrelationNotes("web-01"); len(got) != 0 {
		t.Fatalf("未注入分析器应返回空，got %v", got)
	}
	if got := a.CorrelationNotes(""); len(got) != 0 {
		t.Fatalf("空节点名应返回空，got %v", got)
	}
}

// TestCorrelateText 结论文案：标题 + 摘要组合，缺一即用另一方兜底，均空返回空串（由调用方过滤）。
func TestCorrelateText(t *testing.T) {
	cases := []struct {
		name string
		in   analysis.Correlation
		want string
	}{
		{"标题与摘要", analysis.Correlation{Title: "磁盘将满", Summary: "关联 2 个实例不可用"}, "磁盘将满：关联 2 个实例不可用"},
		{"仅标题（含空白）", analysis.Correlation{Title: " 磁盘将满 "}, "磁盘将满"},
		{"仅摘要", analysis.Correlation{Summary: "仅摘要"}, "仅摘要"},
		{"均为空", analysis.Correlation{}, ""},
	}
	for _, tc := range cases {
		if got := correlateText(tc.in); got != tc.want {
			t.Errorf("%s: correlateText = %q, want %q", tc.name, got, tc.want)
		}
	}
}
