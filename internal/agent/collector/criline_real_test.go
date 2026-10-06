package collector

import (
	"testing"
	"time"
)

// TestParseCRILogLineRealKubeletBytes 用**真实 kubelet 写下的字节**验证 CRI 框架解析。
//
// 夹具逐字取自 2026-10-06 dev-server 上测试 Pod（nebula-podlog/logtest）的容器日志文件
// /var/log/pods/nebula-podlog_logtest_631f4919-d182-4695-aaad-105ea3cba73d/logtest/0.log：
//
//	2026-10-06T21:52:54.944292439+08:00 stdout F 2026-10-06T13:52:54 ERROR podlog-e2e tick=290
//
// 两个容易写错的地方，只有真实字节才暴露：
//  1. 时间戳是 **9 位纳秒 + `+08:00` 偏移**（不是 Z、也不是毫秒），必须按 RFC3339Nano 解析；
//  2. 正文**自己也带一个空格分隔的时间**，且是 UTC（容器内 TZ 为空：13:52:54 就是
//     21:52:54+08:00）。所以剥离不能"取第一个空格之后"就算完，要精确定位到第 3 个空格。
func TestParseCRILogLineRealKubeletBytes(t *testing.T) {
	// 期望毫秒：2026-10-06T21:52:54.944292439+08:00 == 2026-10-06T13:52:54.944292439Z
	realTs := time.Date(2026, 10, 6, 13, 52, 54, 944292439, time.UTC).UnixMilli()

	cases := []struct {
		name      string
		line      string
		wantTs    int64 // 0 表示不校验
		wantText  string
		wantMatch bool
	}{
		{
			name:      "真实 stdout 行（正文自带 UTC 时间）",
			line:      "2026-10-06T21:52:54.944292439+08:00 stdout F 2026-10-06T13:52:54 ERROR podlog-e2e tick=290",
			wantTs:    realTs,
			wantText:  "2026-10-06T13:52:54 ERROR podlog-e2e tick=290",
			wantMatch: true,
		},
		{
			name:      "真实 stderr 行（流为 stderr）",
			line:      "2026-10-06T21:52:57.946260349+08:00 stderr F 2026-10-06T13:52:57 ERROR podlog-e2e tick=291",
			wantText:  "2026-10-06T13:52:57 ERROR podlog-e2e tick=291",
			wantMatch: true,
		},
		{
			name:      "部分写入标记 P（多行写入被运行时拆开）",
			line:      "2026-10-06T21:52:54.944292439+08:00 stdout P 前半段没有换行",
			wantText:  "前半段没有换行",
			wantMatch: true,
		},
		{
			name:      "正文为空（应用写了一行空行）",
			line:      "2026-10-06T21:52:54.944292439+08:00 stdout F ",
			wantText:  "",
			wantMatch: true,
		},
		{
			name:      "Z 结尾的时间戳（某些运行时按 UTC 记时）",
			line:      "2026-10-06T13:52:54.944292439Z stdout F hello",
			wantTs:    realTs,
			wantText:  "hello",
			wantMatch: true,
		},
		{
			name:      "正文里的空格全部保留",
			line:      "2026-10-06T21:52:54.944292439+08:00 stdout F a  b   c",
			wantText:  "a  b   c",
			wantMatch: true,
		},
		// 以下都必须**不认**：普通文件里"恰好长这样"的行不该被改写（那不是 CRI 框架）
		{name: "时间在开头但没有 CRI 框架", line: "2026-10-06 21:52:54 ERROR something", wantMatch: false},
		{name: "时间戳合法但流名非法", line: "2026-10-06T21:52:54.944292439+08:00 blah F hello", wantMatch: false},
		{name: "标记非法", line: "2026-10-06T21:52:54.944292439+08:00 stdout X hello", wantMatch: false},
		{name: "缺标记（流名后直接结束）", line: "2026-10-06T21:52:54.944292439+08:00 stdout", wantMatch: false},
		{name: "标记后没有空格", line: "2026-10-06T21:52:54.944292439+08:00 stdout F", wantMatch: false},
		{name: "空行", line: "", wantMatch: false},
		{name: "nginx 访问日志行", line: `10.0.0.1 - - [06/Oct/2026:21:52:54 +0800] "GET / HTTP/1.1" 200 612`, wantMatch: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, text, ok := parseCRILogLine(c.line)
			if ok != c.wantMatch {
				t.Fatalf("ok = %v，期望 %v（line=%q）", ok, c.wantMatch, c.line)
			}
			if !ok {
				return
			}
			if text != c.wantText {
				t.Errorf("正文 = %q，期望 %q", text, c.wantText)
			}
			if c.wantTs != 0 && ts != c.wantTs {
				// 这里断言的是：带 +08:00 偏移的时间必须换算到同一毫秒，
				// 而不是把"本地钟面值"当成 UTC 再减 8 小时。
				t.Errorf("时间 = %d，期望 %d", ts, c.wantTs)
			}
		})
	}
}
