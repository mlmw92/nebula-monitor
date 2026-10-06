package ops

import (
	"strconv"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/model"
)

// 动作目录与参数校验：这是第三道护栏的实体部分。
// 校验放过的每一种情况，症状都是"任务创建成功但 Agent 执行了你不想要的东西"，
// 因此这里逐条钉死。

func TestValidate_AcceptsKnownActionAndNormalizesParams(t *testing.T) {
	action, params, err := Validate("  "+KindSvcStatus+" ", map[string]string{"unit": "  nginx.service  "})
	if err != nil {
		t.Fatalf("合法动作不应报错: %v", err)
	}
	if action.Kind != KindSvcStatus || !action.ReadOnly {
		t.Fatalf("应返回只读动作 %s，实际 %+v", KindSvcStatus, action)
	}
	// 前后空格必须被去掉：否则 `unit= nginx.service` 会带着空格去 systemctl，报"单元不存在"
	if params["unit"] != "nginx.service" {
		t.Fatalf("参数应被规范化，实际 %q", params["unit"])
	}
}

func TestValidate_Rejects(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		params map[string]string
		wantIn string
	}{
		{"未知动作", "svc.explode", nil, "未知动作"},
		{"未知参数", KindSvcStatus, map[string]string{"unit": "nginx.service", "force": "1"}, "不支持参数"},
		{"缺必填参数", KindSvcStatus, nil, "缺少必填参数"},
		{"参数为空串", KindSvcStatus, map[string]string{"unit": "   "}, "缺少必填参数"},
		// 参数注入：不经过 shell 也不能放过 `--now` 这类选项
		{"参数注入选项", KindSvcRestart, map[string]string{"unit": "--now"}, "不合法"},
		// 非 .service 的单元语义不同（timer/socket），不该被当成服务重启
		{"非 service 单元", KindSvcRestart, map[string]string{"unit": "nginx.timer"}, "不合法"},
		{"路径穿越", KindSvcStatus, map[string]string{"unit": "../../etc/passwd"}, "不合法"},
		{"命令拼接", KindSvcStatus, map[string]string{"unit": "nginx.service;rm -rf /"}, "不合法"},
		{"空格分隔多参数", KindSvcStatus, map[string]string{"unit": "nginx.service --now"}, "不合法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Validate(tc.kind, tc.params)
			if err == nil {
				t.Fatalf("应当拒绝（%s），却通过了", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("错误信息应包含 %q，实际 %q", tc.wantIn, err.Error())
			}
		})
	}
}

// 目录必须自洽：写动作不能被标成只读（否则服务端与界面都会把它当"安全的查询"）。
func TestCatalog_WriteActionsAreNotReadOnly(t *testing.T) {
	for _, a := range Catalog() {
		if a.Kind == KindSvcRestart && a.ReadOnly {
			t.Errorf("动作 %s 会改变目标机器状态，不能标记为只读", a.Kind)
		}
		if a.Title == "" || a.Group == "" || a.Desc == "" {
			t.Errorf("动作 %s 缺少标题/分组/说明（界面上会是一片空白）", a.Kind)
		}
		for _, p := range a.Params {
			if p.Name == "" {
				t.Errorf("动作 %s 有参数缺 Name", a.Kind)
			}
			if p.re == nil && p.Pattern != "" {
				t.Errorf("动作 %s 的参数 %s 正则应已编译", a.Kind, p.Name)
			}
		}
	}
}

// 同一个分组内，只读动作必须排在写动作前面（界面顺序即风险顺序）。
//
// 只要求"组内"：界面是按分组展示的，跨组比较没有意义；而分组顺序由 groupOrder 固定，
// 保证新增分组不会让整个列表顺序变化。
func TestCatalog_ReadOnlyFirstWithinGroup(t *testing.T) {
	seenWrite := map[string]bool{}
	for _, a := range Catalog() {
		if !a.ReadOnly {
			seenWrite[a.Group] = true
			continue
		}
		if seenWrite[a.Group] {
			t.Fatalf("分组 %s 内只读动作 %s 排在了写动作之后", a.Group, a.Kind)
		}
	}
}

// 容器/K8s 只读查询：首批必须**全是只读**，且参数被钉在白名单字符集里。
//
// 这些参数会被拼进 apiserver 的 URL 路径，所以"能通过校验"就等于"能构造出的请求"。
func TestValidate_ContainerActions(t *testing.T) {
	ok := []struct {
		name   string
		kind   string
		params map[string]string
	}{
		{"工作负载（全命名空间）", KindContainerWorkloads, map[string]string{"cluster": "prod-k8s"}},
		{"工作负载（指定命名空间）", KindContainerWorkloads, map[string]string{"cluster": "prod-k8s", "namespace": "kube-system"}},
		{"Pod 列表", KindContainerPods, map[string]string{"cluster": "prod-k8s", "namespace": "default"}},
		{"对象详情", KindContainerDescribe, map[string]string{
			"cluster": "prod-k8s", "namespace": "default", "resource": "pods", "name": "web-7d9f8c6b5-x2k4p",
		}},
		{"事件（全命名空间）", KindContainerEvents, map[string]string{"cluster": "prod-k8s"}},
		{"Pod 日志（默认行数）", KindContainerLogs, map[string]string{
			"cluster": "prod-k8s", "namespace": "default", "name": "web-7d9f8c6b5-x2k4p"}},
		{"Pod 日志（指定容器与上限）", KindContainerLogs, map[string]string{
			"cluster": "prod-k8s", "namespace": "default", "name": "web-7d9f8c6b5-x2k4p",
			"container": "app", "tailLines": strconv.Itoa(model.OpsLogMaxTailLines),
			"sinceSeconds": strconv.Itoa(model.OpsLogMaxSinceSeconds)}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			action, params, err := Validate(tc.kind, tc.params)
			if err != nil {
				t.Fatalf("合法参数不应报错: %v", err)
			}
			if !action.ReadOnly {
				t.Fatalf("容器查询动作 %s 必须标记为只读", action.Kind)
			}
			if action.Group != "容器" {
				t.Fatalf("动作 %s 应归入「容器」分组，实际 %q", action.Kind, action.Group)
			}
			// 可选参数留空时不应出现在归一化结果里（否则 Agent 会收到空串并去拼 `namespaces//pods`）
			for k, v := range params {
				if v == "" {
					t.Fatalf("参数 %q 不应以空值下发", k)
				}
			}
		})
	}

	reject := []struct {
		name   string
		kind   string
		params map[string]string
		wantIn string
	}{
		{"缺集群", KindContainerPods, map[string]string{"namespace": "default"}, "缺少必填参数"},
		{"缺对象名", KindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "pods"}, "缺少必填参数"},
		{"未知参数", KindContainerPods, map[string]string{"cluster": "c", "label": "app=web"}, "不支持参数"},
		// describe 刻意不放开 secret：脱敏规则漏一个字段就是一次凭据泄露
		{"describe 拒绝 secret", KindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "secrets", "name": "db"}, "不合法"},
		// 路径穿越与查询串注入：这些值会被拼进 apiserver 的 URL
		{"命名空间路径穿越", KindContainerPods, map[string]string{"cluster": "c", "namespace": "../../etc"}, "不合法"},
		{"命名空间带斜杠", KindContainerPods, map[string]string{"cluster": "c", "namespace": "a/b"}, "不合法"},
		{"命名空间查询串注入", KindContainerPods, map[string]string{"cluster": "c", "namespace": "a?watch=1"}, "不合法"},
		{"对象名路径穿越", KindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "pods", "name": "../secrets"}, "不合法"},
		{"对象名大写", KindContainerDescribe, map[string]string{
			"cluster": "c", "namespace": "n", "resource": "pods", "name": "Web"}, "不合法"},
		{"集群名带空格", KindContainerWorkloads, map[string]string{"cluster": "prod k8s"}, "不合法"},
		{"集群名是选项", KindContainerWorkloads, map[string]string{"cluster": "--server"}, "不合法"},
		// Pod 日志：命名空间与 Pod 名必填（Pod 名只在命名空间内唯一），
		// 行数/时间窗是纯数字白名单（它们会被拼进 apiserver 的查询串）。
		{"日志缺命名空间", KindContainerLogs, map[string]string{"cluster": "c", "name": "web-1"}, "缺少必填参数"},
		{"日志缺 Pod 名", KindContainerLogs, map[string]string{"cluster": "c", "namespace": "n"}, "缺少必填参数"},
		{"日志容器名带空格", KindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "container": "a b"}, "不合法"},
		{"日志行数非数字", KindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "tailLines": "all"}, "不合法"},
		{"日志行数带符号", KindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "tailLines": "-1"}, "不合法"},
		{"日志时间窗非数字", KindContainerLogs, map[string]string{
			"cluster": "c", "namespace": "n", "name": "web-1", "sinceSeconds": "1h"}, "不合法"},
	}
	for _, tc := range reject {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Validate(tc.kind, tc.params)
			if err == nil {
				t.Fatalf("应当拒绝（%s），却通过了", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("错误信息应包含 %q，实际 %q", tc.wantIn, err.Error())
			}
		})
	}
}

// 文件分发的参数校验：目标路径会被 Agent 直接用于文件系统调用，边界逐条钉住。
func TestValidate_FilePush(t *testing.T) {
	ok := []struct {
		name   string
		params map[string]string
	}{
		{"常规配置文件", map[string]string{"path": "/opt/app/conf/app.conf", "fileId": "obf-1"}},
		{"指定权限", map[string]string{"path": "/etc/nginx/conf.d/site.conf", "fileId": "obf-12", "mode": "0600"}},
		{"点开头的文件名", map[string]string{"path": "/opt/app/.env", "fileId": "obf-3"}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			action, params, err := Validate(KindFilePush, tc.params)
			if err != nil {
				t.Fatalf("合法参数不应报错: %v", err)
			}
			// 标记为写动作是界面正确置灰与提示"去改本机护栏"的前提
			if action.ReadOnly {
				t.Fatal("文件分发必须标记为写动作（ReadOnly=false）")
			}
			for k, v := range params {
				if v == "" {
					t.Fatalf("参数 %q 不应以空值下发", k)
				}
			}
			if tc.params["mode"] == "" {
				if _, has := params["mode"]; has {
					t.Fatal("未指定 mode 时不应出现在下发参数里（Agent 侧按默认 0644 处理）")
				}
			}
		})
	}

	reject := []struct {
		name   string
		params map[string]string
		wantIn string
	}{
		{"相对路径", map[string]string{"path": "etc/nginx.conf", "fileId": "obf-1"}, "不合法"},
		{"路径穿越", map[string]string{"path": "/etc/../etc/passwd", "fileId": "obf-1"}, "不合法"},
		{"结尾斜杠", map[string]string{"path": "/etc/nginx/", "fileId": "obf-1"}, "不合法"},
		{"路径含空格", map[string]string{"path": "/opt/my app/conf", "fileId": "obf-1"}, "不合法"},
		{"路径含变量", map[string]string{"path": "/opt/$HOME/x", "fileId": "obf-1"}, "不合法"},
		{"缺目标路径", map[string]string{"fileId": "obf-1"}, "缺少必填参数"},
		{"缺文件引用", map[string]string{"path": "/opt/app.conf"}, "缺少必填参数"},
		{"文件引用非法", map[string]string{"path": "/opt/app.conf", "fileId": "../../etc/passwd"}, "不合法"},
		// setuid 位刻意不允许：分发一个 setuid 文件等于远程提权
		{"权限带 setuid", map[string]string{"path": "/opt/app.conf", "fileId": "obf-1", "mode": "4755"}, "不合法"},
		{"权限没写前导零", map[string]string{"path": "/opt/app.conf", "fileId": "obf-1", "mode": "644"}, "不合法"},
		{"未知参数", map[string]string{"path": "/opt/app.conf", "fileId": "obf-1", "owner": "root"}, "不支持参数"},
	}
	for _, tc := range reject {
		t.Run("拒绝/"+tc.name, func(t *testing.T) {
			_, _, err := Validate(KindFilePush, tc.params)
			if err == nil {
				t.Fatalf("应当拒绝（%s），却通过了", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("错误信息应包含 %q，实际 %q", tc.wantIn, err.Error())
			}
		})
	}
}

// 容器分组内只有只读动作（exec 属 P2，首批只放只读），且界面分组顺序与声明顺序一致。
//
// 断言的是「顺序 == groupOrder」而不是"某个分组在最后"：后者会在新增分组时失效，
// 而真正要守的约束是"新增一个分组不会打乱其它分组的相对位置"——那正是 groupOrder 存在的理由。
func TestCatalog_ContainerGroupIsReadOnlyAndGroupOrderStable(t *testing.T) {
	groups := []string{}
	seen := map[string]bool{}
	for _, a := range Catalog() {
		if !seen[a.Group] {
			seen[a.Group] = true
			groups = append(groups, a.Group)
		}
		if a.Group == "容器" && !a.ReadOnly {
			t.Fatalf("容器分组内出现写动作 %s：exec 属 P2，首批只放只读", a.Kind)
		}
		// 未在 groupOrder 里声明的分组会被排到最后，顺序就不再可预期
		if groupRank(a.Group) >= len(groupOrder) {
			t.Fatalf("动作 %s 的分组 %q 未在 groupOrder 中声明", a.Kind, a.Group)
		}
	}
	want := []string{}
	for _, g := range groupOrder {
		if seen[g] {
			want = append(want, g)
		}
	}
	if len(groups) != len(want) {
		t.Fatalf("分组数与 groupOrder 不符：实际 %v，期望 %v", groups, want)
	}
	for i := range want {
		if groups[i] != want[i] {
			t.Fatalf("分组顺序应与 groupOrder 一致：实际 %v，期望 %v", groups, want)
		}
	}
}

// 每个动作都要有唯一的 Kind（重复会让 Lookup 静默命中第一个）。
func TestCatalog_KindsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, a := range Catalog() {
		if seen[a.Kind] {
			t.Fatalf("动作标识重复：%s", a.Kind)
		}
		seen[a.Kind] = true
	}
	for _, k := range Kinds() {
		if !seen[k] {
			t.Fatalf("Kinds 返回了目录里没有的动作：%s", k)
		}
	}
}
