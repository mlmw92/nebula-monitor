package collector

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/template"
)

// 阶段三执行侧：exec（本机命令）、file（本机文件）、jdbc（数据库只读查询）。
//
// 命令与数据库这两处 I/O 做成可注入的缝隙（runCmd / queryScalar），
// 因此这些用例不依赖平台上装了哪个可执行文件、也不需要真库——
// 真正的进程执行与 SQL 往返由实机验证覆盖（build/verify-templates.sh）。

func localRunner(t *testing.T, g config.TemplateGuardsConfig) *TemplateRunner {
	t.Helper()
	return NewTemplateRunner("test-node").WithGuards(g)
}

func execConfig(id string, tgt template.Target) template.Config {
	return template.Config{
		ID:      id,
		Kind:    template.KindExec,
		Targets: []template.Target{tgt},
		Rules: template.Rules{Metrics: []template.MetricRule{
			{Name: "ops_per_sec", Pattern: `instantaneous_ops_per_sec:(\d+)`},
			{Name: "used_memory", Pattern: `used_memory:(\d+)`},
		}},
	}
}

func fileConfig(id string, tgt template.Target) template.Config {
	return template.Config{
		ID:      id,
		Kind:    template.KindFile,
		Targets: []template.Target{tgt},
		Rules: template.Rules{Metrics: []template.MetricRule{
			{Name: "queue_depth", Pattern: `(?m)^queue_depth (\d+)$`},
		}},
	}
}

func jdbcConfig(id string, tgt template.Target, rules ...template.MetricRule) template.Config {
	return template.Config{
		ID:      id,
		Kind:    template.KindJDBC,
		Driver:  "mysql",
		Targets: []template.Target{tgt},
		Rules:   template.Rules{Metrics: rules},
	}
}

func jdbcTarget() template.Target {
	return template.Target{
		Instance: "biz-db-01",
		Addr:     "10.0.0.5:3306",
		Database: "appdb",
		Auth:     &template.Auth{Basic: &template.BasicAuth{User: "monitor", Password: "enc:xxxx"}},
	}
}

func guardExec(cmd string) config.TemplateGuardsConfig {
	return config.TemplateGuardsConfig{Exec: config.GuardRule{Enabled: true, Allow: []string{cmd}}}
}

func guardFile(path string) config.TemplateGuardsConfig {
	return config.TemplateGuardsConfig{File: config.GuardRule{Enabled: true, Allow: []string{path}}}
}

func guardJDBC() config.TemplateGuardsConfig {
	return config.TemplateGuardsConfig{JDBC: config.GuardRule{Enabled: true}}
}

// requirePosixPaths file 类的路径校验要求 POSIX 风格绝对路径（Agent 只跑 Linux，不做 Windows 节点），
// 因此这些用例在 Windows 上跳过；Linux 侧由本地/CI 与实机验证覆盖。
func requirePosixPaths(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("路径白名单校验要求 POSIX 绝对路径；该场景在 Linux 上验证")
	}
}

// mustValid 断言测试用的模板本身合法（避免用非法配置去测运行期行为，
// 那样测出来的可能是校验器的行为而不是执行器的）。
func mustValid(t *testing.T, cfg template.Config) {
	t.Helper()
	if err := template.ValidateAll([]template.Config{cfg}); err != nil {
		t.Fatalf("测试模板应合法：%v", err)
	}
}

// ---------- exec ----------

// TestExec_ExtractsFromCommandOutput 命令输出按正则取值；命令与参数**原样 argv 传递**（不经 shell）。
func TestExec_ExtractsFromCommandOutput(t *testing.T) {
	const cmd = "/usr/local/bin/redis-cli"
	cfg := execConfig("redisinfo", template.Target{
		Instance: "cache-01",
		Command:  cmd,
		Args:     []string{"-h", "127.0.0.1", "INFO"},
	})
	mustValid(t, cfg)

	r := localRunner(t, guardExec(cmd))
	var gotName string
	var gotArgs []string
	r.runCmd = func(_ context.Context, name string, args []string) ([]byte, []byte, error) {
		gotName, gotArgs = name, args
		return []byte("instantaneous_ops_per_sec:1234\nused_memory:512000\n"), nil, nil
	}

	got := byName(r.CollectTemplate(context.Background(), cfg))
	if gotName != cmd {
		t.Fatalf("应执行白名单里的命令，got %q", gotName)
	}
	if len(gotArgs) != 3 || gotArgs[2] != "INFO" {
		t.Fatalf("参数应原样传递（argv，不经 shell），got %v", gotArgs)
	}
	if v := got["redisinfo_ops_per_sec"]; len(v) != 1 || v[0].Value != 1234 {
		t.Fatalf("ops_per_sec 取值错误：%+v", v)
	}
	if v := got["redisinfo_used_memory"]; len(v) != 1 || v[0].Value != 512000 {
		t.Fatalf("used_memory 取值错误：%+v", v)
	}
	if up := got[template.UpMetricName]; len(up) != 1 || up[0].Value != 1 {
		t.Fatalf("执行成功应产出 up=1：%+v", up)
	}
}

// TestExec_FailureYieldsOnlyUpZero 退出码非 0 → 只产 up=0，不产任何数据
// （否则上一轮的值会被误读为当前值，也失去「静默无数据」的可见信号）。
func TestExec_FailureYieldsOnlyUpZero(t *testing.T) {
	const cmd = "/usr/local/bin/redis-cli"
	cfg := execConfig("redisinfo", template.Target{Command: cmd})
	mustValid(t, cfg)

	r := localRunner(t, guardExec(cmd))
	r.runCmd = func(context.Context, string, []string) ([]byte, []byte, error) {
		return []byte("instantaneous_ops_per_sec:1234\n"), []byte("connection refused"), os.ErrDeadlineExceeded
	}
	got := r.CollectTemplate(context.Background(), cfg)
	if len(got) != 1 || got[0].Name != template.UpMetricName || got[0].Value != 0 {
		t.Fatalf("失败时应只产 up=0，got %+v", got)
	}
}

// TestExec_GuardDenied 未放行时**不执行命令**且只产 up=0。
// 关键断言是「替身没被调用」——护栏的意义就在于不让它跑起来。
func TestExec_GuardDenied(t *testing.T) {
	cfg := execConfig("redisinfo", template.Target{Command: "/usr/local/bin/redis-cli"})
	mustValid(t, cfg)

	cases := map[string]config.TemplateGuardsConfig{
		"未启用 exec": {},
		"命令不在白名单":  guardExec("/bin/other"),
		"白名单前缀更长":  guardExec("/usr/local/bin/redis-cli-x"),
	}
	for name, g := range cases {
		t.Run(name, func(t *testing.T) {
			r := localRunner(t, g)
			called := false
			r.runCmd = func(context.Context, string, []string) ([]byte, []byte, error) {
				called = true
				return nil, nil, nil
			}
			got := r.CollectTemplate(context.Background(), cfg)
			if called {
				t.Fatal("未放行的命令不得被执行")
			}
			if len(got) != 1 || got[0].Value != 0 {
				t.Fatalf("应只产 up=0，got %+v", got)
			}
		})
	}
}

// TestExec_EmptyOutputProducesNoData 命令成功但没有匹配 → 不产出该指标（与 http-text 语义一致）。
func TestExec_EmptyOutputProducesNoData(t *testing.T) {
	const cmd = "/bin/echo"
	cfg := execConfig("redisinfo", template.Target{Command: cmd})
	mustValid(t, cfg)

	r := localRunner(t, guardExec(cmd))
	r.runCmd = func(context.Context, string, []string) ([]byte, []byte, error) {
		return []byte("nothing here\n"), nil, nil
	}
	got := r.CollectTemplate(context.Background(), cfg)
	// 命令执行成功但没有匹配 → 只产出 up（不产出任何数据指标），与 http-text 的「未命中不产出」一致
	if len(got) != 1 || got[0].Name != template.UpMetricName || got[0].Value != 1 {
		t.Fatalf("命令执行成功应只产出 up=1，got %+v", got)
	}
}

// ---------- file ----------

// TestFile_ReadsTailAndExtracts 快照读取：按正则取当前值。
func TestFile_ReadsTailAndExtracts(t *testing.T) {
	requirePosixPaths(t)
	path := filepath.Join(t.TempDir(), "metrics.txt")
	if err := os.WriteFile(path, []byte("stale 1\nqueue_depth 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := fileConfig("appstate", template.Target{Instance: "app-01", Path: path})
	mustValid(t, cfg)

	got := byName(localRunner(t, guardFile(path)).CollectTemplate(context.Background(), cfg))
	if v := got["appstate_queue_depth"]; len(v) != 1 || v[0].Value != 7 {
		t.Fatalf("取值错误：%+v", v)
	}
}

// TestFile_MaxBytesLimitsToTail 只读末尾 N 字节：更早的内容不参与匹配。
func TestFile_MaxBytesLimitsToTail(t *testing.T) {
	requirePosixPaths(t)
	path := filepath.Join(t.TempDir(), "metrics.txt")
	// 前面一行在末尾 16 字节之外，只有最后一行落在窗口内
	body := "queue_depth 111\n" + strings.Repeat("#\n", 20) + "queue_depth 999\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := fileConfig("appstate", template.Target{Path: path, MaxBytes: 16})
	mustValid(t, cfg)

	got := byName(localRunner(t, guardFile(path)).CollectTemplate(context.Background(), cfg))
	v := got["appstate_queue_depth"]
	if len(v) != 1 || v[0].Value != 999 {
		t.Fatalf("只应取到末尾窗口内的值（999），got %+v", v)
	}
}

// TestFile_GuardDenied 未放行的路径不读取，只产 up=0。
func TestFile_GuardDenied(t *testing.T) {
	requirePosixPaths(t)
	path := filepath.Join(t.TempDir(), "metrics.txt")
	if err := os.WriteFile(path, []byte("queue_depth 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := fileConfig("appstate", template.Target{Path: path})
	mustValid(t, cfg)

	for name, g := range map[string]config.TemplateGuardsConfig{
		"未启用 file": {},
		"路径不在白名单":  {File: config.GuardRule{Enabled: true, Allow: []string{path + ".bak"}}},
	} {
		t.Run(name, func(t *testing.T) {
			got := localRunner(t, g).CollectTemplate(context.Background(), cfg)
			if len(got) != 1 || got[0].Value != 0 {
				t.Fatalf("应只产 up=0，got %+v", got)
			}
		})
	}
}

// TestFile_SymlinkMustResolveIntoAllowlist 软链接按**解析后**的路径比对白名单：
// 否则白名单里写 /safe/x.log，模板给一个指向 /etc/shadow 的软链即可绕过。
func TestFile_SymlinkMustResolveIntoAllowlist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 创建软链接需要额外权限；该场景在 Linux 实机验证覆盖")
	}
	dir := t.TempDir()
	secret := filepath.Join(dir, "shadow")
	if err := os.WriteFile(secret, []byte("queue_depth 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "safe.log")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("无法创建软链接：%v", err)
	}
	cfg := fileConfig("appstate", template.Target{Path: link})
	mustValid(t, cfg)

	// 只放行软链路径（管理员以为在读 safe.log）→ 必须被拒：实际指向别处
	got := localRunner(t, guardFile(link)).CollectTemplate(context.Background(), cfg)
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("软链指向白名单外的真实文件时应拒绝，got %+v", got)
	}

	// 放行真实路径 → 允许
	got2 := byName(localRunner(t, guardFile(secret)).CollectTemplate(context.Background(), cfg))
	if v := got2["appstate_queue_depth"]; len(v) != 1 || v[0].Value != 7 {
		t.Fatalf("放行真实路径后应能读取：%+v", v)
	}
}

// TestFile_RejectsNonRegularFile 目录/设备不是「文件」，直接拒绝（只读快照语义不适用）。
func TestFile_RejectsNonRegularFile(t *testing.T) {
	requirePosixPaths(t)
	dir := t.TempDir()
	cfg := fileConfig("appstate", template.Target{Path: dir})
	mustValid(t, cfg)

	got := localRunner(t, guardFile(dir)).CollectTemplate(context.Background(), cfg)
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("目录不是普通文件，应拒绝，got %+v", got)
	}
}

// TestFile_MissingYieldsUpZero 文件不存在 → up=0（不静默产 0 值）。
func TestFile_MissingYieldsUpZero(t *testing.T) {
	requirePosixPaths(t)
	path := filepath.Join(t.TempDir(), "nope.txt")
	cfg := fileConfig("appstate", template.Target{Path: path})
	mustValid(t, cfg)

	got := localRunner(t, guardFile(path)).CollectTemplate(context.Background(), cfg)
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("应只产 up=0，got %+v", got)
	}
}

// ---------- jdbc ----------

// TestJDBC_RunsOneQueryPerMetric 每条指标一次查询；取标量后按名产出。
func TestJDBC_RunsOneQueryPerMetric(t *testing.T) {
	cfg := jdbcConfig("bizdb", jdbcTarget(),
		template.MetricRule{Name: "order_count", Query: "SELECT COUNT(*) FROM orders"},
		template.MetricRule{Name: "deadlocks", Query: "SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'", Column: "Value"},
	)
	mustValid(t, cfg)

	r := localRunner(t, guardJDBC())
	var seen []string
	r.queryScalar = func(_ context.Context, _ template.Config, _ template.Target, rule template.MetricRule) (float64, bool, error) {
		seen = append(seen, rule.Query)
		if rule.Name == "order_count" {
			return 42, true, nil
		}
		return 3, true, nil
	}

	got := byName(r.CollectTemplate(context.Background(), cfg))
	if len(seen) != 2 {
		t.Fatalf("每条指标应各查一次，got %v", seen)
	}
	if v := got["bizdb_order_count"]; len(v) != 1 || v[0].Value != 42 {
		t.Fatalf("order_count 取值错误：%+v", v)
	}
	if v := got["bizdb_deadlocks"]; len(v) != 1 || v[0].Value != 3 {
		t.Fatalf("deadlocks 取值错误：%+v", v)
	}
}

// TestJDBC_GuardDenied 未放行时**不发起任何查询**。
func TestJDBC_GuardDenied(t *testing.T) {
	cfg := jdbcConfig("bizdb", jdbcTarget(),
		template.MetricRule{Name: "order_count", Query: "SELECT COUNT(*) FROM orders"})
	mustValid(t, cfg)

	r := localRunner(t, config.TemplateGuardsConfig{})
	called := false
	r.queryScalar = func(context.Context, template.Config, template.Target, template.MetricRule) (float64, bool, error) {
		called = true
		return 1, true, nil
	}
	got := r.CollectTemplate(context.Background(), cfg)
	if called {
		t.Fatal("未放行 jdbc 时不得发起查询")
	}
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("应只产 up=0，got %+v", got)
	}
}

// TestJDBC_ReadOnlyCheckRunsAtRuntime 只读校验在执行侧**再查一次**：
// 模板也可能来自 Server 下发，不能只依赖保存时的校验。
func TestJDBC_ReadOnlyCheckRunsAtRuntime(t *testing.T) {
	cfg := jdbcConfig("bizdb", jdbcTarget(),
		template.MetricRule{Name: "bad", Query: "DELETE FROM orders"})
	// 刻意绕过 ValidateAll（模拟「校验被跳过的旁路」）直接调用执行层
	r := localRunner(t, guardJDBC())
	called := false
	r.queryScalar = func(context.Context, template.Config, template.Target, template.MetricRule) (float64, bool, error) {
		called = true
		return 1, true, nil
	}
	got := r.CollectTemplate(context.Background(), cfg)
	if called {
		t.Fatal("写操作 SQL 不得被执行（第二道只读校验失效）")
	}
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("应只产 up=0，got %+v", got)
	}
}

// TestJDBC_QueryErrorYieldsUpZero 查询失败 → up=0 且不产数据。
func TestJDBC_QueryErrorYieldsUpZero(t *testing.T) {
	cfg := jdbcConfig("bizdb", jdbcTarget(),
		template.MetricRule{Name: "order_count", Query: "SELECT COUNT(*) FROM orders"})
	mustValid(t, cfg)

	r := localRunner(t, guardJDBC())
	r.queryScalar = func(context.Context, template.Config, template.Target, template.MetricRule) (float64, bool, error) {
		return 0, false, os.ErrDeadlineExceeded
	}
	got := r.CollectTemplate(context.Background(), cfg)
	if len(got) != 1 || got[0].Value != 0 {
		t.Fatalf("应只产 up=0，got %+v", got)
	}
}

// TestJDBC_ValueMissingProducesNoData 列不存在/无行 → 不产出该指标（与 http-json 的「路径缺失不产出」一致）。
func TestJDBC_ValueMissingProducesNoData(t *testing.T) {
	cfg := jdbcConfig("bizdb", jdbcTarget(),
		template.MetricRule{Name: "order_count", Query: "SELECT COUNT(*) FROM orders", Column: "nope"})
	mustValid(t, cfg)

	r := localRunner(t, guardJDBC())
	r.queryScalar = func(context.Context, template.Config, template.Target, template.MetricRule) (float64, bool, error) {
		return 0, false, nil
	}
	got := r.CollectTemplate(context.Background(), cfg)
	if len(got) != 1 {
		t.Fatalf("取不到值时应只产 up，got %+v", got)
	}
}

// TestSQLToFloat 驱动返回的类型五花八门，取值口径必须明确。
func TestSQLToFloat(t *testing.T) {
	// 用切片而不是 map：[]byte 不可比较，不能做 map 键
	cases := []struct {
		in   any
		want float64
	}{
		{int64(42), 42},
		{float64(1.5), 1.5},
		{true, 1},
		{false, 0},
		{[]byte("314"), 314},
		{"2.5", 2.5},
		{[]byte(" 7 "), 7},
	}
	for _, tc := range cases {
		got, ok := sqlToFloat(tc.in)
		if !ok || got != tc.want {
			t.Errorf("sqlToFloat(%#v) = (%v, %v), want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []any{nil, "abc", []byte("abc"), struct{}{}} {
		if _, ok := sqlToFloat(in); ok {
			t.Errorf("sqlToFloat(%#v) 应判定为取不到值", in)
		}
	}
}

// TestJDBCDSN 连接串沿用既有采集器的口径（超时 + sslmode），并对参数做白名单化拼接。
func TestJDBCDSN(t *testing.T) {
	target := jdbcTarget()
	mysqlDSN := jdbcDSN(template.Config{Driver: "mysql"}, target)
	for _, want := range []string{"monitor:enc:xxxx@tcp(10.0.0.5:3306)/appdb", "timeout=5s", "readTimeout=5s"} {
		if !strings.Contains(mysqlDSN, want) {
			t.Errorf("mysql DSN 应包含 %q，got %q", want, mysqlDSN)
		}
	}

	pgDSN := jdbcDSN(template.Config{Driver: "postgres"},
		template.Target{Addr: "10.0.0.6:5432", Database: "appdb", Auth: target.Auth,
			Params: map[string]string{"sslmode": "require"}})
	for _, want := range []string{"host=10.0.0.6", "port=5432", "sslmode=require", "connect_timeout=5", "dbname=appdb"} {
		if !strings.Contains(pgDSN, want) {
			t.Errorf("postgres DSN 应包含 %q，got %q", want, pgDSN)
		}
	}
	// 未给 sslmode 时回退 disable（与既有 PostgreSQL 采集器一致）
	pgDefault := jdbcDSN(template.Config{Driver: "postgres"}, target)
	if !strings.Contains(pgDefault, "sslmode=disable") {
		t.Errorf("未配置 sslmode 应回退 disable，got %q", pgDefault)
	}
}

func timeNow() any { return struct{}{} }
