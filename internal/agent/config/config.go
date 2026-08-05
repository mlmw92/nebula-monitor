// Package config 定义 Agent 的配置结构与加载逻辑。
package config

import (
	"fmt"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/agent/crypto"
	"github.com/nebula/monitor/internal/model"
)

// Agent 运行模式。
const (
	ModeCollect = "collect" // 普通采集模式（默认，现状不变）
	ModeEdge    = "edge"    // 网闸区 A 边界代理：本地监听汇聚采集 Agent 上报，TLS 隧道转发至 Hub
	ModeHub     = "hub"     // 网闸区 B 边界代理：TLS 监听接收 Edge 隧道，还原请求转发至真实 Server
)

// Config 是 Agent 运行配置。
type Config struct {
	Mode              string                    `yaml:"mode"`              // 运行模式：collect(默认) | edge | hub
	ServerURL         string                    `yaml:"serverURL"`         // Server 接收地址，如 http://10.0.0.1:8080
	Node              string                    `yaml:"node"`              // 节点名（默认自动取 hostname）
	Group             string                    `yaml:"group"`             // 默认分组
	Secret            string                    `yaml:"secret"`            // 接入授权密钥（与 Server agentAuth.secret 一致）
	Labels            map[string]string         `yaml:"labels"`            // 自定义标签
	Interval          int                       `yaml:"interval"`          // 采集间隔（秒）
	BatchSize         int                       `yaml:"batchSize"`         // 单批最大指标数
	Collectors        CollectorToggle           `yaml:"collectors"`        // 采集项开关
	RedisInstances    []model.RedisInstanceConfig    `yaml:"redisInstances"`    // Redis 实例连接配置
	MySQLInstances    []model.MySQLInstanceConfig    `yaml:"mysqlInstances"`    // MySQL 实例连接配置
	PostgresInstances []model.PostgresInstanceConfig `yaml:"postgresInstances"` // PostgreSQL 实例连接配置
	NginxInstances    []model.NginxInstanceConfig    `yaml:"nginxInstances"`    // Nginx 实例连接配置
	KafkaInstances    []model.KafkaInstanceConfig    `yaml:"kafkaInstances"`    // Kafka 实例连接配置
	DockerInstances   []model.DockerInstanceConfig   `yaml:"dockerInstances"`   // Docker 连接配置
	RocketMQInstances []model.RocketMQInstanceConfig `yaml:"rocketmqInstances"` // RocketMQ 实例连接配置
	K8sInstances      []model.K8sInstanceConfig      `yaml:"k8sInstances"`      // Kubernetes 集群连接配置
	MongoDBInstances  []model.MongoDBInstanceConfig  `yaml:"mongoInstances"`   // MongoDB 实例连接配置
	FastDFSInstances  []model.FastDFSInstanceConfig  `yaml:"fastdfsInstances"` // FastDFS 实例连接配置
	PortChecks        []string                  `yaml:"portChecks"`         // TCP 端口存活检测列表，如 ["80","443","3306"]
	Proxy             ProxyConfig               `yaml:"proxy"`              // 代理模式配置，mode=edge/hub 时生效
	CryptoKey         string                    `yaml:"cryptoKey"`          // 中间件密码 AES-GCM 主密钥（留空用内置默认密钥；配置密文以 enc: 前缀标识）
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
		Collectors: CollectorToggle{
			CPU: true, Memory: true, Disk: true, Network: true, Process: true, Load: true,
		},
		Proxy: ProxyConfig{
			Listen:     ":18080",
			BufferSize: 1000,
			PoolSize:   2,
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
	if cfg.Node == "" {
		cfg.Node, _ = os.Hostname()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 200
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
