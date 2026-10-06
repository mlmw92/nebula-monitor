package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// Pod 日志采集：路径通配符（Pod 是短命的，文件集合每轮都在变）、
// 容器身份随批次上行、以及 pod 来源偏移的清理。

// TestExpandLogPaths 无通配符时**原样返回**（既有配置零行为变化）；
// 有通配符时按 arch 排序（顺序稳定是偏移可复现的前提）；非法模式不致命。
func TestExpandLogPaths(t *testing.T) {
	dir := t.TempDir()
	// 刻意乱序创建：Glob 的结果必须被显式排序，否则"同一轮里先读哪个文件"会随目录顺序变化
	for _, name := range []string{"b.log", "a.log", "c.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	literal := filepath.Join(dir, "a.log")
	if got := expandLogPaths(literal); len(got) != 1 || got[0] != literal {
		t.Fatalf("无通配符应原样返回，got %v", got)
	}

	got := expandLogPaths(filepath.Join(dir, "*.log"))
	if len(got) != 3 {
		t.Fatalf("应匹配 3 个文件，got %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("结果应有序，got %v", got)
		}
	}

	if got := expandLogPaths(filepath.Join(dir, "[bad.log")); got != nil {
		t.Fatalf("非法模式应返回 nil（本轮跳过），got %v", got)
	}
}

// TestExpandSourcePaths_CapsFileCount 单轮文件数必须有上限：一个节点上的 Pod 日志
// 很容易匹配到几百个文件，没有上限时一轮采集会把整个节点读一遍。
func TestExpandSourcePaths_CapsFileCount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < maxLogFilesPerSource+5; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('0'+i/26%10))+".log")
		if err := os.WriteFile(name, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src := config.LogSourceConfig{ID: "podlog", PodLogs: true, Paths: []string{filepath.Join(dir, "*.log")}}
	paths, skipped := expandSourcePaths(src)
	if len(paths) != maxLogFilesPerSource {
		t.Fatalf("应被夹到 %d 个，got %d", maxLogFilesPerSource, len(paths))
	}
	if skipped == 0 {
		t.Fatal("被跳过的文件数必须回报（截断要可见）")
	}
}

// TestLogCollector_PodOriginPerFile 一个来源匹配到多个 Pod 的日志文件时，
// **每个文件带自己的身份**上行（批次级携带：一批 = 一个文件）。
func TestLogCollector_PodOriginPerFile(t *testing.T) {
	dir := t.TempDir()
	type sent struct {
		origin *model.LogOrigin
		lines  int
	}
	var got []sent

	// 两个 Pod 各一个容器日志文件
	paths := []string{
		filepath.Join(dir, "pods", "nebula-demo_web-1_aaaa", "nginx", "0.log"),
		filepath.Join(dir, "pods", "kube-system_coredns-abc_bbbb", "coredns", "0.log"),
	}
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("error: boom\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 预置偏移 0（= 已在跟踪），于是本轮会读文件里的既有内容
	seedOffsets(t, filepath.Join(dir, "offsets.json"), "podlog", paths)

	src := config.LogSourceConfig{
		ID: "podlog", PodLogs: true,
		Paths:    []string{filepath.Join(dir, "pods", "*", "*", "*.log")},
		Patterns: []config.LogPattern{{Name: "err", Regex: "error"}},
	}
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, filepath.Join(dir, "offsets.json"))
	c.SetSink(func(_ context.Context, _ string, origin *model.LogOrigin, lines []model.LogLine) (model.LogSinkResult, error) {
		got = append(got, sent{origin: origin, lines: len(lines)})
		return model.LogSinkResult{}, nil
	})
	c.CollectCtx(context.Background())

	if len(got) != 2 {
		t.Fatalf("两个文件应各上行一批，got %d 批：%+v", len(got), got)
	}
	byPod := map[string]*model.LogOrigin{}
	for _, s := range got {
		if s.origin == nil {
			t.Fatal("podLogs 来源的行必须带容器身份")
		}
		if s.lines != 1 {
			t.Fatalf("每个文件应上行 1 行，got %d", s.lines)
		}
		byPod[s.origin.Pod] = s.origin
	}
	web, ok := byPod["web-1"]
	if !ok || web.Namespace != "nebula-demo" || web.Container != "nginx" {
		t.Fatalf("web-1 的身份不符：%+v", byPod)
	}
	if coredns, ok := byPod["coredns-abc"]; !ok || coredns.Namespace != "kube-system" || coredns.Container != "coredns" {
		t.Fatalf("coredns 的身份不符：%+v", byPod)
	}
}

// TestLogCollector_PrunesVanishedPodOffsets 容器日志文件被 kubelet 回收后，
// 它的偏移记录必须被清掉：否则偏移文件随 Pod 更替无界增长（而它每轮都要整体重写）。
func TestLogCollector_PrunesVanishedPodOffsets(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "pods", "default_app-1_aaaa", "app", "0.log")
	if err := os.MkdirAll(filepath.Dir(live), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "pods", "default_app-0_bbbb", "app", "0.log")
	offsetsPath := filepath.Join(dir, "offsets.json")
	seedOffsets(t, offsetsPath, "podlog", []string{live, gone})

	src := config.LogSourceConfig{ID: "podlog", PodLogs: true, All: true, Paths: []string{filepath.Join(dir, "pods", "*", "*", "*.log")}}
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, offsetsPath)
	c.CollectCtx(context.Background())

	if _, ok := c.offsets["podlog|"+gone]; ok {
		t.Fatalf("已消失的容器日志文件偏移应被清理，got %+v", c.offsets)
	}
	if _, ok := c.offsets["podlog|"+live]; !ok {
		t.Fatalf("仍存在的文件偏移不该被清理，got %+v", c.offsets)
	}
}

// TestLogCollector_NonPodSourceKeepsOffsets 普通文件来源**不清理**偏移：
// 它的文件可能被轮转后重建，删掉偏移会让重建的文件被当成新文件"从末尾开始"，
// 静默跳过开头的内容——那是丢日志，比多几行偏移严重。
func TestLogCollector_NonPodSourceKeepsOffsets(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "rotated.log")
	offsetsPath := filepath.Join(dir, "offsets.json")
	seedOffsets(t, offsetsPath, "applog", []string{gone})

	src := config.LogSourceConfig{ID: "applog", All: true, Paths: []string{gone}}
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, offsetsPath)
	c.CollectCtx(context.Background())

	if _, ok := c.offsets["applog|"+gone]; !ok {
		t.Fatalf("非 pod 来源的偏移不该被清理，got %+v", c.offsets)
	}
}

// TestLogCollector_GlobZeroMatchKeepsUp 通配符**零匹配不算失败**：
// Pod 是短命的，此刻没有匹配文件是正常状态；记成 up=0 会让"日志采集不可用"
// 在每次缩容时误报，最后没人再看这个信号。
func TestLogCollector_GlobZeroMatchKeepsUp(t *testing.T) {
	dir := t.TempDir()
	src := config.LogSourceConfig{
		ID: "podlog", PodLogs: true,
		Paths:    []string{filepath.Join(dir, "pods", "*", "*", "*.log")},
		Patterns: []config.LogPattern{{Name: "err", Regex: "error"}},
	}
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, filepath.Join(dir, "offsets.json"))
	ms := c.CollectCtx(context.Background())
	m, ok := logMetric(ms, "podlog_log_up", nil)
	if !ok || m.Value != 1 {
		t.Fatalf("零匹配时 up 应为 1，got %+v", ms)
	}
}

// seedOffsets 预置偏移文件（key = <来源>|<路径>，偏移 0 = 已跟踪该文件）。
func seedOffsets(t *testing.T, path, source string, files []string) {
	t.Helper()
	entries := make([]logOffsetEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, logOffsetEntry{Key: source + "|" + f, Offset: 0})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
