package logstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 这一组用例守的是**方言与参数**：契约用例看的是"结果对不对"，
// 而下面这些细节错了以后结果往往也是对的——只是语义悄悄变了，
// 或者只在某个关键词/字段名/时间戳形态上出错（现场极难复现）。

// buildLogsQL 的翻译：逐条对应本地后端的语义。
func TestBuildLogsQL(t *testing.T) {
	cases := []struct {
		name string
		q    model.LogQuery
		want string
	}{
		{
			name: "无条件时全选（不能拼出空查询）",
			q:    model.LogQuery{},
			want: "*",
		},
		{
			name: "关键词是子串 → 用正则过滤器",
			q:    model.LogQuery{Keyword: "disk"},
			want: `(~"disk")`,
		},
		{
			name: "正则优先于关键词",
			q:    model.LogQuery{Keyword: "zzz", Regex: "err(or)?"},
			want: `(~"err(or)?")`,
		},
		{
			// 两层转义：先 QuoteMeta 把元字符变成字面量，再把每个 `\` 按
			// LogsQL 字符串字面量转义一次。少一层就会让关键词变成"模式匹配"。
			name: "关键词里的正则元字符必须转义（否则语义变成模式匹配）",
			q:    model.LogQuery{Keyword: "a.b*c"},
			want: `(~"a\\.b\\*c")`,
		},
		{
			name: "引号与反斜杠要转义",
			q:    model.LogQuery{Keyword: `say "hi" \ ok`},
			want: `(~"say \"hi\" \\\\ ok")`,
		},
		{
			name: "控制字符转成正则转义（原样带进去会破坏查询）",
			q:    model.LogQuery{Keyword: "a\nb\tc"},
			want: `(~"a\\nb\\tc")`,
		},
		{
			name: "节点用多值精确匹配",
			q:    model.LogQuery{Nodes: []string{"web-01", "web-02"}},
			want: `(node:in("web-01","web-02"))`,
		},
		{
			name: "来源同理",
			q:    model.LogQuery{Sources: []string{"applog"}},
			want: `(source:in("applog"))`,
		},
		{
			name: "字段是精确等值（:= 而不是 :，后者是按词匹配）",
			q:    model.LogQuery{Fields: map[string]string{"status": "500"}},
			want: `(status:="500")`,
		},
		{
			name: "含连字符的字段名必须加引号（- 在 LogsQL 里是取反运算符）",
			q:    model.LogQuery{Fields: map[string]string{"status-code": "500"}},
			want: `("status-code":="500")`,
		},
		{
			name: "含点号的字段名同理",
			q:    model.LogQuery{Fields: map[string]string{"log.level": "error"}},
			want: `("log.level":="error")`,
		},
		{
			name: "多个条件用显式 AND 连接并各自加括号",
			q: model.LogQuery{
				Keyword: "disk", Nodes: []string{"web-01"}, Sources: []string{"applog"},
				Fields: map[string]string{"status": "500"},
			},
			want: `(~"disk") AND (node:in("web-01")) AND (source:in("applog")) AND (status:="500")`,
		},
		{
			name: "字段顺序稳定（同一次查询必须产出同一条语句）",
			q:    model.LogQuery{Fields: map[string]string{"z": "1", "a": "2", "m": "3"}},
			want: `(a:="2") AND (m:="3") AND (z:="1")`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildLogsQL(tc.q); got != tc.want {
				t.Fatalf("buildLogsQL = %q，want %q", got, tc.want)
			}
		})
	}
}

// 写入：流字段/时间字段/消息字段必须显式绑定，且时间用无歧义的 RFC3339。
func TestVictoriaLogs_AppendRequestShape(t *testing.T) {
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)
	now := time.Now().UnixMilli()

	if _, _, _, err := vl.Append(model.LogBatch{
		Source: "applog", Node: "web-01",
		Lines: []model.LogLine{
			{Ts: now, Pattern: "diskfull", Text: `{"status":500,"path":"/data"}`},
			// 正文里带协议保留字段名：绝不能覆盖平台自己写的 node/source
			{Ts: now - 1, Text: `{"node":"evil","source":"evil","_msg":"evil","status":200}`},
		},
	}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	form := fake.lastForm()
	if form == nil {
		t.Fatal("没有收到写入请求")
	}
	if got := form.Get("_stream_fields"); got != "node,source" {
		t.Fatalf("流字段应为 node,source（低基数维度），实际 %q", got)
	}
	if form.Get("_time_field") != "_time" || form.Get("_msg_field") != "_msg" {
		t.Fatalf("时间/消息字段未绑定：%+v", form)
	}

	entries := fake.entriesSnapshot()
	if len(entries) != 2 {
		t.Fatalf("应写入 2 条，实际 %d", len(entries))
	}
	first := entries[0]
	if first["node"] != "web-01" || first["source"] != "applog" {
		t.Fatalf("节点/来源应作为字段写入：%+v", first)
	}
	if first["pattern"] != "diskfull" {
		t.Fatalf("模式名应写入：%+v", first)
	}
	// 时间用 RFC3339：数字形态要按数量级猜单位（秒/毫秒/微秒/纳秒），猜错就是把日志写到几万年以后
	if ts, ok := first["_time"].(string); !ok || !strings.HasPrefix(ts, "20") {
		t.Fatalf("时间应为 RFC3339 字符串：%+v", first["_time"])
	}
	// 解析出的业务字段要平铺到顶层（后端才建得了索引）
	if first["status"] != "500" {
		t.Fatalf("结构化字段应平铺写入：%+v", first)
	}

	// 保留字段不可被正文覆盖：否则一条日志就能伪造自己的来源与节点
	second := entries[1]
	if second["node"] != "web-01" || second["source"] != "applog" || second["_msg"] != `{"node":"evil",...}` {
		if second["node"] == "evil" || second["source"] == "evil" {
			t.Fatalf("正文不得覆盖保留字段：%+v", second)
		}
	}
	if second["status"] != "200" {
		t.Fatalf("普通业务字段应照常写入：%+v", second)
	}
}

// 检索：闭区间要补成 +1ms，分页要带 limit 且多取一行判断截断。
func TestVictoriaLogs_QueryRequestShape(t *testing.T) {
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)
	now := time.Now().UnixMilli()

	if _, err := vl.Query(model.LogQuery{From: now - 1000, To: now, Limit: 10}, Cursor{}); err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	form := fake.lastForm()
	if got := form.Get("limit"); got != "11" {
		t.Fatalf("应多取一行用于判断截断，实际 limit=%q", got)
	}
	if form.Get("offset") != "" {
		t.Fatalf("首页不应带 offset：%+v", form)
	}
	end, ok := parseVLTime(form.Get("end"))
	if !ok {
		t.Fatalf("end 应是可解析的时间：%q", form.Get("end"))
	}
	// 平台的 To 是闭区间，后端的 end 是开区间 → 必须 +1ms
	if end != now+1 {
		t.Fatalf("end 应等于 To+1ms（闭区间），实际 %d，want %d", end, now+1)
	}
	if start, _ := parseVLTime(form.Get("start")); start != now-1000 {
		t.Fatalf("start 应等于 From，实际 %d", start)
	}

	// 续读：带 offset，且必须与 limit 同时出现（后端文档要求）
	if _, err := vl.Query(model.LogQuery{From: now - 1000, To: now, Limit: 10},
		Cursor{Backend: BackendVictoriaLogs, Skip: 10}); err != nil {
		t.Fatalf("续读失败: %v", err)
	}
	form = fake.lastForm()
	if form.Get("offset") != "10" || form.Get("limit") == "" {
		t.Fatalf("续读应同时带 limit 与 offset：%+v", form)
	}
}

// 后端拒绝（4xx）与后端不可用（5xx/网络）要分开：前者是查询语句的问题，
// 后者是运维要去看后端的问题。
func TestVictoriaLogs_ErrorClassification(t *testing.T) {
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)
	fake.failNext(1)
	if _, err := vl.Query(model.LogQuery{From: 1, To: 2}, Cursor{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("5xx 应归类为后端不可用：%v", err)
	}
}

// 时间戳解析要容忍后端与其它写入工具产出的多种形态。
func TestParseVLTime(t *testing.T) {
	base := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	want := base.UnixMilli()
	cases := []struct {
		name string
		raw  any
		want int64
		ok   bool
	}{
		{name: "RFC3339 秒级", raw: "2026-10-06T12:00:00Z", want: want, ok: true},
		{name: "RFC3339 纳秒", raw: "2026-10-06T12:00:00.123456789Z", want: want + 123, ok: true},
		{name: "带时区偏移", raw: "2026-10-06T20:00:00+08:00", want: want, ok: true},
		{name: "Unix 秒", raw: float64(base.Unix()), want: want, ok: true},
		{name: "Unix 毫秒", raw: float64(want), want: want, ok: true},
		{name: "Unix 微秒", raw: float64(want) * 1e3, want: want, ok: true},
		{name: "Unix 纳秒", raw: float64(want) * 1e6, want: want, ok: true},
		{name: "字符串形式的数字", raw: "1760000000000", want: 1760000000000, ok: true},
		{name: "空值", raw: "", ok: false},
		{name: "无法识别的字符串", raw: "not-a-time", ok: false},
		{name: "类型不符", raw: []any{1}, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseVLTime(tc.raw)
			if ok != tc.ok {
				t.Fatalf("ok = %v，want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("ts = %d，want %d", got, tc.want)
			}
		})
	}
}

// 响应解析：坏行跳过而不是整页失败（否则一行损坏就让整个检索不可用）。
func TestParseVLLines(t *testing.T) {
	// 时间必须落在**过去**：clampTS 会把明显超前的值换成当前时间（见 logstore.go），
	// 用未来时间会让两行拿到同一个时间戳，从而掩盖"时间戳是否真的解析成功"。
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	raw := []byte(strings.Join([]string{
		// 顶层 status 刻意写成 999：字段必须**以正文为准**重新提取（500），
		// 否则同一份日志在两个后端下会筛出不同结果（后端里存的字段可能来自别的写入工具）。
		`{"_time":"` + base.Format(time.RFC3339Nano) + `","_msg":"{\"status\":\"500\",\"path\":\"/data\"}","node":"web-01","source":"applog","status":"999"}`,
		``, // 空行
		`{"_msg":"没有时间戳的行","node":"web-01"}`, // 无法定位 → 跳过
		`{bad json`, // 坏行 → 跳过
		`{"_time":"` + base.Add(time.Second).Format(time.RFC3339Nano) + `","_msg":"second","node":"web-02","source":"nginx"}`,
	}, "\n"))

	hits, err := parseVLLines(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("应保留 2 行可定位的日志，实际 %d：%+v", len(hits), hits)
	}
	if hits[0].Node != "web-01" || hits[0].Source != "applog" {
		t.Fatalf("字段映射不符：%+v", hits[0])
	}
	// 字段在读取侧按同一套规则重新提取（两个后端的 Fields 必须一致）
	if hits[0].Fields["status"] != "500" || hits[0].Fields["path"] != "/data" {
		t.Fatalf("字段应从正文重新提取：%+v", hits[0].Fields)
	}
	if hits[1].Text != "second" {
		t.Fatalf("正文应原样返回：%+v", hits[1])
	}
	if hits[0].Ts != base.UnixMilli() || hits[1].Ts != base.Add(time.Second).UnixMilli() {
		t.Fatalf("时间戳应精确解析：%d / %d，want %d / %d",
			hits[0].Ts, hits[1].Ts, base.UnixMilli(), base.Add(time.Second).UnixMilli())
	}
}

// 地址校验：写错了必须启动即失败，而不是等到第一次查日志。
func TestNewVictoriaLogs_ValidatesAddr(t *testing.T) {
	if _, err := NewVictoriaLogs(VictoriaLogsOptions{}); err == nil {
		t.Fatal("空 addr 应报错")
	}
	if _, err := NewVictoriaLogs(VictoriaLogsOptions{Addr: "127.0.0.1:9428"}); err == nil {
		t.Fatal("缺 scheme 的 addr 应报错")
	}
	vl, err := NewVictoriaLogs(VictoriaLogsOptions{Addr: "http://127.0.0.1:9428/"})
	if err != nil {
		t.Fatalf("合法 addr 应通过: %v", err)
	}
	if strings.HasSuffix(vl.addr, "/") {
		t.Fatalf("基址末尾的斜杠应被去掉：%q", vl.addr)
	}
}

// 元数据接口失败不能让检索不可用：候选列表拿不到就退化成空列表。
func TestVictoriaLogs_CatalogsDegradeGracefully(t *testing.T) {
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)
	fake.failNext(2)
	if got := vl.Sources(); len(got) != 0 {
		t.Fatalf("后端不可用时应返回空候选：%+v", got)
	}
	fake.failNext(1)
	if got := vl.FieldNames("applog"); len(got) != 0 {
		t.Fatalf("后端不可用时应返回空候选：%+v", got)
	}
}
