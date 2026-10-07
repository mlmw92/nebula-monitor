package logstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 能力探测的用例（批次 23）。
//
// 两条最要紧：① 本地后端"空目录"是**正常状态**（0 条且不报错）；
// ② 外部后端"探测失败"必须如实回报——退化成 0 会被读成"日志丢了"，那正是这个端点要避免的误导。

func writeShard(t *testing.T, dir, source, day, node string, size int) {
	t.Helper()
	p := filepath.Join(dir, source, day)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatalf("建分片目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p, node+".log"), []byte(strings.Repeat("x", size)), 0o644); err != nil {
		t.Fatalf("写分片失败: %v", err)
	}
}

func TestLocalCapabilityProbesStorage(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, 0)
	if store == nil {
		t.Fatal("本地后端未创建")
	}

	// ① 还没写过日志：0 条、不报错
	cap := store.Capability()
	if cap.Backend != BackendLocal {
		t.Fatalf("backend = %q", cap.Backend)
	}
	if cap.FullTextIndex || cap.FieldIndex {
		t.Fatal("本地后端不该声称有索引——它确实没有（这是刻意不做，不是遗漏）")
	}
	if cap.ScanBudget == nil || cap.ScanBudget.Bytes != DefaultScanBudgetBytes ||
		cap.ScanBudget.Lines != DefaultScanBudgetLines {
		t.Fatalf("扫描预算不符：%#v", cap.ScanBudget)
	}
	if cap.Storage.Sources != 0 || cap.Storage.Err != "" {
		t.Fatalf("空目录应报 0 条且不报错，实际 %#v", cap.Storage)
	}
	if len(cap.Notes) == 0 {
		t.Fatal("应带上给运维看的说明（能力边界）")
	}

	// ② 造分片（含一个非日期目录名：不该污染"能查到哪一天"）
	writeShard(t, dir, "nginx", "2026-10-05", "web-01", 100)
	writeShard(t, dir, "nginx", "2026-10-07", "web-02", 50)
	writeShard(t, dir, "app", "2026-10-06", "web-01", 25)
	writeShard(t, dir, "app", "not-a-date", "web-01", 999)

	cap = store.Capability()
	st := cap.Storage
	if st.Sources != 2 {
		t.Fatalf("来源数 = %d，期望 2", st.Sources)
	}
	if st.Nodes != 2 {
		t.Fatalf("节点数 = %d，期望 2（web-01 / web-02）", st.Nodes)
	}
	if st.OldestDay != "2026-10-05" || st.NewestDay != "2026-10-07" {
		t.Fatalf("时间跨度不符：%s ~ %s", st.OldestDay, st.NewestDay)
	}
	if st.Bytes != 175 {
		t.Fatalf("字节数 = %d，期望 175（100+50+25，非日期目录不计）", st.Bytes)
	}
	if st.Truncated || st.Err != "" {
		t.Fatalf("不该截断或报错：%#v", st)
	}
}

func TestLocalProbeTruncatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, 0)
	for i := 0; i < 5; i++ {
		writeShard(t, dir, "nginx", "2026-10-05", "web-0"+string(rune('1'+i)), 10)
	}
	st := store.probeStorage(2)
	if !st.Truncated {
		t.Fatal("触到列举上限必须标记截断（报的是「至少这么多」，不是精确值）")
	}
	if st.Nodes > 2 {
		t.Fatalf("截断时不该继续累计，实际节点 %d", st.Nodes)
	}
}

func TestVLBackendCapabilityProbesReachability(t *testing.T) {
	f := newFakeVictoriaLogs(t)
	v := f.adapter(t)

	cap := v.Capability()
	if cap.Backend != BackendVictoriaLogs {
		t.Fatalf("backend = %q", cap.Backend)
	}
	if !cap.FullTextIndex || !cap.FieldIndex {
		t.Fatal("外部后端的检索由它自己的倒排索引完成，应声称具备索引能力")
	}
	if cap.ScanBudget != nil {
		t.Fatal("外部后端没有「逐文件扫描」这回事：预算必须是 null，而不是 0")
	}
	if cap.Storage.Err != "" {
		t.Fatalf("可达时不该报探测失败：%q", cap.Storage.Err)
	}
	if f.lastForm() == nil {
		t.Fatal("探测应该真的访问了后端（而不是只看配置值）")
	}

	// 后端 500 → 探测失败必须如实回报
	f.failNext(10)
	cap = v.Capability()
	if !strings.Contains(cap.Storage.Err, "探测外部后端失败") {
		t.Fatalf("后端不可达时必须带上探测失败原因，实际 %q", cap.Storage.Err)
	}
	if !cap.FullTextIndex {
		t.Fatal("探测失败不该把能力也一起否认掉：能力是后端的属性，与当下可达性是两件事")
	}
}

// 能力探测是只读的：它不该往后端写任何东西。
func TestCapabilityProbeDoesNotWrite(t *testing.T) {
	f := newFakeVictoriaLogs(t)
	v := f.adapter(t)
	before := len(f.entriesSnapshot())
	_ = v.Capability()
	if after := len(f.entriesSnapshot()); after != before {
		t.Fatalf("能力探测不该写入：%d -> %d", before, after)
	}
}
