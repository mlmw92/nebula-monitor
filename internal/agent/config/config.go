// Package config 定义 Agent 的配置结构与加载逻辑。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/agent/crypto"
	"github.com/nebula/monitor/internal/model"
	"github.com/nebula/monitor/internal/template"
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
	PortChecks        []string                       `yaml:"portChecks"`        // TCP 端口存活检测列表，如 ["80","443","3306"]
	Templates         []template.Config              `yaml:"templates"`         // 采集项模板（阶段一：只描述「取数 → 映射」，新增中间件无需改 Go 代码）
	Proxy             ProxyConfig                    `yaml:"proxy"`             // 代理模式配置，mode=edge/hub 时生效
	Security          SecurityConfig                 `yaml:"security"`          // 安全采集配置（collectors.security 开启时生效）
	CryptoKey         string                         `yaml:"cryptoKey"`         // 中间件密码 AES-GCM 主密钥（留空用内置默认密钥；配置密文以 enc: 前缀标识）
	// TemplateGuards 是模板「从本机 / 数据库取数」的本机护栏（C1 阶段三），**三类默认全部关闭**。
	TemplateGuards TemplateGuardsConfig `yaml:"templateGuards"`
}

// TemplateGuardsConfig 是模板三类「非网络取数」方式的本机护栏。
//
// 为什么默认全关：`exec` 会以 root 执行命令、`file` 会以 root 读取文件、`jdbc` 会带着库凭据出网；
// 而 Agent 由 systemd 以 root 运行，模板又可由持有 middleware:write 的账号在 Web 端编辑并下发到整组节点——
// 也就是「Web 端一个写权限 = 一批机器的 root」。本机护栏是**唯一由机器自己掌握**的那道门：
// 即便 Server 被入侵或运维误配，影响也只限于本机白名单已明确允许的命令与路径。
//
// 与中心授权的关系：Server 侧仍按权限点与分组过滤下发（第一道），Agent 还会按本机护栏再筛一次（纵深防御）。
type TemplateGuardsConfig struct {
	JDBC GuardRule `yaml:"jdbc"`
	Exec GuardRule `yaml:"exec"`
	File GuardRule `yaml:"file"`
}

// GuardRule 是单个取数方式的本机放行规则。
type GuardRule struct {
	// Enabled 是否放行该取数方式。
	Enabled bool `yaml:"enabled"`
	// Allow 白名单：exec 为**命令绝对路径**、file 为**文件绝对路径**。精确匹配，不支持通配——
	// 通配会把「明确允许」退化成「猜哪些能中」，而白名单的用途正是明确。
	Allow []string `yaml:"allow"`
	// AllowHosts 目标库白名单（仅 jdbc）：留空表示不限制（凭据本就由配置方掌握，风险等级低于本机取数）。
	AllowHosts []string `yaml:"allowHosts"`
}

// guardRule 返回某 kind 对应的护栏段；非护栏类 kind 返回 false。
//
// kind 的字符串与护栏段名刻意保持一致（jdbc / exec / file），这样错误信息可以直接拼出
// 「templateGuards.exec.enabled」这种可照抄的路径，而不必再维护一份映射表。
func (g TemplateGuardsConfig) guardRule(kind template.Kind) (GuardRule, bool) {
	switch kind {
	case template.KindJDBC:
		return g.JDBC, true
	case template.KindExec:
		return g.Exec, true
	case template.KindFile:
		return g.File, true
	default:
		return GuardRule{}, false
	}
}

// AllowsKind 判断某取数方式是否已被本机放行。
func (g TemplateGuardsConfig) AllowsKind(kind template.Kind) bool {
	rule, ok := g.guardRule(kind)
	return ok && rule.Enabled
}

// EnabledKinds 返回本机已放行的取数方式（上报给 Server，Server 据此只下发该节点能跑的模板）。
func (g TemplateGuardsConfig) EnabledKinds() []string {
	out := make([]string, 0, len(template.GuardedKinds))
	for _, k := range template.GuardedKinds {
		if g.AllowsKind(k) {
			out = append(out, string(k))
		}
	}
	return out
}

// AllowsCommand 判断命令是否在本机白名单内（exec）。未放行该方式时一律 false。
func (g TemplateGuardsConfig) AllowsCommand(cmd string) bool {
	return g.Exec.Enabled && containsExact(g.Exec.Allow, cmd)
}

// AllowsPath 判断文件是否在本机白名单内（file）。未放行该方式时一律 false。
func (g TemplateGuardsConfig) AllowsPath(path string) bool {
	return g.File.Enabled && containsExact(g.File.Allow, path)
}

// AllowsDBHost 判断目标库地址是否被允许（jdbc）；未配置白名单表示不限制。
func (g TemplateGuardsConfig) AllowsDBHost(addr string) bool {
	if len(g.JDBC.AllowHosts) == 0 {
		return true
	}
	return containsExact(g.JDBC.AllowHosts, addr)
}

// containsExact 精确匹配（不折叠大小写、不解析通配）。
func containsExact(list []string, s string) bool {
	for _, v := range list {
		if strings.TrimSpace(v) == s {
			return true
		}
	}
	return false
}

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
	// 采集项模板：启动期一次性校验，任一不合法即拒绝启动并打印全部原因。
	// 刻意不做「静默跳过非法模板」——那会变成「为什么没数据」的长期悬案。
	if err := template.ValidateAll(cfg.Templates); err != nil {
		return nil, fmt.Errorf("采集项模板配置非法: %w", err)
	}
	for i := range cfg.Templates {
		// 阶段一不实现独立采集周期：非 0 只告警并忽略，避免运维误以为周期已独立生效
		if cfg.Templates[i].Interval != 0 {
			slog.Warn("模板 interval 在阶段一被忽略（跟随全局采集间隔）",
				"template", cfg.Templates[i].ID, "interval", cfg.Templates[i].Interval)
		}
	}
	// 本机护栏（阶段三）：先校验护栏自身是否自洽，再校验本机模板是否真的被放行。
	if err := validateTemplateGuards(&cfg.TemplateGuards); err != nil {
		return nil, fmt.Errorf("templateGuards 配置非法: %w", err)
	}
	if err := validateGuardedLocalTemplates(cfg); err != nil {
		return nil, err
	}
	// 解密中间件连接密码：以 enc: 前缀的密文经 AES-GCM 解密为明文供采集器使用；
	// 旧明文配置直接保留（向后兼容）。解密失败仅告警并保留原值，避免 agent 启动失败。
	cipher, err := crypto.NewCipher([]byte(cfg.CryptoKey))
	if err != nil {
		slog.Warn("初始化密码解密器失败，中间件密码将保持原样", "err", err)
	} else {
		decryptInstancePasswords(cfg, cipher)
		decryptTemplateCredentials(cfg, cipher)
	}
	return cfg, nil
}

// validateTemplateGuards 校验本机护栏配置自身是否自洽。
//
// 两类问题都在启动期拒绝，因为它们在运行期的表现都是「模板静默不产出」，排查成本极高：
//   - 白名单里写了非绝对路径（白名单按规范化路径精确比对，相对路径永远匹配不上）；
//   - 启用了 exec/file 却没给白名单（等于放行了一切方向、却没放行任何一条，规则必然失败）。
func validateTemplateGuards(g *TemplateGuardsConfig) error {
	for _, it := range []struct {
		kind       string
		rule       GuardRule
		needsAllow bool
	}{
		{string(template.KindExec), g.Exec, true},
		{string(template.KindFile), g.File, true},
		{string(template.KindJDBC), g.JDBC, false},
	} {
		for i, p := range it.rule.Allow {
			if !template.IsAbsolutePath(p) {
				return fmt.Errorf("templateGuards.%s.allow[%d] %q 必须是绝对路径且不含 ..（白名单按规范化后的路径精确比对）", it.kind, i, p)
			}
		}
		if it.rule.Enabled && it.needsAllow && len(it.rule.Allow) == 0 {
			return fmt.Errorf("templateGuards.%s.enabled=true 但 allow 为空：没有放行任何命令/路径，该取数方式必然失败", it.kind)
		}
		if !it.rule.Enabled && len(it.rule.Allow) > 0 {
			// 配了白名单却忘了开开关：多半是漏了一步，告警比静默忽略更有用
			slog.Warn("templateGuards 配了白名单但未启用，白名单不生效", "kind", it.kind)
		}
	}
	for i, h := range g.JDBC.AllowHosts {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("templateGuards.jdbc.allowHosts[%d] 为空", i)
		}
	}
	if !g.JDBC.Enabled && len(g.JDBC.AllowHosts) > 0 {
		slog.Warn("templateGuards 配了目标库白名单但未启用 jdbc，白名单不生效")
	}
	return nil
}

// validateGuardedLocalTemplates 校验本机 agent.yaml 里的模板是否都被护栏放行。
//
// 本机模板不会经过 Server 侧的能力过滤（它压根不走下发），所以这里必须自己挡：
// 否则模板每轮都失败，而界面上只看到「配了却没数据」。
func validateGuardedLocalTemplates(cfg *Config) error {
	for i := range cfg.Templates {
		k := cfg.Templates[i].Kind
		if template.IsGuardedKind(k) && !cfg.TemplateGuards.AllowsKind(k) {
			return fmt.Errorf("模板 %s 使用 kind=%s，但 templateGuards.%s.enabled 未开启：本机未放行该取数方式（exec/file 会以 root 触碰本机、jdbc 会携带库凭据，故默认关闭）",
				cfg.Templates[i].ID, k, k)
		}
	}
	return nil
}

// decryptTemplateCredentials 解密模板 target 的凭据（basic.password / bearer.token / header.value），
// 与实例密码同一套 AES-GCM；非 enc: 前缀的明文原样保留（向后兼容）。
//
// 这三处是模板中唯一可能含凭据的字段，且都不带 json tag，因此解密后的明文不会进入上报体。
func decryptTemplateCredentials(cfg *Config, c *crypto.Cipher) {
	for i := range cfg.Templates {
		id := cfg.Templates[i].ID
		for j := range cfg.Templates[i].Targets {
			a := cfg.Templates[i].Targets[j].Auth
			if a == nil {
				continue
			}
			if a.Basic != nil {
				if d, err := c.Decrypt(a.Basic.Password); err != nil {
					slog.Warn("模板 basic 密码解密失败，保留原值", "template", id, "err", err)
				} else {
					a.Basic.Password = d
				}
			}
			if a.Bearer != nil {
				if d, err := c.Decrypt(a.Bearer.Token); err != nil {
					slog.Warn("模板 bearer token 解密失败，保留原值", "template", id, "err", err)
				} else {
					a.Bearer.Token = d
				}
			}
			if a.Header != nil {
				if d, err := c.Decrypt(a.Header.Value); err != nil {
					slog.Warn("模板 header 值解密失败，保留原值", "template", id, "err", err)
				} else {
					a.Header.Value = d
				}
			}
		}
	}
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
