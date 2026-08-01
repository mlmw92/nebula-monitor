// Package alert 实现阈值告警引擎：规则管理、评估、通知。
package alert

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/model"
)

// RulesStore 管理告警规则，持久化到 YAML 文件。
type RulesStore struct {
	mu    sync.RWMutex
	rules map[string]model.AlertRule
	path  string
}

// NewRulesStore 创建规则存储并加载。若规则文件不存在（全新安装），
// 自动播种一组开箱即用的推荐规则，避免告警中心初始为空、看起来“没用起来”。
func NewRulesStore(path string) *RulesStore {
	s := &RulesStore{
		rules: map[string]model.AlertRule{},
		path:  path,
	}
	s.load()
	s.SeedDefaults()
	return s
}

// DefaultTemplates 返回可复用的规则模板（不含 ID/时间戳），供前端“从模板新建”。
func DefaultTemplates() []model.AlertRule {
	return []model.AlertRule{
		{Name: "CPU 使用率过高", Metric: "cpu_usage", Operator: ">", Threshold: 85, For: "5m", Severity: model.SeverityWarning, Scope: "all", Enabled: true},
		{Name: "内存使用率过高", Metric: "mem_used_percent", Operator: ">", Threshold: 90, For: "5m", Severity: model.SeverityWarning, Scope: "all", Enabled: true},
		{Name: "磁盘使用率过高", Metric: "disk_used_percent", Operator: ">", Threshold: 85, For: "5m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "系统负载过高", Metric: "load1", Operator: ">", Threshold: 8, For: "5m", Severity: model.SeverityWarning, Scope: "all", Enabled: true},
	}
}

// SeedDefaults 仅当规则文件不存在（全新安装）时写入推荐规则，
// 不覆盖用户主动清空规则后的空状态。
func (s *RulesStore) SeedDefaults() {
	if _, err := os.Stat(s.path); err == nil {
		return
	}
	for _, t := range DefaultTemplates() {
		s.Create(t)
	}
}

// sortRulesByCreatedDesc 按创建时间倒序排列（新建在前）；
// CreatedAt 相同时按 ID 倒序，保证顺序确定、不随 map 遍历随机抖动。
func sortRulesByCreatedDesc(out []model.AlertRule) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
}

// List 返回所有规则，按创建时间倒序。
func (s *RulesStore) List() []model.AlertRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		out = append(out, r)
	}
	sortRulesByCreatedDesc(out)
	return out
}

// Get 返回单个规则。
func (s *RulesStore) Get(id string) (model.AlertRule, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rules[id]
	return r, ok
}

// Create 创建规则，自动生成 ID 与时间戳。
func (s *RulesStore) Create(r model.AlertRule) model.AlertRule {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := model.NowMillis()
	if r.ID == "" {
		r.ID = "r-" + strconv.FormatInt(now, 36)
	}
	r.CreatedAt = now
	r.UpdatedAt = now
	s.rules[r.ID] = r
	s.persistLocked()
	return r
}

// Update 更新规则。
func (s *RulesStore) Update(r model.AlertRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[r.ID]; !ok {
		return os.ErrNotExist
	}
	r.UpdatedAt = model.NowMillis()
	s.rules[r.ID] = r
	s.persistLocked()
	return nil
}

// Delete 删除规则。
func (s *RulesStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rules[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.rules, id)
	s.persistLocked()
	return nil
}

func (s *RulesStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []model.AlertRule
	if err := yaml.Unmarshal(data, &list); err != nil {
		// 备份损坏文件（通常为旧版本残留格式），避免内容静默丢失；
		// 下次通过界面保存规则时会以新格式覆盖原文件。
		backup := fmt.Sprintf("%s.corrupt-%d", s.path, time.Now().Unix())
		if werr := os.WriteFile(backup, data, 0o644); werr == nil {
			slog.Warn("解析规则文件失败，已备份为损坏文件", "path", s.path, "backup", backup, "err", err)
		} else {
			slog.Warn("解析规则文件失败", "path", s.path, "err", err, "backupErr", werr)
		}
		return
	}
	for _, r := range list {
		if r.ID == "" {
			r.ID = "r-" + strconv.FormatInt(r.CreatedAt, 36)
		}
		s.rules[r.ID] = r
	}
}

func (s *RulesStore) persistLocked() {
	list := make([]model.AlertRule, 0, len(s.rules))
	for _, r := range s.rules {
		list = append(list, r)
	}
	sortRulesByCreatedDesc(list)
	data, err := yaml.Marshal(list)
	if err != nil {
		slog.Warn("序列化规则失败", "err", err)
		return
	}
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		slog.Warn("创建规则目录失败", "err", err)
		return
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		slog.Warn("写入规则文件失败", "err", err, "path", s.path)
	}
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}

// parseFor 将 "5m" 之类解析为秒。
func parseFor(s string) int64 {
	if s == "" {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return int64(d.Seconds())
}
