package collector

import (
	"testing"
	"time"
)

// kubelet 的 CRI 框架：`<时间> <流> <F|P> <内容>`。时间与内容都在里面，
// 剥掉它正文才是应用自己那一行，时间也才是权威时间（否则只能用采集时刻）。
func TestParseCRILogLine(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		ts      int64
		content string
		ok      bool
	}{
		{
			name: "标准 stdout 行",
			line: "2026-10-06T13:40:27.537868681+08:00 stdout F ERROR tick=1",
			ts:   1791265227537, content: "ERROR tick=1", ok: true,
		},
		{
			name: "stderr 行",
			line: "2026-10-06T13:40:24.536200595+08:00 stderr F ERROR tick=2",
			ts:   1791265224536, content: "ERROR tick=2", ok: true,
		},
		{
			// 多行写入被运行时按行拆分：非末行标记 P，内容里可能还有制表符
			name: "部分行（P）",
			line: "2026-10-06T13:40:00.000000001Z stdout P \tat com.example.Main",
			ts:   1791294000000, content: "\tat com.example.Main", ok: true,
		},
		{name: "内容为空", line: "2026-10-06T13:40:00Z stdout F ", ts: 1791294000000, content: "", ok: true},
		{name: "没有框架（应用原始行）", line: "ERROR plain line", ok: false},
		{name: "时间戳非法", line: "not-a-time stdout F ERROR", ok: false},
		{name: "流名不认识", line: "2026-10-06T13:40:00Z console F ERROR", ok: false},
		{name: "标记不认识", line: "2026-10-06T13:40:00Z stdout X ERROR", ok: false},
		{name: "缺内容分隔空格", line: "2026-10-06T13:40:00Z stdout F", ok: false},
		{name: "空行", line: "", ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ts, content, ok := parseCRILogLine(c.line)
			if ok != c.ok {
				t.Fatalf("ok 不符：got %v（ts=%d content=%q）", ok, ts, content)
			}
			if !ok {
				return
			}
			if ts != c.ts {
				t.Fatalf("时间戳不符：got %d，期望 %d", ts, c.ts)
			}
			if content != c.content {
				t.Fatalf("内容不符：got %q，期望 %q", content, c.content)
			}
		})
	}
}

// 时间的权威来源必须是**行内那一个**：这里独立解析一遍，确认我们没有退回"采集时刻"。
func TestParseCRILogLineUsesEmbeddedTime(t *testing.T) {
	line := "2026-10-06T13:40:27.537868681+08:00 stdout F hello"
	want, err := time.Parse(time.RFC3339Nano, "2026-10-06T13:40:27.537868681+08:00")
	if err != nil {
		t.Fatal(err)
	}
	ts, _, ok := parseCRILogLine(line)
	if !ok || ts != want.UnixMilli() {
		t.Fatalf("应用行内时间：got %d，期望 %d", ts, want.UnixMilli())
	}
}

// 容器日志路径 → 容器身份。这里全是**字符串**用例（不碰文件系统）：
// 解析规则与"文件在哪"是两件事，分开测才能在开发机（没有 kubelet）上覆盖它。
//
// 因此本表**不覆盖**"路径是否为绝对路径、是否在容器日志目录下"——那两条由配置层的
// podLogs 校验负责（见 config 的 normalizeAndValidateLogSources），解析器只管末三段的布局。
func TestParsePodLogPath(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		namespace string
		pod       string
		container string
		ok        bool
	}{
		{
			name:      "标准布局",
			path:      "/var/log/pods/nebula-demo_web-7d9f-abc_1f2e3d/nginx/0.log",
			namespace: "nebula-demo", pod: "web-7d9f-abc", container: "nginx", ok: true,
		},
		{
			name:      "重启后的序号文件",
			path:      "/var/log/pods/default_app-1_9a8b/app/12.log",
			namespace: "default", pod: "app-1", container: "app", ok: true,
		},
		{
			// Pod 名里带点号（k8s 允许 DNS 子域形态的 StatefulSet 名）
			name:      "带点号的 Pod 名",
			path:      "/var/log/pods/kube-system_coredns.abc_77/coredns/0.log",
			namespace: "kube-system", pod: "coredns.abc", container: "coredns", ok: true,
		},
		{name: "不在容器日志目录下（末三段不构成布局）", path: "/var/log/myapp/app.log", ok: false},
		{name: "缺容器目录", path: "/var/log/pods/default_app_1/0.log", ok: false},
		{name: "多一层目录", path: "/var/log/pods/default_app_1/app/extra/0.log", ok: false},
		{name: "首段不是三段下划线布局", path: "/var/log/pods/default_app/nginx/0.log", ok: false},
		{name: "四段下划线", path: "/var/log/pods/default_app_1_2/nginx/0.log", ok: false},
		{name: "文件名不是重启序号", path: "/var/log/pods/default_app_1/nginx/current.log", ok: false},
		{name: "文件名缺 .log", path: "/var/log/pods/default_app_1/nginx/0", ok: false},
		{name: "空路径", path: "", ok: false},
		{name: "非法字符（下划线在容器名里）", path: "/var/log/pods/default_app_1/ng_inx/0.log", ok: false},
		{name: "大写会被规范化成小写", path: "/var/log/pods/Default_App_1/NGINX/0.log", namespace: "default", pod: "app", container: "nginx", ok: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parsePodLogPath(c.path)
			if !c.ok {
				if got != nil {
					t.Fatalf("应解析失败，got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("应解析成功，got nil")
			}
			if got.Namespace != c.namespace || got.Pod != c.pod || got.Container != c.container {
				t.Fatalf("身份不符：got %+v，期望 ns=%s pod=%s container=%s", got, c.namespace, c.pod, c.container)
			}
		})
	}
}
