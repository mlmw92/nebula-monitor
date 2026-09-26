package collector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
)

// LogCollector 增量读取日志文件（C2 集中日志的采集侧）。
//
// 与既有 nginx access / SSH 日志读取的三个关键差异：
//  1. **偏移落盘**：既有实现把偏移放在内存里，进程重启即丢——对「上传到中心」的场景，
//     那意味着要么漏一段、要么重复上传。这里落盘到 LogOffsetsFile。
//  2. **多行合并**：堆栈/异常一条日志跨多行，按行首模式把续行并入上一条。
//  3. **上限与丢弃可见**：单轮有字节/行数上限；超限时**跳过该文件剩余部分并计数**
//     （产 `_log_dropped_total` 指标），不做「悄悄落后」的延迟读取——延迟读取会变成
//     一个永不收敛的积压，而运维只看得到「日志怎么越来越旧」。
type LogCollector struct {
	node        string
	sources     []config.LogSourceConfig
	offsetsPath string

	mu      sync.Mutex
	offsets map[string]int64 // key: sourceID + "|" + path

	// Sink 接收本轮读到的行（由子批次 B 接上「上传到 Server」；为 nil 时只做计数）。
	// 上传失败不清空偏移：本轮的行会随偏移推进而过去，因此上传实现内部必须自行决定
	// 「失败即丢弃并计数」还是「重试」——见 design 文档 §4.3（当前实现选前者）。
	//
	// 返回值区分两类「没上传成功」：限额丢弃（Dropped > 0，正常结果、计入 reason 标签）
	// 与真正失败（error，计入 reason=unreachable）。
	sink func(ctx context.Context, source string, lines []model.LogLine) (model.LogSinkResult, error)
}

// NewLogCollector 创建日志采集器并加载已落盘的偏移。
func NewLogCollector(node string, sources []config.LogSourceConfig, offsetsPath string) *LogCollector {
	c := &LogCollector{
		node:        node,
		sources:     sources,
		offsetsPath: offsetsPath,
		offsets:     map[string]int64{},
	}
	c.loadOffsets()
	return c
}

// SetSink 设置日志行接收方（上传实现）。
func (c *LogCollector) SetSink(f func(ctx context.Context, source string, lines []model.LogLine) (model.LogSinkResult, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sink = f
}

// collectResult 是单个文件一轮的读取结果。
type collectResult struct {
	matched map[string]int64 // 各模式命中数
	total   int64            // 读取并上传的行数
	dropped map[string]int64 // 丢弃计数，reason -> 行数
	ok      bool             // 该文件本轮是否可用（打不开/读失败为 false）
}

// CollectCtx 采集一轮，返回本轮产出的指标（同时把读到的行交给 sink）。
//
// 刻意不返回 error：单个来源/文件失败只影响它自己（记日志 + 产 up=0），
// 一个坏路径不该让整轮采集失败——那会让其它来源也一起没有数据。
func (c *LogCollector) CollectCtx(ctx context.Context) []model.Metric {
	if len(c.sources) == 0 {
		return nil // 未配置日志来源：零行为变化（与改造前完全一致）
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := model.NowMillis()
	var out []model.Metric
	for _, src := range c.sources {
		if ctx.Err() != nil {
			break
		}
		patterns := compileLogPatterns(src)
		res := collectResult{matched: map[string]int64{}, dropped: map[string]int64{}, ok: true}
		for _, path := range src.Paths {
			if ctx.Err() != nil {
				break
			}
			r := c.collectFile(ctx, src, path, patterns)
			if !r.ok {
				res.ok = false
			}
			res.total += r.total
			for k, v := range r.matched {
				res.matched[k] += v
			}
			for k, v := range r.dropped {
				res.dropped[k] += v
			}
		}
		out = append(out, c.buildLogMetrics(src, res, now)...)
	}
	return out
}

// buildLogMetrics 产出日志相关的指标。
//
// 为什么日志要产出指标：告警规则的输入本就是「指标名 + 阈值」，因此「错误日志激增」
// 这类判断可以直接用既有规则配出来，并自动获得静默/抑制/收敛/通知/处置的全部能力。
func (c *LogCollector) buildLogMetrics(src config.LogSourceConfig, res collectResult, now int64) []model.Metric {
	labels := map[string]string{"source": src.ID}
	mk := func(name string, value float64, extra map[string]string) model.Metric {
		lbl := make(map[string]string, len(labels)+len(extra)+3)
		for k, v := range labels {
			lbl[k] = v
		}
		for k, v := range extra {
			lbl[k] = v
		}
		lbl["node"] = c.node
		return model.Metric{Node: c.node, Name: src.ID + "_" + name, Labels: lbl, Value: value, Timestamp: now}
	}

	out := []model.Metric{
		// up：本轮所有路径都读得到为 1，否则 0——「配了却没有数据」的第一现场信号
		mk("log_up", boolToFloat(res.ok), nil),
		mk("log_lines_total", float64(res.total), nil),
	}
	// 每个模式一个**独立指标名**（<来源>_log_<模式>_total），而不是同名 + pattern 标签。
	//
	// 为什么：告警引擎的阈值规则是**按指标名取样本**的（固定不做标签筛选）。若把模式放进标签，
	// 规则只能写在共用的 log_match_total 上——任一模式超标都会触发，而告警消息里看不到是哪个模式，
	// 运维还得回日志页自己猜。模式进指标名后，规则可以直接写 `applog_log_err_total > 5`，
	// 告警文案自带模式名。代价是「每模式一个指标名」，数量由配置决定（个位数），基数可控。
	for name, n := range res.matched {
		if name == "" {
			continue // 全量模式（all: true）没有模式名；这些行已由 log_lines_total 计入
		}
		out = append(out, mk("log_"+name+"_total", float64(n), nil))
	}
	for reason, n := range res.dropped {
		out = append(out, mk("log_dropped_total", float64(n), map[string]string{"reason": reason}))
	}
	return out
}

// collectFile 读取单个文件的一轮增量。
func (c *LogCollector) collectFile(ctx context.Context, src config.LogSourceConfig, path string,
	patterns []compiledLogPattern) collectResult {

	res := collectResult{matched: map[string]int64{}, dropped: map[string]int64{}}
	key := src.ID + "|" + path

	f, err := os.Open(path) // 只读：绝不写、删、改被监控机的日志文件
	if err != nil {
		slog.Warn("日志文件打开失败（该来源本轮标记为不可用）", "source", src.ID, "path", path, "err", err)
		return res
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		slog.Warn("日志路径不是普通文件", "source", src.ID, "path", path)
		return res
	}

	offset, tracked := c.offsets[key]
	if !tracked {
		// 首次见到这个文件：**从文件尾开始，不回溯历史**（设计决策见 docs/c2-central-logs.md §12 决策 3）。
		//
		// 为什么这条必须有：日志文件动辄几百 MB~几 GB，而在「刚打开日志开关」的那一刻，
		// 最坏情况是把整个历史文件按模式筛一遍并全部上传——每日上限会被一次打满、
		// 检索页被无关历史淹没，而运维看到的现象是「刚开启就丢数据 + 满屏老日志」。
		// 需要历史请用其它工具导入（这一句也写进文档）。
		//
		// 注意 up 仍记为 1：这个文件**是**可读的，只是本轮没有可读的增量——
		// 若记 0，「配了却看到 up=0」会被误判成路径/权限问题。
		res.ok = true
		c.offsets[key] = st.Size()
		c.saveOffsets()
		slog.Info("日志来源首次采集，从文件末尾开始（不回溯历史）",
			"source", src.ID, "path", path, "startOffset", st.Size())
		return res
	}
	// 文件变小 = 被轮转/重建：从头重读（与既有 nginx access / SSH 日志的处理一致）。
	//
	// 已知限制：若被替换成**更长**的内容，靠大小判断不出来（偏移落在新内容中间，会读到半行而跳过）。
	// 与既有实现同一取舍——换 inode 比对才能严格识别，但那是平台相关的实现，
	// 而「轮转后文件更小」是绝大多数场景（logrotate 的 create/copytruncate 都满足）。
	if st.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, 0); err != nil {
		slog.Warn("日志文件定位失败", "source", src.ID, "path", path, "err", err)
		return res
	}

	r := bufio.NewReader(f)
	maxLines, maxBytes := logLimits(src)
	var readBytes int64
	var raw []string
	truncated := false
	for {
		line, rerr := r.ReadString('\n')
		if line == "" && rerr != nil {
			break
		}
		readBytes += int64(len(line))
		if readBytes > maxBytes || int64(len(raw)) >= int64(maxLines) {
			// 超限：本轮**跳过该文件剩余部分**并计数（不做延迟读取，见类型注释）
			truncated = true
			break
		}
		// 末行无换行说明日志正在写入：留到下一轮（offset 不推进该行），避免读到半行
		if !strings.HasSuffix(line, "\n") {
			readBytes -= int64(len(line))
			break
		}
		raw = append(raw, strings.TrimRight(line, "\r\n"))
		if rerr != nil {
			break
		}
	}

	// 多行合并（堆栈/异常）
	merged := mergeMultiline(raw, src.Multiline)
	lines := make([]model.LogLine, 0, len(merged))
	now := model.NowMillis()
	for _, text := range merged {
		name, hit := matchLogPattern(patterns, text)
		if !hit && !src.All {
			continue // 默认只上传关心的行（见 config.LogSourceConfig 的隐私默认值说明）
		}
		if hit {
			res.matched[name]++
		}
		lines = append(lines, model.LogLine{Ts: now, Pattern: name, Text: text})
	}

	// log_lines_total 的语义是「**成功上传**的行数」：限额丢弃与上传失败都不计入，
	// 它们分别由 log_dropped_total{reason=...} 体现——两个数字相加才是本轮读到的行数。
	if c.sink != nil && len(lines) > 0 {
		out, err := c.sink(ctx, src.ID, lines)
		switch {
		case err != nil:
			// 上传失败：本轮丢弃并计数（日志是尽力而为的数据；积压会变成永不收敛的问题）
			res.dropped["unreachable"] += int64(len(lines))
			slog.Warn("日志上传失败，本轮丢弃", "source", src.ID, "lines", len(lines), "err", err)
		default:
			res.total += int64(len(lines) - out.Dropped)
			if out.Dropped > 0 {
				reason := out.Reason
				if reason == "" {
					reason = "server"
				}
				res.dropped[reason] += int64(out.Dropped)
			}
		}
	}

	// 推进偏移：正常读完用实际读到的字节；超限则直接跳到文件末尾（跳过剩余部分）
	newOffset := offset + readBytes
	if truncated {
		skipped := st.Size() - newOffset
		if skipped > 0 {
			// 跳过即丢弃：必须计数可见，否则就是静默丢数据
			res.dropped["cap"] += 1
			slog.Warn("日志读取超单轮上限，已跳过该文件剩余部分（下次从文件末尾继续）",
				"source", src.ID, "path", path, "skippedBytes", skipped,
				"hint", "调大 maxBytesPerRound/maxLinesPerRound，或用 patterns 收窄上传范围")
		}
		newOffset = st.Size()
	}
	c.offsets[key] = newOffset
	c.saveOffsets()

	res.ok = true
	return res
}

// compiledLogPattern 是预编译后的模式。
type compiledLogPattern struct {
	name string
	re   *regexp.Regexp
}

// compileLogPatterns 预编译模式。启动期校验已保证可编译，这里失败只跳过该模式
// （防御性是给「旁路加载配置」留的，不应发生）。
func compileLogPatterns(src config.LogSourceConfig) []compiledLogPattern {
	out := make([]compiledLogPattern, 0, len(src.Patterns))
	for _, p := range src.Patterns {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			slog.Warn("日志模式正则非法，已跳过该模式", "source", src.ID, "pattern", p.Name, "err", err)
			continue
		}
		out = append(out, compiledLogPattern{name: p.Name, re: re})
	}
	return out
}

// matchLogPattern 返回首个命中的模式名。
func matchLogPattern(patterns []compiledLogPattern, text string) (string, bool) {
	for _, p := range patterns {
		if p.re.MatchString(text) {
			return p.name, true
		}
	}
	return "", false
}

// mergeMultiline 按行首模式把续行并入上一条（未配置时原样返回）。
//
// maxLines 是硬边界：某些日志永远不会有「行首匹配」的行（例如只有堆栈、没有时间戳），
// 没有这个上限就会把整个文件吸成一条。
func mergeMultiline(lines []string, ml config.LogMultiline) []string {
	if ml.StartPattern == "" || len(lines) == 0 {
		return lines
	}
	re, err := regexp.Compile(ml.StartPattern)
	if err != nil {
		return lines
	}
	maxLines := ml.MaxLines
	if maxLines <= 0 {
		maxLines = config.DefaultLogMultilineMaxLines
	}

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if re.MatchString(line) || len(out) == 0 {
			out = append(out, line) // 新的一条
			continue
		}
		last := &out[len(out)-1]
		if strings.Count(*last, "\n")+1 >= maxLines {
			out = append(out, line) // 到达合并上限：另起一条，避免无限增长
			continue
		}
		*last += "\n" + line
	}
	return out
}

// logOffsetEntry 是偏移文件的持久化形式。
type logOffsetEntry struct {
	Key       string `json:"key"`
	Offset    int64  `json:"offset"`
	UpdatedAt int64  `json:"updatedAt"`
}

// loadOffsets 加载偏移文件；不存在或损坏时视为「没有偏移」并记日志（不阻塞启动）。
func (c *LogCollector) loadOffsets() {
	if c.offsetsPath == "" {
		return
	}
	data, err := os.ReadFile(c.offsetsPath)
	if err != nil {
		return // 首次运行：没有偏移是正常情况
	}
	var entries []logOffsetEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("日志偏移文件损坏，将从文件末尾重新开始（可能漏一段日志）", "path", c.offsetsPath, "err", err)
		return
	}
	for _, e := range entries {
		c.offsets[e.Key] = e.Offset
	}
}

// saveOffsets 落盘偏移。写失败只告警：偏移是「优化」，不该因为它失败就中断采集
// （后果只是重启后可能重复上传一段，比丢日志轻）。
func (c *LogCollector) saveOffsets() {
	if c.offsetsPath == "" {
		return
	}
	entries := make([]logOffsetEntry, 0, len(c.offsets))
	now := model.NowMillis()
	for k, v := range c.offsets {
		entries = append(entries, logOffsetEntry{Key: k, Offset: v, UpdatedAt: now})
	}
	data, err := json.Marshal(entries)
	if err != nil {
		slog.Warn("日志偏移序列化失败", "err", err)
		return
	}
	tmp := fmt.Sprintf("%s.tmp", c.offsetsPath)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("日志偏移写入失败", "path", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, c.offsetsPath); err != nil {
		slog.Warn("日志偏移落盘失败", "path", c.offsetsPath, "err", err)
	}
}

// logLimits 返回来源的单轮上限。启动期校验已补齐默认值，这里再兜一次底：
// 让直接构造配置（如单测）也能得到安全的上限，而不是 0（0 会被解读为「一个字节/一行都不读」）。
func logLimits(src config.LogSourceConfig) (int, int64) {
	lines, bytes := src.MaxLinesPerRound, src.MaxBytesPerRound
	if lines <= 0 {
		lines = config.DefaultLogMaxLinesPerRound
	}
	if bytes <= 0 {
		bytes = config.DefaultLogMaxBytesPerRound
	}
	return lines, bytes
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
