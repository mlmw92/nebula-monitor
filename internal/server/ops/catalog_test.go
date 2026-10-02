package ops

import (
	"strings"
	"testing"
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

// 容器分组排在最后，且组内只有只读动作；顺序稳定是界面可预期的前提。
func TestCatalog_ContainerGroupIsReadOnlyAndLast(t *testing.T) {
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
	}
	if len(groups) == 0 || groups[len(groups)-1] != "容器" {
		t.Fatalf("容器分组应排在最后，实际顺序 %v", groups)
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
