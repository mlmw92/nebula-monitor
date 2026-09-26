package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 日志来源配置的校验与默认值（C2）。
//
// 启动期拒绝的理由：日志配置写错的运行期表现是「界面上什么都没有」，
// 而原因（路径写错、正则不匹配、没权限读）不会自己冒出来。

func logCfg(srcs ...LogSourceConfig) *Config {
	return &Config{LogSources: srcs}
}

func validLogSource() LogSourceConfig {
	return LogSourceConfig{
		ID:       "applog",
		Paths:    []string{"/var/log/myapp/app.log"},
		Patterns: []LogPattern{{Name: "err", Regex: `(?i)\berror\b`}},
	}
}

func TestLogSources_AcceptsValidAndFillsDefaults(t *testing.T) {
	cfg := logCfg(validLogSource())
	if err := normalizeAndValidateLogSources(cfg); err != nil {
		t.Fatalf("合法配置应通过：%v", err)
	}
	if cfg.LogOffsetsFile != DefaultLogOffsetsFile {
		t.Fatalf("偏移文件应补默认值，got %q", cfg.LogOffsetsFile)
	}
	s := cfg.LogSources[0]
	if s.MaxLinesPerRound != DefaultLogMaxLinesPerRound || s.MaxBytesPerRound != DefaultLogMaxBytesPerRound {
		t.Fatalf("单轮上限应补默认值，got %d/%d", s.MaxLinesPerRound, s.MaxBytesPerRound)
	}
	// 多行未配置时不该被塞一个 startPattern（那会改变语义）
	if s.Multiline.StartPattern != "" || s.Multiline.MaxLines != 0 {
		t.Fatalf("未配置多行时不应被改写，got %+v", s.Multiline)
	}
}

func TestLogSources_Rejects(t *testing.T) {
	cases := []struct {
		name string
		src  LogSourceConfig
		want string
	}{
		{"id 为空", func() LogSourceConfig { s := validLogSource(); s.ID = ""; return s }(), "id 不能为空"},
		{"id 格式非法", func() LogSourceConfig { s := validLogSource(); s.ID = "App-Log"; return s }(), "id"},
		{"paths 为空", func() LogSourceConfig { s := validLogSource(); s.Paths = nil; return s }(), "paths 不能为空"},
		{"相对路径", func() LogSourceConfig { s := validLogSource(); s.Paths = []string{"var/log/app.log"}; return s }(), "绝对路径"},
		{"含 .. 的路径", func() LogSourceConfig { s := validLogSource(); s.Paths = []string{"/var/../etc/shadow"}; return s }(), "绝对路径"},
		// 隐私默认值：不给 patterns 就必须显式 all: true，不能悄悄全量上传
		{"既无 patterns 也未 all", func() LogSourceConfig { s := validLogSource(); s.Patterns = nil; return s }(), "patterns"},
		{"pattern 名重复", func() LogSourceConfig {
			s := validLogSource()
			s.Patterns = append(s.Patterns, LogPattern{Name: "err", Regex: "x"})
			return s
		}(), "重复"},
		{"pattern 正则非法", func() LogSourceConfig {
			s := validLogSource()
			s.Patterns = []LogPattern{{Name: "err", Regex: "["}}
			return s
		}(), "regex 非法"},
		{"多行 startPattern 非法", func() LogSourceConfig {
			s := validLogSource()
			s.Multiline = LogMultiline{StartPattern: "[", MaxLines: 5}
			return s
		}(), "startPattern"},
		{"单轮行数越界", func() LogSourceConfig { s := validLogSource(); s.MaxLinesPerRound = MaxLogLinesPerRound + 1; return s }(), "maxLinesPerRound"},
		{"单轮字节越界", func() LogSourceConfig { s := validLogSource(); s.MaxBytesPerRound = MaxLogBytesPerRound + 1; return s }(), "maxBytesPerRound"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := normalizeAndValidateLogSources(logCfg(tc.src))
			if err == nil {
				t.Fatal("应被拒绝")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息应含 %q，got %v", tc.want, err)
			}
		})
	}
}

// TestLogSources_RejectsDuplicateID id 同时是存储分片名与指标前缀，重复会导致两者混淆。
func TestLogSources_RejectsDuplicateID(t *testing.T) {
	err := normalizeAndValidateLogSources(logCfg(validLogSource(), validLogSource()))
	if err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("重复 id 应被拒绝，got %v", err)
	}
}

// TestLogSources_AllWithoutPatterns 全量上传必须显式声明。
func TestLogSources_AllWithoutPatterns(t *testing.T) {
	s := validLogSource()
	s.Patterns = nil
	s.All = true
	if err := normalizeAndValidateLogSources(logCfg(s)); err != nil {
		t.Fatalf("显式 all: true 应通过：%v", err)
	}
}

// TestLogSourcesYAML 键名必须真的能解析出来（tag 写错的后果是「配了却全空」）。
func TestLogSourcesYAML(t *testing.T) {
	const doc = `
logSources:
  - id: applog
    paths: ["/var/log/myapp/app.log"]
    patterns:
      - { name: err, regex: "(?i)error" }
    multiline: { startPattern: "^\\d{4}-", maxLines: 20 }
    maxLinesPerRound: 500
    maxBytesPerRound: 1048576
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(doc), &cfg); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if err := normalizeAndValidateLogSources(&cfg); err != nil {
		t.Fatalf("应通过校验：%v", err)
	}
	s := cfg.LogSources[0]
	if s.ID != "applog" || len(s.Paths) != 1 || len(s.Patterns) != 1 || s.Patterns[0].Name != "err" {
		t.Fatalf("字段未解析出来：%+v", s)
	}
	if s.Multiline.StartPattern != `^\d{4}-` || s.Multiline.MaxLines != 20 {
		t.Fatalf("多行配置未解析出来：%+v", s.Multiline)
	}
	if s.MaxLinesPerRound != 500 || s.MaxBytesPerRound != 1048576 {
		t.Fatalf("上限未解析出来：%d/%d", s.MaxLinesPerRound, s.MaxBytesPerRound)
	}
}
