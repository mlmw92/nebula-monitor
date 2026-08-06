// Package dashboard 管理用户自定义仪表盘。
//
// 仪表盘配置独立持久化到 YAML 文件（默认 /etc/monitor-server/dashboards.yaml），
// 与 ui/screen 配置同级，由 Web 端增删改写入，不受升级覆盖；保存即落盘热生效。
package dashboard

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// ChartType 面板图表类型，与指标目录 ChartType 对应。
type ChartType string

const (
	ChartLine  ChartType = "line"
	ChartArea  ChartType = "area"
	ChartBar   ChartType = "bar"
	ChartGauge ChartType = "gauge"
)

// Panel 仪表盘中的一个面板。
type Panel struct {
	Title     string            `yaml:"title" json:"title"`         // 面板标题
	ChartType ChartType         `yaml:"chartType" json:"chartType"` // 图表类型
	Metric    string            `yaml:"metric" json:"metric"`       // 指标名（来自指标目录）
	Node      string            `yaml:"node,omitempty" json:"node,omitempty"`         // 限定主机（空=全部）
	Instance  string            `yaml:"instance,omitempty" json:"instance,omitempty"` // 限定中间件实例标识（可选）
	Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`     // 附加筛选标签（如 disk/iface）
	Range     string            `yaml:"range,omitempty" json:"range,omitempty"`       // 时间范围，如 1h/6h/24h/7d
	Step      int64             `yaml:"step,omitempty" json:"step,omitempty"`         // 步长（毫秒），0 用默认
}

// Dashboard 一块自定义看板。
type Dashboard struct {
	ID      string  `yaml:"id" json:"id"`
	Name    string  `yaml:"name" json:"name"`
	Updated int64   `yaml:"updated" json:"updated"` // 最后更新毫秒时间戳
	Panels  []Panel `yaml:"panels" json:"panels"`
}

// fileModel 磁盘文件结构。
type fileModel struct {
	Dashboards []Dashboard `yaml:"dashboards"`
}

// Manager 仪表盘配置管理。
type Manager struct {
	path       string
	dashboards []Dashboard
}

// New 加载仪表盘配置；文件不存在则初始化为空。
func New(path string) (*Manager, error) {
	m := &Manager{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			m.dashboards = []Dashboard{}
			return m, nil
		}
		return nil, fmt.Errorf("读取仪表盘配置失败: %w", err)
	}
	var fm fileModel
	if err := yaml.Unmarshal(data, &fm); err != nil {
		return nil, fmt.Errorf("解析仪表盘配置失败: %w", err)
	}
	// 补全空 ID（兼容旧数据）。
	for i := range fm.Dashboards {
		if fm.Dashboards[i].ID == "" {
			fm.Dashboards[i].ID = fmt.Sprintf("db-%d", time.Now().UnixNano())
		}
	}
	m.dashboards = fm.Dashboards
	return m, nil
}

// List 返回全部看板（按名称排序的副本）。
func (m *Manager) List() []Dashboard {
	out := make([]Dashboard, len(m.dashboards))
	copy(out, m.dashboards)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get 按 ID 取看板。
func (m *Manager) Get(id string) (Dashboard, bool) {
	for _, d := range m.dashboards {
		if d.ID == id {
			return d, true
		}
	}
	return Dashboard{}, false
}

// Create 新建看板（生成 ID 并落盘）。
func (m *Manager) Create(name string, panels []Panel) (Dashboard, error) {
	if name == "" {
		return Dashboard{}, fmt.Errorf("看板名称不能为空")
	}
	d := Dashboard{
		ID:      fmt.Sprintf("db-%d", time.Now().UnixNano()),
		Name:    name,
		Updated: time.Now().UnixMilli(),
		Panels:  panels,
	}
	m.dashboards = append(m.dashboards, d)
	if err := m.save(); err != nil {
		// 回滚
		m.dashboards = m.dashboards[:len(m.dashboards)-1]
		return Dashboard{}, err
	}
	return d, nil
}

// Update 更新看板（名称/面板），不存在则返回错误。
func (m *Manager) Update(id, name string, panels []Panel) error {
	for i, d := range m.dashboards {
		if d.ID == id {
			m.dashboards[i].Name = name
			m.dashboards[i].Panels = panels
			m.dashboards[i].Updated = time.Now().UnixMilli()
			return m.save()
		}
	}
	return fmt.Errorf("看板不存在: %s", id)
}

// Delete 删除看板。
func (m *Manager) Delete(id string) error {
	for i, d := range m.dashboards {
		if d.ID == id {
			m.dashboards = append(m.dashboards[:i], m.dashboards[i+1:]...)
			return m.save()
		}
	}
	return fmt.Errorf("看板不存在: %s", id)
}

// save 先备份再写盘。
func (m *Manager) save() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	// 备份旧文件
	if _, err := os.Stat(m.path); err == nil {
		_ = copyFile(m.path, m.path+".bak")
	}
	fm := fileModel{Dashboards: m.dashboards}
	data, err := yaml.Marshal(fm)
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

// copyFile 复制文件（用于备份）。
func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
