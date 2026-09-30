package alert

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/server/config"
	"gopkg.in/yaml.v3"
)

// 告警事件管道（event pipeline）
//
// 在「告警已判定、待发送」阶段对事件做纯变换，不参与判定、不改变状态机：
//   - relabel：标签重命名 / 删除 / 正则替换 / 置值
//   - enrich ：按条件注入标签（如 team / owner / env）
//   - template：按渠道 / 级别 / 规则渲染消息正文，覆盖内置描述
//
// 执行时序：relabel + enrich 在派发入口（Engine.notify / Engine.flushGroup）对事件副本执行一次；
// template 在「逐渠道派发」时渲染，因此不同渠道可以有不同的文案。
// 空管道时两个入口都是零开销直通，行为与改造前完全一致。

// Relabel 操作符。
const (
	RelabelOpRename  = "rename"
	RelabelOpDelete  = "delete"
	RelabelOpReplace = "replace"
	RelabelOpSet     = "set"
)

// RelabelRule 标签重写规则。
type RelabelRule struct {
	Op      string    `yaml:"op" json:"op"`           // rename | delete | replace | set
	Source  string    `yaml:"source" json:"source"`   // 源标签名
	Target  string    `yaml:"target" json:"target"`   // 目标标签名
	Pattern string    `yaml:"pattern" json:"pattern"` // replace：正则
	Replace string    `yaml:"replace" json:"replace"` // replace：替换文本（可用 $1 引用捕获组）
	Value   string    `yaml:"value" json:"value"`     // set：固定值
	When    *MatchSet `yaml:"when" json:"when"`       // 可选：仅当事件标签满足条件时应用
	re      *regexp.Regexp
}

// EnrichRule 标签注入规则：等价于带条件的 set，单独建模以便前端按「标签增补」语义编辑。
type EnrichRule struct {
	Target string    `yaml:"target" json:"target"` // 目标标签名
	Value  string    `yaml:"value" json:"value"`   // 注入的固定值
	When   *MatchSet `yaml:"when" json:"when"`     // 可选：仅当事件标签满足条件时注入
}

// TemplateRule 消息模板规则。匹配条件全部满足时才生效；条件为空表示不限制。
type TemplateRule struct {
	Name     string    `yaml:"name" json:"name"`
	Channel  string    `yaml:"channel" json:"channel"`   // 空=全部渠道；否则 email/webhook/dingtalk/feishu/wecom
	Severity []string  `yaml:"severity" json:"severity"` // 空=全部级别
	RuleIDs  []string  `yaml:"ruleIds" json:"ruleIds"`   // 空=全部规则；精确匹配或前缀匹配
	When     *MatchSet `yaml:"when" json:"when"`         // 可选：事件标签条件
	Template string    `yaml:"template" json:"template"` // Go text/template，作用于 model.AlertEvent
	tpl      *template.Template
}

// PipelineConfig 管道配置。
type PipelineConfig struct {
	Relabels  []RelabelRule  `yaml:"relabels" json:"relabels"`
	Enrich    []EnrichRule   `yaml:"enrich" json:"enrich"`
	Templates []TemplateRule `yaml:"templates" json:"templates"`
}

// PipelineStore 管道配置存储：内存态 + YAML 持久化 + 保存即热生效，
// 与抑制（InhibitStore）/ 分组（GroupingStore）保持同一模式。
type PipelineStore struct {
	mu   sync.RWMutex
	cfg  PipelineConfig
	path string
}

// NewPipelineStore 创建管道配置存储；文件不存在表示空管道，文件损坏时忽略并告警。
func NewPipelineStore(path string) *PipelineStore {
	s := &PipelineStore{path: path}
	cfg, err := s.load()
	if err != nil {
		slog.Warn("告警管道配置解析失败，已忽略（通知回退内置格式）", "path", path, "err", err)
	}
	s.cfg = cfg
	return s
}

func (s *PipelineStore) load() (PipelineConfig, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return PipelineConfig{}, nil // 文件不存在 = 空管道
	}
	var cfg PipelineConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return PipelineConfig{}, err
	}
	if err := compilePipeline(&cfg); err != nil {
		return PipelineConfig{}, err
	}
	return cfg, nil
}

// NewPreviewPipelineStore 基于给定配置构造一个仅内存生效的 store，用于「保存前预览」试算，不落盘。
// 调用方应先通过 Validate 校验配置。
func NewPreviewPipelineStore(cfg PipelineConfig) *PipelineStore {
	_ = compilePipeline(&cfg)
	return &PipelineStore{cfg: cfg}
}

// Get 返回当前配置。
func (s *PipelineStore) Get() PipelineConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Validate 校验配置（操作符合法、正则与模板可编译），供 API 保存前预检使用。
func (s *PipelineStore) Validate(cfg PipelineConfig) error {
	return compilePipeline(&cfg)
}

// Save 校验并持久化配置，成功后热替换内存配置（不重启即生效）。
func (s *PipelineStore) Save(cfg PipelineConfig) error {
	if err := compilePipeline(&cfg); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(s.path, data); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}

// templateFuncs 是消息模板可用的辅助函数。
//
// 只提供「格式化」类函数：模板的职责是排版，取值与判定逻辑不该藏进模板里。
// 之所以必须有 ts：事件里的 StartsAt / EndsAt 是**毫秒整数**，直接渲染出来是 1769…
// 这种没人看得懂的数字，而通知里"什么时候发生的"恰恰是最常被需要的字段。
//
//	{{ts .StartsAt}}  →  2026-09-30 15:04:05（本地时区）
var templateFuncs = template.FuncMap{
	"ts": func(ms int64) string {
		if ms <= 0 {
			return ""
		}
		return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
	},
}

// compilePipeline 校验配置并编译其中的正则与模板。
func compilePipeline(cfg *PipelineConfig) error {
	for i := range cfg.Relabels {
		r := &cfg.Relabels[i]
		switch r.Op {
		case RelabelOpRename:
			if r.Source == "" || r.Target == "" {
				return fmt.Errorf("relabels[%d]: rename 需要 source 与 target", i)
			}
		case RelabelOpDelete:
			if r.Source == "" {
				return fmt.Errorf("relabels[%d]: delete 需要 source", i)
			}
		case RelabelOpReplace:
			if r.Source == "" {
				return fmt.Errorf("relabels[%d]: replace 需要 source", i)
			}
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return fmt.Errorf("relabels[%d]: 正则非法: %w", i, err)
			}
			r.re = re
		case RelabelOpSet:
			if r.Target == "" {
				return fmt.Errorf("relabels[%d]: set 需要 target", i)
			}
		default:
			return fmt.Errorf("relabels[%d]: 未知操作 %q（支持 rename/delete/replace/set）", i, r.Op)
		}
		if err := compileWhen(r.When); err != nil {
			return fmt.Errorf("relabels[%d].when: %w", i, err)
		}
	}
	for i := range cfg.Enrich {
		if cfg.Enrich[i].Target == "" {
			return fmt.Errorf("enrich[%d]: 需要 target", i)
		}
		if err := compileWhen(cfg.Enrich[i].When); err != nil {
			return fmt.Errorf("enrich[%d].when: %w", i, err)
		}
	}
	for i := range cfg.Templates {
		t := &cfg.Templates[i]
		if strings.TrimSpace(t.Template) == "" {
			return fmt.Errorf("templates[%d]: template 不能为空", i)
		}
		tpl, err := template.New(t.Name).Funcs(templateFuncs).Parse(t.Template)
		if err != nil {
			return fmt.Errorf("templates[%d]: 模板解析失败: %w", i, err)
		}
		t.tpl = tpl
		if err := compileWhen(t.When); err != nil {
			return fmt.Errorf("templates[%d].when: %w", i, err)
		}
	}
	return nil
}

// compileWhen 编译 MatchSet 中的正则条件。
func compileWhen(ms *MatchSet) error {
	if ms == nil {
		return nil
	}
	for k, pat := range ms.MatchRegexp {
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("正则条件 %s 非法: %w", k, err)
		}
		if ms.re == nil {
			ms.re = map[string]*regexp.Regexp{}
		}
		ms.re[k] = re
	}
	return nil
}

// Apply 返回应用 relabel + enrich 后的事件副本；空管道时原样返回，不做额外分配。
// 始终作用于副本（标签视图为新建 map），不会污染调用方，也不会触及引擎内的 firing 状态。
//
// 标签视图 = 由事件字段派生的内置标签 ⊕ 事件已有 Labels，再进行 relabel/enrich 变换。
// 内置标签与分组键（groupLabel）保持一致，便于用户在同一套命名下写规则与模板：
//
//	name=规则名  rule=规则ID  node=节点  instance=实例  severity=级别  metric=指标
func (s *PipelineStore) Apply(ev model.AlertEvent) model.AlertEvent {
	cfg := s.Get()
	if len(cfg.Relabels) == 0 && len(cfg.Enrich) == 0 {
		return ev
	}

	labels := deriveEventLabels(ev)

	for i := range cfg.Relabels {
		r := &cfg.Relabels[i]
		if !whenMatches(r.When, labels) {
			continue
		}
		switch r.Op {
		case RelabelOpRename:
			if v, ok := labels[r.Source]; ok {
				delete(labels, r.Source)
				labels[r.Target] = v
			}
		case RelabelOpDelete:
			delete(labels, r.Source)
		case RelabelOpReplace:
			if v, ok := labels[r.Source]; ok && r.re != nil {
				labels[r.Source] = r.re.ReplaceAllString(v, r.Replace)
			}
		case RelabelOpSet:
			// 指定 source 时仅在该标签存在时置值，便于做「有值才补充」的语义
			if r.Source == "" {
				labels[r.Target] = r.Value
			} else if _, ok := labels[r.Source]; ok {
				labels[r.Target] = r.Value
			}
		}
	}

	for i := range cfg.Enrich {
		e := &cfg.Enrich[i]
		if whenMatches(e.When, labels) {
			labels[e.Target] = e.Value
		}
	}

	ev.Labels = labels
	return ev
}

// deriveEventLabels 由事件字段派生内置标签，并叠加事件已有的 Labels（显式值优先）。
// 内置键与分组键（groupLabel）保持一致，便于用户在统一命名下写规则与模板。
func deriveEventLabels(ev model.AlertEvent) map[string]string {
	labels := make(map[string]string, 8+len(ev.Labels))
	set := func(k, v string) {
		if v != "" {
			labels[k] = v
		}
	}
	set("name", ev.RuleName)
	set("rule", ev.RuleID)
	set("node", ev.Node)
	set("instance", ev.Instance)
	set("severity", string(ev.Severity))
	set("metric", ev.Metric)
	for k, v := range ev.Labels {
		labels[k] = v
	}
	return labels
}

func whenMatches(ms *MatchSet, labels map[string]string) bool {
	if ms == nil {
		return true
	}
	return ms.matches(labels)
}

// RenderMessage 返回指定渠道应使用的消息正文。
//
// 选择顺序：渠道专属模板优先于通用模板（同一优先级内以配置顺序靠前者为准）；
// 未命中任何模板、模板渲染失败或渲染结果为空时，一律回退到原始描述
// —— 渲染问题绝不能让告警内容丢失。
func (s *PipelineStore) RenderMessage(ev model.AlertEvent, channel string) string {
	cfg := s.Get()
	if len(cfg.Templates) == 0 {
		return ev.Message
	}

	var generic, channelMatched *TemplateRule
	for i := range cfg.Templates {
		t := &cfg.Templates[i]
		if !templateMatches(t, ev) {
			continue
		}
		if t.Channel == "" {
			if generic == nil {
				generic = t
			}
			continue
		}
		if t.Channel == channel && channelMatched == nil {
			channelMatched = t
		}
	}

	pick := channelMatched
	if pick == nil {
		pick = generic
	}
	if pick == nil {
		return ev.Message
	}

	var buf bytes.Buffer
	if err := pick.tpl.Execute(&buf, ev); err != nil {
		slog.Warn("告警消息模板渲染失败，回退内置描述", "template", pick.Name, "rule", ev.RuleID, "err", err)
		return ev.Message
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return ev.Message
	}
	return out
}

// HasMessageTemplate 表示是否存在任何消息模板（用于引擎决定是否逐渠道渲染，避免空管道时的额外分配）。
func (s *PipelineStore) HasMessageTemplate() bool {
	cfg := s.Get()
	return len(cfg.Templates) > 0
}

func templateMatches(t *TemplateRule, ev model.AlertEvent) bool {
	if len(t.Severity) > 0 {
		hit := false
		for _, sev := range t.Severity {
			if string(ev.Severity) == sev {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(t.RuleIDs) > 0 {
		hit := false
		for _, id := range t.RuleIDs {
			if ev.RuleID == id || strings.HasPrefix(ev.RuleID, id) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return whenMatches(t.When, ev.Labels)
}
