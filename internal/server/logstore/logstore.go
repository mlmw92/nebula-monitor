// Package logstore 负责集中日志的落盘（C2 采集侧的落地端）。
//
// 存储布局：<root>/<source>/<YYYY-MM-DD>/<node>.log，一行一条 JSON。
//
// 为什么这么分片：检索要「不建索引也能有界扫描」——按时间范围先挑出要读的文件、
// 按节点再缩小到几个文件，剩下的是顺序读文本。代价是文件数量随 来源×日期×节点 增长，
// 因此有每日总量上限与保留清理（保留接入 retention）。
package logstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
)

const (
	// DefaultMaxBytesPerDay 是单来源每日写入上限的兜底值（1 GiB）。
	DefaultMaxBytesPerDay = 1 << 30
	// MaxLineBytes 是单行日志的存储上限（超出截断）——一行异常长的日志不该把存储与界面拖垮。
	MaxLineBytes = 8 << 10
	// MaxLinesPerBatch 是单批行数上限（与 Agent 侧的单轮上限同量级）。
	MaxLinesPerBatch = 20000
)

// nodeFilePattern 限定节点名在文件名里的安全字符：节点名会成为路径的一段。
var nodeFilePattern = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Store 是集中日志的落盘存储器。
type Store struct {
	root      string
	maxPerDay int64
	// 检索的扫描预算（见 query.go）——一次查询的代价必须有上限
	scanBudgetBytes int64
	scanBudgetLines int64

	mu sync.Mutex
	// written 记录 (source|date) 已写入字节数。首次触及时从现有文件大小重建，
	// 因此重启不会把当天的配额清零（否则「重启一次就重新拥有全天配额」）。
	written map[string]int64
}

// New 创建存储器。root 为空返回 nil（调用方据此关闭该能力）；maxBytesPerDay ≤ 0 用兜底值。
func New(root string, maxBytesPerDay int64) *Store {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if maxBytesPerDay <= 0 {
		maxBytesPerDay = DefaultMaxBytesPerDay
	}
	return &Store{
		root:            root,
		maxPerDay:       maxBytesPerDay,
		scanBudgetBytes: DefaultScanBudgetBytes,
		scanBudgetLines: DefaultScanBudgetLines,
		written:         map[string]int64{},
	}
}

// budgetBytes / budgetLines 返回扫描预算（构造与 SetScanBudget 都会保证非零，这里只是防御）。
func (s *Store) budgetBytes() int64 {
	if s.scanBudgetBytes <= 0 {
		return DefaultScanBudgetBytes
	}
	return s.scanBudgetBytes
}

func (s *Store) budgetLines() int64 {
	if s.scanBudgetLines <= 0 {
		return DefaultScanBudgetLines
	}
	return s.scanBudgetLines
}

// Root 返回存储根目录（检索侧按同样布局定位文件）。
func (s *Store) Root() string { return s.root }

// Append 写入一批日志，返回接受与丢弃的行数（以及丢弃原因，供 Agent 记进指标）。
//
// 丢弃而不是扩容：单来源每日上限是「日志不该吃满磁盘」的保证。丢弃必须可见——
// 结果里带 dropped/reason，Agent 会把它变成 log_dropped_total{reason=...} 指标。
func (s *Store) Append(b model.LogBatch) (accepted, dropped int, reason string, err error) {
	if s == nil {
		return 0, 0, "", fmt.Errorf("集中日志存储未启用")
	}
	if !model.IsValidLogSourceName(b.Source) {
		return 0, 0, "invalid", fmt.Errorf("source 名非法")
	}
	node := sanitizeNodeName(b.Node)
	if node == "" {
		return 0, 0, "invalid", fmt.Errorf("node 为空")
	}
	if len(b.Lines) == 0 {
		return 0, 0, "", nil
	}
	if len(b.Lines) > MaxLinesPerBatch {
		return 0, 0, "tooManyLines", fmt.Errorf("单批行数 %d 超过上限 %d", len(b.Lines), MaxLinesPerBatch)
	}

	date := time.Now().Format("2006-01-02")
	dir := filepath.Join(s.root, b.Source, date)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, 0, "", err
	}
	path := filepath.Join(dir, node+".log")

	s.mu.Lock()
	defer s.mu.Unlock()
	quotaKey := b.Source + "|" + date
	if _, ok := s.written[quotaKey]; !ok {
		s.written[quotaKey] = dirSize(s.root, b.Source, date)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, 0, "", err
	}
	defer f.Close()

	for _, line := range b.Lines {
		if s.written[quotaKey] >= s.maxPerDay {
			dropped = len(b.Lines) - accepted
			return accepted, dropped, "dailyCap", nil
		}
		rec := model.LogHit{
			Ts:      clampTS(line.Ts),
			Node:    b.Node,
			Source:  b.Source,
			Pattern: line.Pattern,
			Text:    truncate(line.Text, MaxLineBytes),
		}
		data, err := json.Marshal(rec)
		if err != nil {
			dropped++
			continue
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			return accepted, len(b.Lines) - accepted, "writeError", err
		}
		s.written[quotaKey] += int64(len(data))
		accepted++
	}
	return accepted, dropped, "", nil
}

// 落盘的一行直接就是 model.LogHit：存储格式与检索返回的形状一致，
// 于是「磁盘上的东西」与「接口返回的东西」不会漂移（少一层映射就少一处不一致）。

// sanitizeNodeName 把节点名净化成安全文件名：不留任何路径分隔符或特殊字符。
func sanitizeNodeName(node string) string {
	node = strings.TrimSpace(node)
	if node == "" {
		return ""
	}
	if len(node) > 128 {
		node = node[:128]
	}
	out := nodeFilePattern.ReplaceAllString(node, "_")
	if out == "." || out == ".." || strings.Trim(out, "_") == "" {
		return ""
	}
	return out
}

// clampTS 校正时间戳：解析不到（0）或明显超前的值一律替换为当前时间。
// 存原始值会让「按时间范围检索」的结果莫名其妙（未来时间的日志永远落在范围外）。
func clampTS(ts int64) int64 {
	now := time.Now().UnixMilli()
	if ts <= 0 || ts > now+int64(time.Hour/time.Millisecond) {
		return now
	}
	return ts
}

// truncate 截断超长行（按字节，可能截在字符中间——日志行按字节截断是可接受的，
// 但要在末尾留标记，让人知道它被截过）。
func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…[truncated]"
}

// dirSize 统计某来源某日已写入的字节数（用于重启后重建当日配额）。
func dirSize(root, source, date string) int64 {
	dir := filepath.Join(root, source, date)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}
