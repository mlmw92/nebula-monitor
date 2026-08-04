// Package config 定义 Server 的配置结构与加载逻辑。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Mode 部署模式（当前仅支持单机模式 standalone）。
const (
	ModeStandalone = "standalone" // 单机模式
)

// Config 是 Server 运行配置。
type Config struct {
	Mode            string          `yaml:"mode"`            // standalone
	Listen          string          `yaml:"listen"`          // HTTP 监听地址，如 :8080
	TSDB            TSDBConfig      `yaml:"tsdb"`            // 时序库（默认 VictoriaMetrics，可对接主流 PromQL 时序库）
	NodeMeta        string          `yaml:"nodeMeta"`        // 节点分组 meta JSON 路径
	DataDir         string          `yaml:"dataDir"`         // 运行时数据目录
	OfflineTimeout  int             `yaml:"offlineTimeout"`  // 节点离线判定阈值（秒）
	Alert           AlertConfig     `yaml:"alert"`           // 告警配置
	Notify          NotifyConfig    `yaml:"notify"`          // 通知渠道配置
	AgentAuth       AgentAuthConfig `yaml:"agentAuth"`       // Agent 接入授权（参考哪吒探针：密钥注册）
	AgentBinDir     string          `yaml:"agentBinDir"`     // Agent 二进制分发目录（自带 CDN，含 agent/linux/<arch>/agent）
	AgentScriptPath string          `yaml:"agentScriptPath"` // Agent 安装脚本路径（由 /install/agent-install.sh 提供）
	WebDir          string          `yaml:"webDir"`          // 前端静态资源目录（磁盘读取，改前端只需替换文件+重启）
	Auth            AuthConfig      `yaml:"auth"`            // 登录认证配置
	Upgrade         UpgradeConfig   `yaml:"upgrade"`         // Web 系统升级模块配置
	NotifyFile      string          `yaml:"notifyFile"`      // 通知渠道独立配置文件（Web 端配置写入，优先于 server.yaml 的 notify 段）
	DialtestFile    string          `yaml:"dialtestFile"`    // 拨测任务配置文件
	ReportDir       string          `yaml:"reportDir"`       // 报告存储目录
	ScreenFile      string          `yaml:"screenFile"`      // 数据大屏模块显隐配置文件（Web 端设置写入）
	UIFile          string          `yaml:"uiFile"`          // 系统 UI 品牌配置（系统名称/Logo，Web 端设置写入）
}

// ScreenConfig 数据大屏模块显隐配置（全局单份，与 notify 一致）。
// Modules 为模块开关表，key 见前端设置项：topology/gauges/risk/alerts/redis/trends/kpiTop。
// RefreshInterval 为大屏数据自动刷新间隔（秒），同时也是倒计时总时长。
type ScreenConfig struct {
	Modules         map[string]bool `yaml:"modules" json:"modules"`
	RefreshInterval int             `yaml:"refreshInterval" json:"refreshInterval"`
}

// ScreenRefreshIntervals 大屏刷新间隔可选档位（秒），与前端下拉选项保持一致。
var ScreenRefreshIntervals = []int{10, 20, 30, 60}

// IsValidScreenRefreshInterval 判断刷新间隔是否为受支持的档位。
func IsValidScreenRefreshInterval(v int) bool {
	for _, s := range ScreenRefreshIntervals {
		if s == v {
			return true
		}
	}
	return false
}

// DefaultScreenConfig 返回默认全开的大屏模块配置。
// key 见前端设置项：kpiTop/hostMonitor/middlewareMonitor/nginxAnalysis/alerts。
func DefaultScreenConfig() ScreenConfig {
	return ScreenConfig{
		Modules: map[string]bool{
			"kpiTop":            true,
			"hostMonitor":       true,
			"middlewareMonitor": true,
			"nginxAnalysis":     true,
			"alerts":            true,
		},
		RefreshInterval: 30,
	}
}

// AuthConfig 登录认证配置（启用后访问需登录，token 有效期 24h）
type AuthConfig struct {
	Enabled  bool   `yaml:"enabled"`  // 是否启用登录认证
	Username string `yaml:"username"` // 登录用户名
	Password string `yaml:"password"` // 登录密码（明文，建议配置后修改）
	Secret   string `yaml:"secret"`   // token 签名密钥（留空时启动自动生成）
}

// TSDBConfig 时序库连接配置。
// 后端兼容 Prometheus remote_write + PromQL 的主流时序库：
//
//	victoriametrics（默认，写 /api/v1/write）
//	mimir（写 /api/v1/push）、cortex（写 /api/v1/push）
//	thanos（写 /api/v1/receive）、prometheus（经 remote_write receiver）
//	custom（手动指定 writePath/queryPath，兼容任意 PromQL 时序库）
//
// 读取统一走 PromQL：queryPath / queryRangePath（默认 /api/v1/query、/api/v1/query_range）。
type TSDBConfig struct {
	Backend        string `yaml:"backend"`        // 后端类型，默认 victoriametrics
	Addr           string `yaml:"addr"`           // 写入与查询默认基址，如 http://127.0.0.1:8428
	QueryAddr      string `yaml:"queryAddr"`      // 可选：查询基址（与写入不同端口时，如 Thanos/Cortex Query）
	WritePath      string `yaml:"writePath"`      // 自定义写入路径（backend=custom 时必填）
	QueryPath      string `yaml:"queryPath"`      // 自定义查询路径，默认 /api/v1/query
	QueryRangePath string `yaml:"queryRangePath"` // 自定义区间查询路径，默认 /api/v1/query_range
	WriteTimeout   int    `yaml:"writeTimeout"`   // 写超时（秒）
	QueryTimeout   int    `yaml:"queryTimeout"`   // 查询超时（秒）
}

// AlertConfig 告警引擎配置。
type AlertConfig struct {
	Enabled         bool   `yaml:"enabled"`         // 是否启用告警
	RulesFile       string `yaml:"rulesFile"`       // 规则文件路径
	MaintenanceFile string `yaml:"maintenanceFile"` // 维护窗口文件路径
	EvalInterval    int    `yaml:"evalInterval"`    // 评估间隔（秒）
	RecoverInterval int    `yaml:"recoverInterval"` // 恢复检查间隔（秒）
}

// NotifyConfig 通知渠道配置。
type NotifyConfig struct {
	Email    EmailConfig    `yaml:"email" json:"email"`
	Webhook  WebhookConfig  `yaml:"webhook" json:"webhook"`
	DingTalk DingTalkConfig `yaml:"dingtalk" json:"dingtalk"`
	Feishu   FeishuConfig   `yaml:"feishu" json:"feishu"`
	WeCom    WeComConfig    `yaml:"wecom" json:"wecom"`
}

// EmailConfig 邮件通知配置。
type EmailConfig struct {
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	SMTPHost    string   `yaml:"smtpHost" json:"smtpHost"`
	SMTPPort    int      `yaml:"smtpPort" json:"smtpPort"`
	Username    string   `yaml:"username" json:"username"`
	Password    string   `yaml:"password" json:"password"` // 敏感，不写日志；GET 接口脱敏
	From        string   `yaml:"from" json:"from"`
	To          []string `yaml:"to" json:"to"`
	UseTLS      bool     `yaml:"useTLS" json:"useTLS"`           // 隐式 TLS（端口通常 465）
	UseStartTLS bool     `yaml:"useStartTLS" json:"useStartTLS"` // STARTTLS 升级（端口通常 587）
}

// WebhookConfig Webhook 通知配置。
type WebhookConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	URLs    []string `yaml:"urls" json:"urls"` // 敏感，不写日志
}

// DingTalkConfig 钉钉机器人通知配置。
type DingTalkConfig struct {
	Enabled   bool     `yaml:"enabled" json:"enabled"`
	URLs      []string `yaml:"urls" json:"urls"`           // 机器人 Webhook 地址列表（支持多个群）
	Secret    string   `yaml:"secret" json:"secret"`       // 可选，加签密钥（安全设置→加签）
	AtMobiles []string `yaml:"atMobiles" json:"atMobiles"` // 可选，@ 的手机号列表
}

// FeishuConfig 飞书机器人通知配置。
type FeishuConfig struct {
	Enabled bool     `yaml:"enabled" json:"enabled"`
	URLs    []string `yaml:"urls" json:"urls"`     // 机器人 Webhook 地址列表（支持多个群）
	Secret  string   `yaml:"secret" json:"secret"` // 可选，签名密钥（安全设置→签名校验）
}

// WeComConfig 企业微信机器人通知配置。
type WeComConfig struct {
	Enabled       bool     `yaml:"enabled" json:"enabled"`
	URLs          []string `yaml:"urls" json:"urls"`                   // 机器人 Webhook 地址列表（支持多个群，key 已含在 URL）
	MentionedList []string `yaml:"mentionedList" json:"mentionedList"` // 可选，@ 成员（userid 或 "@all"）
}

// AgentAuthConfig Agent 接入授权配置（参考哪吒探针的 client_secret 机制）。
// 启用后，Agent 上报需携带与 Secret 一致的 X-Agent-Secret 头，否则 401。
// Secret 留空且 Enabled=true 时，Server 启动时自动生成随机密钥（重启会变，建议写入配置固定）。
type AgentAuthConfig struct {
	Enabled bool   `yaml:"enabled"`
	Secret  string `yaml:"secret"` // 共享授权密钥
}

// UpgradeConfig 系统升级模块配置。
// Web 端上传 upgrade 包后，server 会：备份当前 server 二进制与 web、按 manifest
// 替换 server、把新 agent 同步到自带 CDN（AgentBinDir/agent/linux/<arch>/agent），
// 并重启 monitor-server。Agent 不主动推送到节点，由管理员在主机列表手动点击升级。
type UpgradeConfig struct {
	Enabled    bool   `yaml:"enabled"`    // 是否启用 Web 升级功能
	Dir        string `yaml:"dir"`        // 升级工作目录（上传包、解压、备份）；默认 <DataDir>/upgrades
	BinDir     string `yaml:"binDir"`     // server 二进制安装目录；默认 /usr/local/bin
	BackupKeep int    `yaml:"backupKeep"` // 保留最近几次备份；默认 3
	UseSystemd bool   `yaml:"useSystemd"` // 是否用 systemd 重启 server；默认 true
	Service    string `yaml:"service"`    // systemd 服务名；默认 monitor-server.service
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		Mode:           ModeStandalone,
		Listen:         ":8080",
		TSDB:           TSDBConfig{Backend: "victoriametrics", Addr: "http://127.0.0.1:8428", WriteTimeout: 5, QueryTimeout: 10},
		NodeMeta:       "/etc/monitor-server/nodes.json",
		DataDir:        "/var/lib/monitor-server",
		OfflineTimeout: 60,
		Alert:          AlertConfig{Enabled: true, RulesFile: "/etc/monitor-server/rules.yaml", MaintenanceFile: "/etc/monitor-server/maintenance.yaml", EvalInterval: 15, RecoverInterval: 30},
		Notify: NotifyConfig{
			Email:    EmailConfig{Enabled: false, SMTPPort: 587, UseStartTLS: true},
			Webhook:  WebhookConfig{Enabled: false},
			DingTalk: DingTalkConfig{Enabled: false},
			Feishu:   FeishuConfig{Enabled: false},
			WeCom:    WeComConfig{Enabled: false},
		},
		AgentAuth:       AgentAuthConfig{Enabled: false, Secret: ""},
		AgentBinDir:     "./dist",
		AgentScriptPath: "./deploy/agent-install.sh",
		WebDir:          "/etc/monitor-server/web",
		NotifyFile:      "/etc/monitor-server/notify.yaml",
		DialtestFile:    "/etc/monitor-server/dialtest.yaml",
		ReportDir:       "/var/lib/monitor-server/reports",
		ScreenFile:      "/etc/monitor-server/screen.yaml",
		UIFile:          "/etc/monitor-server/ui.yaml",
		Auth:            AuthConfig{Enabled: false, Username: "admin", Password: "admin", Secret: ""},
		Upgrade: UpgradeConfig{
			Enabled:    true,
			Dir:        "/var/lib/monitor-server/upgrades",
			BinDir:     "/usr/local/bin",
			BackupKeep: 3,
			UseSystemd: true,
			Service:    "monitor-server.service",
		},
	}
}

// Load 从指定路径读取 YAML 配置并与默认值合并。
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if cfg.Mode != ModeStandalone {
		cfg.Mode = ModeStandalone
	}
	return cfg, nil
}
