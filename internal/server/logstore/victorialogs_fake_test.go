package logstore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeVictoriaLogs 是按官方文档实现**文档化子集**的假后端。
//
// 为什么需要它：契约用例的价值在于"同一套断言跑两个真实适配器"，而 CI 里没有
// VictoriaLogs 实例。这里实现写入与检索两端，并用一个**照着文档语法写的** LogsQL
// 求值器来判断查询——适配器把语义翻译错了（例如把"精确等值"写成"按词匹配"、
// 把闭区间写成开区间）会在这里失败，而不是等到现场。
//
// **它不是真后端的替代品**：只实现本平台会产出的那几种过滤器（`*`、`~"re"`、
// `field:in(...)`、`field:="value"` 与它们的 AND 组合），也不模拟真实后端的
// 存储布局、压缩与排序细节。真实实例的联调清单见
// docs/testing/2026-10-06-platform-review-delta.md §十。
type fakeVictoriaLogs struct {
	mu       sync.Mutex
	entries  []map[string]any
	requests []string
	// forms 按顺序记录每个请求的查询参数：用例据此断言适配器发出去的
	// limit/offset/end/_stream_fields 等**参数级**细节（契约用例只看结果，看不到这些）。
	forms []url.Values
	fail  int // >0 时接下来 N 个请求返回 500（模拟后端不可用）
	srv   *httptest.Server
}

// lastForm 返回最后一个请求的参数（没有请求时返回 nil）。
func (f *fakeVictoriaLogs) lastForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.forms) == 0 {
		return nil
	}
	return f.forms[len(f.forms)-1]
}

// entriesSnapshot 返回已写入的日志副本。
func (f *fakeVictoriaLogs) entriesSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.entries))
	copy(out, f.entries)
	return out
}

func newFakeVictoriaLogs(t *testing.T) *fakeVictoriaLogs {
	t.Helper()
	f := &fakeVictoriaLogs{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

// adapter 返回指向假后端的适配器（契约用例用它当作"第二个后端"）。
func (f *fakeVictoriaLogs) adapter(t *testing.T) *VictoriaLogs {
	t.Helper()
	v, err := NewVictoriaLogs(VictoriaLogsOptions{Addr: f.srv.URL})
	if err != nil {
		t.Fatalf("创建适配器失败: %v", err)
	}
	return v
}

// failNext 让接下来 n 个请求返回 500。
func (f *fakeVictoriaLogs) failNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = n
}

func (f *fakeVictoriaLogs) handle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.requests = append(f.requests, r.URL.Path)
	f.forms = append(f.forms, r.Form)
	failing := f.fail > 0
	if failing {
		f.fail--
	}
	f.mu.Unlock()
	if failing {
		http.Error(w, "backend down", http.StatusInternalServerError)
		return
	}
	switch r.URL.Path {
	case "/insert/jsonline":
		f.insert(w, r)
	case "/select/logsql/query":
		f.query(w, r)
	case "/select/logsql/field_names":
		f.fieldNames(w, r)
	case "/select/logsql/stream_field_values", "/select/logsql/field_values":
		f.fieldValues(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeVictoriaLogs) insert(w http.ResponseWriter, r *http.Request) {
	timeField := r.FormValue("_time_field")
	if timeField == "" {
		timeField = "_time"
	}
	msgField := r.FormValue("_msg_field")
	if msgField == "" {
		msgField = "_msg"
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			// 文档行为：无效 JSON 行被跳过并继续，HTTP 仍返回成功。
			continue
		}
		if msgField != "_msg" {
			if v, ok := obj[msgField]; ok {
				obj["_msg"] = v
				delete(obj, msgField)
			}
		}
		if timeField != "_time" {
			if v, ok := obj[timeField]; ok {
				obj["_time"] = v
				delete(obj, timeField)
			}
		}
		f.entries = append(f.entries, obj)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("{}"))
}

func (f *fakeVictoriaLogs) query(w http.ResponseWriter, r *http.Request) {
	limitStr, offsetStr := r.FormValue("limit"), r.FormValue("offset")
	// 文档约束：offset 只在带 limit 时生效。这里显式拒绝，
	// 用来抓"适配器分页时忘了带 limit"这类错误。
	if offsetStr != "" && limitStr == "" {
		http.Error(w, "offset requires limit", http.StatusBadRequest)
		return
	}
	start, end, err := fakeWindow(r.FormValue("start"), r.FormValue("end"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit, offset := 0, 0
	if limitStr != "" {
		if limit, err = strconv.Atoi(limitStr); err != nil {
			http.Error(w, "bad limit", http.StatusBadRequest)
			return
		}
	}
	if offsetStr != "" {
		if offset, err = strconv.Atoi(offsetStr); err != nil {
			http.Error(w, "bad offset", http.StatusBadRequest)
			return
		}
	}
	filter, err := fakeCompileLogsQL(r.FormValue("query"))
	if err != nil {
		http.Error(w, "cannot parse query: "+err.Error(), http.StatusBadRequest)
		return
	}

	f.mu.Lock()
	matched := make([]map[string]any, 0, len(f.entries))
	for _, e := range f.entries {
		ts, ok := parseVLTime(e["_time"])
		if !ok {
			continue
		}
		// end 是**开区间**（文档明确），适配器必须自己把闭区间补成 +1ms。
		if ts < start || ts >= end {
			continue
		}
		if !filter(e) {
			continue
		}
		matched = append(matched, e)
	}
	f.mu.Unlock()

	// limit=N 返回**最新** N 条；offset 跳过最新的 M 条。
	sort.SliceStable(matched, func(i, j int) bool {
		a, _ := parseVLTime(matched[i]["_time"])
		b, _ := parseVLTime(matched[j]["_time"])
		return a > b
	})
	if offset > 0 {
		if offset >= len(matched) {
			matched = nil
		} else {
			matched = matched[offset:]
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	w.Header().Set("Content-Type", "application/stream+json")
	for _, e := range matched {
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		_, _ = w.Write(data)
		_, _ = w.Write([]byte("\n"))
	}
}

func (f *fakeVictoriaLogs) fieldNames(w http.ResponseWriter, r *http.Request) {
	filter, err := fakeCompileLogsQL(r.FormValue("query"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	f.mu.Lock()
	for _, e := range f.entries {
		if !filter(e) {
			continue
		}
		for k := range e {
			seen[k] = true
		}
	}
	f.mu.Unlock()
	fakeWriteValues(w, seen)
}

func (f *fakeVictoriaLogs) fieldValues(w http.ResponseWriter, r *http.Request) {
	field := r.FormValue("field")
	if field == "" {
		http.Error(w, "field is required", http.StatusBadRequest)
		return
	}
	filter, err := fakeCompileLogsQL(r.FormValue("query"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	f.mu.Lock()
	for _, e := range f.entries {
		if !filter(e) {
			continue
		}
		if v := fakeFieldString(e, field); v != "" {
			seen[v] = true
		}
	}
	f.mu.Unlock()
	fakeWriteValues(w, seen)
}

func fakeWriteValues(w http.ResponseWriter, set map[string]bool) {
	values := make([]string, 0, len(set))
	for v := range set {
		values = append(values, v)
	}
	sort.Strings(values)
	out := struct {
		Values []map[string]any `json:"values"`
	}{}
	for _, v := range values {
		out.Values = append(out.Values, map[string]any{"value": v, "hits": 1})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// fakeParseDuration 解析 LogsQL 的相对时长（文档允许 `5m` / `2.5d15m` 这类写法）。
// 假后端只覆盖"一个数值 + 一个单位"的常见形态，遇到复合写法直接报错——
// 报错好过静默当成别的时长（那会让用例给出错误的通过结论）。
func fakeParseDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("空时长")
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.ParseFloat(strings.TrimSuffix(raw, "d"), 64)
		if err != nil {
			return 0, fmt.Errorf("非法时长 %q", raw)
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("非法时长 %q", raw)
	}
	return d, nil
}

// fakeWindow 解析 start/end（适配器发的是 RFC3339）；空值按"不设边界"处理。
func fakeWindow(startRaw, endRaw string) (int64, int64, error) {
	start := int64(-1) << 62
	end := int64(1) << 62
	if strings.TrimSpace(startRaw) != "" {
		ts, ok := parseVLTime(startRaw)
		if !ok {
			return 0, 0, fmt.Errorf("bad start: %q", startRaw)
		}
		start = ts
	}
	if strings.TrimSpace(endRaw) != "" {
		ts, ok := parseVLTime(endRaw)
		if !ok {
			return 0, 0, fmt.Errorf("bad end: %q", endRaw)
		}
		end = ts
	}
	return start, end, nil
}

// ---- 文档化子集的 LogsQL 求值器 ----

func fakeCompileLogsQL(query string) (func(map[string]any) bool, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	parts := fakeSplitAnd(query)
	conds := make([]func(map[string]any) bool, 0, len(parts))
	for _, part := range parts {
		cond, err := fakeCompileFilter(part)
		if err != nil {
			return nil, err
		}
		conds = append(conds, cond)
	}
	return func(e map[string]any) bool {
		for _, cond := range conds {
			if !cond(e) {
				return false
			}
		}
		return true
	}, nil
}

// fakeSplitAnd 按顶层 " AND " 切分（跳过括号内与引号内的内容）。
func fakeSplitAnd(s string) []string {
	const sep = " AND "
	var parts []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], sep):
			parts = append(parts, strings.TrimSpace(s[start:i]))
			i += len(sep) - 1
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(s[start:]))
}

func fakeCompileFilter(part string) (func(map[string]any) bool, error) {
	part = strings.TrimSpace(part)
	// 适配器保证每个条件都被一对括号包住（见 buildLogsQL）。
	if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
		part = strings.TrimSpace(part[1 : len(part)-1])
	}
	if part == "*" {
		return func(map[string]any) bool { return true }, nil
	}
	if raw, ok := strings.CutPrefix(part, "_time:"); ok {
		// 相对时间窗（`_time:7d`）：元数据查询用它把代价限住，
		// 假后端按"最近 N 时间"求值。
		d, err := fakeParseDuration(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		from := time.Now().Add(-d).UnixMilli()
		return func(e map[string]any) bool {
			ts, ok := parseVLTime(e["_time"])
			return ok && ts >= from
		}, nil
	}
	if strings.HasPrefix(part, "~") {
		pattern, err := fakeUnquote(part[1:])
		if err != nil {
			return nil, err
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("bad regex %q: %w", pattern, err)
		}
		return func(e map[string]any) bool {
			msg, _ := e["_msg"].(string)
			return re.MatchString(msg)
		}, nil
	}
	if i := strings.Index(part, ":="); i > 0 {
		name, err := fakeFieldName(part[:i])
		if err != nil {
			return nil, err
		}
		value, err := fakeUnquote(part[i+2:])
		if err != nil {
			return nil, err
		}
		return func(e map[string]any) bool { return fakeFieldString(e, name) == value }, nil
	}
	if i := strings.Index(part, ":in("); i > 0 {
		name, err := fakeFieldName(part[:i])
		if err != nil {
			return nil, err
		}
		raw := strings.TrimSpace(part[i+len(":in("):])
		raw = strings.TrimSuffix(raw, ")")
		values, err := fakeSplitQuotedList(raw)
		if err != nil {
			return nil, err
		}
		return func(e map[string]any) bool {
			got := fakeFieldString(e, name)
			for _, want := range values {
				if got == want {
					return true
				}
			}
			return false
		}, nil
	}
	return nil, fmt.Errorf("假后端未实现的过滤器（只覆盖文档化子集）: %q", part)
}

func fakeFieldName(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, `"`) {
		return fakeUnquote(raw)
	}
	if raw == "" {
		return "", fmt.Errorf("空字段名")
	}
	return raw, nil
}

func fakeFieldString(e map[string]any, name string) string {
	s, _ := e[name].(string)
	return s
}

// fakeUnquote 解析字符串字面量（文档支持三种引号，`\` 转义下一个字符）。
func fakeUnquote(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 {
		return "", fmt.Errorf("非法字面量: %q", raw)
	}
	quote := raw[0]
	if quote != '"' && quote != '\'' && quote != '`' {
		return "", fmt.Errorf("非法字面量（缺少引号）: %q", raw)
	}
	if raw[len(raw)-1] != quote {
		return "", fmt.Errorf("字面量引号不配对: %q", raw)
	}
	body := raw[1 : len(raw)-1]
	if quote == '`' {
		return body, nil
	}
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' && i+1 < len(body) {
			i++
		}
		b.WriteByte(body[i])
	}
	return b.String(), nil
}

func fakeSplitQuotedList(raw string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if quote != 0 {
			if c == '\\' && i+1 < len(raw) {
				i++
				cur.WriteByte('\\')
				cur.WriteByte(raw[i])
				continue
			}
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		}
		switch {
		case c == '"' || c == '\'' || c == '`':
			quote = c
			cur.WriteByte(c)
		case c == ',':
			value, err := fakeUnquote(cur.String())
			if err != nil {
				return nil, err
			}
			out = append(out, value)
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		value, err := fakeUnquote(cur.String())
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

