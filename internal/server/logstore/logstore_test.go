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

// 落盘侧要点：分片布局可预期、超长行截断、时间戳校正、**每日上限丢弃必须可见（带原因）**、
// 节点名不能带出路径分隔符。

func batch(source, node string, texts ...string) model.LogBatch {
	lines := make([]model.LogLine, 0, len(texts))
	for _, s := range texts {
		lines = append(lines, model.LogLine{Ts: time.Now().UnixMilli(), Text: s})
	}
	return model.LogBatch{Source: source, Node: node, Lines: lines}
}

func readAll(t *testing.T, root, source, node string) []model.LogHit {
	t.Helper()
	path := filepath.Join(root, source, time.Now().Format("2006-01-02"), node+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取落盘文件失败：%v", err)
	}
	var out []model.LogHit
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r model.LogHit
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("落盘行不是合法 JSON：%v（%q）", err, line)
		}
		out = append(out, r)
	}
	return out
}

func TestStore_AppendWritesShardedFiles(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	if s == nil {
		t.Fatal("root 非空时应创建存储器")
	}
	accepted, dropped, reason, err := s.Append(batch("applog", "node-1", "error: a", "error: b"))
	if err != nil || accepted != 2 || dropped != 0 || reason != "" {
		t.Fatalf("写入结果异常：accepted=%d dropped=%d reason=%q err=%v", accepted, dropped, reason, err)
	}
	got := readAll(t, root, "applog", "node-1")
	if len(got) != 2 || got[0].Text != "error: a" {
		t.Fatalf("落盘内容不符：%+v", got)
	}
	if got[0].Node != "node-1" || got[0].Source != "applog" {
		t.Fatalf("记录应自带 node/source（检索不依赖目录名）：%+v", got[0])
	}
	// 二次追加是追加而不是覆盖
	if _, _, _, err := s.Append(batch("applog", "node-1", "error: c")); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, root, "applog", "node-1"); len(got) != 3 {
		t.Fatalf("应追加到同一文件，got %d 行", len(got))
	}
}

// TestStore_DailyCapDropsWithReason 每日上限：超出即丢弃，并且**必须带原因**——
// 丢弃不可见就等于静默丢数据。
func TestStore_DailyCapDropsWithReason(t *testing.T) {
	root := t.TempDir()
	s := New(root, 1) // 上限 1 字节：第一行写进去，之后都超限
	accepted, dropped, reason, err := s.Append(batch("applog", "node-1", "a", "b", "c"))
	if err != nil {
		t.Fatalf("限额丢弃不是错误：%v", err)
	}
	if accepted != 1 || dropped != 2 || reason != "dailyCap" {
		t.Fatalf("应接受 1 条、丢弃 2 条并给出原因，got accepted=%d dropped=%d reason=%q", accepted, dropped, reason)
	}
	// 后续批次继续被丢弃（配额是按天的，不会因为换批次重置）
	_, dropped2, reason2, _ := s.Append(batch("applog", "node-1", "d"))
	if dropped2 != 1 || reason2 != "dailyCap" {
		t.Fatalf("当日配额用尽后应持续丢弃，got dropped=%d reason=%q", dropped2, reason2)
	}
}

// TestStore_RebuildsQuotaFromDisk 重启后从磁盘重建当日配额：
// 否则「重启一次就重新拥有全天配额」，上限形同虚设。
func TestStore_RebuildsQuotaFromDisk(t *testing.T) {
	root := t.TempDir()
	if _, _, _, err := New(root, 0).Append(batch("applog", "node-1", strings.Repeat("x", 100))); err != nil {
		t.Fatal(err)
	}
	// 新实例（模拟重启），上限设得比已写入的小 → 第一批就应被丢弃
	_, dropped, reason, _ := New(root, 50).Append(batch("applog", "node-1", "y"))
	if dropped != 1 || reason != "dailyCap" {
		t.Fatalf("重启后应按磁盘已有大小继续限额，got dropped=%d reason=%q", dropped, reason)
	}
}

func TestStore_RejectsInvalidInput(t *testing.T) {
	s := New(t.TempDir(), 0)
	if _, _, _, err := s.Append(batch("Bad-Name", "node-1", "x")); err == nil {
		t.Fatal("非法来源名应被拒绝")
	}
	if _, _, _, err := s.Append(batch("applog", "  ", "x")); err == nil {
		t.Fatal("空节点名应被拒绝")
	}
	if _, _, _, err := s.Append(model.LogBatch{Source: "applog", Node: "n"}); err != nil {
		t.Fatalf("空批次应是无害的空操作，got %v", err)
	}
	if New("", 0) != nil {
		t.Fatal("root 为空应返回 nil（调用方据此关闭该能力）")
	}
}

// TestStore_SanitizesNodeName 节点名会成为文件名：必须净化，不能带出路径分隔符。
func TestStore_SanitizesNodeName(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	if _, _, _, err := s.Append(batch("applog", "../../etc/passwd", "x")); err != nil {
		t.Fatal(err)
	}
	// 落点必须仍在存储根目录内
	var found string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			found = p
		}
		return nil
	})
	if found == "" || !strings.HasPrefix(found, root) {
		t.Fatalf("落盘位置越出根目录：%q", found)
	}
	// 关键断言：落点必须正好是「来源/日期」目录，而不是被 .. 带出去的别处。
	// 名字里残留的 .. 无害（它已被降级为普通字符，不含路径分隔符）。
	wantDir := filepath.Join(root, "applog", time.Now().Format("2006-01-02"))
	if got := filepath.Dir(found); got != wantDir {
		t.Fatalf("落点应在预期目录内：got %q want %q", got, wantDir)
	}
	if strings.ContainsAny(filepath.Base(found), `/\`) {
		t.Fatalf("文件名不应含路径分隔符：%q", filepath.Base(found))
	}
}

// TestStore_TruncatesLongLineAndClampsTS 超长行截断（留标记）、异常时间戳校正为当前时间。
func TestStore_TruncatesLongLineAndClampsTS(t *testing.T) {
	root := t.TempDir()
	s := New(root, 0)
	long := strings.Repeat("a", MaxLineBytes+100)
	b := model.LogBatch{Source: "applog", Node: "n1", Lines: []model.LogLine{
		{Ts: 0, Text: long}, // 解析不到时间
		{Ts: time.Now().Add(72 * time.Hour).UnixMilli(), Text: "future"}, // 明显超前
	}}
	if _, _, _, err := s.Append(b); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, root, "applog", "n1")
	if !strings.HasSuffix(got[0].Text, "[truncated]") {
		t.Fatalf("超长行应被截断并留标记：%q", got[0].Text[:min(40, len(got[0].Text))])
	}
	now := time.Now().UnixMilli()
	for _, r := range got {
		if r.Ts > now+int64(time.Hour/time.Millisecond) || r.Ts <= 0 {
			t.Fatalf("异常时间戳应被校正为当前时间，got %d（now=%d）", r.Ts, now)
		}
	}
}
