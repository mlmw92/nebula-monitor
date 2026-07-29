// Package config 定义 Agent 的配置结构与加载逻辑。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
)

// Config 是 Agent 运行配置。
type Config struct {
	ServerURL      string               `yaml:"serverURL"`      // Server 接收地址，如 http://10.0.0.1:8080
	Node           string               `yaml:"node"`           // 节点名（默认自动取 hostname）
	Group          string               `yaml:"group"`          // 默认分组
	Secret         string               `yaml:"secret"`         // 接入授权密钥（与 Server agentAuth.secret 一致）
	Labels         map[string]string    `yaml:"labels"`         // 自定义标签
	Interval       int                  `yaml:"interval"`       // 采集间隔（秒）
	BatchSize      int                  `yaml:"batchSize"`      // 单批最大指标数
	Collectors     CollectorToggle      `yaml:"collectors"`     // 采集项开关
	RedisInstances []model.RedisInstanceConfig `yaml:"redisInstances"` // Redis 实例连接配置（密码仅存本地）
}

// CollectorToggle 控制各采集器是否启用。
type CollectorToggle struct {
	CPU     bool `yaml:"cpu"`
	Memory  bool `yaml:"memory"`
	Disk    bool `yaml:"disk"`
	Network bool `yaml:"network"`
	Process bool `yaml:"process"`
	Load    bool `yaml:"load"`
	Redis   bool `yaml:"redis"` // Redis 中间件监控开关，默认关闭
}

// Default 返回默认配置。
func Default() *Config {
	hostname, _ := os.Hostname()
	return &Config{
		ServerURL: "http://127.0.0.1:8080",
		Node:      hostname,
		Group:     "default",
		Interval:  15,
		BatchSize: 200,
		Collectors: CollectorToggle{
			CPU: true, Memory: true, Disk: true, Network: true, Process: true, Load: true,
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
	return cfg, nil
}
