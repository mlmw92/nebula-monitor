// Package screencfg 管理数据大屏模块显隐的独立配置文件。
// 配置默认存于 <DataDir>/screen.yaml（由 Config.ScreenFile 指定），与 server.yaml 解耦；
// 文件不存在时用默认全开配置初始化并落盘（向后兼容旧部署）。
package screencfg

import (
	"log/slog"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/server/config"
)

// Manager 大屏配置管理器。
type Manager struct {
	path    string
	mu      sync.RWMutex
	current config.ScreenConfig
}

// New 创建管理器并加载配置：文件存在则读取；不存在则用 initial 初始化并落盘。
func New(path string, initial config.ScreenConfig) (*Manager, error) {
	m := &Manager{path: path}
	if err := m.loadOrInit(initial); err != nil {
		return nil, err
	}
	return m, nil
}

// loadOrInit 加载或初始化配置。
func (m *Manager) loadOrInit(initial config.ScreenConfig) error {
	data, err := os.ReadFile(m.path)
	if err == nil {
		var c config.ScreenConfig
		if err := yaml.Unmarshal(data, &c); err != nil {
			return err
		}
		if c.Modules == nil {
			c.Modules = initial.Modules
		} else {
			// 老配置补齐新增模块 key（默认开启），避免升级后新板块不显示
			for k, v := range initial.Modules {
				if _, ok := c.Modules[k]; !ok {
					c.Modules[k] = v
				}
			}
		}
		// 老配置缺省或非预设档位时回退到默认值
		if !config.IsValidScreenRefreshInterval(c.RefreshInterval) {
			c.RefreshInterval = initial.RefreshInterval
		}
		// 老配置未设置服务器所在地时回退到默认值
		if !config.IsValidDeployLocation(c.DeployLocation) {
			c.DeployLocation = initial.DeployLocation
		}
		m.current = c
		slog.Info("已加载大屏配置文件", "path", m.path)
		return nil
	}
	if os.IsNotExist(err) {
		m.current = initial
		if err := m.persist(); err != nil {
			return err
		}
		slog.Info("大屏配置文件不存在，已用默认配置初始化", "path", m.path)
		return nil
	}
	return err
}

// Get 返回当前内存中的配置。
func (m *Manager) Get() config.ScreenConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Save 校验后原子落盘。
func (m *Manager) Save(c config.ScreenConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.Modules == nil {
		c.Modules = map[string]bool{}
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(m.path, data); err != nil {
		return err
	}
	m.current = c
	slog.Info("大屏配置已保存", "path", m.path)
	return nil
}

// persist 原子写当前配置到文件（初始化阶段使用）。
func (m *Manager) persist() error {
	data, err := yaml.Marshal(m.current)
	if err != nil {
		return err
	}
	return config.AtomicWrite(m.path, data)
}
