package alert

import (
	"log/slog"
	"os"
	"regexp"
	"sync"

	"gopkg.in/yaml.v3"
)

// MatchSet 匹配的标签集合，支持精确匹配（match）与正则匹配（matchRegex）。
type MatchSet struct {
	Match       map[string]string `yaml:"match" json:"match"`
	MatchRegexp map[string]string `yaml:"matchRegex" json:"matchRegex"`
	re          map[string]*regexp.Regexp
}

// matches 判断标签集是否满足匹配条件。
func (m MatchSet) matches(labels map[string]string) bool {
	for k, v := range m.Match {
		if labels[k] != v {
			return false
		}
	}
	for k, re := range m.re {
		val, ok := labels[k]
		if !ok || !re.MatchString(val) {
			return false
		}
	}
	return true
}

// InhibitRule 抑制规则：当存在满足 source 且处于 firing 的告警时，
// 抑制满足 target 且 equal 标签相等的告警（不发送通知，但仍记录并在前端标记）。
type InhibitRule struct {
	Source MatchSet `yaml:"source" json:"source"`
	Target MatchSet `yaml:"target" json:"target"`
	Equal  []string `yaml:"equal" json:"equal"`
}

// compile 编译正则条件。
func (r *InhibitRule) compile() {
	if len(r.Source.MatchRegexp) > 0 {
		r.Source.re = compileRegexps(r.Source.MatchRegexp)
	}
	if len(r.Target.MatchRegexp) > 0 {
		r.Target.re = compileRegexps(r.Target.MatchRegexp)
	}
}

func compileRegexps(m map[string]string) map[string]*regexp.Regexp {
	out := make(map[string]*regexp.Regexp, len(m))
	for k, v := range m {
		re, err := regexp.Compile(v)
		if err != nil {
			slog.Warn("抑制规则正则编译失败，忽略该条件", "key", k, "err", err)
			continue
		}
		out[k] = re
	}
	return out
}

// InhibitConfig 抑制规则配置（顶层为规则列表）。
type InhibitConfig []InhibitRule

// InhibitStore 抑制规则存储，支持从 YAML 加载与 Web 写入。
type InhibitStore struct {
	mu    sync.RWMutex
	rules InhibitConfig
	path  string
}

// NewInhibitStore 创建抑制规则存储并加载已有配置（文件不存在时为空）。
func NewInhibitStore(path string) *InhibitStore {
	s := &InhibitStore{path: path}
	s.rules = s.load()
	return s
}

func (s *InhibitStore) load() InhibitConfig {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil
	}
	var cfg InhibitConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		slog.Warn("抑制规则解析失败，忽略", "err", err)
		return nil
	}
	for i := range cfg {
		cfg[i].compile()
	}
	return cfg
}

// List 返回当前抑制规则列表。
func (s *InhibitStore) List() InhibitConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rules
}

// Save 持久化抑制规则并热更新内存中的规则（不重启即生效）。
func (s *InhibitStore) Save(rules []InhibitRule) error {
	for i := range rules {
		rules[i].compile()
	}
	data, err := yaml.Marshal(rules)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, data, 0644); err != nil {
		return err
	}
	s.mu.Lock()
	s.rules = rules
	s.mu.Unlock()
	return nil
}
