package model

import (
	"strings"
	"testing"
)

// 容器身份的校验规则：采集侧解析出的身份要经它过滤，服务端收到后也要用**同一条规则**校验
// （两个进程各判一次，规则漂移的症状是"Agent 认为合法、Server 拒绝"）。
func TestNormalizeLogOrigin(t *testing.T) {
	cases := []struct {
		name string
		in   *LogOrigin
		want *LogOrigin
		ok   bool
	}{
		{
			name: "合法身份原样返回",
			in:   &LogOrigin{Namespace: "nebula-demo", Pod: "web-7d9f", Container: "nginx"},
			want: &LogOrigin{Namespace: "nebula-demo", Pod: "web-7d9f", Container: "nginx"},
			ok:   true,
		},
		{
			name: "两侧空白被去掉、大写折叠为小写",
			in:   &LogOrigin{Namespace: " Default ", Pod: "APP-1", Container: "NGINX"},
			want: &LogOrigin{Namespace: "default", Pod: "app-1", Container: "nginx"},
			ok:   true,
		},
		{
			// Pod 名可以是 DNS 子域（StatefulSet 常见）：点号必须允许
			name: "带点号的 Pod 名",
			in:   &LogOrigin{Namespace: "default", Pod: "db-0.master", Container: "mysql"},
			want: &LogOrigin{Namespace: "default", Pod: "db-0.master", Container: "mysql"},
			ok:   true,
		},
		{name: "nil 视为没有身份", in: nil, ok: true},
		{name: "三段全空视为没有身份", in: &LogOrigin{}, ok: true},
		{name: "只有空白", in: &LogOrigin{Namespace: " ", Pod: " ", Container: " "}, ok: true},
		// 只给一半身份无法唯一定位：定位不到资产是可接受的降级，**定位错**不是
		{name: "缺 namespace", in: &LogOrigin{Pod: "web-1", Container: "nginx"}, ok: false},
		{name: "缺 pod", in: &LogOrigin{Namespace: "default", Container: "nginx"}, ok: false},
		{name: "缺 container", in: &LogOrigin{Namespace: "default", Pod: "web-1"}, ok: false},
		{name: "含下划线", in: &LogOrigin{Namespace: "default", Pod: "web_1", Container: "nginx"}, ok: false},
		{name: "含斜杠", in: &LogOrigin{Namespace: "default", Pod: "web/1", Container: "nginx"}, ok: false},
		{name: "含空格", in: &LogOrigin{Namespace: "default", Pod: "web 1", Container: "nginx"}, ok: false},
		{name: "含中文", in: &LogOrigin{Namespace: "default", Pod: "容器", Container: "nginx"}, ok: false},
		{name: "首字符是连字符", in: &LogOrigin{Namespace: "default", Pod: "-web", Container: "nginx"}, ok: false},
		{name: "末字符是点号", in: &LogOrigin{Namespace: "default", Pod: "web.", Container: "nginx"}, ok: false},
		{
			name: "超过长度上限",
			in:   &LogOrigin{Namespace: "default", Pod: strings.Repeat("a", MaxLogOriginPartLen+1), Container: "nginx"},
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := NormalizeLogOrigin(c.in)
			if ok != c.ok {
				t.Fatalf("ok 不符：got %v，期望 %v（got=%+v）", ok, c.ok, got)
			}
			if !ok {
				if got != nil {
					t.Fatalf("非法时应返回 nil，got %+v", got)
				}
				return
			}
			if c.want == nil {
				if got != nil {
					t.Fatalf("应视为没有身份，got %+v", got)
				}
				return
			}
			if got == nil || *got != *c.want {
				t.Fatalf("身份不符：got %+v，期望 %+v", got, c.want)
			}
		})
	}
}

// Empty 是"有没有身份"的统一判断：三个字段都空即没有（供接口层决定是否展示/联动）。
func TestLogOriginEmpty(t *testing.T) {
	var nilOrigin *LogOrigin
	if !nilOrigin.Empty() {
		t.Fatal("nil 应为空")
	}
	if !(&LogOrigin{}).Empty() {
		t.Fatal("全空应为空")
	}
	if (&LogOrigin{Pod: "web-1"}).Empty() {
		t.Fatal("有 Pod 名就不算空")
	}
}
