package logstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 检索的要点都在这组用例里：
//  - 顺序（时间倒序，跨天/跨文件都要对）；
//  - 过滤（关键词 / 正则 / 节点 / 来源）；
//  - **翻页不重不漏**——这是游标设计成「绝对字节偏移」而不是「距末尾字节数」的原因；
//  - 有界（命中上限与扫描预算都要 truncated + 可续读）；
//  - 坏输入不致命（缺目录、坏行、坏游标）。

func shard(t *testing.T, root, source, date, node string, entries ...model.LogHit) {
	t.Helper()
	dir := filepath.Join(root, source, date)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, e := range entries {
		e.Source, e.Node = source, node
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, node+".log"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendShard(t *testing.T, root, source, date, node string, entries ...model.LogHit) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(root, source, date, node+".log"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range entries {
		e.Source, e.Node = source, node
		data, _ := json.Marshal(e)
		_, _ = f.Write(append(data, '\n'))
	}
}

func dayTS(t *testing.T, date string, hour, min int) int64 {
	t.Helper()
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	return d.Add(time.Duration(hour)*time.Hour + time.Duration(min)*time.Minute).UnixMilli()
}

func textsOf(lines []model.LogHit) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// TestQuery_TimeDescAcrossFilesAndDays 跨天、跨节点文件的整体顺序必须是时间倒序。
func TestQuery_TimeDescAcrossFilesAndDays(t *testing.T) {
	root := t.TempDir()
	shard(t, root, "applog", "2026-09-24", "n1",
		model.LogHit{Ts: dayTS(t, "2026-09-24", 1, 0), Text: "old-1"},
		model.LogHit{Ts: dayTS(t, "2026-09-24", 2, 0), Text: "old-2"})
	shard(t, root, "applog", "2026-09-25", "n1",
		model.LogHit{Ts: dayTS(t, "2026-09-25", 10, 0), Text: "mid-1"})
	shard(t, root, "applog", "2026-09-25", "n2",
		model.LogHit{Ts: dayTS(t, "2026-09-25", 11, 0), Text: "mid-2"})
	shard(t, root, "applog", "2026-09-26", "n1",
		model.LogHit{Ts: dayTS(t, "2026-09-26", 9, 0), Text: "new-1"},
		model.LogHit{Ts: dayTS(t, "2026-09-26", 9, 5), Text: "new-2"})

	s := New(root, 0)
	res, err := s.Query(model.LogQuery{
		From: dayTS(t, "2026-09-24", 0, 0), To: dayTS(t, "2026-09-27", 0, 0), Limit: 100,
	}, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"new-2", "new-1", "mid-2", "mid-1", "old-2", "old-1"}
	if got := textsOf(res.Lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("顺序不符：\n got %v\nwant %v", got, want)
	}
	if res.Truncated {
		t.Fatalf("未达上限不应标记截断：%+v", res)
	}
	if res.Files != 4 {
		t.Fatalf("应扫描 4 个文件，got %d", res.Files)
	}
}

// TestQuery_Filters 关键词 / 正则 / 节点 / 来源 / 时间范围逐项生效。
func TestQuery_Filters(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	shard(t, root, "applog", "2026-09-26", "n1",
		model.LogHit{Ts: base, Text: "error: disk full"},
		model.LogHit{Ts: base + 1000, Text: "INFO ok"})
	shard(t, root, "syslog", "2026-09-26", "n1",
		model.LogHit{Ts: base + 2000, Text: "error: oom"})
	shard(t, root, "applog", "2026-09-26", "n2",
		model.LogHit{Ts: base + 3000, Text: "error: dns"})

	s := New(root, 0)
	q := model.LogQuery{From: base - 1000, To: base + 10000, Limit: 100}

	// 关键词
	q.Keyword = "error"
	res, _ := s.Query(q, Cursor{})
	if len(res.Lines) != 3 {
		t.Fatalf("关键词应命中 3 条，got %v", textsOf(res.Lines))
	}
	// 正则（优先于关键词）
	q.Regex = `error: (disk|oom)`
	res, _ = s.Query(q, Cursor{})
	if len(res.Lines) != 2 {
		t.Fatalf("正则应命中 2 条，got %v", textsOf(res.Lines))
	}
	// 来源过滤
	q.Regex, q.Sources = "", []string{"syslog"}
	res, _ = s.Query(q, Cursor{})
	if len(res.Lines) != 1 || res.Lines[0].Source != "syslog" {
		t.Fatalf("来源过滤失效：%v", res.Lines)
	}
	// 节点过滤
	q.Sources, q.Nodes = nil, []string{"n2"}
	res, _ = s.Query(q, Cursor{})
	if len(res.Lines) != 1 || res.Lines[0].Node != "n2" {
		t.Fatalf("节点过滤失效：%v", res.Lines)
	}
	// 时间范围（窄到只含第一天的一条）
	q.Nodes = nil
	q.From, q.To = base+2500, base+3000
	res, _ = s.Query(q, Cursor{})
	if len(res.Lines) != 1 || res.Lines[0].Text != "error: dns" {
		t.Fatalf("时间过滤失效：%v", res.Lines)
	}
}

// TestQuery_CursorPagingNoLossNoDup 翻页必须不重不漏（游标续读的核心保证）。
func TestQuery_CursorPagingNoLossNoDup(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	var entries []model.LogHit
	for i := 0; i < 10; i++ {
		entries = append(entries, model.LogHit{Ts: base + int64(i)*1000, Text: "line-" + string(rune('a'+i))})
	}
	shard(t, root, "applog", "2026-09-26", "n1", entries...)

	s := New(root, 0)
	q := model.LogQuery{From: base - 1000, To: base + 100000, Limit: 3}
	var all []string
	cursor := Cursor{}
	for pages := 0; ; pages++ {
		res, err := s.Query(q, cursor)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, textsOf(res.Lines)...)
		if !res.Truncated {
			break
		}
		if res.Cursor == "" {
			t.Fatal("标记截断时必须给出游标，否则前端无法继续翻页")
		}
		cursor, err = DecodeCursor(res.Cursor)
		if err != nil {
			t.Fatalf("游标应可解码：%v", err)
		}
		if pages > 6 {
			t.Fatal("翻页未收敛")
		}
	}
	want := []string{"line-j", "line-i", "line-h", "line-g", "line-f", "line-e", "line-d", "line-c", "line-b", "line-a"}
	if strings.Join(all, ",") != strings.Join(want, ",") {
		t.Fatalf("翻页结果不符：\n got %v\nwant %v", all, want)
	}
}

// TestQuery_CursorExactlyAtFileStartIsExhausted 单页刚好等于文件内容时的边界：
// 续读**不能**把同一批结果再返回一次。
//
// 这条守的是一个真实踩过的坑：游标停在文件头时（offset=0），而 0 在反向读取里表示
// 「从文件末尾开始」——两种含义混用会让「加载更多」把同一页再吐一遍。
func TestQuery_CursorExactlyAtFileStartIsExhausted(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	shard(t, root, "applog", "2026-09-26", "n1",
		model.LogHit{Ts: base, Text: "line-a"},
		model.LogHit{Ts: base + 1000, Text: "line-b"})

	s := New(root, 0)
	q := model.LogQuery{From: base - 1000, To: base + 100000, Limit: 2} // 恰好等于文件内容
	p1, err := s.Query(q, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Lines) != 2 || !p1.Truncated || p1.Cursor == "" {
		t.Fatalf("首页应满 2 条并给出游标：%+v", p1)
	}
	cursor, err := DecodeCursor(p1.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.Query(q, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Lines) != 0 {
		t.Fatalf("续读不应重复返回已翻过的那一页，got %v", textsOf(p2.Lines))
	}
	if p2.Truncated {
		t.Fatal("内容已全部翻完，不应再标记截断")
	}
	if p2.Cursor != "" {
		t.Fatalf("不该再给游标：%q", p2.Cursor)
	}
}

// TestQuery_CursorSurvivesFileGrowth 翻页途中文件追加新行，续读**不能跳过还没看过的老行**。
//
// 这正是游标用「绝对字节偏移」而不是「距末尾多少字节」的原因：后者在文件长大时会整体前移，
// 把尚未扫描的老行静默跳过。
func TestQuery_CursorSurvivesFileGrowth(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	var entries []model.LogHit
	for i := 0; i < 6; i++ {
		entries = append(entries, model.LogHit{Ts: base + int64(i)*1000, Text: "line-" + string(rune('a'+i))})
	}
	shard(t, root, "applog", "2026-09-26", "n1", entries...)

	s := New(root, 0)
	q := model.LogQuery{From: base - 1000, To: base + 100000, Limit: 2}
	first, err := s.Query(q, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(first.Lines); strings.Join(got, ",") != "line-f,line-e" {
		t.Fatalf("首页不符：%v", got)
	}
	// 期间来了新日志（追加写）
	appendShard(t, root, "applog", "2026-09-26", "n1", model.LogHit{Ts: base + 9000, Text: "line-new"})

	cursor, err := DecodeCursor(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Query(q, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got := textsOf(second.Lines); strings.Join(got, ",") != "line-d,line-c" {
		t.Fatalf("续读跳行或重读了：got %v（应接着 line-e 往前）", got)
	}
}

// TestQuery_ScanBudgetStopsAndOffersCursor 扫描预算用尽也要 truncated + 可续读，
// 否则「查不到」会变成「把 Server 拖住」且用户完全不知道发生了什么。
//
// 场景刻意构造成「针在文件最老的一端、近端全是噪声」：反向扫描先消耗近端，
// 预算在中途耗尽可能——这也是**按行**检查预算（而不是读完整个文件才检查）的原因。
func TestQuery_ScanBudgetStopsAndOffersCursor(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	entries := []model.LogHit{{Ts: base, Text: "error: needle"}} // 最老的一端
	for i := 0; i < 50; i++ {
		entries = append(entries, model.LogHit{Ts: base + int64(i+1)*1000, Text: "noise-" + strings.Repeat("x", 100)})
	}
	shard(t, root, "applog", "2026-09-26", "n1", entries...)

	s := New(root, 0)
	s.SetScanBudget(2000, 0) // 只允许扫 2000 字节：远小于整份文件
	q := model.LogQuery{From: base - 1000, To: base + 200000, Limit: 100, Keyword: "needle"}
	res, err := s.Query(q, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 0 {
		t.Fatalf("预算内不该扫到那根针，got %v", textsOf(res.Lines))
	}
	if !res.Truncated || res.Cursor == "" {
		t.Fatal("预算用尽必须标记截断并给出游标")
	}
	if res.ScannedBytes == 0 || res.ScannedLines == 0 {
		t.Fatalf("诊断字段应给出实际扫描量：%+v", res)
	}
	if res.Files != 1 {
		t.Fatalf("预算应在同一个文件内就用尽（这正是按行检查的意义），got files=%d", res.Files)
	}
	cursor, _ := DecodeCursor(res.Cursor)
	if cursor.Offset <= 0 {
		t.Fatalf("游标应指向文件中部（已扫过的近端之后），got %+v", cursor)
	}

	// 放宽预算后从游标续读，应能找到那根针
	s.SetScanBudget(1<<20, 0)
	next, err := s.Query(q, cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Lines) != 1 || next.Lines[0].Text != "error: needle" {
		t.Fatalf("续读应命中 needle，got %v", textsOf(next.Lines))
	}
}

// TestQuery_LimitIsClampedAndBadInputIsHarmless 上限被夹紧；缺目录、坏行、坏游标都不致命。
func TestQuery_LimitIsClampedAndBadInputIsHarmless(t *testing.T) {
	root := t.TempDir()
	base := dayTS(t, "2026-09-26", 10, 0)
	shard(t, root, "applog", "2026-09-26", "n1", model.LogHit{Ts: base, Text: "ok"})
	// 混入一条坏行
	f, err := os.OpenFile(filepath.Join(root, "applog", "2026-09-26", "n1.log"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("this is not json\n")
	_ = f.Close()

	s := New(root, 0)
	res, err := s.Query(model.LogQuery{From: base - 1000, To: base + 1000, Limit: MaxLimit + 1000}, Cursor{})
	if err != nil {
		t.Fatalf("坏行不应让查询失败：%v", err)
	}
	if len(res.Lines) != 1 || res.Lines[0].Text != "ok" {
		t.Fatalf("坏行应被跳过、好行应返回，got %v", textsOf(res.Lines))
	}

	// 根目录还不存在 → 空结果而非错误（首次部署的常见情形）
	empty := New(filepath.Join(root, "not-created"), 0)
	if res, err := empty.Query(model.LogQuery{From: base - 1000, To: base + 1000}, Cursor{}); err != nil || len(res.Lines) != 0 {
		t.Fatalf("缺目录应返回空结果，got %+v err=%v", res, err)
	}
	// 正则非法 → 明确报错（而不是悄悄当成没写正则，返回一堆无关结果）
	if _, err := s.Query(model.LogQuery{From: base - 1000, To: base + 1000, Regex: "(["}, Cursor{}); err == nil {
		t.Fatal("非法正则应报错")
	}
}

// TestCursor_RoundTripAndRejectsGarbage 游标是不透明字符串，但解码必须拒绝垃圾输入。
func TestCursor_RoundTripAndRejectsGarbage(t *testing.T) {
	c := Cursor{File: "applog/2026-09-26/n1.log", Offset: 123}
	got, err := DecodeCursor(EncodeCursor(c))
	if err != nil || got != c {
		t.Fatalf("游标往返失败：%+v err=%v", got, err)
	}
	if EncodeCursor(Cursor{}) != "" {
		t.Fatal("空游标应编码为空串（表示从头开始）")
	}
	if got, err := DecodeCursor(""); err != nil || got != (Cursor{}) {
		t.Fatalf("空串应解码为零值游标：%+v err=%v", got, err)
	}
	for _, bad := range []string{"!!!", "bm90LWpzb24"} {
		if _, err := DecodeCursor(bad); err == nil {
			t.Fatalf("垃圾游标 %q 应被拒绝", bad)
		}
	}
}
