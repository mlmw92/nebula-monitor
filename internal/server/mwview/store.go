// Package mwview 维护「中间件监控页面展示哪些类型」的全局配置。
//
// 背景：平台内置的中间件类型越来越多，但单个部署通常只跑其中几类；
// 未部署的类型在页面上永远显示 0 实例，只会增加噪音。本包把「展示哪些类型」
// 存为一份可编辑的清单（空 = 全部展示），供前端 Tab 与总览卡片过滤。
package mwview

import (
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Store 是展示配置的文件存储（空清单 = 全部展示）。
type Store struct {
	mu  sync.RWMutex
	path string
}

// Default 全局默认存储，main 启动时 SetPath 指定文件位置。
var Default = &Store{}

// SetPath 设置配置文件路径。
func (s *Store) SetPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.path = path
}

type viewFile struct {
	Enabled []string `yaml:"enabled"`
}

// Enabled 返回启用的类型清单；空切片表示「全部展示」。
func (s *Store) Enabled() []string {
	s.mu.RLock()
	path := s.path
	s.mu.RUnlock()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var vf viewFile
	if err := yaml.Unmarshal(data, &vf); err != nil {
		return nil
	}
	return vf.Enabled
}

// Save 保存启用的类型清单（空清单 = 恢复全部展示）。
func (s *Store) Save(enabled []string) error {
	s.mu.RLock()
	path := s.path
	s.mu.RUnlock()
	if path == "" {
		return os.ErrNotExist
	}
	data, err := yaml.Marshal(viewFile{Enabled: enabled})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
