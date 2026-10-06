package logstore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 检索（C2 子批次 C）：**不建索引也能有界扫描**。
//
// 三层有界：
//  1. 分片目录先把「时间 + 来源 + 节点」筛成少数文件——绝大多数文件根本不用打开；
//  2. 打开的文件**从末尾反向逐行读**（文件是追加写的，时间戳单调不减）——
//     于是天然得到「时间倒序」，且游标只需记「文件内绝对字节偏移」：
//     翻页既不重复也不丢行（用「距末尾多少字节」做游标在文件增长时会跳行，所以不用它）；
//  3. 全局扫描预算（字节/行数）兜底——一次查询的代价必须有上限，
//     否则「查不到东西」会变成「把 Server 拖住」。
const (
	// DefaultLimit 是单页默认命中数。
	DefaultLimit = 200
	// MaxLimit 是单页命中数上限；再大就该缩小时间范围，而不是让一次请求返回十万行。
	MaxLimit = 1000
	// MaxRegexLen 是正则模式长度上限。RE2 无回溯风险，但代价随模式长度增长，
	// 而模式来自外部输入，必须设一个天花板。
	MaxRegexLen = 256
	// MaxKeywordLen 是关键词长度上限（同上：外部输入）。
	MaxKeywordLen = 256
	// scanChunkBytes 是反向读取的块大小。
	scanChunkBytes = 64 << 10
	// DefaultScanBudgetBytes 是单次查询允许扫描的字节上限。
	DefaultScanBudgetBytes = 64 << 20
	// DefaultScanBudgetLines 是单次查询允许扫描的行数上限。
	DefaultScanBudgetLines = 200000
)

// Cursor 是翻页游标。**字段分属不同后端**，因此必须按 Backend 区分：
// 把一个后端的游标喂给另一个后端，轻则空页、重则静默跳行——而"少了几行日志"
// 这种症状在现场极难归因。Backend 因此是必填项，由 checkCursor 在 Query 入口校验。
//
// 空 Backend 视为**本地后端**：接口引入前只有自研落盘一种实现，历史游标必须继续可用。
//
// 本地后端用「文件 + 文件内绝对字节偏移」（该偏移之后的部分均已扫描过）：
// 用绝对偏移而不是「距末尾的字节数」——日志文件是追加写的，用距末尾的距离做游标，
// 一旦文件在两次翻页之间长大，续读就会跳过还没看过的老行（静默丢数据）。
type Cursor struct {
	Backend string `json:"b,omitempty"` // 后端标识（BackendLocal / BackendVictoriaLogs）
	File    string `json:"f,omitempty"` // 本地后端：相对 root 的文件路径
	Offset  int64  `json:"o,omitempty"` // 本地后端：文件内绝对字节偏移（从文件头算）
	// Skip 是外部后端的续读位置：跳过「最新的 N 条」（VictoriaLogs 的 limit+offset 分页）。
	// 与本地后端的偏移语义不同，因此两者不能互相翻译——这也是 Backend 必填的原因。
	Skip int `json:"k,omitempty"`
}

// EncodeCursor 把游标编码成不透明字符串（前端只回传，不解析）。
// 没有任何续读位置时返回空串：空游标对前端意味着「没有下一页」。
func EncodeCursor(c Cursor) string {
	if c.File == "" && c.Skip == 0 {
		return ""
	}
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor 解析游标；空串表示从头开始。
func DecodeCursor(s string) (Cursor, error) {
	if strings.TrimSpace(s) == "" {
		return Cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("游标格式非法")
	}
	var c Cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return Cursor{}, fmt.Errorf("游标内容非法")
	}
	return c, nil
}

// SetScanBudget 调整扫描预算（默认 64 MiB / 20 万行）。
func (s *Store) SetScanBudget(bytes, lines int64) {
	if bytes > 0 {
		s.scanBudgetBytes = bytes
	}
	if lines > 0 {
		s.scanBudgetLines = lines
	}
}

// Query 执行一次有界检索。返回的行按时间倒序（同日内跨节点也会在同页内重排，见下方说明）。
func (s *Store) Query(q model.LogQuery, cursor Cursor) (model.LogQueryResult, error) {
	if s == nil {
		return model.LogQueryResult{}, fmt.Errorf("集中日志存储未启用")
	}
	// 游标必须属于本后端：另一个后端的游标在这里没有任何意义，
	// 硬当成偏移用会静默跳行（见 Cursor 的说明）。
	if err := checkCursor(cursor, BackendLocal); err != nil {
		return model.LogQueryResult{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	var re *regexp.Regexp
	if q.Regex != "" {
		r, err := regexp.Compile(q.Regex)
		if err != nil {
			return model.LogQueryResult{}, fmt.Errorf("正则非法：%v", err)
		}
		re = r
	}

	files, err := s.listFiles(q)
	if err != nil {
		return model.LogQueryResult{}, err
	}

	res := model.LogQueryResult{Lines: []model.LogHit{}}
	budgetBytes := s.budgetBytes()
	budgetLines := s.budgetLines()

	// 游标定位：游标指向的文件若已不在候选集（被保留策略清理掉），
	// 从候选集开头重来——宁可重复几行，也不静默丢数据。
	startIdx := 0
	if cursor.File != "" {
		for i, rel := range files {
			if rel == cursor.File {
				startIdx = i
				break
			}
		}
	}

	match := func(h model.LogHit) bool {
		if h.Ts < q.From || h.Ts > q.To {
			return false
		}
		if len(q.Nodes) > 0 && !containsStr(q.Nodes, h.Node) {
			return false
		}
		if len(q.Sources) > 0 && !containsStr(q.Sources, h.Source) {
			return false
		}
		// 容器身份过滤：没有身份的行不命中任何容器条件（它不属于任何 Pod）
		if !h.Origin.MatchesPodFilter(q.Pods) {
			return false
		}
		// 结构化字段是**精确**等值匹配（且要求全部命中）：
		// 「status=500」用关键词会命中任何含该串的行（包括别的字段的值），那不是语义正确的筛法。
		if !matchFields(h.Fields, q.Fields) {
			return false
		}
		switch {
		case re != nil:
			return re.MatchString(h.Text)
		case q.Keyword != "":
			return strings.Contains(h.Text, q.Keyword)
		}
		return true
	}

	// 命中上限达成时的游标（指向**尚未扫描的部分**，即最后一条命中所在行的起点）。
	var next Cursor
	stop := false
	for i := startIdx; i < len(files) && !stop; i++ {
		rel := files[i]
		offset := int64(0) // 0 表示「从文件末尾开始」
		if i == startIdx && cursor.File == rel {
			if cursor.Offset <= 0 {
				// 该文件已扫完（游标停在文件头）→ 直接看下一个文件。
				//
				// 这一步不能省：0 在 scanBackward 里表示「从文件末尾开始」，
				// 若把「已扫完」也编码成 0，续读会把同一批结果**再返回一次**
				// （单页刚好等于文件内容时必现——这是实机验证抓出来的）。
				continue
			}
			offset = cursor.Offset
		}
		res.Files++
		path := filepath.Join(s.root, rel)
		err := scanBackward(path, offset, func(lineStart int64, line []byte) bool {
			res.ScannedLines++
			res.ScannedBytes += int64(len(line)) + 1
			var hit model.LogHit
			if json.Unmarshal(line, &hit) == nil && match(hit) {
				res.Lines = append(res.Lines, hit)
			}
			// 游标一律指向**本行起点**：续读从这里往文件头方向继续，
			// 因此本行不会被重复返回，也不会被跳过。
			if len(res.Lines) >= limit {
				// 已够一页
				next = Cursor{Backend: BackendLocal, File: rel, Offset: lineStart}
				res.Truncated = true
				stop = true
				return false
			}
			if res.ScannedBytes >= budgetBytes || res.ScannedLines >= budgetLines {
				// 预算用尽：按行判断（而不是每个文件读完才判断），
				// 否则一个超大文件会被整份扫完——「有界」就名不副实了。
				next = Cursor{Backend: BackendLocal, File: rel, Offset: lineStart}
				res.Truncated = true
				stop = true
				return false
			}
			return true
		})
		if err != nil {
			// 单个文件读失败（被清理、权限问题）不该让整次查询失败；计入诊断继续往前找
			continue
		}
	}

	// 同页内按时间倒序：跨文件（不同节点/来源同一天）天然是有序的，但同一页里多文件拼接后
	// 可能不完全严格递减。这里只对**这一页**（≤ limit）排序，代价可忽略。
	sort.SliceStable(res.Lines, func(i, j int) bool { return res.Lines[i].Ts > res.Lines[j].Ts })

	if res.Truncated {
		res.Cursor = EncodeCursor(next)
	}
	return res, nil
}

// listFiles 按「时间 + 来源 + 节点」筛出候选文件（相对 root 的路径）。
func (s *Store) listFiles(q model.LogQuery) ([]string, error) {
	fromDate := time.UnixMilli(q.From).Format("2006-01-02")
	toDate := time.UnixMilli(q.To).Format("2006-01-02")

	sources := q.Sources
	if len(sources) == 0 {
		entries, err := os.ReadDir(s.root)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil // 还没有任何日志：空结果，不是错误
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				sources = append(sources, e.Name())
			}
		}
	}

	var out []string
	for _, src := range sources {
		if !model.IsValidLogSourceName(src) {
			continue
		}
		dates, err := os.ReadDir(filepath.Join(s.root, src))
		if err != nil {
			continue
		}
		for _, d := range dates {
			if !d.IsDir() {
				continue
			}
			date := d.Name()
			// 日期目录名是 YYYY-MM-DD，字典序即时间序
			if date < fromDate || date > toDate {
				continue
			}
			files, err := os.ReadDir(filepath.Join(s.root, src, date))
			if err != nil {
				continue
			}
			for _, f := range files {
				if f.IsDir() || !strings.HasSuffix(f.Name(), ".log") {
					continue
				}
				node := strings.TrimSuffix(f.Name(), ".log")
				if len(q.Nodes) > 0 && !containsStr(q.Nodes, node) {
					continue
				}
				out = append(out, src+"/"+date+"/"+f.Name())
			}
		}
	}
	// 确定性排序（游标翻页依赖它稳定）：日期新→旧，其次来源、节点
	sort.Slice(out, func(i, j int) bool {
		di, si, ni := splitRel(out[i])
		dj, sj, nj := splitRel(out[j])
		if di != dj {
			return di > dj
		}
		if si != sj {
			return si < sj
		}
		return ni < nj
	})
	return out, nil
}

// scanBackward 从 offset 起向文件头方向逐行读取，每行回调 (lineStart, line)。
// offset 为 0 或越界时从文件末尾开始；回调返回 false 立即停止。
func scanBackward(path string, offset int64, onLine func(lineStart int64, line []byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if offset <= 0 || offset > size {
		offset = size
	}

	pos := offset
	buf := make([]byte, scanChunkBytes)
	var pending []byte // 更早位置的半行：它的左半部分尚未读到，右半部分已在 pending
	for pos > 0 {
		n := int64(len(buf))
		if pos < n {
			n = pos
		}
		start := pos - n
		if _, err := f.ReadAt(buf[:n], start); err != nil && err != io.EOF {
			return err
		}
		chunk := buf[:n]

		// 从右往左按 '\n' 切：块尾恰好是行边界（游标总落在行首，且文件是追加写的），
		// 因此最右侧的一段是**完整行**；最左侧的一段可能是半行（左边界在 start 之前）。
		first := true
		segEnd := len(chunk)
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] != '\n' {
				continue
			}
			seg := chunk[i+1 : segEnd]
			segStart := start + int64(i+1)
			segEnd = i
			if first && len(pending) > 0 {
				// 与上一块挂起的半行拼成一条完整行（本段是左半部分）
				line := make([]byte, 0, len(seg)+len(pending))
				line = append(line, seg...)
				line = append(line, pending...)
				pending = nil
				first = false
				if !onLine(segStart, line) {
					return nil
				}
				continue
			}
			first = false
			if len(seg) > 0 && !onLine(segStart, seg) {
				return nil
			}
		}
		if segEnd > 0 {
			left := make([]byte, segEnd)
			copy(left, chunk[:segEnd])
			pending = append(left, pending...)
		}
		pos = start
	}
	if len(pending) > 0 {
		// 走到文件开头仍未遇到分隔符 → 这就是第一行
		onLine(0, pending)
	}
	return nil
}

// splitRel 拆分相对路径 src/date/node.log。
func splitRel(rel string) (date, source, node string) {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 {
		return rel, "", ""
	}
	return parts[1], parts[0], strings.TrimSuffix(parts[2], ".log")
}

// matchFields 判断命中行的字段是否包含查询要求的**全部**键值（全等）。
// want 为空表示不按字段过滤；命中行没有字段（老分片或纯文本日志）时一律不匹配——
// 那是"这行没有这个字段"，而不是"这个字段的值恰好为空"。
func matchFields(have, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
