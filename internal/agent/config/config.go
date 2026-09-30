// Package config 定义 Agent 的配置结构与加载逻辑。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/agent/crypto"
	"github.com/nebula/monitor/internal/model"
)

// Agent 运行模式。
const (
	// ModeCollect 普通采集模式（默认，现状不变）。
	ModeCollect = "collect"
	// ModeEdge 网闸区 A 边界代理：本地监听汇聚采集 Agent 上报，TLS 隧道转发至 Hub。
	ModeEdge = "edge"
	// ModeHub 网闸区 B 边界代理：TLS 监听接收 Edge 隧道，还原请求转发至真实 Server。
	ModeHub = "hub"
)

// Config 是 Agent 运行配置。
type Config struct {
	Mode              string                         `yaml:"mode"`              // 运行模式：collect(默认) | edge | hub
	ServerURL         string                         `yaml:"serverURL"`         // Server 接收地址，如 http://10.0.0.1:8080
	Node              string                         `yaml:"node"`              // 节点名（默认自动取 hostname）
	Group             string                         `yaml:"group"`             // 默认分组
	Secret            string                         `yaml:"secret"`            // 接入授权密钥（与 Server agentAuth.secret 一致）
	Labels            map[string]string              `yaml:"labels"`            // 自定义标签
	Interval          int                            `yaml:"interval"`          // 采集间隔（秒）
	BatchSize         int                            `yaml:"batchSize"`         // 单批最大指标数
	CollectTimeout    int                            `yaml:"collectTimeout"`    // 单个采集任务超时（秒），默认 8；0 表示不限制
	Collectors        CollectorToggle                `yaml:"collectors"`        // 采集项开关
	RedisInstances    []model.RedisInstanceConfig    `yaml:"redisInstances"`    // Redis 实例连接配置
	MySQLInstances    []model.MySQLInstanceConfig    `yaml:"mysqlInstances"`    // MySQL 实例连接配置
	PostgresInstances []model.PostgresInstanceConfig `yaml:"postgresInstances"` // PostgreSQL 实例连接配置
	NginxInstances    []model.NginxInstanceConfig    `yaml:"nginxInstances"`    // Nginx 实例连接配置
	KafkaInstances    []model.KafkaInstanceConfig    `yaml:"kafkaInstances"`    // Kafka 实例连接配置
	DockerInstances   []model.DockerInstanceConfig   `yaml:"dockerInstances"`   // Docker 连接配置
	RocketMQInstances []model.RocketMQInstanceConfig `yaml:"rocketmqInstances"` // RocketMQ 实例连接配置
	K8sInstances      []model.K8sInstanceConfig      `yaml:"k8sInstances"`      // Kubernetes 集群连接配置
	MongoDBInstances  []model.MongoDBInstanceConfig  `yaml:"mongoInstances"`    // MongoDB 实例连接配置
	FastDFSInstances  []model.FastDFSInstanceConfig  `yaml:"fastdfsInstances"`  // FastDFS 实例连接配置
	RabbitMQInstances []model.RabbitMQInstanceConfig `yaml:"rabbitmqInstances"` // RabbitMQ 实例连接配置
	ElasticsearchInstances []model.ElasticsearchInstanceConfig `yaml:"elasticsearchInstances"` // Elasticsearch 实例连接配置
	ClickHouseInstances []model.ClickHouseInstanceConfig  `yaml:"clickhouseInstances"`  // ClickHouse 实例连接配置
	NacosInstances    []model.NacosInstanceConfig    `yaml:"nacosInstances"`    // Nacos 实例连接配置
	ZooKeeperInstances []model.ZooKeeperInstanceConfig `yaml:"zookeeperInstances"` // ZooKeeper 实例连接配置
	PortChecks        []string                       `yaml:"portChecks"`        // TCP 端口存活检测列表，如 ["80","443","3306"]
	Proxy             ProxyConfig                    `yaml:"proxy"`             // 代理模式配置，mode=edge/hub 时生效
	Security          SecurityConfig                 `yaml:"security"`          // 安全采集配置（collectors.security 开启时生效）
	CryptoKey         string                         `yaml:"cryptoKey"`         // 中间件密码 AES-GCM 主密钥（留空用内置默认密钥；配置密文以 enc: 前缀标识）

	// LogSources 是集中日志的采集来源（C2），**默认为空 = 不采集任何日志**。
	LogSources []LogSourceConfig `yaml:"logSources"`
	// LogOffsetsFile 是日志读取偏移的落盘路径（重启不丢进度、不重复上传）。
	LogOffsetsFile string `yaml:"logOffsetsFile"`

	// Guards 是本机护栏：机器自己决定允不允许平台下发操作动作（见 internal/agent/ops）。
	Guards GuardsConfig `yaml:"guards"`
}

// GuardsConfig 是本机护栏配置。
//
// 为什么护栏在 Agent 侧而不只在 Server 侧：Agent 以 root 运行，Web 上的一个写权限
// ≈ 一批机器的 root。因此"能不能在这台机器上执行写操作"必须由**机器自己的配置**决定，
// 中心只能决定"要不要下发"——机器决定"要不要执行"。这条原则沿用自已移除的采集项模板护栏。
type GuardsConfig struct {
	Ops OpsGuards `yaml:"ops"`
}

// OpsGuards 是下行操作（ops）的本机护栏。
//
// 默认值刻意保守：只读允许、写操作全禁。要放行写操作必须同时写 `write: true` **和**
// 列出允许的单元——少任何一个都不放行，避免"开了一个总开关就放开了所有服务"。
type OpsGuards struct {
	// ReadOnly 是否允许只读动作（节点诊断包 / 查询服务状态）。默认 true；显式写 false 可整体关闭。
	ReadOnly *bool `yaml:"readOnly"`
	// Write 是否允许写动作（重启服务）。默认 false。
	Write bool `yaml:"write"`
	// Units 是允许写操作的 systemd 单元清单（可省略 `.service` 后缀）。
	// 为空时即使 write=true 也不放行任何写操作。
	Units []string `yaml:"units"`
}

// OpsReadOnlyEnabled 返回是否放行只读动作（默认放行）。
func (g OpsGuards) OpsReadOnlyEnabled() bool {
	return g.ReadOnly == nil || *g.ReadOnly
}

// OpsAllowedUnits 返回归一化后的允许单元集合（统一补 .service 后缀）。
//
// 归一化在此处做一次：配置文件里写 `nginx` 与写 `nginx.service` 都应该放行同一个单元，
// 否则"我明明加了却还是被拒"会变成一个纯配置拼写的谜题。
func (g OpsGuards) OpsAllowedUnits() map[string]bool {
	out := make(map[string]bool, len(g.Units))
	for _, u := range g.Units {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		out[u] = true
	}
	return out
}

// LogSourceConfig 是一个日志来源。
//
// 隐私默认值很关键：**默认只上传匹配 patterns 的行**，要全量必须显式 all: true。
// 日志内容会离开被监控机，把「上传范围」从「整个文件」收窄到「你明确关心的行」，
// 是这项能力里最重要的一个默认值。
type LogSourceConfig struct {
	// ID 是来源唯一标识（同时作为指标前缀与检索时的来源过滤值）。
	ID string `yaml:"id"`
	// Paths 是日志文件路径（**绝对路径**，且不得含 ..）。
	Paths []string `yaml:"paths"`
	// Patterns 是「关心的行」：命中即上传，并按 name 计数（可配告警）。
	Patterns []LogPattern `yaml:"patterns"`
	// All 为 true 时忽略 Patterns、上传全部行（显式开启，默认 false）。
	All bool `yaml:"all"`
	// Multiline 描述「一条日志跨多行」的合并方式（堆栈/异常）。
	Multiline LogMultiline `yaml:"multiline"`
	// MaxLinesPerRound / MaxBytesPerRound 是单轮单文件的读取上限（超限丢弃并计数）。
	MaxLinesPerRound int   `yaml:"maxLinesPerRound"`
	MaxBytesPerRound int64 `yaml:"maxBytesPerRound"`
}

// LogPattern 是一条「关心的行」的模式。
type LogPattern struct {
	Name  string `yaml:"name"`
	Regex string `yaml:"regex"`
}

// LogMultiline 描述多行日志的合并规则。
type LogMultiline struct {
	// StartPattern 匹配「新一条日志的行首」；不匹配的行视为上一条的续行。
	StartPattern string `yaml:"startPattern"`
	// MaxLines 是单条日志最多合并多少行（防止一个永不匹配的行首把整个文件吸进来）。
	MaxLines int `yaml:"maxLines"`
}

// 日志采集的默认值。
const (
	// DefaultLogOffsetsFile 是偏移文件默认位置（可被 logOffsetsFile 覆盖）。
	DefaultLogOffsetsFile = "/var/lib/monitor-agent/log_offsets.json"
	// DefaultLogMaxLinesPerRound / DefaultLogMaxBytesPerRound 是单轮单文件的读取上限。
	DefaultLogMaxLinesPerRound = 2000
	DefaultLogMaxBytesPerRound = 4 << 20
	// MaxLogLinesPerRound / MaxLogBytesPerRound 是上述上限的天花板（配置不得越过）。
	MaxLogLinesPerRound = 20000
	MaxLogBytesPerRound = 32 << 20
	// DefaultLogMultilineMaxLines / MaxLogMultilineLines 是单条日志合并行数上限。
	DefaultLogMultilineMaxLines = 50
	MaxLogMultilineLines        = 500
)

// SecurityConfig 是安全采集（SSH 审计/FIM/基线/异常进程/sudo）的可配置项。
// 所有字段均有合理默认值，开启 collectors.security 后无需额外配置即可工作。
type SecurityConfig struct {
	// FIMPaths 需做完整性监测的文件列表（SHA256 基线比对）。默认监测关键系统文件。
	FIMPaths []string `yaml:"fimPaths"`
	// FIMBaselinePath 本地 FIM 基线（文件哈希）持久化路径；留空则使用默认路径。
	FIMBaselinePath string `yaml:"fimBaselinePath"`
	// SSHLogPaths SSH 登录日志路径；留空则自动探测 /var/log/auth.log 与 /var/log/secure。
	SSHLogPaths []string `yaml:"sshLogPaths"`
	// BruteForceThreshold 同一来源 IP 在时间窗口内失败登录超过此次数判定为暴力破解。
	BruteForceThreshold int `yaml:"bruteForceThreshold"`
	// BruteForceWindowSec 暴力破解检测的时间窗口（秒）。
	BruteForceWindowSec int `yaml:"bruteForceWindowSec"`
	// WeakPasswordCheck 是否检查 /etc/shadow 空口令账户（需要 root 权限，默认开启）。
	WeakPasswordCheck bool `yaml:"weakPasswordCheck"`
}

// ProxyConfig 是 Agent 代理模式（edge/hub）的配置。
//
// Edge 模式（区 A 边界代理）：
//   - Listen:     本地汇聚监听口，采集 Agent 的 serverURL 指向此地址（如 :18080）
//   - HubAddr:    Hub 的地址 host:port（如 10.0.0.2:8443），Edge 主动拨出 TLS 隧道
//   - TLSCert/Key/CA: mTLS 双向校验证书
//   - BufferSize: 断连期间内存缓冲条数，默认 1000
//   - PoolSize:   到 Hub 的并发隧道连接数，默认 2
//
// Hub 模式（区 B 边界代理）：
//   - Listen:    TLS 监听口，接收 Edge 隧道连接（如 :8443）
//   - ServerURL: 真实 Server 地址（如 http://127.0.0.1:8080），Hub 转发至此
//   - TLSCert/Key/CA: mTLS 双向校验证书
type ProxyConfig struct {
	Listen     string `yaml:"listen"`     // 监听地址
	HubAddr    string `yaml:"hubAddr"`    // Edge: Hub 地址 host:port
	ServerURL  string `yaml:"serverURL"`  // Hub: 真实 Server 地址
	TLSCert    string `yaml:"tlsCert"`    // TLS 证书文件路径
	TLSKey     string `yaml:"tlsKey"`     // TLS 私钥文件路径
	TLSCA      string `yaml:"tlsCa"`      // CA 证书文件路径（mTLS 双向校验）
	BufferSize int    `yaml:"bufferSize"` // Edge 断连时内存缓冲条数，默认 1000
	PoolSize   int    `yaml:"poolSize"`   // Edge 到 Hub 的并发隧道连接数，默认 2
}

// CollectorToggle 控制各采集器是否启用。
type CollectorToggle struct {
	CPU      bool `yaml:"cpu"`
	Memory   bool `yaml:"memory"`
	Disk     bool `yaml:"disk"`
	Network  bool `yaml:"network"`
	Process  bool `yaml:"process"`
	Load     bool `yaml:"load"`
	Redis    bool `yaml:"redis"`    // Redis 中间件监控，默认关闭
	MySQL    bool `yaml:"mysql"`    // MySQL 中间件监控，默认关闭
	Postgres bool `yaml:"postgres"` // PostgreSQL 中间件监控，默认关闭
	Nginx    bool `yaml:"nginx"`    // Nginx 中间件监控，默认关闭
	NginxLog bool `yaml:"nginxLog"` // Nginx access log 访问日志解析（需实例配置 accessLog 路径），默认关闭
	Kafka    bool `yaml:"kafka"`    // Kafka 中间件监控，默认关闭
	Docker   bool `yaml:"docker"`   // Docker 容器监控，默认关闭
	RocketMQ bool `yaml:"rocketmq"` // RocketMQ 中间件监控，默认关闭
	K8s      bool `yaml:"k8s"`      // Kubernetes 集群监控，默认关闭
	MongoDB  bool `yaml:"mongodb"`  // MongoDB 中间件监控，默认关闭
	FastDFS  bool `yaml:"fastdfs"`  // FastDFS 中间件监控，默认关闭
	RabbitMQ bool `yaml:"rabbitmq"` // RabbitMQ 中间件监控，默认关闭
	Elasticsearch bool `yaml:"elasticsearch"` // Elasticsearch 中间件监控，默认关闭
	ClickHouse bool `yaml:"clickhouse"` // ClickHouse 中间件监控，默认关闭
	Nacos   bool `yaml:"nacos"`    // Nacos 中间件监控，默认关闭
	ZooKeeper bool `yaml:"zookeeper"` // ZooKeeper 中间件监控，默认关闭
	Port     bool `yaml:"port"`     // 端口存活检测，默认关闭
	Security bool `yaml:"security"` // 安全采集（SSH 审计/FIM/基线/异常进程/sudo），默认关闭
}

// Default 返回默认配置。
func Default() *Config {
	hostname, _ := os.Hostname()
	return &Config{
		Mode:      ModeCollect,
		ServerURL: "http://127.0.0.1:8080",
		Node:      hostname,
		Group:     "default",
		Interval:  15,
		BatchSize: 200,
		// 单个采集任务的超时（秒）。默认 8s（小于默认采集间隔 15s）；
		// 设为 0 表示不限制，任务仅受父 context 约束。
		CollectTimeout: 8,
		Collectors: CollectorToggle{
			CPU: true, Memory: true, Disk: true, Network: true, Process: true, Load: true,
			Security: true,
		},
		Proxy: ProxyConfig{
			Listen:     ":18080",
			BufferSize: 1000,
			PoolSize:   2,
		},
		Security: DefaultSecurity(),
	}
}

// DefaultSecurity 返回安全采集的默认配置。
func DefaultSecurity() SecurityConfig {
	return SecurityConfig{
		FIMPaths: []string{
			"/etc/passwd",
			"/etc/shadow",
			"/etc/group",
			"/etc/ssh/sshd_config",
			"/etc/sudoers",
		},
		BruteForceThreshold: 5,
		BruteForceWindowSec: 300,
		WeakPasswordCheck:   true,
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
	if cfg.Node == "" {
		cfg.Node, _ = os.Hostname()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
	}
	// 采集任务超时：0 表示不限制；负值统一归零（等价于不限制）
	if cfg.CollectTimeout < 0 {
		cfg.CollectTimeout = 0
	}
	// 规范化 Mode：空值/未知值统一回退为 collect，确保向后兼容
	switch cfg.Mode {
	case ModeCollect, ModeEdge, ModeHub:
		// 合法值，保持不变
	default:
		cfg.Mode = ModeCollect
	}
	// 代理模式默认值补齐
	if cfg.Proxy.BufferSize <= 0 {
		cfg.Proxy.BufferSize = 1000
	}
	if cfg.Proxy.PoolSize <= 0 {
		cfg.Proxy.PoolSize = 2
	}
	if cfg.Mode == ModeEdge && cfg.Proxy.Listen == "" {
		cfg.Proxy.Listen = ":18080"
	}
	if cfg.Mode == ModeHub && cfg.Proxy.Listen == "" {
		cfg.Proxy.Listen = ":8443"
	}
	// 集中日志（C2）：校验来源配置并补齐默认值。
	if err := normalizeAndValidateLogSources(cfg); err != nil {
		return nil, err
	}
	// 解密中间件连接密码：以 enc: 前缀的密文经 AES-GCM 解密为明文供采集器使用；
	// 旧明文配置直接保留（向后兼容）。解密失败仅告警并保留原值，避免 agent 启动失败。
	cipher, err := crypto.NewCipher([]byte(cfg.CryptoKey))
	if err != nil {
		slog.Warn("初始化密码解密器失败，中间件密码将保持原样", "err", err)
	} else {
		decryptInstancePasswords(cfg, cipher)
	}
	return cfg, nil
}

// isSafeAbsPath 判断路径是否为安全的绝对路径（绝对且不含 ..），用于日志来源路径校验。
// 跨平台语义：Unix 绝对路径以 / 开头；Windows 下再接受盘符路径（Agent 目标平台为 Linux，
// 本地开发在 Windows 跑测试时也需通过）。
func isSafeAbsPath(p string) bool {
	cleaned := strings.ReplaceAll(filepath.ToSlash(p), "//", "/")
	if strings.Contains(cleaned, "..") {
		return false
	}
	return strings.HasPrefix(cleaned, "/") || filepath.IsAbs(p)
}

// 来源 id 的合法性由 model 统一给出：Server 与 Agent 必须用同一条规则，
// 否则会出现「Agent 认为合法、Server 拒绝」这种只在现场才暴露的问题。

// normalizeAndValidateLogSources 校验日志来源配置并补齐默认值。
//
// 启动期拒绝（而不是运行时跳过）的理由与模板一致：日志配置写错的运行期表现是
// 「界面上什么都没有」，而原因（路径写错、正则不匹配、没权限读）不会自己冒出来。
func normalizeAndValidateLogSources(cfg *Config) error {
	if cfg.LogOffsetsFile == "" {
		cfg.LogOffsetsFile = DefaultLogOffsetsFile
	}
	seen := make(map[string]bool, len(cfg.LogSources))
	for i := range cfg.LogSources {
		s := &cfg.LogSources[i]
		switch {
		case strings.TrimSpace(s.ID) == "":
			return fmt.Errorf("logSources[%d]：id 不能为空", i)
		case !model.IsValidLogSourceName(s.ID):
			return fmt.Errorf("logSources[%d]：id %q 非法（小写字母开头，只含小写字母/数字/下划线）", i, s.ID)
		case seen[s.ID]:
			return fmt.Errorf("logSources[%d]：id %q 重复（它同时是存储分片名与指标前缀）", i, s.ID)
		}
		seen[s.ID] = true

		if len(s.Paths) == 0 {
			return fmt.Errorf("logSources[%d]（%s）：paths 不能为空", i, s.ID)
		}
		for j, p := range s.Paths {
			if !isSafeAbsPath(p) {
				return fmt.Errorf("logSources[%d]（%s）：paths[%d] %q 必须是绝对路径且不含 ..", i, s.ID, j, p)
			}
		}
		if !s.All && len(s.Patterns) == 0 {
			return fmt.Errorf("logSources[%d]（%s）：必须给出 patterns（只上传关心的行）；确需全量请显式设置 all: true", i, s.ID)
		}
		names := make(map[string]bool, len(s.Patterns))
		for j, p := range s.Patterns {
			if strings.TrimSpace(p.Name) == "" {
				return fmt.Errorf("logSources[%d]（%s）：patterns[%d].name 不能为空", i, s.ID, j)
			}
			if names[p.Name] {
				return fmt.Errorf("logSources[%d]（%s）：patterns[%d].name %q 重复", i, s.ID, j, p.Name)
			}
			// name 会拼进指标名（<来源>_log_<name>_total），因此必须是合法的指标名片段
			if !model.IsValidLogPatternName(p.Name) {
				return fmt.Errorf("logSources[%d]（%s）：patterns[%d].name %q 非法（会拼进指标名，需匹配 %s）",
					i, s.ID, j, p.Name, model.LogPatternNamePattern.String())
			}
			names[p.Name] = true
			if strings.TrimSpace(p.Regex) == "" {
				return fmt.Errorf("logSources[%d]（%s）：patterns[%d].regex 不能为空", i, s.ID, j)
			}
			if _, err := regexp.Compile(p.Regex); err != nil {
				return fmt.Errorf("logSources[%d]（%s）：patterns[%d].regex 非法：%v", i, s.ID, j, err)
			}
		}
		if s.Multiline.StartPattern != "" {
			if _, err := regexp.Compile(s.Multiline.StartPattern); err != nil {
				return fmt.Errorf("logSources[%d]（%s）：multiline.startPattern 非法：%v", i, s.ID, err)
			}
			if s.Multiline.MaxLines == 0 {
				s.Multiline.MaxLines = DefaultLogMultilineMaxLines
			}
			if s.Multiline.MaxLines < 0 || s.Multiline.MaxLines > MaxLogMultilineLines {
				return fmt.Errorf("logSources[%d]（%s）：multiline.maxLines 越界（0 表示默认 %d；上限 %d）",
					i, s.ID, DefaultLogMultilineMaxLines, MaxLogMultilineLines)
			}
		}
		if s.MaxLinesPerRound == 0 {
			s.MaxLinesPerRound = DefaultLogMaxLinesPerRound
		}
		if s.MaxLinesPerRound < 0 || s.MaxLinesPerRound > MaxLogLinesPerRound {
			return fmt.Errorf("logSources[%d]（%s）：maxLinesPerRound 越界（0 表示默认 %d；上限 %d）",
				i, s.ID, DefaultLogMaxLinesPerRound, MaxLogLinesPerRound)
		}
		if s.MaxBytesPerRound == 0 {
			s.MaxBytesPerRound = DefaultLogMaxBytesPerRound
		}
		if s.MaxBytesPerRound < 0 || s.MaxBytesPerRound > MaxLogBytesPerRound {
			return fmt.Errorf("logSources[%d]（%s）：maxBytesPerRound 越界（0 表示默认 %d；上限 %d）",
				i, s.ID, DefaultLogMaxBytesPerRound, MaxLogBytesPerRound)
		}
	}
	return nil
}

// decryptInstancePasswords 对含 Password 的中间件实例配置做解密后处理（写回内存明文）。
func decryptInstancePasswords(cfg *Config, c *crypto.Cipher) {
	for i := range cfg.RedisInstances {
		if d, err := c.Decrypt(cfg.RedisInstances[i].Password); err != nil {
			slog.Warn("Redis 密码解密失败，保留原值", "err", err)
		} else {
			cfg.RedisInstances[i].Password = d
		}
	}
	for i := range cfg.MySQLInstances {
		if d, err := c.Decrypt(cfg.MySQLInstances[i].Password); err != nil {
			slog.Warn("MySQL 密码解密失败，保留原值", "err", err)
		} else {
			cfg.MySQLInstances[i].Password = d
		}
	}
	for i := range cfg.PostgresInstances {
		if d, err := c.Decrypt(cfg.PostgresInstances[i].Password); err != nil {
			slog.Warn("PostgreSQL 密码解密失败，保留原值", "err", err)
		} else {
			cfg.PostgresInstances[i].Password = d
		}
	}
	for i := range cfg.MongoDBInstances {
		if d, err := c.Decrypt(cfg.MongoDBInstances[i].Password); err != nil {
			slog.Warn("MongoDB 密码解密失败，保留原值", "err", err)
		} else {
			cfg.MongoDBInstances[i].Password = d
		}
	}
}
