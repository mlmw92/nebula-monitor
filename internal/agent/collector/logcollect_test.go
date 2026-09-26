package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// 日志采集侧的要点：偏移**落盘**（重启不丢进度）、轮转重读、多行合并、模式过滤、
// 单轮上限「跳过并计数」（不做悄悄落后的延迟读取）。

// logFixture 搭一个「来源 + 临时日志文件 + 偏移文件」的测试环境，并收集 sink 收到的行。
type logFixture struct {
	dir         string
	logPath     string
	offsetsPath string
	lines       []model.LogLine
}

func newLogFixture(t *testing.T, content string) *logFixture {
	t.Helper()
	dir := t.TempDir()
	f := &logFixture{
		dir:         dir,
		logPath:     filepath.Join(dir, "app.log"),
		offsetsPath: filepath.Join(dir, "offsets.json"),
	}
	if err := os.WriteFile(f.logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// 预置偏移 0 = 「这个文件已经在跟踪中」，于是测试读的是文件里的既有内容。
	// 「首次见到一个文件」的另一种行为（**从文件尾开始、不回溯历史**）由
	// TestLogCollector_FirstSightStartsAtEOF 单独覆盖——那是上线时的默认行为，
	// 不能由这些用例的夹具悄悄带过。
	// 必须用 json.Marshal：Windows 路径里的反斜杠手工拼进 JSON 会变成非法转义，
	// 偏移文件解析失败 → 采集器又回到「首次见到」的行为（这个坑在写夹具时踩到过）
	seed, err := json.Marshal([]logOffsetEntry{{Key: "applog|" + f.logPath, Offset: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.offsetsPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *logFixture) source(t *testing.T, mut func(*config.LogSourceConfig)) config.LogSourceConfig {
	t.Helper()
	src := config.LogSourceConfig{
		ID:       "applog",
		Paths:    []string{f.logPath},
		Patterns: []config.LogPattern{{Name: "err", Regex: `(?i)\b(error|fatal)\b`}},
	}
	if mut != nil {
		mut(&src)
	}
	return src
}

// collector 新建采集器（offsetPath 传空字符串可模拟「未落盘」）。
func (f *logFixture) collector(src config.LogSourceConfig, offsetsPath string) *LogCollector {
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, offsetsPath)
	c.SetSink(func(_ context.Context, _ string, lines []model.LogLine) (model.LogSinkResult, error) {
		f.lines = append(f.lines, lines...)
		return model.LogSinkResult{}, nil
	})
	return c
}

func (f *logFixture) append(t *testing.T, text string) {
	t.Helper()
	fh, err := os.OpenFile(f.logPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(text); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
}

func logMetric(ms []model.Metric, name string, labels map[string]string) (model.Metric, bool) {
	for _, m := range ms {
		if m.Name != name {
			continue
		}
		ok := true
		for k, v := range labels {
			if m.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return m, true
		}
	}
	return model.Metric{}, false
}

// TestLogCollector_FirstSightStartsAtEOF 首次见到一个文件时**从文件尾开始读**（不回溯历史）。
//
// 这条守的是「刚打开日志开关」那一刻的行为：日志文件动辄几百 MB 到几 GB，
// 若从头读，最坏情况是把整个历史文件按模式筛一遍全部上传——每日上限一次打满、
// 检索页被无关历史淹没。up 仍记为 1：文件是可读的，只是本轮没有可读的增量
// （记 0 会被误判成路径/权限问题）。
func TestLogCollector_FirstSightStartsAtEOF(t *testing.T) {
	f := newLogFixture(t, "error: 历史行（不该上传）\n")
	_ = os.Remove(f.offsetsPath) // 模拟从未见过这个文件
	src := f.source(t, nil)

	c := f.collector(src, f.offsetsPath)
	ms := c.CollectCtx(context.Background())
	if len(f.lines) != 0 {
		t.Fatalf("首次采集不应回溯历史行，got %+v", f.lines)
	}
	if m, ok := logMetric(ms, "applog_log_up", nil); !ok || m.Value != 1 {
		t.Fatalf("文件可读时 up 应为 1（否则会被误读成路径/权限问题），got %+v", ms)
	}
	// 起点要落盘：重启后不会因为「又是首次」而再跳一次
	if data, err := os.ReadFile(f.offsetsPath); err != nil || !strings.Contains(string(data), `"offset"`) {
		t.Fatalf("首次采集应把起点写入偏移文件：err=%v data=%q", err, string(data))
	}

	// 之后追加的行要正常读到
	f.append(t, "error: 新行\n")
	c2 := f.collector(src, f.offsetsPath)
	_ = c2.CollectCtx(context.Background())
	if len(f.lines) != 1 || !strings.Contains(f.lines[0].Text, "新行") {
		t.Fatalf("追加的行应被读到，got %+v", f.lines)
	}
}

// TestLogCollector_OffsetPersistedAcrossRestart 偏移落盘：重启后不重复上传、也不漏读。
func TestLogCollector_OffsetPersistedAcrossRestart(t *testing.T) {
	f := newLogFixture(t, "2026-09-26 INFO ok\nerror: first failure\n")
	src := f.source(t, nil)

	c := f.collector(src, f.offsetsPath)
	_ = c.CollectCtx(context.Background())
	if len(f.lines) != 1 || !strings.Contains(f.lines[0].Text, "first failure") {
		t.Fatalf("首轮应上传 1 条命中行，got %+v", f.lines)
	}

	// 模拟进程重启：新建采集器（同一偏移文件）→ 不应重复上传
	f.lines = nil
	c2 := f.collector(src, f.offsetsPath)
	_ = c2.CollectCtx(context.Background())
	if len(f.lines) != 0 {
		t.Fatalf("偏移已落盘，重启后不应重复上传，got %+v", f.lines)
	}

	// 追加的新行应被继续读到
	f.append(t, "error: second failure\n")
	c3 := f.collector(src, f.offsetsPath)
	_ = c3.CollectCtx(context.Background())
	if len(f.lines) != 1 || !strings.Contains(f.lines[0].Text, "second failure") {
		t.Fatalf("追加的行应被续读，got %+v", f.lines)
	}
}

// TestLogCollector_RotationResetsOffset 文件变小 = 被轮转/重建 → 从头重读。
func TestLogCollector_RotationResetsOffset(t *testing.T) {
	f := newLogFixture(t, "error: a\n")
	src := f.source(t, nil)
	if err := os.WriteFile(f.logPath, []byte("error: a\nerror: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := f.collector(src, f.offsetsPath)
	_ = c.CollectCtx(context.Background())
	if len(f.lines) != 2 {
		t.Fatalf("应读到 2 条，got %d", len(f.lines))
	}

	// 轮转：文件被换成一个**更短**的（既有实现就是靠「文件变小」识别轮转与重建）
	f.lines = nil
	if err := os.WriteFile(f.logPath, []byte("error: rot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c2 := f.collector(src, f.offsetsPath)
	_ = c2.CollectCtx(context.Background())
	if len(f.lines) != 1 || !strings.Contains(f.lines[0].Text, "rot") {
		t.Fatalf("轮转后应从头读，got %+v", f.lines)
	}
}

// TestLogCollector_PatternFilterAndMetrics 默认只上传命中 patterns 的行，并产出计数指标。
func TestLogCollector_PatternFilterAndMetrics(t *testing.T) {
	f := newLogFixture(t, "INFO all good\nerror: boom\nFATAL: worse\n")
	src := f.source(t, nil)
	c := f.collector(src, f.offsetsPath)
	ms := c.CollectCtx(context.Background())

	if len(f.lines) != 2 {
		t.Fatalf("应只上传 2 条命中行，got %+v", f.lines)
	}
	// 模式进指标名（而不是 pattern 标签）：阈值规则按指标名取样本，不支持标签筛选，
	// 只有进名字才能配出「某个模式激增」的精确告警。
	if m, ok := logMetric(ms, "applog_log_err_total", nil); !ok || m.Value != 2 {
		t.Fatalf("应产出匹配计数 applog_log_err_total=2，got %+v", ms)
	}
	if m, ok := logMetric(ms, "applog_log_lines_total", nil); !ok || m.Value != 2 {
		t.Fatalf("应产出上传行数 applog_log_lines_total=2，got %+v", ms)
	}
	if m, ok := logMetric(ms, "applog_log_up", nil); !ok || m.Value != 1 {
		t.Fatalf("文件可读应 up=1，got %+v", ms)
	}
}

// TestLogCollector_AllUploadsEverything 显式 all: true 时不受 patterns 约束。
func TestLogCollector_AllUploadsEverything(t *testing.T) {
	f := newLogFixture(t, "line one\nline two\n")
	src := f.source(t, func(s *config.LogSourceConfig) { s.All = true; s.Patterns = nil })
	c := f.collector(src, f.offsetsPath)
	_ = c.CollectCtx(context.Background())
	if len(f.lines) != 2 {
		t.Fatalf("all: true 应全量上传，got %+v", f.lines)
	}
}

// TestLogCollector_MultilineMerge 续行并入上一条（堆栈）。
func TestLogCollector_MultilineMerge(t *testing.T) {
	content := "2026-09-26 ERROR start\n" +
		"    at com.example.Foo(bar.java:1)\n" +
		"    at com.example.Baz(qux.java:2)\n" +
		"2026-09-26 INFO next\n"
	f := newLogFixture(t, content)
	src := f.source(t, func(s *config.LogSourceConfig) {
		s.Patterns = []config.LogPattern{{Name: "err", Regex: "ERROR"}}
		s.Multiline = config.LogMultiline{StartPattern: `^\d{4}-\d\d-\d\d `, MaxLines: 10}
	})
	c := f.collector(src, f.offsetsPath)
	_ = c.CollectCtx(context.Background())

	if len(f.lines) != 1 {
		t.Fatalf("应只上传 1 条（ERROR 及其续行），got %d：%+v", len(f.lines), f.lines)
	}
	if got := strings.Count(f.lines[0].Text, "\n"); got != 2 {
		t.Fatalf("续行应并入同一条（2 个换行），got %d：%q", got, f.lines[0].Text)
	}
}

// TestLogCollector_CapSkipsRestAndCounts 超单轮上限时跳过该文件剩余部分——必须计数可见。
func TestLogCollector_CapSkipsRestAndCounts(t *testing.T) {
	f := newLogFixture(t, "error: 1\nerror: 2\nerror: 3\nerror: 4\n")
	src := f.source(t, func(s *config.LogSourceConfig) { s.MaxLinesPerRound = 2 })
	c := f.collector(src, f.offsetsPath)
	ms := c.CollectCtx(context.Background())

	if len(f.lines) != 2 {
		t.Fatalf("应只读上限内的 2 条，got %d", len(f.lines))
	}
	if _, ok := logMetric(ms, "applog_log_dropped_total", map[string]string{"reason": "cap"}); !ok {
		t.Fatalf("跳过剩余部分必须计数（log_dropped_total{reason=cap}），got %+v", ms)
	}
	// 偏移已推进到文件末尾：下一轮不应把被跳过的行再读一遍（否则就是「悄悄落后」）
	f.lines = nil
	c2 := f.collector(src, f.offsetsPath)
	_ = c2.CollectCtx(context.Background())
	if len(f.lines) != 0 {
		t.Fatalf("被跳过的行不应在下一轮重新出现，got %+v", f.lines)
	}
}

// TestLogCollector_MissingPathMarksDown 路径读不到 → up=0（「配了却没数据」的第一现场信号）。
func TestLogCollector_MissingPathMarksDown(t *testing.T) {
	f := newLogFixture(t, "")
	src := f.source(t, func(s *config.LogSourceConfig) {
		s.Paths = []string{filepath.Join(f.dir, "not-exist.log")}
	})
	c := f.collector(src, f.offsetsPath)
	ms := c.CollectCtx(context.Background())
	if m, ok := logMetric(ms, "applog_log_up", nil); !ok || m.Value != 0 {
		t.Fatalf("读不到文件应 up=0，got %+v", ms)
	}
}

// TestLogCollector_NoSourcesIsNoop 未配置来源时零行为变化（不产任何指标、不发任何数据）。
func TestLogCollector_NoSourcesIsNoop(t *testing.T) {
	c := NewLogCollector("n1", nil, "")
	called := false
	c.SetSink(func(context.Context, string, []model.LogLine) (model.LogSinkResult, error) {
		called = true
		return model.LogSinkResult{}, nil
	})
	if ms := c.CollectCtx(context.Background()); len(ms) != 0 {
		t.Fatalf("未配置来源不应产出任何指标，got %+v", ms)
	}
	if called {
		t.Fatal("未配置来源不应调用 sink")
	}
}

// TestLogCollector_SinkDropCountsAsReason 限额丢弃（每日上限、限速）是**正常结果**：
// 必须计数可见、但不能算作上传失败——否则限速一触发就每次刷「上传失败」，把真正的问题淹掉。
func TestLogCollector_SinkDropCountsAsReason(t *testing.T) {
	f := newLogFixture(t, "error: 1\nerror: 2\nerror: 3\n")
	src := f.source(t, nil)
	c := NewLogCollector("n1", []config.LogSourceConfig{src}, f.offsetsPath)
	c.SetSink(func(context.Context, string, []model.LogLine) (model.LogSinkResult, error) {
		return model.LogSinkResult{Dropped: 1, Reason: "rate"}, nil
	})
	ms := c.CollectCtx(context.Background())

	// 成功上传 2 条（3 条中被打回 1 条）
	if m, ok := logMetric(ms, "applog_log_lines_total", nil); !ok || m.Value != 2 {
		t.Fatalf("log_lines_total 应只计成功上传数（2），got %+v", ms)
	}
	if m, ok := logMetric(ms, "applog_log_dropped_total", map[string]string{"reason": "rate"}); !ok || m.Value != 1 {
		t.Fatalf("应产出 log_dropped_total{reason=rate}=1，got %+v", ms)
	}
	// 不该出现 unreachable（那是上传失败，与限额丢弃是两回事）
	if _, ok := logMetric(ms, "applog_log_dropped_total", map[string]string{"reason": "unreachable"}); ok {
		t.Fatalf("限额丢弃不应记为 unreachable，got %+v", ms)
	}
	// up 仍为 1：采集本身是成功的
	if m, ok := logMetric(ms, "applog_log_up", nil); !ok || m.Value != 1 {
		t.Fatalf("限额丢弃不影响采集可用性（up 应仍为 1），got %+v", ms)
	}
}

// TestMergeMultiline 合并逻辑的边界：无配置原样、达到上限另起一条。
func TestMergeMultiline(t *testing.T) {
	if got := mergeMultiline([]string{"a", "b"}, config.LogMultiline{}); len(got) != 2 {
		t.Fatalf("未配置 startPattern 应原样返回，got %v", got)
	}
	lines := []string{"start", "cont1", "cont2", "start2"}
	got := mergeMultiline(lines, config.LogMultiline{StartPattern: "^start", MaxLines: 2})
	if len(got) != 3 {
		t.Fatalf("达到 maxLines 应另起一条（合并上限保护），got %v", got)
	}
	if got[0] != "start\ncont1" {
		t.Fatalf("续行应并入上一条，got %q", got[0])
	}
}
