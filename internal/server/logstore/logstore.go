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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/logparse"
)

const (
	// DefaultMaxBytesPerDay 是单来源每日写入上限的兜底值（1 GiB）。
	DefaultMaxBytesPerDay = 1 << 30
	// MaxLineBytes 是单行日志的存储上限（超出截断）——一行异常长的日志不该把存储与界面拖垮。
	MaxLineBytes = 8 << 10
	// MaxLinesPerBatch 是单批行数上限（与 Agent 侧的单轮上限同量级）。
	MaxLinesPerBatch = 20000
	// MaxFieldNamesPerSource 是单个来源允许出现的**不同**字段名上限（卡口基数控制）。
	//
	// 超过之后只接受已见过的名字：新名字被忽略（原文与已有字段都不受影响）。
	// 为什么必须有：字段名来自外部输入（日志内容），不设上限时一条被注入的日志
	// 就能把"字段目录"撑成无界集合——界面的字段筛选与将来的字段索引都会被它拖垮。
	MaxFieldNamesPerSource = 128
	// fieldCatalogFile 是字段目录的持久化文件名（放在 root 下，与来源目录同级）。
	//
	// 为什么要落盘：字段目录是"界面能给你哪些筛选候选"的唯一来源。
	// 只放内存的话，每次重启后候选都会变空（而旧分片里明明有字段），
	// 同时每来源的名字上限也被重置——那等于上限只在单次进程内成立。
	// 它是 root 下的一个普通文件，不参与分片扫描（listFiles/Sources 只认目录）。
	fieldCatalogFile = "field_names.json"
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
	// fieldNames 记录每个来源已见过的结构化字段名（卡口基数控制，见 MaxFieldNamesPerSource）。
	// 进程内状态：重启后重新积累，上限本身仍然成立。
	fieldNames map[string]map[string]struct{}
}

// New 创建存储器。root 为空返回 nil（调用方据此关闭该能力）；maxBytesPerDay ≤ 0 用兜底值。
func New(root string, maxBytesPerDay int64) *Store {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if maxBytesPerDay <= 0 {
		maxBytesPerDay = DefaultMaxBytesPerDay
	}
	s := &Store{
		root:            root,
		maxPerDay:       maxBytesPerDay,
		scanBudgetBytes: DefaultScanBudgetBytes,
		scanBudgetLines: DefaultScanBudgetLines,
		written:         map[string]int64{},
		fieldNames:      map[string]map[string]struct{}{},
	}
	s.loadFieldCatalog()
	return s
}

// loadFieldCatalog 读取字段目录（不存在或损坏时按空目录继续：它只是候选列表，
// 丢了不影响检索——下次写入会把名字重新积累起来）。
func (s *Store) loadFieldCatalog() {
	data, err := os.ReadFile(filepath.Join(s.root, fieldCatalogFile))
	if err != nil {
		return
	}
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	for source, names := range raw {
		if !model.IsValidLogSourceName(source) {
			continue
		}
		set := make(map[string]struct{}, len(names))
		for _, name := range names {
			if !model.IsValidLogFieldName(name) || len(set) >= MaxFieldNamesPerSource {
				continue
			}
			set[name] = struct{}{}
		}
		if len(set) > 0 {
			s.fieldNames[source] = set
		}
	}
}

// saveFieldCatalog 落盘字段目录（调用方已持有 s.mu）。写失败只影响候选列表，不影响落盘主链路。
func (s *Store) saveFieldCatalog() {
	raw := make(map[string][]string, len(s.fieldNames))
	for source, set := range s.fieldNames {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		raw[source] = names
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(s.root, fieldCatalogFile), data, 0o600)
}

// Sources 列出已有日志的来源（目录名，有序）。供界面的来源下拉与字段候选使用。
func (s *Store) Sources() []string {
	if s == nil {
		return nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && model.IsValidLogSourceName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// FieldNames 返回某个来源已见过的字段名（有序）。
//
// 给界面做字段筛选的候选值用：**只列服务端真的见过、且还在上限内的名字**，
// 不列"可能存在的字段"——那样用户会按一个永远查不到的名字去筛。
func (s *Store) FieldNames(source string) []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	known := s.fieldNames[source]
	out := make([]string, 0, len(known))
	for name := range known {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// extractFields 提取结构化字段并施加每来源的名字基数上限（调用方已持有 s.mu）。
func (s *Store) extractFields(source, text string) map[string]string {
	fields := parseFields(text)
	if len(fields) == 0 {
		return nil
	}
	known := s.fieldNames[source]
	if known == nil {
		known = map[string]struct{}{}
		s.fieldNames[source] = known
	}
	out := make(map[string]string, len(fields))
	added := false
	for name, value := range fields {
		if _, ok := known[name]; !ok {
			if len(known) >= MaxFieldNamesPerSource {
				continue
			}
			known[name] = struct{}{}
			added = true
		}
		out[name] = value
	}
	if added {
		// 目录只在**出现新名字**时才落盘：日志写入是高频路径，不能每批都写一次小文件。
		s.saveFieldCatalog()
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// Backend 返回后端标识。实现 logstore.LogStore。
//
// 值接收者：nil 接收者也要能回答（它会被放进接口变量后调用）。
func (s *Store) Backend() string { return BackendLocal }

// parseFields 从原文里提取结构化字段（两个后端共用同一条解析规则）。
//
// 必须共用：换后端不得改变"哪些字段能筛"，否则用户会看到同一份日志在
// 两个后端下筛出不同结果——这种不一致没有报错，只会被当成"偶发"。
func parseFields(text string) map[string]string { return logparse.Extract(text) }

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
	// 容器身份（可选）与外部后端同一套校验：两个后端必须接受/拒绝同一批输入，
	// 否则"换后端"会变成"有些 Agent 突然开始报错"。
	origin, ok := model.NormalizeLogOrigin(b.Origin)
	if !ok {
		return 0, 0, "invalid", fmt.Errorf("容器身份非法")
	}
	b.Origin = origin
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
			// 结构化字段在**落盘时**提取：与原文写在同一条 JSON 里，
			// 因此不需要另建索引，检索仍是"顺序读 + 有界扫描"（见 query.go）。
			Fields: s.extractFields(b.Source, line.Text),
			// 容器身份随行落盘：检索时要靠它把行标到 Pod 资产上，事后无处可补。
			// 批次级携带（一批 = 一个文件），这里展开到每一行。
			Origin: b.Origin,
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
