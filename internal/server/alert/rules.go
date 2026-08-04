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
		// ===== 场景化告警模板 =====
		{Name: "主机离线", Type: model.RuleTypeNodeOffline, For: "5m", Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 15, ToSeverity: model.SeverityCritical, RepeatMinutes: 30}},
		{Name: "MySQL 服务离线", Type: model.RuleTypeServiceDown, Service: "mysql", Operator: "<=", Threshold: 0, For: "3m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "Redis 服务离线", Type: model.RuleTypeServiceDown, Service: "redis", Operator: "<=", Threshold: 0, For: "3m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "Nginx 服务离线", Type: model.RuleTypeServiceDown, Service: "nginx", Operator: "<=", Threshold: 0, For: "3m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "Kafka 服务离线", Type: model.RuleTypeServiceDown, Service: "kafka", Operator: "<=", Threshold: 0, For: "3m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "Kubernetes 集群离线", Type: model.RuleTypeServiceDown, Service: "k8s", Operator: "<=", Threshold: 0, For: "3m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
		{Name: "MySQL 主从切换", Type: model.RuleTypeRoleChange, Service: "mysql", Topology: "cluster", For: "0s", Severity: model.SeverityWarning, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 10, ToSeverity: model.SeverityCritical, RepeatMinutes: 0}},
		{Name: "PostgreSQL 主从切换", Type: model.RuleTypeRoleChange, Service: "postgres", Topology: "replication", For: "0s", Severity: model.SeverityWarning, Scope: "all", Enabled: true},
		{Name: "MySQL 集群状态损坏", Type: model.RuleTypeClusterFault, Service: "mysql", Topology: "cluster", For: "2m", Severity: model.SeverityCritical, Scope: "all", Enabled: true,
			Escalation: &model.Escalation{Enabled: true, AfterMinutes: 10, ToSeverity: model.SeverityCritical, RepeatMinutes: 20}},
		{Name: "Kubernetes 集群状态损坏", Type: model.RuleTypeClusterFault, Service: "k8s", Topology: "cluster", For: "2m", Severity: model.SeverityCritical, Scope: "all", Enabled: true},
	}
}

// seedMarkerPath 返回播种记录文件路径，记录已经播种过的内置规则名。
func (s *RulesStore) seedMarkerPath() string { return s.path + ".seeded" }

// loadSeedMarker 读取已播种的内置规则名集合。文件不存在时返回空集合。
func (s *RulesStore) loadSeedMarker() map[string]bool {
	seeded := map[string]bool{}
	data, err := os.ReadFile(s.seedMarkerPath())
	if err != nil {
		return seeded
	}
	var names []string
	if err := yaml.Unmarshal(data, &names); err != nil {
		slog.Warn("解析告警规则播种记录失败", "path", s.seedMarkerPath(), "err", err)
		return seeded
	}
	for _, n := range names {
		seeded[n] = true
	}
	return seeded
}

// saveSeedMarker 记录当前版本全部内置规则名，表示它们均已播种过。
// 之后用户删除其中任何一条都不会被重新写回。
func (s *RulesStore) saveSeedMarker(names []string) {
	data, err := yaml.Marshal(names)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dirOf(s.path), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(s.seedMarkerPath(), data, 0o644); err != nil {
		slog.Warn("写入告警规则播种记录失败", "err", err, "path", s.seedMarkerPath())
	}
}

// SeedDefaults 播种开箱即用的推荐规则。
//
// 播种以「规则名」为准做增量处理，并用一个独立的播种记录文件记住哪些内置规则已经播种过：
//   - 全新安装：全部写入；
//   - 版本升级：只补写本次版本新增、且此前从未播种过的内置规则（例如「主机离线」等场景化规则），
//     老版本已存在的规则不会重复创建；
//   - 用户主动删除过的内置规则不会被重新写回，因为它已记录在播种记录中。
func (s *RulesStore) SeedDefaults() {
	templates := DefaultTemplates()
	names := make([]string, 0, len(templates))
	for _, t := range templates {
		names = append(names, t.Name)
	}

	_, statErr := os.Stat(s.path)
	freshInstall := os.IsNotExist(statErr)

	seeded := s.loadSeedMarker()
	if freshInstall {
		// 全新安装：规则文件尚不存在，忽略可能残留的播种记录，全部写入
		seeded = map[string]bool{}
	}

	// 已存在的规则名，避免与用户自建的同名规则重复
	s.mu.RLock()
	existing := make(map[string]bool, len(s.rules))
	for _, r := range s.rules {
		existing[r.Name] = true
	}
	s.mu.RUnlock()

	added := 0
	for _, t := range templates {
		if seeded[t.Name] || existing[t.Name] {
			continue
		}
		s.Create(t)
		added++
	}
	if added > 0 {
		slog.Info("已补充内置告警规则", "count", added)
	}
	s.saveSeedMarker(names)
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
