package alert

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
)

// MaintenanceStore 管理维护窗口配置，持久化到 YAML 文件。
type MaintenanceStore struct {
	mu   sync.RWMutex
	mw   model.MaintenanceWindow
	path string
}

// NewMaintenanceStore 创建维护窗口存储并加载。
func NewMaintenanceStore(path string) *MaintenanceStore {
	s := &MaintenanceStore{path: path}
	s.load()
	return s
}

// Get 返回当前维护窗口配置。
func (s *MaintenanceStore) Get() model.MaintenanceWindow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mw
}

// Set 更新维护窗口并持久化。
func (s *MaintenanceStore) Set(mw model.MaintenanceWindow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mw = mw
	s.persistLocked()
}

// IsActive 判断维护窗口是否处于活跃状态（当前时间在窗口内）。
func (s *MaintenanceStore) IsActive(now int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.mw.Enabled {
		return false
	}
	return now >= s.mw.Start && now <= s.mw.End
}

func (s *MaintenanceStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	_ = yaml.Unmarshal(data, &s.mw)
}

func (s *MaintenanceStore) persistLocked() {
	data, err := yaml.Marshal(s.mw)
	if err != nil {
		return
	}
	_ = os.MkdirAll(dirOf(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}
