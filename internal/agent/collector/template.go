package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/agent/config"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
)

// templateFetchTimeout 是模板单次 HTTP 拉取的上界，与既有 exporter 拉取保持一致
// （任务级 ctx 仍在更外层兜底，两者谁先到谁生效）。
const templateFetchTimeout = 5 * time.Second

// TemplateRunner 执行采集项模板：拉取 → 解析 → 过滤/改名/打标签 → 产出 model.Metric。
//
// 每个模板由 collect_all.go 挂为独立采集任务，因此天然继承 per-task 超时与失败隔离；
// 本类型自身不做并发（模板之间已由任务调度器并发）。
type TemplateRunner struct {
	node   string
	now    func() int64
	client *http.Client
	// res 缓存已编译正则：模板规则在 Agent 生命周期内不变，缓存可避免每轮重复编译。
	res sync.Map // pattern -> *regexp.Regexp
	// warned 记录已告警过的产出问题（如「同名同标签序列」）：同一问题只告警一次，
	// 否则每轮采集都刷屏，反而把其它问题淹掉。
	warned sync.Map // key: tplID\x00metric
	// guards 是本机护栏（阶段三）：决定 exec/file/jdbc 三类是否放行、以及允许哪些命令与路径。
	guards config.TemplateGuardsConfig
	// runCmd 与 queryScalar 是两处 I/O 缝隙：默认走真实实现，单测注入替身，
	// 这样 jdbc/exec 的取值与护栏判定不依赖平台上的具体数据库与可执行文件。
	runCmd      func(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)
	queryScalar func(ctx context.Context, tpl template.Config, t template.Target, rule template.MetricRule) (float64, bool, error)
}

// NewTemplateRunner 创建模板执行器。
//
// 未注入护栏时（零值）三类「本机 / 数据库取数」全部视为**未放行**，
// 因此忘记注入的后果是这些模板不产出，而不是悄悄获得 root 能力——
// 默认值必须站在安全的一侧。
func NewTemplateRunner(node string) *TemplateRunner {
	return &TemplateRunner{
		node:   node,
		now:    model.NowMillis,
		client: &http.Client{Timeout: templateFetchTimeout},
	}
}

// WithGuards 注入本机护栏（阶段三）。返回自身便于链式构造。
func (r *TemplateRunner) WithGuards(g config.TemplateGuardsConfig) *TemplateRunner {
	r.guards = g
	return r
}

// CollectTemplate 采集单个模板的全部 target。
//
// 产出顺序：先数据指标（受 maxMetricsPerTemplate 截断），后存活指标。
// 存活指标单独累积、不参与截断——它是「模板是否在产出」的唯一健康信号，
// 若被数据指标挤掉，超限时就只剩静默无数据。
func (r *TemplateRunner) CollectTemplate(ctx context.Context, tpl template.Config) []model.Metric {
	var data, ups []model.Metric
	for _, t := range tpl.Targets {
		d, up := r.collectTarget(ctx, tpl, t)
		data = append(data, d...)
		ups = append(ups, up)
	}
	// 合并「声明了聚合」的同名序列；未声明聚合却出现重复序列时只保留一条并告警。
	// 详见 template_aggregate.go：多条同名同标签序列写进时序库是 last-write-wins 的静默损坏。
	if merged, err := r.applyAggregate(tpl, data); err != nil {
		slog.Warn("模板聚合规则执行失败，本轮未按规则聚合", "template", tpl.ID, "err", err)
	} else {
		data = merged
	}
	data = r.dropCollisions(tpl, data)

	if len(data) > template.MaxMetricsPerTemplate {
		// 宁可丢数据也必须让运维看见：静默放大基数会拖垮时序库
		slog.Warn("模板产出超过上限，已截断",
			"template", tpl.ID, "produced", len(data), "limit", template.MaxMetricsPerTemplate,
			"hint", "用 rules.keep 收窄、rules.aggregate 汇总，或为该中间件写专用采集器")
		data = data[:template.MaxMetricsPerTemplate]
	}
	return append(data, ups...)
}

// collectTarget 采集单个 target，返回（数据指标, 存活指标）。
//
// 失败语义：拉取或解析失败时只返回 up=0，不返回任何数据指标——
// 否则上一轮的值会被误读为「当前实时值」，也失去「静默无数据」的可见信号。
func (r *TemplateRunner) collectTarget(ctx context.Context, tpl template.Config, t template.Target) ([]model.Metric, model.Metric) {
	now := r.now()
	instance := t.EffectiveInstance()
	up := model.Metric{
		Node: r.node, Name: template.UpMetricName, Value: 0, Timestamp: now,
		Labels: map[string]string{"template": tpl.ID, "instance": instance},
	}
	if ctx.Err() != nil {
		return nil, up
	}

	// 只有网络取数类需要先拉响应体；本机/数据库取数（exec/file/jdbc）各自在下方取数，
	// 它们的 target 没有 addr，走 HTTP 拉取必然失败。
	var body []byte
	if !template.IsGuardedKind(tpl.Kind) {
		b, ferr := r.fetch(ctx, t)
		if ferr != nil {
			// 日志只带模板 id / instance / 地址与错误，绝不回显响应体与 auth
			slog.Warn("模板采集失败", "template", tpl.ID, "instance", instance, "url", sanitizeURL(t.Addr), "err", ferr)
			return nil, up
		}
		body = b
	}

	var metrics []model.Metric
	var err error
	switch tpl.Kind {
	case template.KindPrometheusExporter:
		metrics, err = r.mapPrometheus(tpl, t, body, now)
	case template.KindHTTPJSON:
		metrics, err = r.mapJSON(tpl, t, body, now)
	case template.KindHTTPText:
		metrics, err = r.mapText(tpl, t, body, now)
	case template.KindExec:
		metrics, err = r.collectExec(ctx, tpl, t, now)
	case template.KindFile:
		metrics, err = r.collectFile(ctx, tpl, t, now)
	case template.KindJDBC:
		metrics, err = r.collectJDBC(ctx, tpl, t, now)
	default:
		// 校验器已拦截非法 kind，这里兜底以免配置热更新等旁路路径漏检
		err = fmt.Errorf("未知模板类型 %q", tpl.Kind)
	}
	if err != nil {
		var denied errGuardDenied
		if errors.As(err, &denied) {
			// 护栏拒绝是配置问题、每轮都会发生：按「模板 + 原因」去重告警，并把修法直接写出来
			r.warnOnce("guard\x00"+tpl.ID+"\x00"+denied.reason, "模板被本机护栏拦下",
				"template", tpl.ID, "instance", instance, "reason", denied.reason,
				"hint", "在 agent.yaml 的 templateGuards 中启用该取数方式，并把目标加入 allow 白名单")
			return nil, up
		}
		slog.Warn("模板解析失败", "template", tpl.ID, "instance", instance, "url", sanitizeURL(t.Addr), "err", err)
		return nil, up
	}
	up.Value = 1
	return metrics, up
}

// fetch 拉取 target 的响应体，并施加 headers / auth 与体积上限。
func (r *TemplateRunner) fetch(ctx context.Context, t template.Target) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.Addr, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	applyAuth(req, t.Auth)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("返回状态码 %d", resp.StatusCode)
	}
	// 多读 1 字节用于判断是否超限：静默截断会让 Prometheus 文本少解析若干行而不报错
	body, err := io.ReadAll(io.LimitReader(resp.Body, template.MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > template.MaxBodyBytes {
		return nil, fmt.Errorf("响应体超过上限 %d 字节", template.MaxBodyBytes)
	}
	return body, nil
}

// applyAuth 按配置施加认证；三种方式由校验器保证至多启用一种。
func applyAuth(req *http.Request, a *template.Auth) {
	if a == nil {
		return
	}
	if a.Basic != nil {
		req.SetBasicAuth(a.Basic.User, a.Basic.Password)
	}
	if a.Bearer != nil && a.Bearer.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Bearer.Token)
	}
	if a.Header != nil && a.Header.Name != "" {
		req.Header.Set(a.Header.Name, a.Header.Value)
	}
}

// sanitizeURL 去掉 URL 中的用户信息，避免日志泄露凭据（http://user:pass@host 形式）。
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = nil
	}
	return u.String()
}

// mapPrometheus 处理 prometheus-exporter：解析文本后按 keep/drop/rename 映射。
//
// 规则作用对象统一为「响应中的原名」：先过滤、再改名，最后才加模板前缀。
// 由于前缀在最后添加，rename 无法把指标改到模板命名空间之外（如改成 redis_xxx）。
func (r *TemplateRunner) mapPrometheus(tpl template.Config, t template.Target, body []byte, now int64) ([]model.Metric, error) {
	keep, err := r.regexp(tpl.Rules.Keep)
	if err != nil {
		return nil, err
	}
	drop, err := r.regexp(tpl.Rules.Drop)
	if err != nil {
		return nil, err
	}
	renames := make([]renameMatcher, 0, len(tpl.Rules.Rename))
	for _, rn := range tpl.Rules.Rename {
		re, err := r.regexp(rn.Match)
		if err != nil {
			return nil, err
		}
		if re == nil {
			continue
		}
		renames = append(renames, renameMatcher{re: re, to: rn.To})
	}
	promotes := make([]promoteMatcher, 0, len(tpl.Rules.PromoteLabel))
	for _, p := range tpl.Rules.PromoteLabel {
		re, err := r.regexp(p.Match)
		if err != nil {
			return nil, err
		}
		if re == nil {
			continue
		}
		promotes = append(promotes, promoteMatcher{re: re, label: p.Label})
	}

	// 前缀传空串：prometheus-exporter 模板不过滤指标族，由 keep/drop 决定保留范围
	parsed := parsePrometheusTextWithPrefix(string(body), r.node, t.EffectiveInstance(), "", now)
	out := make([]model.Metric, 0, len(parsed))
	for _, m := range parsed {
		name := m.Name
		if keep != nil && !keep.MatchString(name) {
			continue
		}
		if drop != nil && drop.MatchString(name) {
			continue
		}
		// promoteLabel：把标签取值提升为指标名的一部分（先于 rename，故 match 对响应原名）。
		// 命中但样本没有该标签、或取值无法净化时保持原样：宁可留一个未拆分的样本，也不丢数据。
		// m.Labels 是解析时为每条样本新建的 map（见 parsePromLine/parsePromLabels），可安全原地删除。
		for _, pr := range promotes {
			if !pr.re.MatchString(name) {
				continue
			}
			segment := template.SanitizeMetricSegment(m.Labels[pr.label])
			if segment == "" {
				break
			}
			name += "_" + segment
			delete(m.Labels, pr.label)
			break
		}
		for _, rn := range renames {
			if rn.re.MatchString(name) {
				name = rn.re.ReplaceAllString(name, rn.to)
				break // 只应用首条匹配的改名规则，避免多条规则相互覆盖
			}
		}
		name = template.EnsurePrefix(tpl.ID, name)
		if len(name) > template.MaxMetricNameLen {
			continue
		}
		out = append(out, model.Metric{
			Node: r.node, Name: name, Labels: r.buildLabels(tpl, t, m.Labels), Value: m.Value, Timestamp: now,
		})
	}
	return out, nil
}

// mapJSON 处理 http-json：按 path（如 a.b[0].c）取值；路径缺失不产出该指标、不报错。
func (r *TemplateRunner) mapJSON(tpl template.Config, t template.Target, body []byte, now int64) ([]model.Metric, error) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 保留数值原样，避免大整数经 float64 丢精度
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("JSON 解析失败: %w", err)
	}

	out := make([]model.Metric, 0, len(tpl.Rules.Metrics))
	for _, rule := range tpl.Rules.Metrics {
		v, ok := navigateJSON(root, rule.Path)
		if !ok {
			continue
		}
		f, ok := toFloat(v)
		if !ok {
			continue
		}
		name := template.EnsurePrefix(tpl.ID, rule.Name)
		if len(name) > template.MaxMetricNameLen {
			continue
		}
		out = append(out, model.Metric{
			Node: r.node, Name: name, Labels: r.buildLabels(tpl, t, nil), Value: f, Timestamp: now,
		})
	}
	return out, nil
}

// mapText 处理 http-text：按正则抓取，取第 1 个捕获组（无捕获组时取整段匹配）并转为数值。
func (r *TemplateRunner) mapText(tpl template.Config, t template.Target, body []byte, now int64) ([]model.Metric, error) {
	text := string(body)
	out := make([]model.Metric, 0, len(tpl.Rules.Metrics))
	for _, rule := range tpl.Rules.Metrics {
		re, err := r.regexp(rule.Pattern)
		if err != nil {
			return nil, err
		}
		if re == nil {
			continue
		}
		m := re.FindStringSubmatch(text)
		if m == nil {
			continue // 未命中不产出
		}
		raw := m[0]
		if len(m) > 1 {
			raw = m[1]
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			continue
		}
		name := template.EnsurePrefix(tpl.ID, rule.Name)
		if len(name) > template.MaxMetricNameLen {
			continue
		}
		out = append(out, model.Metric{
			Node: r.node, Name: name, Labels: r.buildLabels(tpl, t, nil), Value: f, Timestamp: now,
		})
	}
	return out, nil
}

// buildLabels 组装最终标签集：响应标签（去掉 unlabel）→ 静态标签 → 引擎标签。
//
// 引擎标签最后写入且不可被覆盖（校验器已拦截配置中的冲突，这里是运行期兜底）；
// 标签数超限时按名排序丢弃响应自带标签，保留静态标签与引擎标签（后两者是用户与系统的明确意图）。
func (r *TemplateRunner) buildLabels(tpl template.Config, t template.Target, in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+len(tpl.Rules.Labels)+len(template.ReservedLabelNames))
	for k, v := range in {
		if containsString(tpl.Rules.Unlabel, k) {
			continue
		}
		out[k] = truncateLabelValue(v)
	}
	for k, v := range tpl.Rules.Labels {
		if template.IsReservedLabel(k) {
			continue
		}
		out[k] = truncateLabelValue(v)
	}
	out["node"] = r.node
	out["instance"] = t.EffectiveInstance()
	out["template"] = tpl.ID

	if len(out) > template.MaxLabelsPerMetric {
		over := make([]string, 0, len(out))
		for k := range out {
			if template.IsReservedLabel(k) {
				continue
			}
			if _, isStatic := tpl.Rules.Labels[k]; isStatic {
				continue
			}
			over = append(over, k)
		}
		sort.Strings(over)
		for i := 0; i < len(over) && len(out) > template.MaxLabelsPerMetric; i++ {
			delete(out, over[i])
		}
	}
	return out
}

// renameMatcher 是预编译后的改名规则。
type renameMatcher struct {
	re *regexp.Regexp
	to string
}

// promoteMatcher 是预编译后的「标签值提升为指标名」规则。
type promoteMatcher struct {
	re    *regexp.Regexp
	label string
}

// regexp 返回编译后的正则；pattern 为空表示「不启用该规则」，返回 nil。
func (r *TemplateRunner) regexp(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	if v, ok := r.res.Load(pattern); ok {
		return v.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("正则 %q 非法: %w", pattern, err)
	}
	r.res.Store(pattern, re)
	return re, nil
}

// navigateJSON 按 "a.b[0].c" 逐段取值；任一段缺失或类型不符时返回 (nil, false)，不 panic。
func navigateJSON(root any, path string) (any, bool) {
	cur := root
	for _, seg := range strings.Split(path, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return nil, false
		}
		for seg != "" {
			if seg[0] == '[' {
				end := strings.IndexByte(seg, ']')
				if end < 0 {
					return nil, false
				}
				n, err := strconv.Atoi(seg[1:end])
				if err != nil {
					return nil, false
				}
				arr, ok := cur.([]any)
				if !ok || n < 0 || n >= len(arr) {
					return nil, false
				}
				cur, seg = arr[n], seg[end+1:]
				continue
			}
			end := strings.IndexByte(seg, '[')
			key := seg
			if end >= 0 {
				key, seg = seg[:end], seg[end:]
			} else {
				seg = ""
			}
			m, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := m[key]
			if !ok {
				return nil, false
			}
			cur = v
		}
	}
	return cur, true
}

// toFloat 把 JSON 取值转换为数值：数字、数字字符串、布尔（true=1/false=0）可转，其余不产出。
func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// truncateLabelValue 截断超长标签值（保留前 MaxLabelValueLen 字节）。
func truncateLabelValue(v string) string {
	if len(v) <= template.MaxLabelValueLen {
		return v
	}
	return v[:template.MaxLabelValueLen]
}

// containsString 判断字符串切片是否包含目标值。
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
