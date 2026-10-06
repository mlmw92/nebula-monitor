package logstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nebula/monitor/internal/model"
)

// 契约用例：**同一套断言跑两个真实适配器**。
//
// 为什么必须两边都跑：接口的价值在于"换后端不改变语义"，而语义恰恰是最容易
// 在替换时悄悄变掉的东西——子串变成按词、精确等值变成包含、闭区间变成开区间，
// 每一条都不会报错，只会让检索结果少几行、多几行。只有同一套断言同时钉住
// 两个实现，才能保证"换后端"对用户是透明的。
//
// 第二个适配器跑在假后端上（victorialogs_fake_test.go）：它用**照文档语法写的**
// LogsQL 求值器来判断查询。**未覆盖**：真实 VictoriaLogs 实例的存储/排序细节、
// 方言的完整语法、集群与租户行为——见 docs/testing/2026-10-06-platform-review-delta.md §十。

type backendContract struct {
	name     string
	newStore func(t *testing.T) LogStore
}

func contractBackends() []backendContract {
	return []backendContract{
		{
			name: BackendLocal,
			newStore: func(t *testing.T) LogStore {
				t.Helper()
				store := New(t.TempDir(), 0)
				if store == nil {
					t.Fatal("本地后端创建失败")
				}
				return store
			},
		},
		{
			name: BackendVictoriaLogs,
			newStore: func(t *testing.T) LogStore {
				t.Helper()
				return newFakeVictoriaLogs(t).adapter(t)
			},
		},
	}
}

// seedLines 写入一批日志；写入失败直接终止用例（错误路径另有专门用例）。
func seedLines(t *testing.T, store LogStore, source, node string, lines ...model.LogLine) {
	t.Helper()
	accepted, dropped, reason, err := store.Append(model.LogBatch{Source: source, Node: node, Lines: lines})
	if err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if accepted != len(lines) || dropped != 0 {
		t.Fatalf("写入应全部接受：accepted=%d dropped=%d reason=%s", accepted, dropped, reason)
	}
}

func mustQuery(t *testing.T, store LogStore, q model.LogQuery) model.LogQueryResult {
	t.Helper()
	res, err := store.Query(q, Cursor{})
	if err != nil {
		t.Fatalf("检索失败: %v", err)
	}
	return res
}

// contractFixture 是契约用例共用的数据集。
//
// 数据刻意包含三组"容易在换后端时被悄悄改变语义"的形态：
//   - `status` 值为 `500 timeout` 与 `500` 两行 → 区分**精确等值**与按词匹配；
//   - 文本含 `diskfull` → 区分**子串**与按词匹配（关键词 `disk` 必须命中它）；
//   - 最新一行在 web-02 → 节点过滤与时间倒序都能被验证。
type contractFixture struct {
	now     int64
	newest  int64 // web-02 那一行的时间戳（用于时间边界断言）
	oldest  int64 // web-01 最早一行（用于闭区间断言）
	lineTS  map[string]int64
}

func newContractFixture() contractFixture {
	now := time.Now().UnixMilli()
	return contractFixture{
		now:    now,
		newest: now - 500,
		oldest: now - 3000,
		lineTS: map[string]int64{
			"timeout": now - 3000,
			"exact":   now - 2000,
			"plain":   now - 1000,
			"web02":   now - 500,
		},
	}
}

func (f contractFixture) seed(t *testing.T, store LogStore) {
	t.Helper()
	seedLines(t, store, "applog", "web-01",
		model.LogLine{Ts: f.lineTS["timeout"], Pattern: "patternonly", Text: `{"status":"500 timeout","path":"/data","msg":"diskfull on /data"}`},
		model.LogLine{Ts: f.lineTS["exact"], Text: `{"status":"500"}`},
		model.LogLine{Ts: f.lineTS["plain"], Text: "plain line without fields"},
	)
	seedLines(t, store, "applog", "web-02",
		model.LogLine{Ts: f.lineTS["web02"], Text: `{"status":"500"}`},
	)
	seedLines(t, store, "nginx", "web-01",
		model.LogLine{Ts: f.now - 800, Text: `{"status":404}`},
	)
}

// 全量检索：条数、时间倒序、节点/来源/文本与**字段提取**都一致。
func TestBackendContract_QueryAll(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			res := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now})
			if len(res.Lines) != 5 {
				t.Fatalf("应命中 5 条，实际 %d：%+v", len(res.Lines), res.Lines)
			}
			for i := 1; i < len(res.Lines); i++ {
				if res.Lines[i].Ts > res.Lines[i-1].Ts {
					t.Fatalf("结果必须时间倒序：%+v", res.Lines)
				}
			}
			if res.Truncated {
				t.Fatal("未达上限不应标截断")
			}
			if res.Lines[0].Node != "web-02" || res.Lines[0].Source != "applog" {
				t.Fatalf("最新一条应来自 web-02/applog：%+v", res.Lines[0])
			}
			// 字段提取必须一致：换后端不得改变"哪些字段能筛"
			var timeoutHit *model.LogHit
			for i := range res.Lines {
				if res.Lines[i].Ts == f.lineTS["timeout"] {
					timeoutHit = &res.Lines[i]
				}
			}
			if timeoutHit == nil {
				t.Fatal("缺少 timeout 那一行")
			}
			if timeoutHit.Fields["status"] != "500 timeout" || timeoutHit.Fields["path"] != "/data" {
				t.Fatalf("字段提取不一致：%+v", timeoutHit.Fields)
			}
			if timeoutHit.Pattern != "patternonly" {
				t.Fatalf("模式名应随行返回：%+v", timeoutHit)
			}
		})
	}
}

// 关键词是**子串**匹配：`disk` 必须命中 `diskfull`（按词匹配会漏掉它）。
func TestBackendContract_KeywordIsSubstring(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			res := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Keyword: "disk"})
			if len(res.Lines) != 1 || res.Lines[0].Ts != f.lineTS["timeout"] {
				t.Fatalf("子串关键词应命中 diskfull 那一行：%+v", res.Lines)
			}
			// 关键词只匹配**正文**，不匹配模式名：模式是检索结果里的一个字段，
			// 不是正文的一部分（否则按模式名搜会返回一堆正文里根本没有该词的行）。
			byPattern := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Keyword: "patternonly"})
			if len(byPattern.Lines) != 0 {
				t.Fatalf("关键词不应匹配模式名：%+v", byPattern.Lines)
			}
			// 关键词只匹配**正文**，不匹配字段名或值
			if got := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Keyword: "/data"}); len(got.Lines) != 1 {
				t.Fatalf("正文里的子串应命中：%+v", got.Lines)
			}
			empty := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Keyword: "no-such-text"})
			if len(empty.Lines) != 0 || empty.Truncated || empty.Cursor != "" {
				t.Fatalf("无命中应是空结果且不带游标：%+v", empty)
			}
		})
	}
}

// 正则匹配，且**优先于**关键词。
func TestBackendContract_RegexWinsOverKeyword(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			// 关键词不可能命中（"zzz"），正则命中两行 status=500 —— 结果说明正则生效
			res := mustQuery(t, store, model.LogQuery{
				From: f.now - 10_000, To: f.now, Keyword: "zzz", Regex: `"status":"500"`,
			})
			if len(res.Lines) != 2 {
				t.Fatalf("正则应优先于关键词并命中 2 行：%+v", res.Lines)
			}
			// 非法正则：两个后端都必须报错，而不是返回空结果
			if _, err := store.Query(model.LogQuery{From: f.now - 10_000, To: f.now, Regex: "("}, Cursor{}); err == nil {
				t.Fatal("非法正则应报错")
			}
		})
	}
}

// 字段过滤是**精确等值**（不是按词、不是子串）。
func TestBackendContract_FieldsAreExact(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			res := mustQuery(t, store, model.LogQuery{
				From: f.now - 10_000, To: f.now, Fields: map[string]string{"status": "500"},
			})
			// 只有两行 status 恰好等于 500；"500 timeout" 不能被算作命中
			if len(res.Lines) != 2 {
				t.Fatalf("精确等值应命中 2 行，实际 %d：%+v", len(res.Lines), res.Lines)
			}
			for _, hit := range res.Lines {
				if hit.Fields["status"] != "500" {
					t.Fatalf("命中的行字段值不等于查询值：%+v", hit.Fields)
				}
			}
			// 多个字段条件必须同时满足
			both := mustQuery(t, store, model.LogQuery{
				From: f.now - 10_000, To: f.now,
				Fields: map[string]string{"status": "500 timeout", "path": "/data"},
			})
			if len(both.Lines) != 1 {
				t.Fatalf("多字段条件应同时生效：%+v", both.Lines)
			}
			none := mustQuery(t, store, model.LogQuery{
				From: f.now - 10_000, To: f.now,
				Fields: map[string]string{"status": "500", "path": "/data"},
			})
			if len(none.Lines) != 0 {
				t.Fatalf("字段值不匹配时不应命中：%+v", none.Lines)
			}
		})
	}
}

// 节点与来源过滤（为空表示不限）。
func TestBackendContract_NodeAndSourceFilter(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			byNode := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Nodes: []string{"web-02"}})
			if len(byNode.Lines) != 1 || byNode.Lines[0].Node != "web-02" {
				t.Fatalf("节点过滤失效：%+v", byNode.Lines)
			}
			byNodes := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Nodes: []string{"web-01", "web-02"}})
			if len(byNodes.Lines) != 5 {
				t.Fatalf("多节点过滤应命中全部：%+v", byNodes.Lines)
			}
			bySource := mustQuery(t, store, model.LogQuery{From: f.now - 10_000, To: f.now, Sources: []string{"nginx"}})
			if len(bySource.Lines) != 1 || bySource.Lines[0].Source != "nginx" {
				t.Fatalf("来源过滤失效：%+v", bySource.Lines)
			}
			// 过滤条件叠加：两个条件都要满足
			combined := mustQuery(t, store, model.LogQuery{
				From: f.now - 10_000, To: f.now, Nodes: []string{"web-01"}, Sources: []string{"nginx"},
			})
			if len(combined.Lines) != 1 {
				t.Fatalf("叠加过滤应命中 1 条：%+v", combined.Lines)
			}
		})
	}
}

// 时间范围是**闭区间**：恰好落在 From / To 上的行必须返回。
//
// 这条最容易在换后端时被破坏：外部后端的 end 是开区间，少补 1ms 就会静默丢掉
// "到某一毫秒为止"的那一行——而丢的恰好是用户要找的那条时，最难被发现。
func TestBackendContract_TimeRangeIsInclusive(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			res := mustQuery(t, store, model.LogQuery{From: f.oldest, To: f.oldest})
			if len(res.Lines) != 1 || res.Lines[0].Ts != f.oldest {
				t.Fatalf("From == To == 某行时间戳时必须返回该行：%+v", res.Lines)
			}
			upper := mustQuery(t, store, model.LogQuery{From: f.oldest, To: f.newest})
			if len(upper.Lines) != 5 {
				t.Fatalf("两端闭区间应命中全部：%+v", upper.Lines)
			}
			below := mustQuery(t, store, model.LogQuery{From: f.oldest, To: f.newest - 1})
			if len(below.Lines) != 4 {
				t.Fatalf("To 少 1ms 应少一行：%+v", below.Lines)
			}
		})
	}
}

// 分页：截断必须显式标记，续读不重复、不丢失。
func TestBackendContract_Pagination(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			now := time.Now().UnixMilli()
			const total = 8
			lines := make([]model.LogLine, 0, total)
			for i := 0; i < total; i++ {
				lines = append(lines, model.LogLine{Ts: now - int64(i)*1000, Text: "page row " + strings.Repeat("x", i)})
			}
			seedLines(t, store, "applog", "web-01", lines...)

			q := model.LogQuery{From: now - 60_000, To: now, Limit: 3}
			seen := map[int64]bool{}
			var cursor Cursor
			pages := 0
			for {
				res, err := store.Query(q, cursor)
				if err != nil {
					t.Fatalf("第 %d 页失败: %v", pages+1, err)
				}
				pages++
				for _, hit := range res.Lines {
					if seen[hit.Ts] {
						t.Fatalf("分页出现重复行：%d", hit.Ts)
					}
					seen[hit.Ts] = true
				}
				if !res.Truncated {
					if res.Cursor != "" {
						t.Fatalf("未截断时不应给游标：%q", res.Cursor)
					}
					break
				}
				if res.Cursor == "" {
					t.Fatal("截断时必须给游标（否则用户翻不到下一页）")
				}
				if len(res.Lines) == 0 {
					t.Fatal("截断但没有任何行：续读会空转")
				}
				if cursor, err = DecodeCursor(res.Cursor); err != nil {
					t.Fatalf("游标应可解码: %v", err)
				}
				if pages > total+2 {
					t.Fatal("分页未收敛（游标没有前进）")
				}
			}
			if len(seen) != total {
				t.Fatalf("分页应覆盖全部 %d 行，实际 %d", total, len(seen))
			}
			if pages < 3 {
				t.Fatalf("每页 3 行、共 8 行，至少应分 3 页，实际 %d", pages)
			}
		})
	}
}

// 游标跨后端必须被拒绝：偏移语义不同，硬用会静默跳行。
func TestBackendContract_CursorIsBackendScoped(t *testing.T) {
	local := New(t.TempDir(), 0)
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)

	localCursor := Cursor{Backend: BackendLocal, File: "applog/2026-10-06/web-01.log", Offset: 128}
	if _, err := vl.Query(model.LogQuery{From: 1, To: 2}, localCursor); err == nil {
		t.Fatal("外部后端必须拒绝本地后端的游标")
	}
	vlCursor := Cursor{Backend: BackendVictoriaLogs, Skip: 5}
	if _, err := local.Query(model.LogQuery{From: 1, To: 2}, vlCursor); err == nil {
		t.Fatal("本地后端必须拒绝外部后端的游标")
	}
	// 零值游标（从头开始）两个后端都要接受
	if _, err := vl.Query(model.LogQuery{From: 1, To: 2}, Cursor{}); err != nil {
		t.Fatalf("零值游标应被接受: %v", err)
	}
	// 历史游标（有偏移、无后端标识）只对本地后端有效
	if _, err := local.Query(model.LogQuery{From: 1, To: 2}, Cursor{File: "x.log", Offset: 8}); err != nil {
		t.Fatalf("本地后端应接受历史游标: %v", err)
	}
	if _, err := vl.Query(model.LogQuery{From: 1, To: 2}, Cursor{File: "x.log", Offset: 8}); err == nil {
		t.Fatal("外部后端不应接受没有后端标识的历史游标")
	}
}

// 来源与字段目录：候选列表必须来自"真的写进去过的东西"。
func TestBackendContract_Catalogs(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)
			f := newContractFixture()
			f.seed(t, store)

			sources := store.Sources()
			if !containsStr(sources, "applog") || !containsStr(sources, "nginx") {
				t.Fatalf("来源清单应含写入过的两个来源：%+v", sources)
			}
			fields := store.FieldNames("applog")
			if !containsStr(fields, "status") || !containsStr(fields, "path") {
				t.Fatalf("字段清单应含解析出的字段：%+v", fields)
			}
			// 协议/流字段不是"业务字段"：它们要么有自己的查询参数（节点/来源），
			// 要么就是消息本身（_msg），列进筛选只会误导。
			for _, reserved := range []string{"_msg", "_time", "node", "source"} {
				if containsStr(fields, reserved) {
					t.Fatalf("字段清单不应含 %s：%+v", reserved, fields)
				}
			}
		})
	}
}

// 写入校验：两个后端必须接受/拒绝同一批输入。
func TestBackendContract_AppendValidation(t *testing.T) {
	for _, bc := range contractBackends() {
		t.Run(bc.name, func(t *testing.T) {
			store := bc.newStore(t)

			if _, _, _, err := store.Append(model.LogBatch{Source: "BAD", Node: "web-01", Lines: []model.LogLine{{Ts: 1, Text: "x"}}}); err == nil {
				t.Fatal("非法来源名应报错")
			}
			if _, _, _, err := store.Append(model.LogBatch{Source: "applog", Node: "  ", Lines: []model.LogLine{{Ts: 1, Text: "x"}}}); err == nil {
				t.Fatal("空节点名应报错")
			}
			// 空批次是正常结果（Agent 这一轮没有日志），不是错误
			accepted, dropped, reason, err := store.Append(model.LogBatch{Source: "applog", Node: "web-01"})
			if err != nil || accepted != 0 || dropped != 0 || reason != "" {
				t.Fatalf("空批次应静默成功：accepted=%d dropped=%d reason=%q err=%v", accepted, dropped, reason, err)
			}
		})
	}
}

// 两个后端的 Backend() 必须各不相同：它是游标校验与界面提示的依据。
func TestBackendContract_BackendIdentity(t *testing.T) {
	seen := map[string]bool{}
	for _, bc := range contractBackends() {
		store := bc.newStore(t)
		name := store.Backend()
		if name == "" {
			t.Fatalf("%s 的后端标识为空", bc.name)
		}
		if seen[name] {
			t.Fatalf("后端标识重复：%s", name)
		}
		seen[name] = true
		if name != bc.name {
			t.Fatalf("后端标识应与用例名一致：%s != %s", name, bc.name)
		}
	}
}

// 工厂：后端名写错必须报错（不能静默降级成"日志不可用"）。
func TestNewBackend(t *testing.T) {
	store, err := NewBackend("", BackendOptions{Dir: t.TempDir()})
	if err != nil || store == nil || store.Backend() != BackendLocal {
		t.Fatalf("空后端名应默认本地：store=%v err=%v", store, err)
	}
	// 本地后端未配置目录 = 该能力关闭：必须返回**真正的 nil 接口**
	disabled, err := NewBackend(BackendLocal, BackendOptions{})
	if err != nil || disabled != nil {
		t.Fatalf("未配置目录应返回 nil 接口：store=%v err=%v", disabled, err)
	}
	if _, err := NewBackend("elasticsearch", BackendOptions{}); err == nil {
		t.Fatal("未知后端名应报错")
	}
	if _, err := NewBackend(BackendVictoriaLogs, BackendOptions{}); err == nil {
		t.Fatal("外部后端缺 addr 应报错")
	}
	if _, err := NewBackend(BackendVictoriaLogs, BackendOptions{
		VictoriaLogs: VictoriaLogsOptions{Addr: "not-a-url"},
	}); err == nil {
		t.Fatal("非法 addr 应报错")
	}
	vl, err := NewBackend(BackendVictoriaLogs, BackendOptions{
		VictoriaLogs: VictoriaLogsOptions{Addr: "http://127.0.0.1:9428/"},
	})
	if err != nil || vl == nil || vl.Backend() != BackendVictoriaLogs {
		t.Fatalf("外部后端应创建成功：store=%v err=%v", vl, err)
	}
}

// 后端不可用必须可与"查询本身有问题"区分开：接口层据此回 502 而不是 400。
func TestVictoriaLogs_BackendUnavailableIsClassified(t *testing.T) {
	fake := newFakeVictoriaLogs(t)
	vl := fake.adapter(t)
	fake.failNext(1)

	if _, err := vl.Query(model.LogQuery{From: 1, To: 2}, Cursor{}); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("后端 5xx 应归类为 ErrBackendUnavailable：%v", err)
	}
	fake.failNext(1)
	accepted, _, _, err := vl.Append(model.LogBatch{
		Source: "applog", Node: "web-01", Lines: []model.LogLine{{Ts: 1, Text: "x"}},
	})
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("写入失败应归类为 ErrBackendUnavailable：%v", err)
	}
	// 后端不可用时不得报告"接受了行"：那会让 Agent 把它记成"上传成功"，
	// 于是丢日志这件事在指标上完全看不见。
	if accepted != 0 {
		t.Fatalf("后端不可用时不应报告已接受 %d 行", accepted)
	}
}
