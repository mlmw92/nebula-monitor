// Package notify 管理通知渠道的独立配置文件，并提供热加载衔接。
// 配置默认存于 <DataDir>/notify.yaml（由 Config.NotifyFile 指定），与 server.yaml 解耦；
// server.yaml 的 notify 段仅作为首次初始化来源，运行时以独立文件为准。
package notify

import (
	"log/slog"
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/server/config"
)

// ApplyFunc 热加载回调：保存后由调用方重建内存通知器（如 engine.SetNotifiers）。
type ApplyFunc func(config.NotifyConfig)

// Manager 通知配置管理器。
type Manager struct {
	path   string
	apply  ApplyFunc
	mu     sync.RWMutex
	current config.NotifyConfig
}

// New 创建管理器并加载配置：文件存在则读取；不存在则用 initial 初始化并落盘（向后兼容旧部署）。
func New(path string, initial config.NotifyConfig, apply ApplyFunc) (*Manager, error) {
	m := &Manager{path: path, apply: apply}
	if err := m.loadOrInit(initial); err != nil {
		return nil, err
	}
	return m, nil
}

// loadOrInit 加载或初始化配置。
func (m *Manager) loadOrInit(initial config.NotifyConfig) error {
	data, err := os.ReadFile(m.path)
	if err == nil {
		var c config.NotifyConfig
		if err := yaml.Unmarshal(data, &c); err != nil {
			return err
		}
		m.current = c
		slog.Info("已加载通知配置文件", "path", m.path)
		return nil
	}
	if os.IsNotExist(err) {
		m.current = initial
		if err := m.persist(); err != nil {
			return err
		}
		slog.Info("通知配置文件不存在，已用 server.yaml 的 notify 段初始化", "path", m.path)
		return nil
	}
	return err
}

// Get 返回当前内存中的配置（含敏感字段，仅服务端内部使用，不要直接返回给前端）。
func (m *Manager) Get() config.NotifyConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Save 校验后原子落盘并热加载。敏感字段（密码/secret）由调用方在传入前处理（空值保留旧值）。
func (m *Manager) Save(c config.NotifyConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := config.AtomicWrite(m.path, data); err != nil {
		return err
	}
	m.current = c
	if m.apply != nil {
		m.apply(c)
	}
	slog.Info("通知配置已保存并热加载", "path", m.path)
	return nil
}

// persist 原子写当前配置到文件（内部加锁外使用，调用方需持有写锁或初始化阶段）。
func (m *Manager) persist() error {
	data, err := yaml.Marshal(m.current)
	if err != nil {
		return err
	}
	return config.AtomicWrite(m.path, data)
}
