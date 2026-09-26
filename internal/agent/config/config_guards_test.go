package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/template"
)

// 阶段三的本机护栏：三类「从本机 / 数据库取数」默认全关，白名单精确匹配。
// 这些测试守的是「默认必须落在安全一侧」和「配置自相矛盾要在启动期就报出来」两件事。

func TestTemplateGuards_DefaultsAreClosed(t *testing.T) {
	var g TemplateGuardsConfig
	for _, k := range template.GuardedKinds {
		if g.AllowsKind(k) {
			t.Errorf("%s 的默认值必须是「未放行」", k)
		}
	}
	if len(g.EnabledKinds()) != 0 {
		t.Fatalf("默认不应上报任何已放行的取数方式，got %v", g.EnabledKinds())
	}
	// 未放行时判定方法一律为假（避免「忘了注入护栏」变成「允许一切」）
	if g.AllowsCommand("/bin/sh") || g.AllowsPath("/etc/shadow") {
		t.Fatal("未放行时不得允许任何命令或路径")
	}
}

func TestTemplateGuards_AllowIsExactMatch(t *testing.T) {
	g := TemplateGuardsConfig{
		Exec: GuardRule{Enabled: true, Allow: []string{"/usr/local/bin/redis-cli"}},
		File: GuardRule{Enabled: true, Allow: []string{"/var/lib/myapp/metrics.txt"}},
	}
	if !g.AllowsCommand("/usr/local/bin/redis-cli") {
		t.Fatal("白名单内的命令应被允许")
	}
	for _, bad := range []string{
		"/usr/local/bin/redis-cli2",   // 前缀更长
		"/usr/local/bin/redis",        // 前缀更短
		"/usr/local/bin/redis-cli -h", // 带参数（命令与参数必须分开给）
		"redis-cli",                   // 相对路径
		"/usr/local/bin/REDIS-CLI",    // 大小写不同
	} {
		if g.AllowsCommand(bad) {
			t.Errorf("%q 不应被允许（白名单是精确匹配，不支持通配）", bad)
		}
	}
	if !g.AllowsPath("/var/lib/myapp/metrics.txt") {
		t.Fatal("白名单内的路径应被允许")
	}
	if g.AllowsPath("/var/lib/myapp/metrics.txt.bak") {
		t.Error("白名单外（后缀不同）的路径不应被允许")
	}
	// 只开 exec 时 file 仍未放行
	if g.AllowsPath("/var/lib/myapp/metrics.txt") && !g.AllowsKind(template.KindFile) {
		t.Error("file 未启用时不应放行路径")
	}
}

func TestTemplateGuards_JDBCAllowHosts(t *testing.T) {
	open := TemplateGuardsConfig{JDBC: GuardRule{Enabled: true}}
	if !open.AllowsDBHost("10.0.0.5:3306") {
		t.Fatal("未配置 allowHosts 表示不限制")
	}
	limited := TemplateGuardsConfig{JDBC: GuardRule{Enabled: true, AllowHosts: []string{"10.0.0.5:3306"}}}
	if !limited.AllowsDBHost("10.0.0.5:3306") || limited.AllowsDBHost("10.0.0.9:3306") {
		t.Fatal("配置了 allowHosts 后只允许其中列出的目标库")
	}
}

// TestValidateTemplateGuards 两类问题都在启动期拒绝：它们的运行期表现都是「模板静默不产出」。
func TestValidateTemplateGuards(t *testing.T) {
	ok := []TemplateGuardsConfig{
		{}, // 全关：合法
		{Exec: GuardRule{Enabled: true, Allow: []string{"/bin/echo"}}},
		{File: GuardRule{Enabled: true, Allow: []string{"/var/log/app.log"}}},
		{JDBC: GuardRule{Enabled: true}}, // 无白名单表示不限制目标库
		// 配了白名单但没启用：只告警，不拒绝
		{Exec: GuardRule{Allow: []string{"/bin/echo"}}},
	}
	for i, g := range ok {
		if err := validateTemplateGuards(&g); err != nil {
			t.Errorf("用例 %d 应通过，got %v", i, err)
		}
	}

	cases := []struct {
		name string
		g    TemplateGuardsConfig
		want string
	}{
		{"白名单非绝对路径", TemplateGuardsConfig{Exec: GuardRule{Enabled: true, Allow: []string{"redis-cli"}}}, "绝对路径"},
		{"白名单含 ..", TemplateGuardsConfig{File: GuardRule{Enabled: true, Allow: []string{"/var/../etc/shadow"}}}, "绝对路径"},
		// 「开了开关却没放行任何东西」= 该方式必然失败，不如直接拒启动
		{"exec 启用但 allow 为空", TemplateGuardsConfig{Exec: GuardRule{Enabled: true}}, "allow 为空"},
		{"file 启用但 allow 为空", TemplateGuardsConfig{File: GuardRule{Enabled: true}}, "allow 为空"},
		{"allowHosts 空条目", TemplateGuardsConfig{JDBC: GuardRule{Enabled: true, AllowHosts: []string{"  "}}}, "allowHosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTemplateGuards(&tc.g)
			if err == nil {
				t.Fatal("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestValidateGuardedLocalTemplates 本机 agent.yaml 的模板不走 Server 侧能力过滤，
// 因此必须在这里挡住「用了却没放行」——否则模板每轮都失败，界面上只看到「配了却没数据」。
func TestValidateGuardedLocalTemplates(t *testing.T) {
	execTpl := template.Config{ID: "job", Kind: template.KindExec}
	fileTpl := template.Config{ID: "state", Kind: template.KindFile}
	httpTpl := template.Config{ID: "mq", Kind: template.KindPrometheusExporter}

	// 未放行 → 拒绝
	cfg := &Config{Templates: []template.Config{execTpl}}
	if err := validateGuardedLocalTemplates(cfg); err == nil {
		t.Fatal("未放行 exec 却配了 exec 模板，应被拒绝")
	}
	cfg = &Config{Templates: []template.Config{fileTpl}}
	if err := validateGuardedLocalTemplates(cfg); err == nil {
		t.Fatal("未放行 file 却配了 file 模板，应被拒绝")
	}

	// 放行 → 通过
	cfg = &Config{
		TemplateGuards: TemplateGuardsConfig{Exec: GuardRule{Enabled: true, Allow: []string{"/bin/echo"}}},
		Templates:      []template.Config{execTpl},
	}
	if err := validateGuardedLocalTemplates(cfg); err != nil {
		t.Fatalf("已放行应通过，got %v", err)
	}

	// 网络取数类不受护栏约束
	cfg = &Config{Templates: []template.Config{httpTpl}}
	if err := validateGuardedLocalTemplates(cfg); err != nil {
		t.Fatalf("网络取数类不应受本机护栏约束，got %v", err)
	}
}

// TestTemplateGuardsYAML agent.yaml 的键名与字段名必须真的能解析出来
//（写错 tag 的后果是本机护栏「看起来配了、实际全关」）。
func TestTemplateGuardsYAML(t *testing.T) {
	const doc = `
templateGuards:
  jdbc:
    enabled: true
    allowHosts: ["10.0.0.5:3306"]
  exec:
    enabled: true
    allow: ["/usr/local/bin/redis-cli"]
  file:
    enabled: false
    allow: ["/var/lib/myapp/metrics.txt"]
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	g := cfg.TemplateGuards
	if !g.AllowsKind(template.KindJDBC) || !g.AllowsKind(template.KindExec) {
		t.Fatalf("jdbc / exec 应被解析为已启用，got %+v", g)
	}
	if g.AllowsKind(template.KindFile) {
		t.Fatalf("file 显式为 false，不应启用，got %+v", g)
	}
	if !g.AllowsCommand("/usr/local/bin/redis-cli") {
		t.Fatalf("exec 白名单未解析出来，got %+v", g.Exec)
	}
	if !g.AllowsDBHost("10.0.0.5:3306") {
		t.Fatalf("allowHosts 未解析出来，got %+v", g.JDBC)
	}
	want := []string{string(template.KindJDBC), string(template.KindExec)}
	got := g.EnabledKinds()
	if len(got) != len(want) {
		t.Fatalf("EnabledKinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EnabledKinds = %v, want %v", got, want)
		}
	}
}
