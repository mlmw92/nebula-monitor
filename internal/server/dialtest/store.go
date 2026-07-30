package dialtest

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Store 管理拨测任务配置，持久化到 YAML 文件。
type Store struct {
	mu    sync.RWMutex
	tasks map[string]Task
	path  string
}

// NewStore 创建拨测任务存储并加载。
func NewStore(path string) *Store {
	s := &Store{
		tasks: map[string]Task{},
		path:  path,
	}
	s.load()
	return s
}

// List 返回所有任务。
func (s *Store) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, t)
	}
	return out
}

// Get 返回单个任务。
func (s *Store) Get(id string) (Task, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	return t, ok
}

// Create 创建任务。
func (s *Store) Create(t Task) Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.ID == "" {
		t.ID = "dt-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if t.Interval <= 0 {
		t.Interval = 60
	}
	if t.Timeout <= 0 {
		t.Timeout = 10
	}
	s.tasks[t.ID] = t
	s.persistLocked()
	return t
}

// Update 更新任务。
func (s *Store) Update(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; !ok {
		return os.ErrNotExist
	}
	s.tasks[t.ID] = t
	s.persistLocked()
	return nil
}

// Delete 删除任务。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.tasks, id)
	s.persistLocked()
	return nil
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []Task
	if err := yaml.Unmarshal(data, &list); err != nil {
		return
	}
	for _, t := range list {
		if t.ID == "" {
			t.ID = "dt-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		s.tasks[t.ID] = t
	}
}

func (s *Store) persistLocked() {
	list := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		list = append(list, t)
	}
	data, err := yaml.Marshal(list)
	if err != nil {
		return
	}
	_ = os.MkdirAll(dirOf(s.path), 0o755)
	_ = os.WriteFile(s.path, data, 0o644)
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// Errorf 格式化错误。
func Errorf(format string, args ...interface{}) error {
	return fmt.Errorf(format, args...)
}
