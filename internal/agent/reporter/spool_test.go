package reporter

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

func newSpool(t *testing.T, name string, maxBytes int64) (*Spool, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	s, err := OpenSpool(path, maxBytes)
	if err != nil {
		t.Fatalf("打开缓冲失败: %v", err)
	}
	return s, path
}

// batch 造一批具名指标：名字用来断言"这一批有没有被补传上去"。
func batch(name string, n int) []model.Metric {
	out := make([]model.Metric, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, model.Metric{Node: "web-01", Name: name, Value: float64(i), Timestamp: int64(i)})
	}
	return out
}

func names(ms []model.Metric) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		out[m.Name]++
	}
	return out
}

// 基本往返：落盘 → 取出（此时还没删）→ 确认 → 不再取出。
func TestSpool_AppendDrainAck(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("cpu_usage", 3))
	if s.Depth() == 0 {
		t.Fatal("落盘后深度应大于 0")
	}

	got, consumed := s.Drain(maxSpoolDrainBytes)
	if len(got) != 3 || consumed == 0 {
		t.Fatalf("应取回 3 个点，实际 %d（consumed=%d）", len(got), consumed)
	}
	// 取出不等于删除：没确认之前必须还在，否则发送失败就丢了
	if again, _ := s.Drain(maxSpoolDrainBytes); len(again) != 3 {
		t.Fatalf("未确认前应能重复取出，实际 %d", len(again))
	}

	s.Ack(consumed)
	if rest, _ := s.Drain(maxSpoolDrainBytes); len(rest) != 0 {
		t.Fatalf("确认后不应还有数据，实际 %d", len(rest))
	}
	if s.Depth() != 0 {
		t.Fatalf("确认后深度应为 0，实际 %d", s.Depth())
	}
}

// 核心用例：连续失败不丢数据。三轮上报里前两轮失败、第三轮成功，
// 成功的 payload 必须覆盖全部三轮的指标（可以重复，但绝不能少）。
func TestSpool_SendLosesNothingAcrossFailures(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 1<<20)
	fail := true

	var sent []model.Metric
	send := func(p model.ReportPayload) (ReportResponse, error) {
		if fail {
			return ReportResponse{}, fmt.Errorf("server unreachable")
		}
		sent = append(sent, p.Metrics...)
		return ReportResponse{Status: "ok"}, nil
	}

	rounds := []string{"cpu_usage", "mem_used_percent", "disk_used_percent"}
	for i, name := range rounds {
		if i == len(rounds)-1 {
			fail = false // 第三轮恢复
		}
		if _, err := s.Send(model.ReportPayload{Node: "web-01", Metrics: batch(name, 2)}, send); err != nil && !fail {
			t.Fatalf("第 %d 轮不应失败: %v", i+1, err)
		}
	}

	got := names(sent)
	for _, name := range rounds {
		if got[name] != 2 {
			t.Fatalf("恢复后的上报必须覆盖 %s 的 2 个点，实际 %d（整批 %v）", name, got[name], got)
		}
	}
	// 全部确认后缓冲必须清空，否则会一直是"欠着数据"的状态
	if s.Depth() != 0 {
		t.Fatalf("恢复并确认后缓冲应清空，实际剩 %d 字节", s.Depth())
	}
}

// 失败时本轮指标必须落盘；且已经取出但未确认的那段不能被重复落盘。
func TestSpool_SendOnFailureSpoolsOnlyLive(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("old", 1)) // 上一轮遗留

	calls := 0
	send := func(model.ReportPayload) (ReportResponse, error) {
		calls++
		return ReportResponse{}, fmt.Errorf("boom")
	}
	_, _ = s.Send(model.ReportPayload{Node: "web-01", Metrics: batch("live", 1)}, send)

	got, consumed := s.Drain(maxSpoolDrainBytes)
	n := names(got)
	if n["old"] != 1 || n["live"] != 1 {
		t.Fatalf("缓冲里应各有一份 old 与 live，实际 %v", n)
	}
	if consumed == 0 {
		t.Fatal("consumed 不应为 0")
	}
}

// 重启不丢：关掉（丢弃对象）再打开，缓冲里的数据还在。
func TestSpool_SurvivesReopen(t *testing.T) {
	s, path := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("cpu_usage", 2))
	s.Append(batch("mem_used_percent", 2))

	again, err := OpenSpool(path, 1<<20)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	got, _ := again.Drain(maxSpoolDrainBytes)
	n := names(got)
	if n["cpu_usage"] != 2 || n["mem_used_percent"] != 2 {
		t.Fatalf("重启后应能取回全部数据，实际 %v", n)
	}
}

// 已确认但还没被物理删掉的前缀，在重启时被真正删掉（否则文件会随时间无限变长）。
//
// 注意 Ack 在"已确认过半"时会**当场**整理，所以这条用例要走"前缀未过半"的场景，
// 否则测的其实是 Ack 的整理而不是重启的整理。
func TestSpool_CompactOnReopen(t *testing.T) {
	s, path := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("a", 1))
	unit := s.Depth()
	s.Append(batch("b", 1))
	s.Append(batch("c", 1))

	got, consumed := s.Drain(int(unit)) // 只取走第一批
	if len(got) != 1 || names(got)["a"] != 1 {
		t.Fatalf("应只取回第一批，实际 %v", names(got))
	}
	s.Ack(consumed)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if before.Size() <= unit {
		t.Fatalf("前缀未过半时文件里应还留着已确认的数据（实测 %d，一批 %d）", before.Size(), unit)
	}

	again, err := OpenSpool(path, 1<<20)
	if err != nil {
		t.Fatalf("重新打开失败: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("重启整理后文件应更小：前 %d → 后 %d", before.Size(), after.Size())
	}
	n := names(mustDrain(t, again))
	if n["a"] != 0 || n["b"] != 1 || n["c"] != 1 {
		t.Fatalf("整理后应只保留未确认的数据（b、c），实际 %v", n)
	}
}

func mustDrain(t *testing.T, s *Spool) []model.Metric {
	t.Helper()
	got, _ := s.Drain(maxSpoolDrainBytes)
	return got
}

// 超过上限时丢最老的一批，保留最近的。
func TestSpool_TrimDropsOldest(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("unit", 1))
	unit := s.Depth() // 一批的字节数

	s2, _ := newSpool(t, "spool2.jsonl", unit+unit/2) // 1.5 批的上限
	for _, name := range []string{"a", "b", "c"} {
		s2.Append(batch(name, 1))
	}
	if d := s2.Depth(); d > unit+unit/2 {
		t.Fatalf("深度不应超过上限：%d > %d", d, unit+unit/2)
	}
	got, _ := s2.Drain(maxSpoolDrainBytes)
	n := names(got)
	if n["a"] != 0 {
		t.Fatalf("最老的一批应被丢弃，实际 %v", n)
	}
	if n["c"] != 1 {
		t.Fatalf("最新的一批必须保留，实际 %v", n)
	}
}

// 长时间断网（只追加、从不确认）时，**数据文件本身**也必须被限制住。
//
// 这条是真机实测出来的：只推进偏移等于"逻辑丢弃"，文件仍在变长——
// 断网 7 小时后长到 113 MB，而"未确认数据"一直稳在上限内。
// 机器磁盘小的场景下这会把盘写满，所以文件大小必须有自己的上界。
func TestSpool_FileSizeStaysBoundedDuringLongOutage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spool.jsonl")
	const limit = 64 << 10
	s, err := OpenSpool(path, limit)
	if err != nil {
		t.Fatalf("打开缓冲失败: %v", err)
	}

	// 累计追加约 5 倍上限的量，期间从不 Ack（模拟 Server 一直不可达）
	for i := 0; i < 50; i++ {
		s.Append(batch(fmt.Sprintf("m%d", i), 100))
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if fi.Size() > 3*limit {
		t.Fatalf("数据文件不应随断网时长无限增长：实际 %d 字节，上限 %d", fi.Size(), limit)
	}
	if d := s.Depth(); d > limit {
		t.Fatalf("未确认数据不应超过上限：%d > %d", d, limit)
	}
	// 整理之后最新的一批必须还在（丢的是最老的）
	n := names(mustDrain(t, s))
	if n["m49"] != 100 {
		t.Fatalf("最新一批必须保留，实际 %v", n)
	}
}

// 单批就超过上限：丢弃本批（留着也发不出去），且不影响后续追加。
func TestSpool_OversizeBatchIsDropped(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 200)
	s.Append(batch("huge", 50)) // 远超 200 字节
	if s.Depth() != 0 {
		t.Fatalf("超限的单批不应落盘，实际深度 %d", s.Depth())
	}
	s.Append(batch("ok", 1))
	if s.Depth() == 0 {
		t.Fatal("后续正常批次应能落盘")
	}
}

// 坏行跳过但计入 consumed：一行坏数据不能把整条补传链路永久卡住。
func TestSpool_SkipsCorruptLine(t *testing.T) {
	s, path := newSpool(t, "spool.jsonl", 1<<20)
	s.Append(batch("good", 1))

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("打开缓冲失败: %v", err)
	}
	if _, err := f.WriteString("{这不是合法的 JSON\n"); err != nil {
		t.Fatalf("写入坏行失败: %v", err)
	}
	f.Close()

	got, consumed := s.Drain(maxSpoolDrainBytes)
	if names(got)["good"] != 1 {
		t.Fatalf("应能取回好行，实际 %v", names(got))
	}
	s.Ack(consumed)
	if rest, _ := s.Drain(maxSpoolDrainBytes); len(rest) != 0 {
		t.Fatalf("坏行被跳过并确认后不应残留，实际 %d 个点", len(rest))
	}
}

// 单轮补传有字节上限：否则一次把几 MB 塞进上报体会让每条上报都超限，
// 缓冲就永远排不空（这是最需要防住的失败模式）。
func TestSpool_DrainRespectsByteCap(t *testing.T) {
	s, _ := newSpool(t, "spool.jsonl", 1<<20)
	for i := 0; i < 5; i++ {
		s.Append(batch(fmt.Sprintf("m%d", i), 1))
	}
	unit := s.Depth() / 5
	got, _ := s.Drain(int(unit) + int(unit)/2) // 1.5 批的上限
	if len(got) > 2 {
		t.Fatalf("单轮补传应被字节上限截断，实际取回 %d 个点", len(got))
	}
	if len(got) == 0 {
		t.Fatal("字节上限至少应允许一批通过（否则大记录会永远取不出来）")
	}
}

// 关闭（maxBytes<=0 或未打开）时所有方法都是空操作，且 Send 直接透传。
func TestSpool_Disabled(t *testing.T) {
	s, err := OpenSpool(filepath.Join(t.TempDir(), "x.jsonl"), 0)
	if err != nil {
		t.Fatalf("关闭状态不应报错: %v", err)
	}
	if s.Enabled() {
		t.Fatal("maxBytes=0 应视为关闭")
	}
	s.Append(batch("a", 1))
	if got, consumed := s.Drain(maxSpoolDrainBytes); len(got) != 0 || consumed != 0 {
		t.Fatal("关闭时取出应为空")
	}
	if s.Depth() != 0 {
		t.Fatal("关闭时深度应为 0")
	}

	called := false
	if _, err := s.Send(model.ReportPayload{Metrics: batch("live", 1)}, func(p model.ReportPayload) (ReportResponse, error) {
		called = true
		return ReportResponse{Status: "ok"}, nil
	}); err != nil || !called {
		t.Fatal("关闭时 Send 应直接调用底层发送")
	}

	// nil 缓冲同样安全（例如打开失败时）
	var nilSpool *Spool
	nilSpool.Append(batch("a", 1))
	if nilSpool.Enabled() || nilSpool.Depth() != 0 {
		t.Fatal("nil 缓冲应表现为关闭")
	}
}
