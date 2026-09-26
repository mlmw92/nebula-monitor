// Package template 定义采集项模板的 DSL 与启动期校验。
//
// 阶段一支持三种取数方式：prometheus-exporter（Prometheus 文本）、http-json（JSON 路径取值）、
// http-text（正则抓取）。设计原则：
//   - 只描述「取数 → 映射」，不描述「计算」：不做表达式求值、不做跨指标运算；
//   - 能力边界 = 拉取 + 解析 + 改名 + 打标签 + 过滤，超出即「该写专用采集器」；
//   - 默认安全：默认只读、默认不发凭据、默认有上限。
//
// 为什么是共享包（而不是放在 collector 或 server 内）：
//   - 放在 collector 不行：collector 已依赖 agent/config，而 config 必须持有 Templates 字段，
//     会形成 import 环；
//   - 放在 server 不行：Agent 侧要执行模板；
//   - 两端必须用**同一份校验器**：Server 在 Web 端保存模板时要校验，Agent 在校验通过后才执行，
//     两处各写一份必然漂移（曾有过「同一语义两处实现」导致的静默不一致）。
//
// 本包零依赖（仅标准库），因此 Agent 与 Server 都能引用。
package template

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Kind 是模板的取数方式。
type Kind string

const (
	// KindPrometheusExporter 拉取 Prometheus 文本格式（各类 exporter 的 /metrics）。
	KindPrometheusExporter Kind = "prometheus-exporter"
	// KindHTTPJSON 拉取 JSON，按路径取值。
	KindHTTPJSON Kind = "http-json"
	// KindHTTPText 拉取纯文本，按正则抓取。
	KindHTTPText Kind = "http-text"
)

// 硬编码上限。刻意不做成配置项：上限被调大到失去保护的代价，高于「不可调」的不便。
const (
	// MaxTemplates 模板数上限（每个模板挂一个采集任务）。
	MaxTemplates = 20
	// MaxTargetsPerTemplate 单模板拉取目标数上限。
	MaxTargetsPerTemplate = 32
	// MaxMetricsPerTemplate 单轮单模板产出的数据指标条数上限，超出截断并告警。
	MaxMetricsPerTemplate = 2000
	// MaxLabelsPerMetric 单指标标签数上限（含引擎注入的保留标签）。
	MaxLabelsPerMetric = 16
	// MaxLabelValueLen 标签值长度上限。
	MaxLabelValueLen = 128
	// MaxBodyBytes 响应体上限（8 MiB）。
	MaxBodyBytes = 8 << 20
	// MaxMetricNameLen 产出指标名长度上限。
	MaxMetricNameLen = 200
	// UpMetricName 是每个 target 每轮必产出的存活指标名。
	//
	// 刻意不叫 `<id>_instance_up`：既有的 `*_instance_up` 有两种产出范式
	// （多数由 Agent 采集器直接产出，Redis/K8s 由 Server 的 receiver 合成），
	// 用独立名字可避免与专用采集器形成同名双序列。
	UpMetricName = "template_target_up"
)

// ReservedLabelNames 是引擎统一注入、模板不可覆盖的标签名。
// 允许覆盖 instance 就等于允许伪造他机数据（见 docs/c1-collector-templates.md §5）。
var ReservedLabelNames = []string{"node", "instance", "group", "template"}

// reservedPrefixes 是既有指标族与引擎保留前缀：模板 id 与最终指标名都不得落入其中，
// 否则会与专用采集器产出形成「同名不同来源」的双序列，且从指标名无法分辨来源。
var reservedPrefixes = []string{
	"cpu_", "mem_", "disk_", "net_", "network_", "load1", "swap_", "host_", "process_",
	"redis_", "mysql_", "postgres_", "nginx_", "nginx_access_", "kafka_", "docker_",
	"rocketmq_", "k8s_", "mongodb_", "fastdfs_",
	"template_", "self_", "proxy_", "monitor_", "alert_", "security_",
}

var (
	idPattern         = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	labelKeyPattern   = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
)

// Config 是单个模板的配置。
type Config struct {
	// ID 唯一标识，同时作为指标名前缀与 template 标签值。
	ID string `yaml:"id" json:"id"`
	// Title 展示名（阶段一仅用于日志）。
	Title string `yaml:"title" json:"title"`
	// Kind 取数方式。
	Kind Kind `yaml:"kind" json:"kind"`
	// Interval 独立采集周期（秒）。阶段一仅接受 0（跟随全局采集间隔），
	// 非 0 会在加载时告警并被忽略（不做静默生效，避免运维误以为周期已独立）。
	Interval int `yaml:"interval" json:"interval"`
	// AllowHosts 出站主机白名单。阶段一未实现，仅预留字段位置（见设计件 §5）。
	AllowHosts []string `yaml:"allowHosts" json:"allowHosts"`
	// Targets 拉取目标。
	Targets []Target `yaml:"targets" json:"targets"`
	// Rules 解析与映射规则。
	Rules Rules `yaml:"rules" json:"rules"`
}

// Target 是单个拉取目标。
type Target struct {
	// Instance 实例标识（写入 instance 标签）；留空则取 addr 的 host:port。
	Instance string `yaml:"instance" json:"instance"`
	// Addr 拉取地址，仅支持 http / https。
	Addr string `yaml:"addr" json:"addr"`
	// Headers 自定义请求头。
	Headers map[string]string `yaml:"headers" json:"headers"`
	// Auth 认证方式（可选）。刻意不打 json tag：凭据永不进入上报体，
	// 沿用 model.RedisInstanceConfig.Password 的既有做法。
	Auth *Auth `yaml:"auth" json:"-"`
}

// Auth 是请求认证配置，三种方式至多启用一种。
type Auth struct {
	Basic  *BasicAuth  `yaml:"basic" json:"-"`
	Bearer *BearerAuth `yaml:"bearer" json:"-"`
	Header *HeaderAuth `yaml:"header" json:"-"`
}

// BasicAuth 是 HTTP Basic 认证配置。
type BasicAuth struct {
	User     string `yaml:"user" json:"-"`
	Password string `yaml:"password" json:"-"`
}

// BearerAuth 是 Authorization: Bearer 认证配置。
type BearerAuth struct {
	Token string `yaml:"token" json:"-"`
}

// HeaderAuth 是自定义认证头配置。
type HeaderAuth struct {
	Name  string `yaml:"name" json:"-"`
	Value string `yaml:"value" json:"-"`
}

// Rules 描述「响应 → 指标」的映射规则。
type Rules struct {
	// Keep 只保留匹配该正则的指标名（作用于响应中的原名）。
	Keep string `yaml:"keep" json:"keep"`
	// Drop 丢弃匹配该正则的指标名（先 keep 后 drop）。
	Drop string `yaml:"drop" json:"drop"`
	// Rename 指标改名（作用于响应中的原名，之后才加模板前缀）。
	Rename []RenameRule `yaml:"rename" json:"rename"`
	// Labels 追加的静态标签。
	Labels map[string]string `yaml:"labels" json:"labels"`
	// Unlabel 需要删除的响应自带标签。
	Unlabel []string `yaml:"unlabel" json:"unlabel"`
	// Metrics http-json / http-text 的取值规则。
	Metrics []MetricRule `yaml:"metrics" json:"metrics"`
}

// RenameRule 是单条改名规则：match 匹配原名，to 为改写后的名字（支持 $1 反向引用）。
type RenameRule struct {
	Match string `yaml:"match" json:"match"`
	To    string `yaml:"to" json:"to"`
}

// MetricRule 描述一个指标的取值方式：http-json 用 path，http-text 用 pattern。
type MetricRule struct {
	Name    string `yaml:"name" json:"name"`
	Path    string `yaml:"path" json:"path"`
	Pattern string `yaml:"pattern" json:"pattern"`
	// Type 仅作声明记录（counter / gauge），阶段一不做计算，故不影响取值。
	Type string `yaml:"type" json:"type"`
}

// EffectiveInstance 返回写入 instance 标签的值：显式 instance 优先，否则取 addr 的 host:port。
func (t Target) EffectiveInstance() string {
	if s := strings.TrimSpace(t.Instance); s != "" {
		return s
	}
	u, err := url.Parse(t.Addr)
	if err != nil || u.Host == "" {
		return t.Addr
	}
	return u.Host
}

// EnsurePrefix 按模板 id 给指标名加前缀；名字已带该前缀时不重复添加
// （模板 id 常与指标族同名，如 id=rabbitmq 时响应里本就是 rabbitmq_xxx）。
func EnsurePrefix(id, name string) string {
	prefix := id + "_"
	if strings.HasPrefix(name, prefix) {
		return name
	}
	return prefix + name
}

// MaxStaticLabels 是模板可追加的静态标签数上限：总标签上限需为引擎注入的保留标签留位置，
// 否则「静态标签写满 16 个」会把 node/instance/template 挤掉（那会破坏来源可辨识性）。
func MaxStaticLabels() int { return MaxLabelsPerMetric - len(ReservedLabelNames) }

// IsReservedLabel 判断标签名是否为引擎保留名。
func IsReservedLabel(name string) bool {
	for _, n := range ReservedLabelNames {
		if n == name {
			return true
		}
	}
	return false
}

// IsReservedMetricName 判断指标名是否落入既有指标族或引擎保留前缀。
func IsReservedMetricName(name string) bool {
	for _, rp := range reservedPrefixes {
		if strings.HasPrefix(name, rp) {
			return true
		}
	}
	return false
}

// ValidateAll 校验全部模板，返回的 error 汇总所有问题。
//
// 一次报全而非「改一个重启一次」：模板配置错误属启动期问题，运维需要一眼看到全部原因。
// 校验失败必须拒绝启动——静默跳过会变成「为什么没数据」的长期悬案。
func ValidateAll(cfgs []Config) error {
	var errs []error
	if len(cfgs) > MaxTemplates {
		errs = append(errs, fmt.Errorf("模板数 %d 超过上限 %d（每个模板一个采集任务）", len(cfgs), MaxTemplates))
	}
	count := make(map[string]int, len(cfgs))
	for i := range cfgs {
		cfg := &cfgs[i]
		if err := cfg.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("模板 #%d（id=%q）：%w", i+1, cfg.ID, err))
		}
		count[cfg.ID]++
	}
	for _, cfg := range cfgs {
		if count[cfg.ID] > 1 {
			errs = append(errs, fmt.Errorf("模板 id %q 重复出现 %d 次", cfg.ID, count[cfg.ID]))
		}
	}
	// id 互为前缀：`a` 与 `a_b` 产出的指标名无法分辨归属（a_b_x 既可属 a 也可属 a_b）
	for i := range cfgs {
		for j := i + 1; j < len(cfgs); j++ {
			a, b := cfgs[i].ID, cfgs[j].ID
			if a == "" || b == "" || a == b {
				continue
			}
			if strings.HasPrefix(a+"_", b+"_") || strings.HasPrefix(b+"_", a+"_") {
				errs = append(errs, fmt.Errorf("模板 id %q 与 %q 互为前缀，指标名无法区分归属", a, b))
			}
		}
	}
	return errors.Join(errs...)
}

// Validate 校验单个模板，返回的 error 汇总该模板的所有问题。
func (c *Config) Validate() error {
	var errs []error

	if !idPattern.MatchString(c.ID) {
		errs = append(errs, fmt.Errorf("id 必须匹配 %s（当前 %q）", idPattern.String(), c.ID))
	}
	for _, rp := range reservedPrefixes {
		// 两个方向都要拦：id 落在保留族内（redis_custom），或保留族落在 id 内（redis）
		if strings.HasPrefix(c.ID+"_", rp) || strings.HasPrefix(rp, c.ID+"_") {
			errs = append(errs, fmt.Errorf("id %q 与保留前缀 %q 冲突：会与既有指标族形成同名双序列", c.ID, rp))
		}
	}

	switch c.Kind {
	case KindPrometheusExporter, KindHTTPJSON, KindHTTPText:
	default:
		errs = append(errs, fmt.Errorf("kind 必须是 %s / %s / %s 之一（当前 %q）",
			KindPrometheusExporter, KindHTTPJSON, KindHTTPText, c.Kind))
	}

	if len(c.Targets) == 0 {
		errs = append(errs, errors.New("targets 不能为空"))
	}
	if len(c.Targets) > MaxTargetsPerTemplate {
		errs = append(errs, fmt.Errorf("targets 数量 %d 超过上限 %d", len(c.Targets), MaxTargetsPerTemplate))
	}
	for i, t := range c.Targets {
		errs = append(errs, t.validate(fmt.Sprintf("targets[%d]", i))...)
	}

	errs = append(errs, c.Rules.validate(c.Kind, c.ID)...)
	return errors.Join(errs...)
}

// validate 校验单个 target。
func (t Target) validate(where string) []error {
	var errs []error
	if t.Addr == "" {
		return append(errs, fmt.Errorf("%s：addr 不能为空", where))
	}
	u, err := url.Parse(t.Addr)
	switch {
	case err != nil:
		errs = append(errs, fmt.Errorf("%s：addr %q 无法解析：%v", where, t.Addr, err))
	case u.Scheme != "http" && u.Scheme != "https":
		errs = append(errs, fmt.Errorf("%s：addr %q 的协议 %q 不被允许（仅 http / https）", where, t.Addr, u.Scheme))
	case u.Host == "":
		errs = append(errs, fmt.Errorf("%s：addr %q 缺少主机", where, t.Addr))
	}
	if t.Auth == nil {
		return errs
	}
	enabled := 0
	if t.Auth.Basic != nil {
		enabled++
	}
	if t.Auth.Bearer != nil {
		enabled++
	}
	if t.Auth.Header != nil {
		enabled++
		if strings.TrimSpace(t.Auth.Header.Name) == "" {
			errs = append(errs, fmt.Errorf("%s：auth.header.name 不能为空", where))
		}
	}
	if enabled > 1 {
		errs = append(errs, fmt.Errorf("%s：auth 只能启用 basic / bearer / header 之一", where))
	}
	return errs
}

// validate 校验映射规则；kind 决定哪些规则适用。
func (r *Rules) validate(kind Kind, id string) []error {
	var errs []error

	if r.Keep != "" {
		if _, err := regexp.Compile(r.Keep); err != nil {
			errs = append(errs, fmt.Errorf("rules.keep 正则非法：%v", err))
		}
	}
	if r.Drop != "" {
		if _, err := regexp.Compile(r.Drop); err != nil {
			errs = append(errs, fmt.Errorf("rules.drop 正则非法：%v", err))
		}
	}
	for i, rn := range r.Rename {
		if rn.Match == "" {
			errs = append(errs, fmt.Errorf("rules.rename[%d]：match 不能为空", i))
		} else if _, err := regexp.Compile(rn.Match); err != nil {
			errs = append(errs, fmt.Errorf("rules.rename[%d]：match 正则非法：%v", i, err))
		}
		if strings.TrimSpace(rn.To) == "" {
			errs = append(errs, fmt.Errorf("rules.rename[%d]：to 不能为空", i))
		}
	}
	if len(r.Labels) > MaxStaticLabels() {
		errs = append(errs, fmt.Errorf("rules.labels 数量 %d 超过上限 %d（单指标标签总上限 %d，需为 %v 留位）",
			len(r.Labels), MaxStaticLabels(), MaxLabelsPerMetric, ReservedLabelNames))
	}
	for k, v := range r.Labels {
		if !labelKeyPattern.MatchString(k) {
			errs = append(errs, fmt.Errorf("rules.labels 的键 %q 非法（需匹配 %s）", k, labelKeyPattern.String()))
		}
		if IsReservedLabel(k) {
			errs = append(errs, fmt.Errorf("rules.labels 不得覆盖保留标签 %q：会伪造他机或来源信息", k))
		}
		if len(v) > MaxLabelValueLen {
			errs = append(errs, fmt.Errorf("rules.labels 的键 %q 值长度 %d 超过上限 %d", k, len(v), MaxLabelValueLen))
		}
	}
	for _, name := range r.Unlabel {
		if IsReservedLabel(name) {
			errs = append(errs, fmt.Errorf("rules.unlabel 不得删除保留标签 %q", name))
		}
	}

	switch kind {
	case KindPrometheusExporter:
		if len(r.Metrics) > 0 {
			errs = append(errs, fmt.Errorf("kind=%s 不需要 rules.metrics（指标名直接来自响应）", KindPrometheusExporter))
		}
	case KindHTTPJSON, KindHTTPText:
		if len(r.Metrics) == 0 {
			errs = append(errs, fmt.Errorf("kind=%s 必须配置 rules.metrics", kind))
		}
	}
	if len(r.Metrics) > MaxMetricsPerTemplate {
		errs = append(errs, fmt.Errorf("rules.metrics 数量 %d 超过上限 %d", len(r.Metrics), MaxMetricsPerTemplate))
	}

	seen := make(map[string]bool, len(r.Metrics))
	for i, m := range r.Metrics {
		if !metricNamePattern.MatchString(m.Name) {
			errs = append(errs, fmt.Errorf("rules.metrics[%d]：name %q 非法（需匹配 %s）", i, m.Name, metricNamePattern.String()))
		}
		if seen[m.Name] {
			errs = append(errs, fmt.Errorf("rules.metrics[%d]：name %q 重复", i, m.Name))
		}
		seen[m.Name] = true

		// 最终名字 = id + "_" + name，故按加了前缀的结果校验长度与保留族
		final := EnsurePrefix(id, m.Name)
		if len(final) > MaxMetricNameLen {
			errs = append(errs, fmt.Errorf("rules.metrics[%d]：最终指标名长度 %d 超过上限 %d", i, len(final), MaxMetricNameLen))
		}
		if IsReservedMetricName(final) {
			errs = append(errs, fmt.Errorf("rules.metrics[%d]：最终指标名 %q 落入既有指标族", i, final))
		}

		switch kind {
		case KindHTTPJSON:
			if strings.TrimSpace(m.Path) == "" {
				errs = append(errs, fmt.Errorf("rules.metrics[%d]（%s）：http-json 必须配置 path", i, m.Name))
			}
		case KindHTTPText:
			switch {
			case strings.TrimSpace(m.Pattern) == "":
				errs = append(errs, fmt.Errorf("rules.metrics[%d]（%s）：http-text 必须配置 pattern", i, m.Name))
			default:
				if _, err := regexp.Compile(m.Pattern); err != nil {
					errs = append(errs, fmt.Errorf("rules.metrics[%d]（%s）：pattern 正则非法：%v", i, m.Name, err))
				}
			}
		}
		switch m.Type {
		case "", "counter", "gauge":
		default:
			errs = append(errs, fmt.Errorf("rules.metrics[%d]：type 只能是 counter / gauge（当前 %q）", i, m.Type))
		}
	}
	return errs
}
