package collector

import "testing"

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
			name: "标准布局",
			path: "/var/log/pods/nebula-demo_web-7d9f-abc_1f2e3d/nginx/0.log",
			namespace: "nebula-demo", pod: "web-7d9f-abc", container: "nginx", ok: true,
		},
		{
			name: "重启后的序号文件",
			path: "/var/log/pods/default_app-1_9a8b/app/12.log",
			namespace: "default", pod: "app-1", container: "app", ok: true,
		},
		{
			// Pod 名里带点号（k8s 允许 DNS 子域形态的 StatefulSet 名）
			name: "带点号的 Pod 名",
			path: "/var/log/pods/kube-system_coredns.abc_77/coredns/0.log",
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
