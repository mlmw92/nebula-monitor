// Package templates 管理采集项模板（C1 阶段二）：Web 端 CRUD + 落盘持久化，并作为下发给 Agent 的数据源。
//
// 与 Agent 侧的关系：模板最终由 Agent 执行（`internal/agent/collector/template.go`），
// 但「写什么」由本包在 Server 侧统一存管——否则要逐台改 agent.yaml。
// 校验复用共享 DSL（`internal/template`），保证「保存时接受的」与「Agent 执行时接受的」完全一致。
//
// 容错取向（与 Agent 侧刻意不同，职责不同）：
//   - `Upsert` 校验失败返回错误且不落盘——Web 端不该存进非法配置；
//   - 启动加载时若文件被手工改坏，只告警并在 UI 中如实展示问题条目，**不阻止 Server 启动**：
//     一个手写错误不应让整套监控起不来。Agent 侧仍是 fail-fast（执行前必须合法）。
package templates

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"

	dsl "github.com/nebula/monitor/internal/template"
)

// Store 是模板配置存储：按 id 索引，保持写入顺序，并维护一个供下发端判断的版本号。
type Store struct {
	mu       sync.RWMutex
	items    map[string]dsl.Config
	order    []string // 保持 YAML 列表顺序，前端展示与 diff 才稳定
	revision uint64   // 每次变更递增：下发端据此判断 Agent 是否需要重新接收
	path     string
}

// NewStore 创建存储并加载已有配置。
func NewStore(path string) *Store {
	s := &Store{items: map[string]dsl.Config{}, revision: 1, path: path}
	s.load()
	return s
}

// List 返回全部模板（按写入顺序）。
func (s *Store) List() []dsl.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked()
}

// Get 返回单个模板。
func (s *Store) Get(id string) (dsl.Config, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.items[id]
	return c, ok
}

// Revision 返回当前版本号（变更即递增）。
func (s *Store) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// Snapshot 一次性返回「模板列表 + 版本号」，供下发使用（避免取完列表后版本又变了）。
func (s *Store) Snapshot() ([]dsl.Config, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.listLocked(), s.revision
}

// Upsert 新增或更新一个模板。
//
// 校验对象是**整个候选集合**而非单条：id 唯一性与「id 互为前缀」都是跨条约束，
// 只校验单条会漏（例如新模板 id 与既有模板构成前缀包含）。
// 校验通过后才落盘；落盘失败会回滚内存，避免「内存里有、硬盘上没有」的假成功。
func (s *Store) Upsert(cfg dsl.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateLocked(cfg); err != nil {
		return err
	}

	prev, existed := s.items[cfg.ID]
	prevOrder := s.order
	s.items[cfg.ID] = cfg
	if !existed {
		s.order = append(append([]string{}, s.order...), cfg.ID)
	}
	if err := s.persistLocked(); err != nil {
		// 回滚：宁可让调用方收到错误，也不能让内存与磁盘不一致
		if existed {
			s.items[cfg.ID] = prev
		} else {
			delete(s.items, cfg.ID)
			s.order = prevOrder
		}
		return err
	}
	s.revision++
	return nil
}

// Validate 在「当前集合 + cfg」上做完整校验，不落盘。
//
// 单独暴露的原因：前端编辑器需要在保存前拿到**与保存时完全一致**的校验结果
// （包括 id 是否与既有模板互为前缀这类跨条约束），前端自己实现一份必然漂移。
func (s *Store) Validate(cfg dsl.Config) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.validateLocked(cfg)
}

// candidateLocked 构造「用 cfg 替换同 id 条目后」的完整集合，供校验使用。
func (s *Store) candidateLocked(cfg dsl.Config) []dsl.Config {
	candidate := make([]dsl.Config, 0, len(s.items)+1)
	for _, id := range s.order {
		if id == cfg.ID {
			continue
		}
		candidate = append(candidate, s.items[id])
	}
	return append(candidate, cfg)
}

// validateLocked 校验候选集合 = 共享 DSL 规则 + Server 侧附加规则。
func (s *Store) validateLocked(cfg dsl.Config) error {
	candidate := s.candidateLocked(cfg)
	return errors.Join(dsl.ValidateAll(candidate), requireGroups(candidate))
}

// requireGroups 是 Server 侧的附加规则：由 Server 下发的模板必须声明生效的节点分组。
//
// 为什么不放进共享 DSL：`agent.yaml` 的本机模板天然只对本机生效，填 groups 无意义，
// 放进 DSL 会迫使本机配置多写一个不起作用的字段。
// 为什么必须要求：若允许留空而默认「全部节点」，一台只跑某中间件的机器配一个模板，
// 会让其余节点每轮各报一个 `template_target_up=0`——序列与日志双噪声。
func requireGroups(cfgs []dsl.Config) error {
	var errs []error
	for _, c := range cfgs {
		if len(c.Groups) == 0 {
			errs = append(errs, fmt.Errorf(
				"模板 %s 未指定 groups：由 Server 下发的模板必须声明生效的节点分组，否则会在所有节点上产出 template_target_up=0", c.ID))
		}
	}
	return errors.Join(errs...)
}

// Delete 删除模板；不存在时返回 os.ErrNotExist。
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return os.ErrNotExist
	}
	delete(s.items, id)
	next := make([]string, 0, len(s.order))
	for _, v := range s.order {
		if v != id {
			next = append(next, v)
		}
	}
	s.order = next
	if err := s.persistLocked(); err != nil {
		return err
	}
	s.revision++
	return nil
}

// ListIDs 返回全部模板 id（按写入顺序），供下发与前端展示。
func (s *Store) ListIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.order...)
}

func (s *Store) listLocked() []dsl.Config {
	out := make([]dsl.Config, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id])
	}
	return out
}

// load 读取并加载配置文件；文件损坏或条目非法时只告警，不阻止启动。
func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // 首次运行无文件属正常
	}
	var list []dsl.Config
	if err := yaml.Unmarshal(data, &list); err != nil {
		slog.Warn("采集项模板文件解析失败，本次不加载任何模板", "path", s.path, "err", err)
		return
	}
	// 如实展示问题但不阻止启动：手工编辑出错时，管理员需要能进 UI 修，
	// 而不是被一个配置文件挡在门外（保存路径上的校验会挡住后续非法写入）。
	if err := dsl.ValidateAll(list); err != nil {
		slog.Warn("采集项模板存在非法条目（仍会加载，便于在 Web 端修正）", "path", s.path, "err", err)
	}
	for _, cfg := range list {
		if cfg.ID == "" {
			slog.Warn("采集项模板缺少 id，已跳过", "path", s.path)
			continue
		}
		if _, dup := s.items[cfg.ID]; dup {
			slog.Warn("采集项模板 id 重复，仅保留首次出现", "id", cfg.ID)
			continue
		}
		s.items[cfg.ID] = cfg
		s.order = append(s.order, cfg.ID)
	}
	slog.Info("已加载采集项模板", "count", len(s.order), "path", s.path)
}

// persistLocked 原子落盘（临时文件 + rename），避免写到一半被读取到半截 YAML。
func (s *Store) persistLocked() error {
	data, err := yaml.Marshal(s.listLocked())
	if err != nil {
		return fmt.Errorf("序列化模板配置失败: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建模板配置目录失败: %w", err)
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入模板配置失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换模板配置失败: %w", err)
	}
	return nil
}
