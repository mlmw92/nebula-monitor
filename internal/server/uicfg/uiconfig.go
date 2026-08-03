// Package uicfg 管理系统 UI 品牌配置：系统名称与 Logo。
// 配置独立持久化到文件（默认 /etc/monitor-server/ui.yaml），由 Web 端设置写入，
// 与 notify/screen 配置同级，独立于 server.yaml，便于运行时修改且不受升级覆盖。
package uicfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// MaxNameLen 系统名称最大字符数（按 rune 计）。
	MaxNameLen = 64
	// MaxLogoLen Logo（data URL 明文）最大字节数，约对应 3MB 原图。
	MaxLogoLen = 4 * 1024 * 1024
	// MaxFooterLen 页脚文本最大字符数（按 rune 计）。
	MaxFooterLen = 512
)

// UIConfig 系统 UI 品牌配置。
type UIConfig struct {
	Name   string `yaml:"name" json:"name"`   // 系统名称
	Logo   string `yaml:"logo" json:"logo"`   // 可选 Logo：图片 data URL（data:image/...;base64,）或 http(s) 链接
	Footer string `yaml:"footer" json:"footer"` // 页脚文本（支持 HTML），空则隐藏
}

// DefaultUIConfig 返回默认品牌（系统名 NebulaEye，无自定义 Logo 时使用前端默认徽标）。
func DefaultUIConfig() UIConfig {
	return UIConfig{Name: "NebulaEye", Logo: "", Footer: ""}
}

// Manager 负责 UI 配置的加载与落盘。
type Manager struct {
	path string
	cfg  UIConfig
}

// New 从 path 加载配置；文件不存在则用 initial 初始化并落盘；解析失败返回错误。
func New(path string, initial UIConfig) (*Manager, error) {
	m := &Manager{path: path, cfg: initial}
	data, err := os.ReadFile(path)
	if err == nil {
		var c UIConfig
		if err := yaml.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("解析 UI 配置失败: %w", err)
		}
		if strings.TrimSpace(c.Name) == "" {
			c.Name = initial.Name
		}
		m.cfg = c
		return m, nil
	} else if os.IsNotExist(err) {
		if err := m.Save(initial); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, fmt.Errorf("读取 UI 配置失败: %w", err)
}

// Get 返回当前配置的副本。
func (m *Manager) Get() UIConfig { return m.cfg }

// Save 校验并持久化新配置（先写内存，成功后再落盘）。
func (m *Manager) Save(c UIConfig) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		c.Name = DefaultUIConfig().Name
	}
	if len([]rune(c.Name)) > MaxNameLen {
		return fmt.Errorf("系统名称过长（上限 %d 字符）", MaxNameLen)
	}
	if len(c.Logo) > MaxLogoLen {
		return fmt.Errorf("Logo 数据过大（上限 %d 字节，建议小于 3MB）", MaxLogoLen)
	}
	if c.Logo != "" && !isValidLogo(c.Logo) {
		return fmt.Errorf("Logo 仅支持图片 data URL（data:image/...;base64,）或 http(s) 链接")
	}
	if len([]rune(c.Footer)) > MaxFooterLen {
		return fmt.Errorf("页脚文本过长（上限 %d 字符）", MaxFooterLen)
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.path, data, 0o644); err != nil {
		return err
	}
	m.cfg = c
	return nil
}

// isValidLogo 校验 Logo 为图片 data URL 或 http(s) URL。
func isValidLogo(s string) bool {
	if strings.HasPrefix(s, "data:image/") && strings.Contains(s, "base64,") {
		return true
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	return false
}
