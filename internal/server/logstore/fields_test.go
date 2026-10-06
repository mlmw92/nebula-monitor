package logstore

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 结构化字段与原文写在**同一条 JSON**里：不需要另建索引，检索仍是顺序读 + 有界扫描。
func TestAppendExtractsFieldsToDisk(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	if _, _, _, err := s.Append(batch("applog", "node-1",
		`{"level":"error","status":500,"http":{"method":"GET"}}`,
		`level=warn msg="disk almost full"`,
		`plain line without structure`,
	)); err != nil {
		t.Fatalf("写入失败：%v", err)
	}

	got := readAll(t, root, "applog", "node-1")
	if len(got) != 3 {
		t.Fatalf("应有 3 行，实际 %d", len(got))
	}
	if got[0].Fields["level"] != "error" || got[0].Fields["status"] != "500" || got[0].Fields["http.method"] != "GET" {
		t.Fatalf("JSON 字段未落盘：%+v", got[0].Fields)
	}
	if got[1].Fields["level"] != "warn" || got[1].Fields["msg"] != "disk almost full" {
		t.Fatalf("key=value 字段未落盘：%+v", got[1].Fields)
	}
	if len(got[2].Fields) != 0 {
		t.Fatalf("无结构行不应有字段：%+v", got[2].Fields)
	}
	// 原文必须在：字段是附加检索维度，不是替代品
	if got[0].Text == "" || got[1].Text == "" {
		t.Fatalf("原文不得丢失：%+v", got)
	}
}

// 字段过滤是**精确等值**且要求全部命中：关键词做不到这件事
// （`status=500` 用关键词会命中任何含该串的行，包括别的字段的值）。
func TestQueryFiltersByField(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	now := time.Now().UnixMilli()
	lines := []model.LogLine{
		{Ts: now, Text: `{"level":"error","status":500,"path":"/api/orders"}`},
		{Ts: now, Text: `{"level":"error","status":404,"path":"/api/orders"}`},
		{Ts: now, Text: `{"level":"info","status":500,"path":"/api/users"}`},
		{Ts: now, Text: `status=500 in plain text`},
	}
	if _, _, _, err := s.Append(model.LogBatch{Source: "applog", Node: "node-1", Lines: lines}); err != nil {
		t.Fatal(err)
	}
	q := model.LogQuery{From: now - int64(time.Hour/time.Millisecond), To: now + int64(time.Hour/time.Millisecond), Limit: 50}

	query := func(fields map[string]string) []model.LogHit {
		t.Helper()
		qq := q
		qq.Fields = fields
		res, err := s.Query(qq, Cursor{})
		if err != nil {
			t.Fatalf("查询失败：%v", err)
		}
		return res.Lines
	}

	if got := query(nil); len(got) != 4 {
		t.Fatalf("不带字段过滤应返回全部 4 行，实际 %d", len(got))
	}
	if got := query(map[string]string{"status": "500"}); len(got) != 3 {
		t.Fatalf("status=500 应命中 3 行（含纯文本里解析出的那条），实际 %d", len(got))
	}
	// 多个条件是「与」：level=error 且 status=500 只应命中第 1 行
	if got := query(map[string]string{"level": "error", "status": "500"}); len(got) != 1 {
		t.Fatalf("多条件应为与关系，实际 %d 行", len(got))
	}
	// 精确匹配而不是前缀/子串：500 不该命中 5000
	if got := query(map[string]string{"status": "50"}); len(got) != 0 {
		t.Fatalf("字段是精确等值匹配，实际命中 %d 行", len(got))
	}
	// 不存在的字段 → 空结果（而不是退化成不过滤）
	if got := query(map[string]string{"nosuchfield": "x"}); len(got) != 0 {
		t.Fatalf("不存在的字段应返回空结果，实际 %d 行", len(got))
	}
}

// 字段目录（界面筛选候选）要跨重启保留，且每来源的名字基数有上限：
// 上限只在单次进程内成立等于没有上限——重启一次就能再涨一轮。
func TestFieldCatalogPersistsAndCapsCardinality(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)

	// 造 MaxFieldNamesPerSource + 10 个不同名字。
	// 必须**分散到多行**：单行字段数本身有上限（logparse.MaxFieldsPerLine），
	// 一行塞不下这么多名字——那正是两层上限各管一段的体现。
	texts := make([]string, 0, MaxFieldNamesPerSource+10)
	for i := 0; i < MaxFieldNamesPerSource+10; i++ {
		texts = append(texts, fmt.Sprintf(`{"f%03d":"v"}`, i))
	}
	if _, _, _, err := s.Append(batch("applog", "node-1", texts...)); err != nil {
		t.Fatal(err)
	}

	names := s.FieldNames("applog")
	if len(names) != MaxFieldNamesPerSource {
		t.Fatalf("字段名应封顶 %d，实际 %d", MaxFieldNamesPerSource, len(names))
	}
	if names[0] > names[len(names)-1] {
		t.Fatalf("字段名应有序：%v", names)
	}

	// 重启（同一 root 新建 Store）：目录仍在，上限也仍在
	reopened := New(root, 0)
	if got := reopened.FieldNames("applog"); len(got) != MaxFieldNamesPerSource {
		t.Fatalf("重启后字段目录应保留，实际 %d 个", len(got))
	}
	// 再写一行：**已见过**的名字照常提取，**超限的新名字**被忽略（原文照常落盘）
	if _, _, _, err := reopened.Append(batch("applog", "node-1", `{"f000":"v2","f999":"v"}`)); err != nil {
		t.Fatal(err)
	}
	after := reopened.FieldNames("applog")
	if len(after) != MaxFieldNamesPerSource {
		t.Fatalf("超限的新名字不应进入目录，实际 %d 个", len(after))
	}
	for _, n := range after {
		if n == "f999" {
			t.Fatal("超限的新名字不应进入目录")
		}
	}
	lines := readAll(t, root, "applog", "node-1")
	last := lines[len(lines)-1]
	if last.Fields["f000"] != "v2" {
		t.Fatalf("已见过的名字应照常提取：%+v", last.Fields)
	}
	if _, ok := last.Fields["f999"]; ok {
		t.Fatalf("超限的新名字不应被提取：%+v", last.Fields)
	}
	if last.Text == "" {
		t.Fatal("原文必须保留（字段被限流不等于日志被丢弃）")
	}
}

// 字段目录落盘不应影响分片扫描：它是 root 下的普通文件，不是来源目录。
func TestFieldCatalogFileIsNotTreatedAsSource(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	if _, _, _, err := s.Append(batch("applog", "node-1", `{"level":"info"}`)); err != nil {
		t.Fatal(err)
	}
	for _, src := range s.Sources() {
		if src == strings.TrimSuffix(fieldCatalogFile, ".json") || strings.Contains(src, "field_names") {
			t.Fatalf("字段目录文件不应被当成来源：%v", s.Sources())
		}
	}
	if len(s.Sources()) != 1 || s.Sources()[0] != "applog" {
		t.Fatalf("来源列表不符：%v", s.Sources())
	}
}
