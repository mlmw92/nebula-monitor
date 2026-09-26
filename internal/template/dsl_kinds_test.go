package template

import (
	"strings"
	"testing"
)

// 阶段三的三类 kind：`jdbc` / `exec` / `file`。
// 它们与前两批（网络取数）性质不同——后两类**以 root 触碰被监控机本身**，
// 所以校验除了「格式对不对」，更要挡住「看起来能跑、实际是灾难」的配置：
// 写操作 SQL、多语句、相对路径、跨 kind 的残留字段（复制粘贴而来）。

func validJDBC() Config {
	return Config{
		ID:     "bizdb",
		Kind:   KindJDBC,
		Driver: "mysql",
		Targets: []Target{{
			Instance: "biz-db-01",
			Addr:     "10.0.0.5:3306",
			Database: "appdb",
			Auth:     &Auth{Basic: &BasicAuth{User: "monitor", Password: "enc:xxxx"}},
		}},
		Rules: Rules{Metrics: []MetricRule{{
			Name:  "order_count",
			Query: "SELECT COUNT(*) FROM orders",
		}}},
	}
}

func validExec() Config {
	return Config{
		ID:   "redisinfo",
		Kind: KindExec,
		Targets: []Target{{
			Instance: "cache-01",
			Command:  "/usr/local/bin/redis-cli",
			Args:     []string{"-h", "127.0.0.1", "INFO"},
		}},
		Rules: Rules{Metrics: []MetricRule{{
			Name:    "ops_per_sec",
			Pattern: `instantaneous_ops_per_sec:(\d+)`,
		}}},
	}
}

func validFile() Config {
	return Config{
		ID:   "appstate",
		Kind: KindFile,
		Targets: []Target{{
			Instance: "app-01",
			Path:     "/var/lib/myapp/metrics.txt",
		}},
		Rules: Rules{Metrics: []MetricRule{{
			Name:    "queue_depth",
			Pattern: `^queue_depth (\d+)$`,
		}}},
	}
}

func TestValidate_AcceptsThreeNewKinds(t *testing.T) {
	cases := map[string]Config{"jdbc": validJDBC(), "exec": validExec(), "file": validFile()}
	for name, c := range cases {
		if err := ValidateAll([]Config{c}); err != nil {
			t.Errorf("%s 最小合法模板应通过校验，got %v", name, err)
		}
	}
}

// TestValidate_NewKindsRejectResponseRules 三类新 kind 是「声明式取值」：
// 指标名与标签全部来自 rules.metrics，响应里没有「一堆指标名」可供 keep/drop/rename 作用。
// 静默忽略这些字段会让人以为规则生效了（排查成本极高），故一律拒绝。
func TestValidate_NewKindsRejectResponseRules(t *testing.T) {
	cases := []struct {
		name string
		mut  func(c *Config)
	}{
		{"keep", func(c *Config) { c.Rules.Keep = "^x_" }},
		{"drop", func(c *Config) { c.Rules.Drop = "_created$" }},
		{"rename", func(c *Config) { c.Rules.Rename = []RenameRule{{Match: "^a$", To: "b"}} }},
		{"unlabel", func(c *Config) { c.Rules.Unlabel = []string{"k"} }},
		{"aggregate", func(c *Config) { c.Rules.Aggregate = []AggregateRule{{Match: "^x$", Op: AggSum}} }},
		{"promoteLabel", func(c *Config) { c.Rules.PromoteLabel = []PromoteLabelRule{{Match: "^x$", Label: "k"}} }},
	}
	mk := func(kind Kind) Config {
		switch kind {
		case KindJDBC:
			return validJDBC()
		case KindExec:
			return validExec()
		default:
			return validFile()
		}
	}
	for _, kind := range GuardedKinds {
		for _, tc := range cases {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				c := mk(kind)
				tc.mut(&c)
				if err := ValidateAll([]Config{c}); err == nil {
					t.Fatalf("应为「不适用于该 kind」而拒绝")
				}
			})
		}
	}
	// 静态标签对所有 kind 都有效，不应被误伤
	c := validFile()
	c.Rules.Labels = map[string]string{"app": "portal"}
	if err := ValidateAll([]Config{c}); err != nil {
		t.Fatalf("静态标签应仍被允许，got %v", err)
	}
}

// TestValidate_NewKindsRequireMetrics 三类都必须给出取数声明，否则模板毫无产出。
func TestValidate_NewKindsRequireMetrics(t *testing.T) {
	for name, c := range map[string]Config{"jdbc": validJDBC(), "exec": validExec(), "file": validFile()} {
		c.Rules.Metrics = nil
		if err := ValidateAll([]Config{c}); err == nil || !strings.Contains(err.Error(), "metrics") {
			t.Errorf("%s 缺 metrics 应被拒绝并说明原因，got %v", name, err)
		}
	}
}

// TestValidate_JDBCRejects 数据库直连的「灾难配置」：写操作、多语句、缺库名/账号、跨 kind 残留字段。
func TestValidate_JDBCRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{"driver 为空", func(c *Config) { c.Driver = "" }, "driver"},
		{"driver 非法", func(c *Config) { c.Driver = "oracle" }, "driver"},
		{"缺 database", func(c *Config) { c.Targets[0].Database = "" }, "database"},
		{"缺 user", func(c *Config) { c.Targets[0].Auth = nil }, "user"},
		{"缺 addr", func(c *Config) { c.Targets[0].Addr = "" }, "addr"},
		{"缺 query", func(c *Config) { c.Rules.Metrics[0].Query = "" }, "query"},
		{"写操作 query", func(c *Config) { c.Rules.Metrics[0].Query = "DELETE FROM orders" }, "只读"},
		{"多语句 query", func(c *Config) { c.Rules.Metrics[0].Query = "SELECT 1; DROP TABLE t" }, "单条"},
		{"column 非法", func(c *Config) { c.Rules.Metrics[0].Column = "1; drop" }, "column"},
		// 复制粘贴残留：jdbc 不认识 command / path
		{"残留 command", func(c *Config) { c.Targets[0].Command = "/bin/echo" }, "command"},
		{"残留 path", func(c *Config) { c.Targets[0].Path = "/etc/passwd" }, "path"},
		// 连接串字段会被拼进 DSN（postgres 用空格分隔参数），故必须限制字符集
		{"addr 含空格", func(c *Config) { c.Targets[0].Addr = "10.0.0.5:3306 sslmode=require" }, "不允许的字符"},
		{"addr 含斜杠", func(c *Config) { c.Targets[0].Addr = "10.0.0.5/appdb" }, "不允许的字符"},
		{"database 含斜杠", func(c *Config) { c.Targets[0].Database = "appdb/x" }, "database"},
		{"params 值含空格", func(c *Config) {
			c.Targets[0].Params = map[string]string{"sslmode": "require connect_timeout=1"}
		}, "params"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validJDBC()
			tc.mut(&c)
			err := ValidateAll([]Config{c})
			if err == nil {
				t.Fatalf("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestValidate_ExecRejects 命令执行的边界：必须是绝对路径、参数可控、超时有上限。
func TestValidate_ExecRejects(t *testing.T) {
	long := strings.Repeat("a", 513)
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{"相对路径", func(c *Config) { c.Targets[0].Command = "redis-cli" }, "绝对路径"},
		{"含 .. 的路径", func(c *Config) { c.Targets[0].Command = "/usr/../bin/sh" }, "绝对路径"},
		{"路径过长", func(c *Config) { c.Targets[0].Command = "/" + long }, "长度"},
		{"参数过多", func(c *Config) { c.Targets[0].Args = make([]string, MaxCommandArgs+1) }, "参数"},
		{"参数含 NUL", func(c *Config) { c.Targets[0].Args = []string{"a\x00b"} }, "NUL"},
		{"超时越界", func(c *Config) { c.Targets[0].TimeoutSec = MaxExecTimeoutSec + 1 }, "timeoutSec"},
		{"超时为负", func(c *Config) { c.Targets[0].TimeoutSec = -1 }, "timeoutSec"},
		{"缺 pattern", func(c *Config) { c.Rules.Metrics[0].Pattern = "" }, "pattern"},
		{"缺 command", func(c *Config) { c.Targets[0].Command = "" }, "command"},
		// 复制粘贴残留：exec 不认识 addr / path
		{"残留 addr", func(c *Config) { c.Targets[0].Addr = "http://127.0.0.1:9100/metrics" }, "addr"},
		{"残留 path", func(c *Config) { c.Targets[0].Path = "/etc/passwd" }, "path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validExec()
			tc.mut(&c)
			err := ValidateAll([]Config{c})
			if err == nil {
				t.Fatalf("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestValidate_ExecDefaults 未配置超时时用默认值（5s），而不是 0（0 会被误读为不超时）。
func TestValidate_ExecDefaults(t *testing.T) {
	c := validExec()
	if err := ValidateAll([]Config{c}); err != nil {
		t.Fatalf("默认超时应通过校验，got %v", err)
	}
	if got := c.Targets[0].EffectiveTimeoutSec(); got != DefaultExecTimeoutSec {
		t.Fatalf("EffectiveTimeoutSec = %d, want %d", got, DefaultExecTimeoutSec)
	}
	c.Targets[0].TimeoutSec = 3
	if got := c.Targets[0].EffectiveTimeoutSec(); got != 3 {
		t.Fatalf("显式配置应生效，got %d", got)
	}
}

// TestValidate_FileRejects 文件读取的边界：绝对路径、大小上限。
func TestValidate_FileRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(c *Config)
		want string
	}{
		{"相对路径", func(c *Config) { c.Targets[0].Path = "var/log/x.log" }, "绝对路径"},
		{"含 .. 的路径", func(c *Config) { c.Targets[0].Path = "/etc/../etc/shadow" }, "绝对路径"},
		{"路径过长", func(c *Config) { c.Targets[0].Path = "/" + strings.Repeat("a", 513) }, "长度"},
		{"maxBytes 越界", func(c *Config) { c.Targets[0].MaxBytes = MaxFileReadBytes + 1 }, "maxBytes"},
		{"maxBytes 为负", func(c *Config) { c.Targets[0].MaxBytes = -1 }, "maxBytes"},
		{"缺 path", func(c *Config) { c.Targets[0].Path = "" }, "path"},
		{"残留 command", func(c *Config) { c.Targets[0].Command = "/bin/cat" }, "command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validFile()
			tc.mut(&c)
			err := ValidateAll([]Config{c})
			if err == nil {
				t.Fatalf("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestValidate_FileDefaults 未配置 maxBytes 时用默认 1 MiB（快照语义：最新值通常在文件末尾）。
func TestValidate_FileDefaults(t *testing.T) {
	c := validFile()
	if err := ValidateAll([]Config{c}); err != nil {
		t.Fatalf("got %v", err)
	}
	if got := c.Targets[0].EffectiveMaxBytes(); got != DefaultFileReadBytes {
		t.Fatalf("EffectiveMaxBytes = %d, want %d", got, DefaultFileReadBytes)
	}
}

// TestValidateReadOnlyQuery 只读判定是「防批量数据损坏」的第一道防线
// （第二道在执行侧再次调用），因此要覆盖常见的绕过写法。
func TestValidateReadOnlyQuery(t *testing.T) {
	ok := []string{
		"SELECT 1",
		"select count(*) from orders where id > 0",
		"  SELECT 1  ",
		"SELECT 1;",
		"SHOW GLOBAL STATUS LIKE 'Innodb_deadlocks'",
		"EXPLAIN SELECT 1",
		"select(1)",
	}
	for _, q := range ok {
		if err := ValidateReadOnlyQuery(q); err != nil {
			t.Errorf("应接受 %q，got %v", q, err)
		}
	}
	bad := []string{
		"",
		"   ",
		"DELETE FROM orders",
		"UPDATE t SET a=1",
		"INSERT INTO t VALUES (1)",
		"DROP TABLE t",
		"TRUNCATE t",
		"SELECT 1; DROP TABLE t",               // 多语句
		"selectfrom_x",                         // 前缀不在词边界
		"/* hint */ SELECT 1",                  // 注释开头：判定不冒险放宽（要求直接以只读关键字开头）
		"WITH x AS (SELECT 1) SELECT * FROM x", // CTE 可能是写操作的载体，阶段三不支持
	}
	for _, q := range bad {
		if err := ValidateReadOnlyQuery(q); err == nil {
			t.Errorf("应拒绝 %q", q)
		}
	}
}

// TestIsAbsolutePath agent 只跑在 Linux 上（不做 Windows 节点），且白名单比对依赖规范化后的文本，
// 因此要求 POSIX 风格绝对路径并直接拒绝 `..`。
func TestIsAbsolutePath(t *testing.T) {
	for _, p := range []string{"/bin/sh", "/var/log/x.log", "/a"} {
		if !IsAbsolutePath(p) {
			t.Errorf("%q 应判定为绝对路径", p)
		}
	}
	for _, p := range []string{"", "bin/sh", "./x", "/usr/../bin/sh", "C:\\x", "/a/../../etc/shadow"} {
		if IsAbsolutePath(p) {
			t.Errorf("%q 不应判定为绝对路径", p)
		}
	}
}

// TestKindEnabledOnNode 节点能否执行某 kind 的模板（Server 据此过滤下发）。
func TestKindEnabledOnNode(t *testing.T) {
	// 网络取数：任何节点都能执行，与声明无关
	for _, k := range []Kind{KindPrometheusExporter, KindHTTPJSON, KindHTTPText} {
		if !KindEnabledOnNode(k, nil) {
			t.Errorf("%s 不依赖本机能力，任何节点都应能执行", k)
		}
	}
	// 护栏类：必须由该节点声明
	for _, k := range GuardedKinds {
		if KindEnabledOnNode(k, nil) {
			t.Errorf("%s 未声明时不应下发", k)
		}
		if !KindEnabledOnNode(k, []string{string(k)}) {
			t.Errorf("%s 声明后应下发", k)
		}
		// 声明了别的取数方式不算
		other := string(KindExec)
		if k == KindExec {
			other = string(KindFile)
		}
		if KindEnabledOnNode(k, []string{other}) {
			t.Errorf("%s 不应因声明了 %s 而被下发", k, other)
		}
	}
	// 旧 Agent 上报空清单（或压根不报）：护栏类一律不下发
	if KindEnabledOnNode(KindExec, []string{}) {
		t.Error("旧 Agent 不应收到 exec 模板")
	}
}

// TestIsGuardedKind 三类「本机取数」需要本机护栏放行，网络取数不需要。
func TestIsGuardedKind(t *testing.T) {
	for _, k := range GuardedKinds {
		if !IsGuardedKind(k) {
			t.Errorf("%s 应为受护栏约束的 kind", k)
		}
	}
	for _, k := range []Kind{KindPrometheusExporter, KindHTTPJSON, KindHTTPText} {
		if IsGuardedKind(k) {
			t.Errorf("%s 不应受本机护栏约束", k)
		}
	}
	if len(AllKinds) != 6 {
		t.Fatalf("AllKinds 应有 6 类，got %d", len(AllKinds))
	}
}
