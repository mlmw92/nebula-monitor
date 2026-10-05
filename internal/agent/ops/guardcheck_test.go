package ops

import (
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
)

// 这组用例钉住「护栏组合 = 任意代码执行」的两条路径，以及**不该报的那些**——
// 后者同样重要：一个见"两项都配了"就报警的实现会天天误报，然后被所有人无视。

func guards(write bool, units []string, fileWrite bool, dirs []string) config.OpsGuards {
	return config.OpsGuards{
		Write: write,
		Units: units,
		File:  config.OpsFileGuards{Write: fileWrite, Dirs: dirs},
	}
}

func kinds(risks []GuardRisk) []string {
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, r.Kind)
	}
	return out
}

// 允许写「可重启服务的可执行文件所在目录」+ 允许重启它 = 推文件加重启即任意代码执行。
func TestCheckGuardRisksFlagsExecutableInsideAllowedDir(t *testing.T) {
	g := guards(true, []string{"nginx.service"}, true, []string{"/usr/sbin"})
	risks := CheckGuardRisks(g, func(unit string) []string { return []string{"/usr/sbin/nginx"} })

	if len(risks) != 1 || risks[0].Kind != "exec" {
		t.Fatalf("应命中 1 条 exec 风险，实际 %+v", risks)
	}
	if !strings.Contains(risks[0].Unit, "nginx") || risks[0].Exposed != "/usr/sbin/nginx" {
		t.Fatalf("风险描述不对：%+v", risks[0])
	}
	if risks[0].AllowedDir != "/usr/sbin" {
		t.Fatalf("AllowedDir 应是覆盖它的那个允许目录，实际 %q", risks[0].AllowedDir)
	}
	if !strings.Contains(risks[0].Describe(), "任意代码执行") {
		t.Fatalf("说明里要说清后果，实际 %q", risks[0].Describe())
	}
}

// 常态配置不该报：允许写配置目录、允许重启服务，但可执行文件不在可写目录里。
func TestCheckGuardRisksIgnoresUnrelatedDirs(t *testing.T) {
	g := guards(true, []string{"nginx.service"}, true, []string{"/opt/app/conf"})
	risks := CheckGuardRisks(g, func(unit string) []string { return []string{"/usr/sbin/nginx"} })
	if len(risks) != 0 {
		t.Fatalf("可执行文件不在可写目录里就不该报，实际 %+v", risks)
	}
}

// 第二条路径：允许写「系统自己会执行的目录」——**连重启都不需要**，因此与 units 清单无关。
func TestCheckGuardRisksFlagsPersistenceDirsWithoutRestart(t *testing.T) {
	g := guards(false, nil, true, []string{"/etc/systemd/system"})
	risks := CheckGuardRisks(g, nil)

	if len(risks) != 1 || risks[0].Kind != "persistence" {
		t.Fatalf("应命中 1 条 persistence 风险，实际 %+v", risks)
	}
	if risks[0].Exposed != "/etc/systemd/system" {
		t.Fatalf("应指出暴露的是哪个目录，实际 %q", risks[0].Exposed)
	}
	if !strings.Contains(risks[0].Describe(), "不需要重启") {
		t.Fatalf("说明里要点出「不需要重启」，实际 %q", risks[0].Describe())
	}
}

// 允许写的目录很宽（如整个 /etc）时，会同时命中多个系统执行目录——都要报出来。
func TestCheckGuardRisksFlagsEveryPersistenceDirCovered(t *testing.T) {
	g := guards(false, nil, true, []string{"/etc"})
	risks := CheckGuardRisks(g, nil)

	if len(risks) < 5 {
		t.Fatalf("/etc 应覆盖多个系统执行目录，实际只报出 %d 条：%+v", len(risks), risks)
	}
	for _, r := range risks {
		if !strings.HasPrefix(r.Exposed, "/etc/") {
			t.Fatalf("报出的目录应在 /etc 之下，实际 %q", r.Exposed)
		}
	}
}

// 路径前缀必须带分隔符边界：允许 /etc/sys 不等于允许 /etc/systemd。
func TestCheckGuardRisksHonoursPathBoundary(t *testing.T) {
	for _, dir := range []string{"/etc/sys", "/etc/system", "/opt/systemd"} {
		g := guards(false, nil, true, []string{dir})
		if risks := CheckGuardRisks(g, nil); len(risks) != 0 {
			t.Fatalf("目录 %q 不该命中任何系统执行目录，实际 %+v", dir, risks)
		}
	}
}

// 只允许重启、不允许写文件（或没写目录清单）→ 无从组合，一条都不报。
func TestCheckGuardRisksNeedsFilePushEnabled(t *testing.T) {
	cases := []config.OpsGuards{
		guards(true, []string{"nginx.service"}, false, []string{"/usr/sbin"}),
		guards(true, []string{"nginx.service"}, true, nil),
		guards(true, []string{"nginx.service"}, true, []string{"relative/dir"}),
	}
	for i, g := range cases {
		if risks := CheckGuardRisks(g, func(string) []string { return []string{"/usr/sbin/nginx"} }); len(risks) != 0 {
			t.Fatalf("第 %d 种：文件分发未真正放行时不该报，实际 %+v", i, risks)
		}
	}
}

// 允许写可执行目录、但**不允许重启**任何服务：exec 类不成立（persistence 另算）。
func TestCheckGuardRisksSkipsExecCheckWithoutRestartPermission(t *testing.T) {
	g := guards(false, []string{"nginx.service"}, true, []string{"/usr/sbin"})
	risks := CheckGuardRisks(g, func(string) []string { return []string{"/usr/sbin/nginx"} })
	if len(risks) != 0 {
		t.Fatalf("不允许重启时 exec 类不该成立，实际 %+v", risks)
	}
}

// 解析 systemctl show 的输出：一个单元可能有多个 ExecStart / ExecReload 行。
func TestParseExecPaths(t *testing.T) {
	out := `ExecStart={ path=/usr/sbin/nginx ; argv[]=/usr/sbin/nginx -g daemon off; ; ignore_errors=no ; start_time=[n/a] }
ExecReload={ path=/usr/sbin/nginx ; argv[]=/usr/sbin/nginx -s reload ; ignore_errors=no }
`
	got := parseExecPaths(out)
	if len(got) != 2 || got[0] != "/usr/sbin/nginx" || got[1] != "/usr/sbin/nginx" {
		t.Fatalf("应解析出两个可执行路径，实际 %+v", got)
	}

	// 空属性（单元不存在 / 未设置）与完全读不到时都给空，而不是编一个路径出来
	if got := parseExecPaths("ExecStart=\nExecReload=\n"); len(got) != 0 {
		t.Fatalf("空属性不该产出路径，实际 %+v", got)
	}
	if got := parseExecPaths(""); len(got) != 0 {
		t.Fatalf("空输出不该产出路径，实际 %+v", got)
	}
}
