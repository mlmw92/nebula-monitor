package logstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// VictoriaLogs 是外部日志后端适配器。
//
// 为什么是它：Apache-2.0、单二进制、与现用 VictoriaMetrics 同厂（调研报告 §2.3、
// 决策记录 docs/adr/0002-log-backend-abstraction.md）。默认**不启用**：默认后端仍是
// 自研分片落盘，离线包的默认真空不因此改变。
//
// 接口面按官方文档实现（2026-10-06 取一手文档核对）：
//
//   - 写入 `POST /insert/jsonline`：请求体是 NDJSON（每行一个 JSON 对象），
//     `Content-Type: application/stream+json`；参数 `_stream_fields` / `_time_field` /
//     `_msg_field` 指定哪些字段是流标签、时间与消息。
//   - 检索 `POST /select/logsql/query`：参数 `query`（LogsQL）、`start`/`end`
//     （**end 是开区间**）、`limit`（返回最新 N 条）、`offset`（跳过最新 M 条，
//     需与 limit 同时给）、`timeout`；响应是 JSON Lines，每行含 `_time`（RFC3339）、
//     `_msg`、`_stream`、`_stream_id` 及其余字段。
//   - 字段目录 `POST /select/logsql/field_names` 与 `/select/logsql/stream_field_values`，
//     响应形如 `{"values":[{"value":"...","hits":N}]}`。
//
// **与本地后端的能力差异（是后端能力边界，不是实现遗漏）**：
//
//   - 没有"逐文件有界扫描"，因此三项扫描诊断（ScannedBytes/ScannedLines/Files）恒为 0。
//     界面只在"没有命中"时才展示扫描诊断，因此这里不会显示成"扫了 0 个文件"的假象。
//   - 单来源每日字节上限不适用：容量治理由后端自己负责（VictoriaLogs 的 `-retentionPeriod`）。
//     平台的**上行限速与请求体上限仍然生效**（它们在 receiver 侧，与后端无关）。
//   - 分页按 `limit+offset`（每次续读重跑一次查询），而不是本地后端的"文件内绝对偏移续读"。
//
// **已在真实 VictoriaLogs（v1.53.0）上完成八项联调（2026-10-06）**：写入/时间闭区间/
// 分页不重不漏/方言/元数据/容量/部署/故障演练全部通过，逐项证据见
// docs/testing/2026-10-06-platform-review-delta.md §10.4。
//
// 联调发现的一条**使用侧特性**：写入成功到可被检索之间有**秒级延迟**（后端的内存数据
// flush 周期）。现场若"刚上报就搜不到"，先看 `vl_rows_ingested_total` 是否已增长——
// 增长即已落库，等几秒再查即可，不是丢日志。
type VictoriaLogs struct {
	addr        string
	writeClient *http.Client
	queryClient *http.Client
}

// 外部后端的默认超时。查询默认更宽松：它可能扫一整天数据。
const (
	vlDefaultWriteTimeout = 10 * time.Second
	vlDefaultQueryTimeout = 30 * time.Second
	// vlMaxResponseBytes 是单次检索响应的读取上限（安全网）。
	// 正常情况下 limit+1 行（≤1001 行 × 单行 8 KiB）远小于它。
	vlMaxResponseBytes = 64 << 20
	// vlMaxCatalogValues 是字段/来源候选列表的客户端截断条数。
	// 后端这两个接口没有 limit 参数，而候选列表是给下拉框用的，多了反而没法选。
	vlMaxCatalogValues = 500
	// vlCatalogWindow 是元数据查询的时间窗。
	// 不加时间窗的元数据查询等于让后端扫全量——"看一下有哪些字段"不该是重操作。
	vlCatalogWindow = "7d"
)

// NewVictoriaLogs 创建外部后端适配器。addr 为空或不是合法 URL 时返回错误。
//
// **不在启动时探测连通性**：后端可能比 Server 晚起（同机部署的常见顺序），
// 启动即失败会让"日志暂时不可用"升级成"平台起不来"。真正的连通性问题
// 会在写入/检索时以明确的错误暴露出来。
func NewVictoriaLogs(opt VictoriaLogsOptions) (*VictoriaLogs, error) {
	addr := strings.TrimRight(strings.TrimSpace(opt.Addr), "/")
	if addr == "" {
		return nil, fmt.Errorf("日志后端 victorialogs 需要配置 addr（如 http://127.0.0.1:9428）")
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("日志后端 addr 非法: %q（应形如 http://127.0.0.1:9428）", opt.Addr)
	}
	wt, qt := opt.WriteTimeout, opt.QueryTimeout
	if wt <= 0 {
		wt = vlDefaultWriteTimeout
	}
	if qt <= 0 {
		qt = vlDefaultQueryTimeout
	}
	return &VictoriaLogs{
		addr:        addr,
		writeClient: &http.Client{Timeout: wt},
		queryClient: &http.Client{Timeout: qt},
	}, nil
}

// Backend 返回后端标识。实现 logstore.LogStore。
func (v *VictoriaLogs) Backend() string { return BackendVictoriaLogs }

// 写入时绑定的流字段。选 node + source：它们的基数由"机器数 × 来源数"决定，
// 是典型低基数维度；pattern 与解析出的业务字段基数不可控，绝不能进流标签
// （后端会为每个不同的流建独立索引，高基数的流字段等于把索引写爆）。
const vlStreamFields = "node,source"

// vlReservedFields 是协议保留字段：解析出的业务字段若与它们同名，必须丢弃。
// 否则一条日志的内容就能覆盖它自己的来源/节点/时间（等于伪造归属）。
//
// k8s_* 三个身份字段同样在列：它们是**采集侧解析出的容器身份**，若允许日志正文
// 覆盖它们，一条日志就能把自己标到别人的 Pod 资产上——那是伪造归属，不是格式问题。
var vlReservedFields = map[string]struct{}{
	"_time": {}, "_msg": {}, "_stream": {}, "_stream_id": {},
	"node": {}, "source": {}, "pattern": {},
	vlFieldOriginNamespace: {}, vlFieldOriginPod: {}, vlFieldOriginContainer: {},
}

// 容器身份在后端里的字段名。加 k8s_ 前缀是为了不与正文里解析出的业务字段撞名
// （正文里出现 pod / namespace 这类名字太常见了）。
const (
	vlFieldOriginNamespace = "k8s_namespace"
	vlFieldOriginPod       = "k8s_pod"
	vlFieldOriginContainer = "k8s_container"
)

// Append 写入一批日志，返回接受与丢弃的行数。
//
// 与本地后端的差异：没有单来源每日上限（容量治理归后端），因此正常路径下
// dropped 恒为 0；reason 只在输入非法时出现（与本地后端同一套校验）。
func (v *VictoriaLogs) Append(b model.LogBatch) (accepted, dropped int, reason string, err error) {
	if v == nil {
		return 0, 0, "", fmt.Errorf("集中日志存储未启用")
	}
	// 校验与本地后端逐条对齐：两个后端必须接受/拒绝同一批输入，
	// 否则"换后端"会变成"有些 Agent 突然开始报错"。
	if !model.IsValidLogSourceName(b.Source) {
		return 0, 0, "invalid", fmt.Errorf("source 名非法")
	}
	// 容器身份与本地后端同一套校验：两个后端必须接受/拒绝同一批输入
	origin, ok := model.NormalizeLogOrigin(b.Origin)
	if !ok {
		return 0, 0, "invalid", fmt.Errorf("容器身份非法")
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

	var buf bytes.Buffer
	for _, line := range b.Lines {
		// 截断与字段提取与本地后端共用同一套规则：写进去的东西必须一致，
		// 否则同一份日志在两个后端下会筛出不同结果。
		ts := clampTS(line.Ts)
		text := truncate(line.Text, MaxLineBytes)
		obj := make(map[string]any, 6)
		// 时间用 RFC3339 而不是 Unix 毫秒：后端对数字时间戳要按**数量级**猜单位
		// （秒/毫秒/微秒/纳秒都合法），猜错就是把日志写到几万年以后。RFC3339 没有歧义。
		obj["_time"] = time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
		obj["_msg"] = text
		obj["node"] = node
		obj["source"] = b.Source
		if line.Pattern != "" {
			obj["pattern"] = line.Pattern
		}
		if origin != nil {
			// 身份字段**最后写**（且在解析字段之前已由 vlReservedFields 挡掉同名业务字段）：
			// 它来自采集侧解析出的文件路径，是权威值。
			obj[vlFieldOriginNamespace] = origin.Namespace
			obj[vlFieldOriginPod] = origin.Pod
			obj[vlFieldOriginContainer] = origin.Container
		}
		for k, val := range parseFields(text) {
			if _, reserved := vlReservedFields[k]; reserved {
				continue
			}
			obj[k] = val
		}
		data, err := json.Marshal(obj)
		if err != nil {
			dropped++
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return 0, dropped, "", nil
	}

	q := url.Values{}
	q.Set("_stream_fields", vlStreamFields)
	q.Set("_time_field", "_time")
	q.Set("_msg_field", "_msg")
	req, err := http.NewRequest(http.MethodPost, v.addr+"/insert/jsonline?"+q.Encode(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return 0, 0, "writeError", err
	}
	req.Header.Set("Content-Type", "application/stream+json")
	resp, err := v.writeClient.Do(req)
	if err != nil {
		return 0, 0, "writeError", fmt.Errorf("%w: 写入日志后端失败: %v", ErrBackendUnavailable, err)
	}
	defer resp.Body.Close()
	// 成功响应体没有文档承诺，因此只读一小段用于失败时的诊断信息。
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
	if resp.StatusCode/100 != 2 {
		msg := fmt.Errorf("%w: 写入日志后端失败: HTTP %d %s", ErrBackendUnavailable, resp.StatusCode, strings.TrimSpace(string(snippet)))
		// 4xx 是"这批数据后端不收"（请求构造问题），5xx/网络是"后端不可用"。
		// 两者的处置完全不同：前者要改写入逻辑，后者要去看后端。
		if resp.StatusCode < 500 {
			msg = fmt.Errorf("写入日志后端被拒绝: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		}
		return 0, 0, "writeError", msg
	}
	// 注意：jsonline 对**无效 JSON 行**是"跳过并记 warning"，HTTP 仍返回成功。
	// 我们发出去的行由 json.Marshal 生成，不会出现无效 JSON，因此不额外校验。
	return len(b.Lines), dropped, "", nil
}

// Query 执行一次有界检索。实现 logstore.LogStore。
func (v *VictoriaLogs) Query(q model.LogQuery, cursor Cursor) (model.LogQueryResult, error) {
	if v == nil {
		return model.LogQueryResult{}, fmt.Errorf("集中日志存储未启用")
	}
	if err := checkCursor(cursor, BackendVictoriaLogs); err != nil {
		return model.LogQueryResult{}, err
	}
	// 正则先在本侧校验：否则非法正则会以"后端 400"的形式回来，
	// 用户看到的是"查询失败"而不是"正则写错了"（与本地后端行为一致）。
	if q.Regex != "" {
		if _, err := regexp.Compile(q.Regex); err != nil {
			return model.LogQueryResult{}, fmt.Errorf("正则非法：%v", err)
		}
	}
	limit := normalizeLimit(q.Limit)

	form := url.Values{}
	form.Set("query", buildLogsQL(q))
	form.Set("start", time.UnixMilli(q.From).UTC().Format(time.RFC3339Nano))
	// 平台的 To 是**闭区间**（毫秒，含），而后端的 end 是开区间：
	// 必须 +1ms，否则"查到某一毫秒为止"的那一行会被悄悄丢掉。
	form.Set("end", time.UnixMilli(q.To+1).UTC().Format(time.RFC3339Nano))
	// 多要一行来判断"是否还有更多"：Truncated 是契约的一部分，不能靠猜。
	form.Set("limit", strconv.Itoa(limit+1))
	if cursor.Skip > 0 {
		form.Set("offset", strconv.Itoa(cursor.Skip))
	}
	if v.queryClient.Timeout > 0 {
		form.Set("timeout", v.queryClient.Timeout.String())
	}

	req, err := http.NewRequest(http.MethodPost, v.addr+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return model.LogQueryResult{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.queryClient.Do(req)
	if err != nil {
		return model.LogQueryResult{}, fmt.Errorf("%w: 查询日志后端失败: %v", ErrBackendUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		// 400 通常是查询语句被后端拒绝（方言不被支持），必须让用户看到原文，
		// 否则只能看到"查询失败"，无从判断是数据问题还是我们的翻译问题。
		if resp.StatusCode < 500 {
			return model.LogQueryResult{}, fmt.Errorf("查询被日志后端拒绝: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
		}
		return model.LogQueryResult{}, fmt.Errorf("%w: 查询日志后端失败: HTTP %d %s", ErrBackendUnavailable, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, vlMaxResponseBytes))
	if err != nil {
		return model.LogQueryResult{}, fmt.Errorf("%w: 读取日志后端响应失败: %v", ErrBackendUnavailable, err)
	}
	hits, parseErr := parseVLLines(raw)
	if parseErr != nil {
		return model.LogQueryResult{}, parseErr
	}
	// 输出顺序由本侧归一：后端在带 limit 时返回"最新 N 条"，但同一毫秒内的
	// 相对顺序不作承诺——接口契约要求时间倒序，不能把顺序问题留给界面。
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Ts > hits[j].Ts })

	res := model.LogQueryResult{Lines: []model.LogHit{}}
	if len(hits) > limit {
		res.Lines = hits[:limit]
		res.Truncated = true
		// 续读位置 = 已返回条数（后端按"最新 N 条"分页）。
		// 注意：这依赖查询的时间窗在后端侧不变（前端每页都会带上同一个 from/to），
		// 与本地后端的"文件内绝对偏移"是两种不同性质的续读。
		res.Cursor = EncodeCursor(Cursor{Backend: BackendVictoriaLogs, Skip: cursor.Skip + limit})
		return res, nil
	}
	res.Lines = hits
	return res, nil
}

// Sources 列出已有日志的来源（有序）。实现 logstore.LogStore。
//
// 写入时把 source 绑成了流字段，因此先问流字段值；为空再退回普通字段值——
// 同一个 VictoriaLogs 里可能有别的工具灌进来的数据（那种数据没绑流字段）。
func (v *VictoriaLogs) Sources() []string {
	if v == nil {
		return nil
	}
	for _, endpoint := range []string{"stream_field_values", "field_values"} {
		values, err := v.fieldValues(endpoint, "source")
		if err != nil || len(values) == 0 {
			continue
		}
		sort.Strings(values)
		return values
	}
	return nil
}

// FieldNames 返回某来源已见过的结构化字段名（有序）。实现 logstore.LogStore。
//
// 与本地后端的口径对齐：**只列业务字段**——协议与流字段（`_` 前缀、node/source/pattern）
// 不列。它们要么有自己的查询参数（节点、来源），要么是消息本身（`_msg`），
// 列进"字段筛选"只会让人以为筛 `_msg` 比关键词更精确。
func (v *VictoriaLogs) FieldNames(source string) []string {
	if v == nil {
		return nil
	}
	// 显式写 AND（而不是靠文档允许的隐式 AND）：查询语句只有一个来源，
	// 显式写法的解析结果不依赖"空格即 AND"这条规则，排障时也更好读。
	query := "_time:" + vlCatalogWindow
	if model.IsValidLogSourceName(source) {
		query = "source:=" + logsQLQuote(source) + " AND " + query
	}
	form := url.Values{}
	form.Set("query", query)
	values, err := v.valuesOf("field_names", form)
	if err != nil {
		// 候选列表拿不到不该让检索不可用：返回空列表（界面退化成"手动输入字段名"）。
		return nil
	}
	out := make([]string, 0, len(values))
	for _, name := range values {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if _, reserved := vlReservedFields[name]; reserved {
			continue
		}
		if !model.IsValidLogFieldName(name) {
			// 与写入侧的字段名规则对齐：列一个平台自己都筛不了的名字没有意义。
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) > vlMaxCatalogValues {
		out = out[:vlMaxCatalogValues]
	}
	return out
}

// fieldValues 取某个字段的全部取值（优先流字段）。
func (v *VictoriaLogs) fieldValues(endpoint, field string) ([]string, error) {
	form := url.Values{}
	form.Set("field", field)
	form.Set("query", "_time:"+vlCatalogWindow)
	form.Set("limit", strconv.Itoa(vlMaxCatalogValues))
	return v.valuesOf(endpoint, form)
}

// valuesOf 调用返回 `{"values":[{"value":...,"hits":...}]}` 形态的元数据接口。
func (v *VictoriaLogs) valuesOf(endpoint string, form url.Values) ([]string, error) {
	req, err := http.NewRequest(http.MethodPost, v.addr+"/select/logsql/"+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.queryClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: 查询日志后端元数据失败: %v", ErrBackendUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return nil, fmt.Errorf("查询日志后端元数据失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var out struct {
		Values []struct {
			Value string `json:"value"`
		} `json:"values"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, vlMaxResponseBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析日志后端元数据失败: %w", err)
	}
	values := make([]string, 0, len(out.Values))
	for _, item := range out.Values {
		if v := strings.TrimSpace(item.Value); v != "" {
			values = append(values, v)
		}
	}
	return values, nil
}

// parseVLLines 解析 JSON Lines 响应。
//
// 逐行解析而不是整体 json.Unmarshal：响应是流式 JSON Lines（每行一条日志），
// 整体解析会因为"第一行之后还有内容"直接失败。
func parseVLLines(raw []byte) ([]model.LogHit, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	// 单行上限：单条日志本身有 MaxLineBytes 上限，JSON 转义后会更长，
	// 给足余量；超过的行按"这一行不完整"跳过，而不是让整个查询失败。
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	out := make([]model.LogHit, 0, 64)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(line, &obj); err != nil {
			// 单行解析失败不中断：能返回多少返回多少，好过整页失败。
			continue
		}
		ts, ok := parseVLTime(obj["_time"])
		if !ok {
			// 没有时间戳的行无法参与"时间倒序"的契约，也无法定位，直接跳过。
			continue
		}
		text, _ := obj["_msg"].(string)
		hit := model.LogHit{
			Ts:   clampTS(ts),
			Node: stringField(obj, "node"),
			Source: stringField(obj, "source"),
			Text: truncate(text, MaxLineBytes),
		}
		if hit.Node != "" {
			// 与写入侧同一套净化：写进去的节点名是净化过的，读出来也必须一致，
			// 否则检索按节点过滤会时灵时不灵。
			hit.Node = sanitizeNodeName(hit.Node)
		}
		hit.Pattern = stringField(obj, "pattern")
		// 容器身份从**存储字段**读回（与 node/source/pattern 同一取向），而不是从正文猜：
		// 正文里恰好出现 k8s_pod=... 的行不该被当成"来自某个 Pod"。
		if o, ok := model.NormalizeLogOrigin(&model.LogOrigin{
			Namespace: stringField(obj, vlFieldOriginNamespace),
			Pod:       stringField(obj, vlFieldOriginPod),
			Container: stringField(obj, vlFieldOriginContainer),
		}); ok {
			hit.Origin = o
		}
		// 字段在**读取时**按同一套规则重新提取：后端里存的字段可能来自别的写入工具
		// （或旧版本的规则），重新提取才能保证"两个后端返回的 Fields 完全一致"。
		hit.Fields = parseFields(hit.Text)
		out = append(out, hit)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取日志后端响应失败: %w", err)
	}
	return out, nil
}

func stringField(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// parseVLTime 解析后端返回的时间戳。
//
// 文档给出的是 RFC3339（可带纳秒），但同一份数据也可能来自别的写入工具
// （Unix 秒/毫秒/微秒/纳秒都合法），因此数字形态也认——否则这些行会被整条丢掉，
// 症状是"明明有日志却查不到"。
func parseVLTime(raw any) (int64, bool) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UnixMilli(), true
		}
		if n, err := strconv.ParseFloat(s, 64); err == nil {
			return unixToMillis(n), true
		}
		return 0, false
	case float64:
		return unixToMillis(v), true
	case json.Number:
		n, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return unixToMillis(n), true
	}
	return 0, false
}

// unixToMillis 按数量级判断 Unix 时间戳单位（秒/毫秒/微秒/纳秒）并统一成毫秒。
// 猜错的后果是时间戳跑到几万年以后或 1970 年，而界面上只会显示一个奇怪的时间——
// 所以这里按量级分档，而不是假定某一种单位。
func unixToMillis(v float64) int64 {
	switch {
	case v >= 1e17: // 纳秒
		return int64(v / 1e6)
	case v >= 1e14: // 微秒
		return int64(v / 1e3)
	case v >= 1e11: // 毫秒
		return int64(v)
	default: // 秒
		return int64(v * 1e3)
	}
}

// buildLogsQL 把平台的检索条件翻译成 LogsQL。
//
// 逐条对齐本地后端的语义（backend_contract_test.go 用同一套用例跑两个后端）：
//
//   - Keyword 是**子串**匹配 → 正则过滤器 `~"QuoteMeta(kw)"`。不用 LogsQL 的
//     `*x*` 子串过滤器：它要求关键词自己处理引号与通配符的拼接（含空格、含 `*`
//     时尤其容易拼错），而正则的语义是确定的；
//   - Regex → `~"<re>"`（平台侧已校验可编译，两边都是 RE2）；
//   - Fields 是**精确等值** → `field:="value"`。绝不能用 `field:value`——
//     那是**按词**匹配（`status:500` 会命中 `status:5000`）；
//   - Nodes / Sources → `field:in(...)`：多值精确匹配，比多个 exact 用 OR 连接更快。
func buildLogsQL(q model.LogQuery) string {
	parts := make([]string, 0, 4+len(q.Fields))
	if q.Regex != "" {
		parts = append(parts, "~"+logsQLQuote(q.Regex))
	} else if q.Keyword != "" {
		parts = append(parts, "~"+logsQLQuote(regexQuoteLiteral(q.Keyword)))
	}
	if len(q.Nodes) > 0 {
		parts = append(parts, logsQLFieldName("node")+":in("+logsQLValueList(q.Nodes)+")")
	}
	if len(q.Sources) > 0 {
		parts = append(parts, logsQLFieldName("source")+":in("+logsQLValueList(q.Sources)+")")
	}
	// 字段过滤按键排序：同一次查询必须产出同一条 LogsQL，
	// 否则测试断言与现场排障都没法复现（map 遍历顺序是随机的）。
	keys := make([]string, 0, len(q.Fields))
	for k := range q.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, logsQLFieldName(k)+":="+logsQLQuote(q.Fields[k]))
	}
	if len(parts) == 0 {
		// 没有任何过滤条件时不能拼出空查询：后端会拒绝空 query。
		// `*` 是全选过滤器，即使命中海量日志也不会压垮后端。
		return "*"
	}
	// 每个条件都加括号：LogsQL 里 AND 可省略、NOT 可写作 `-` 或 `!`，
	// 且 `a -b OR c` 的实际结合方式与直觉不同。括号是唯一不依赖"我记得优先级"的写法。
	for i, p := range parts {
		parts[i] = "(" + p + ")"
	}
	return strings.Join(parts, " AND ")
}

// logsQLQuote 把值包成 LogsQL 字符串字面量。
//
// 统一用双引号并转义 `\` 与 `"`：LogsQL 支持双引号/单引号/反引号三种字面量，
// 但"选一种并转义"比"挑一种值里没出现的引号"更容易推理，也不会因为值里
// 同时出现三种引号而失效。
func logsQLQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// logsQLValueList 把多值拼成 `"a","b"` 形态。
func logsQLValueList(values []string) string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, logsQLQuote(v))
	}
	return strings.Join(out, ",")
}

// logsQLSimpleField 是"无需加引号"的字段名形态。
var logsQLSimpleField = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// logsQLFieldName 在字段名含特殊字符时加引号。
//
// 平台的字段名允许点号与连字符（model.LogFieldNamePattern），而 `-` 在 LogsQL 里
// 是取反运算符：`status-code:="500"` 会被解析成 `status` 且非 `code:="500"`——
// 语法合法、结果全错。因此这类名字必须加引号。
func logsQLFieldName(name string) string {
	if logsQLSimpleField.MatchString(name) {
		return name
	}
	return logsQLQuote(name)
}

// regexQuoteLiteral 把「子串」关键词变成等价的 RE2 正则。
//
// 控制字符必须显式转义：它们会被拼进一行 LogsQL（以及表单编码），
// 原样带进去会破坏查询语法。
func regexQuoteLiteral(s string) string {
	escaped := regexp.QuoteMeta(s)
	return strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(escaped)
}
