package collector

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// 本文件实现阶段三的三类「非网络取数」：exec（本机命令）、file（本机文件）、jdbc（数据库只读查询）。
//
// 三者共享两个前提：
//   - 都要先过**本机护栏**（`templateGuards`）：未放行的取数方式、以及不在白名单里的命令/路径一律拒绝。
//     这是「机器自身的同意优先于中心的授权」的落点——Server 侧过滤只是第一道门。
//   - 失败语义与阶段一一致：取数/解析失败只产 template_target_up=0，**不产数据**
//     （否则上一轮的值会被误读为当前值，也失去「静默无数据」这个可见信号）。

// execDefaultPath 是传给被执行的程序的环境变量 PATH 兜底值。
const execDefaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// errGuardDenied 表示被本机护栏拦下。
//
// 与「采集失败」分开的原因：护栏拒绝属**配置问题，且每轮都会发生**，
// 由调用点做去重告警（同一模板同一原因只报一次）；否则会按采集周期刷屏，把真正的问题淹掉。
type errGuardDenied struct{ reason string }

func (e errGuardDenied) Error() string { return e.reason }

func guardDenied(format string, args ...any) error {
	return errGuardDenied{reason: fmt.Sprintf(format, args...)}
}

// collectExec 执行本机命令并解析其输出。
func (r *TemplateRunner) collectExec(ctx context.Context, tpl template.Config, t template.Target, now int64) ([]model.Metric, error) {
	if !r.guards.AllowsCommand(t.Command) {
		return nil, guardDenied("命令 %q 未在本机 templateGuards.exec.allow 中放行", t.Command)
	}
	run := r.runCmd
	if run == nil {
		run = runLocalCommand
	}
	// 超时到期由 CommandContext 直接杀进程（任务级 ctx 仍在更外层兜底）
	cctx, cancel := context.WithTimeout(ctx, time.Duration(t.EffectiveTimeoutSec())*time.Second)
	defer cancel()

	stdout, stderr, err := run(cctx, t.Command, t.Args)
	if err != nil {
		// 日志只带程序名与截断后的 stderr；不整段回显（输出可能很长，也可能含敏感信息）
		return nil, fmt.Errorf("执行 %s 失败：%w（stderr: %s）", filepath.Base(t.Command), err, truncateForLog(stderr))
	}
	// 解析沿用 http-text 的正则语义（含「未命中不产出」）
	return r.mapText(tpl, t, stdout, now)
}

// runLocalCommand 真实执行命令：argv 直传、工作目录固定、环境变量只给 PATH。
func runLocalCommand(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// 工作目录固定：读相对路径的行为不该随 Agent 的工作目录变化
	cmd.Dir = "/"
	// 环境变量只给 PATH：不把 Agent 进程的敏感环境变量（如 cryptoKey）交给被执行的程序
	path := os.Getenv("PATH")
	if strings.TrimSpace(path) == "" {
		path = execDefaultPath
	}
	cmd.Env = []string{"PATH=" + path}

	stdout := &cappedBuffer{max: template.MaxBodyBytes}
	stderr := &cappedBuffer{max: 4096}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// cappedBuffer 是有上限的写出缓冲：命令可能疯狂输出，不能无上限进内存。
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remain := c.max - c.buf.Len(); remain > 0 {
		if len(p) > remain {
			p = p[:remain]
		}
		c.buf.Write(p)
	}
	// 始终返回成功：写满后若返回错误，命令会因写失败提前退出，反而拿不到真实的退出码与状态
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }

// collectFile 读取本机文件末尾并解析（快照语义：当前值通常在文件末尾）。
func (r *TemplateRunner) collectFile(ctx context.Context, tpl template.Config, t template.Target, now int64) ([]model.Metric, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// 软链接必须**解析后**再比对白名单：白名单里写的是 /var/log/app.log 时，
	// 模板只要给一个指向 /etc/shadow 的软链就能绕过——校验的必须是「实际读到的那个文件」。
	resolved, err := filepath.EvalSymlinks(t.Path)
	if err != nil {
		return nil, fmt.Errorf("路径不可用：%w", err)
	}
	if !r.guards.AllowsPath(resolved) {
		if resolved != t.Path {
			return nil, guardDenied("路径 %q（实际指向 %q）未在本机 templateGuards.file.allow 中放行", t.Path, resolved)
		}
		return nil, guardDenied("路径 %q 未在本机 templateGuards.file.allow 中放行", t.Path)
	}
	body, err := readFileTail(resolved, t.EffectiveMaxBytes())
	if err != nil {
		return nil, err
	}
	return r.mapText(tpl, t, body, now)
}

// readFileTail 读取文件末尾最多 maxBytes 字节；只接受普通文件（目录/设备没有「末尾」语义）。
func readFileTail(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path) // 只读，不创建、不写入
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s 不是普通文件", path)
	}
	if off := st.Size() - maxBytes; off > 0 {
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, maxBytes))
}

// collectJDBC 连接数据库执行只读查询取值。
func (r *TemplateRunner) collectJDBC(ctx context.Context, tpl template.Config, t template.Target, now int64) ([]model.Metric, error) {
	if !r.guards.AllowsKind(template.KindJDBC) {
		return nil, guardDenied("kind=jdbc 未在本机放行（templateGuards.jdbc.enabled=false）")
	}
	if !r.guards.AllowsDBHost(t.Addr) {
		return nil, guardDenied("目标库 %q 未在本机 templateGuards.jdbc.allowHosts 中放行", t.Addr)
	}
	query := r.queryScalar
	if query == nil {
		query = runScalarQuery
	}
	out := make([]model.Metric, 0, len(tpl.Rules.Metrics))
	for _, rule := range tpl.Rules.Metrics {
		// 第二道只读校验：模板也可能来自 Server 下发，不能只依赖保存时的校验
		if err := template.ValidateReadOnlyQuery(rule.Query); err != nil {
			return nil, err
		}
		v, ok, err := query(ctx, tpl, t, rule)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue // 取不到值（无行 / 列不存在）→ 不产出该指标，与 http-json 的「路径缺失不产出」一致
		}
		name := template.EnsurePrefix(tpl.ID, rule.Name)
		if len(name) > template.MaxMetricNameLen {
			continue
		}
		out = append(out, model.Metric{
			Node: r.node, Name: name, Labels: r.buildLabels(tpl, t, nil), Value: v, Timestamp: now,
		})
	}
	return out, nil
}

// runScalarQuery 真实执行一次查询并取标量；连接每次新建、用完即关
//（与既有 MySQL / PostgreSQL 采集器一致，避免在采集进程里长期持有连接池）。
func runScalarQuery(ctx context.Context, tpl template.Config, t template.Target, rule template.MetricRule) (float64, bool, error) {
	db, err := sql.Open(tpl.Driver, jdbcDSN(tpl, t))
	if err != nil {
		return 0, false, err
	}
	defer db.Close()
	qctx, cancel := context.WithTimeout(ctx, templateFetchTimeout)
	defer cancel()
	rows, err := db.QueryContext(qctx, rule.Query)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	return scanScalar(rows, rule.Column)
}

// scanScalar 取结果首行的标量值：column 为空时取第一列。
func scanScalar(rows *sql.Rows, column string) (float64, bool, error) {
	if !rows.Next() {
		return 0, false, rows.Err()
	}
	cols, err := rows.Columns()
	if err != nil {
		return 0, false, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return 0, false, err
	}
	idx := 0
	if column != "" {
		found := false
		for i, c := range cols {
			if c == column {
				idx, found = i, true
				break
			}
		}
		if !found {
			// 列名在不同版本可能不同：视作「取不到值」而不是报错，避免整轮采集因一个列名失败
			return 0, false, nil
		}
	}
	v, ok := sqlToFloat(vals[idx])
	return v, ok, nil
}

// sqlToFloat 把驱动返回的值转成数值。驱动可能给出 int64 / float64 / []byte / string / bool；
// nil 或不可解析的文本视为「取不到值」。
func sqlToFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case []byte:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(x)), 64)
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// jdbcDSN 拼出驱动连接串，沿用既有采集器的参数口径（超时 + sslmode），不另造一套。
//
// 库名与参数由 DSL 校验限制为「标识符 / 安全字符」，因此拼接不会污染连接串
//（例如 postgres DSN 用空格分隔参数，值里带空格就能注入参数）。
func jdbcDSN(tpl template.Config, t template.Target) string {
	user, pass := "", ""
	if t.Auth != nil && t.Auth.Basic != nil {
		user, pass = t.Auth.Basic.User, t.Auth.Basic.Password
	}
	if tpl.Driver == "postgres" {
		host, port := splitHostPort(t.Addr, "5432")
		ssl := ""
		if t.Params != nil {
			ssl = t.Params["sslmode"]
		}
		if ssl == "" {
			ssl = "disable" // 与既有 PostgreSQL 采集器的回退一致
		}
		return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s connect_timeout=5",
			host, port, user, pass, t.Database, ssl)
	}
	return fmt.Sprintf("%s:%s@tcp(%s)/%s?timeout=5s&readTimeout=5s", user, pass, t.Addr, t.Database)
}

// truncateForLog 截断用于日志的文本。
func truncateForLog(b []byte) string {
	const limit = 512
	s := strings.TrimSpace(string(b))
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
