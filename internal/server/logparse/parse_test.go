package logparse

import (
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// JSON 日志：顶层标量 + 一层嵌套（点号连接）都要提出来，数组与 null 跳过。
func TestExtractJSONObject(t *testing.T) {
	got := Extract(`{"level":"error","status":500,"ok":false,"traceId":1234567890123456789,"http":{"method":"GET","status":500},"tags":["a","b"],"note":null}`)
	want := map[string]string{
		"level":       "error",
		"status":      "500",
		"ok":          "false",
		"traceId":     "1234567890123456789", // 长整数不能退化成科学计数法
		"http.method": "GET",
		"http.status": "500",
	}
	if len(got) != len(want) {
		t.Fatalf("字段集合不符：got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("字段 %s 不符：got %q want %q（全量 %v）", k, got[k], v, got)
		}
	}
	if _, ok := got["tags"]; ok {
		t.Fatal("数组不应展开成字段（键名没有共识）")
	}
	if _, ok := got["note"]; ok {
		t.Fatal("null 不应成为字段")
	}
}

// 坏 JSON 不能丢原文，也不能把原文里"像字段"的东西当成 JSON 解析结果：
// 退到 key=value 扫描，扫不到就返回 nil（调用方照常落盘原文）。
func TestExtractFallsBackOnBrokenJSON(t *testing.T) {
	if got := Extract(`{"level":"error"`); got != nil {
		t.Fatalf("残缺 JSON 且无 key=value 时应返回 nil，实际 %v", got)
	}
	got := Extract(`{"level":"error" trailing status=500`)
	if got["status"] != "500" {
		t.Fatalf("残缺 JSON 应退回 key=value 扫描，实际 %v", got)
	}
}

// 纯文本日志里的 key=value（含引号、单引号、无引号三种写法）。
func TestExtractKeyValue(t *testing.T) {
	got := Extract(`ts=2026-10-06T10:00:00Z level=warn msg="disk almost full" path='/var/lib' code=507 retry=false`)
	for k, want := range map[string]string{
		"ts":    "2026-10-06T10:00:00Z",
		"level": "warn",
		"msg":   "disk almost full",
		"path":  "/var/lib",
		"code":  "507",
		"retry": "false",
	} {
		if got[k] != want {
			t.Fatalf("字段 %s 不符：got %q want %q（全量 %v）", k, got[k], want, got)
		}
	}
}

// 无引号的值到空白为止：不允许空格，否则 "a=1 and b=2" 会把 "1 and b" 吞成一个值。
func TestExtractKeyValueStopsAtWhitespace(t *testing.T) {
	got := Extract(`user=alice action=login from=10.0.0.1`)
	if got["user"] != "alice" || got["action"] != "login" || got["from"] != "10.0.0.1" {
		t.Fatalf("字段解析不符：%v", got)
	}
}

// 凭据类键名不进字段表：原文照旧落盘，但平台不该提供"按口令检索"的索引。
func TestExtractSkipsSensitiveKeys(t *testing.T) {
	got := Extract(`{"user":"alice","password":"hunter2","nested":{"access_token":"t0ken"},"db":{"passwd":"p"}}`)
	if _, ok := got["password"]; ok {
		t.Fatalf("password 不应成为字段：%v", got)
	}
	if _, ok := got["nested.access_token"]; ok {
		t.Fatalf("嵌套的 access_token 不应成为字段：%v", got)
	}
	if _, ok := got["db.passwd"]; ok {
		t.Fatalf("嵌套的 passwd 不应成为字段：%v", got)
	}
	if got["user"] != "alice" {
		t.Fatalf("普通字段应保留：%v", got)
	}

	kv := Extract(`password=hunter2 user=alice token=abc`)
	if _, ok := kv["password"]; ok {
		t.Fatalf("key=value 形态同样要挡凭据：%v", kv)
	}
	if _, ok := kv["token"]; ok {
		t.Fatalf("key=value 形态同样要挡 token：%v", kv)
	}
	if kv["user"] != "alice" {
		t.Fatalf("普通字段应保留：%v", kv)
	}
}

// 名字与值都有界：非法名字丢掉、值截断、每行字段数封顶。
func TestExtractBounds(t *testing.T) {
	// 名字非法（含空格 / 以数字开头）→ 丢掉；合法名字保留
	got := Extract(`{` + strings.Join([]string{
		`"1bad":"x"`, `"bad name":"x"`, `"good_name":"y"`,
	}, ",") + `}`)
	if len(got) != 1 || got["good_name"] != "y" {
		t.Fatalf("非法字段名应被丢弃：%v", got)
	}
	if !model.IsValidLogFieldName("good_name") || model.IsValidLogFieldName("1bad") {
		t.Fatal("字段名规则与 model 不一致")
	}

	// 值超长 → 截断（且不切断多字节字符）
	long := strings.Repeat("中", 300)
	got = Extract(`{"msg":"` + long + `"}`)
	v := got["msg"]
	if len(v) > MaxFieldValueBytes+len("…") {
		t.Fatalf("字段值应被截断：len=%d", len(v))
	}
	if !strings.HasSuffix(v, "…") {
		t.Fatalf("截断要留标记：%q", v[len(v)-8:])
	}
	if strings.ContainsRune(v, '\uFFFD') {
		t.Fatalf("截断不应切出非法字符：%q", v[len(v)-8:])
	}

	// 字段数封顶：构造 40 个字段，只应留 MaxFieldsPerLine 个
	var parts []string
	for i := 0; i < 40; i++ {
		parts = append(parts, `"f`+string(rune('a'+i%26))+`_`+strings.Repeat("x", i%3)+`":"v"`)
	}
	got = Extract(`{` + strings.Join(parts, ",") + `}`)
	if len(got) > MaxFieldsPerLine {
		t.Fatalf("每行字段数应封顶 %d，实际 %d", MaxFieldsPerLine, len(got))
	}
}

// 空行与纯文本（没有任何结构）返回 nil：调用方据此不加字段，而不是加一堆空字段。
func TestExtractNoStructure(t *testing.T) {
	for _, text := range []string{"", "   ", "just a plain message without structure", "[2026-10-06] service started"} {
		if got := Extract(text); got != nil {
			t.Fatalf("%q 不应产出字段，实际 %v", text, got)
		}
	}
}
