package auth

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nebula/monitor/internal/server/crypto"
)

// DataFile 是用户与角色持久化文件（与 server.yaml 解耦）。
// 写入采用「临时文件 + 备份 + 原子替换」，写后刷新内存缓存。
type DataFile struct {
	Users []User `yaml:"users" json:"users"`
	Roles []Role `yaml:"roles" json:"roles"`
	// ScopeLabelKey 是业务范围**约定的资产标签键**（默认见 DefaultScopeLabelKey，即 biz）。
	//
	// 放在授权数据文件里而不是 server.yaml：它只被授权判定用到，"授权相关的东西"放一起最不容易漏改。
	// 换键后的行为是安全的：选择器自带键名，旧授权只会匹配不到任何资产（fail-closed），
	// 不会被静默解释成新键的含义。
	ScopeLabelKey string `yaml:"scope_label_key,omitempty" json:"scope_label_key,omitempty"`
}

// Store 管理用户与角色数据：加载、原子保存、缓存、备份与并发保护。
type Store struct {
	mu      sync.RWMutex
	path    string
	data    DataFile
	roles   map[string]Role // 角色名 -> 角色（含内置）
	userIdx map[string]int  // 用户名 -> data.Users 下标
}

// NewStore 创建 Store 并尝试从 path 加载；文件不存在时以内置角色初始化（无用户）。
func NewStore(path string) (*Store, error) {
	s := &Store{path: path}
	// 始终注入内置角色，自定义角色从文件加载追加/覆盖。
	for _, r := range BuiltinRoles() {
		s.roles = setRole(s.roles, r)
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// ScopeLabelKey 返回当前约定的业务范围标签键（未配置时用默认键）。
func (s *Store) ScopeLabelKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scopeLabelKeyLocked()
}

// scopeLabelKeyLocked 是 ScopeLabelKey 的**持锁版本**。
//
// 必须分开：Update* 系列在写锁内工作，直接调 ScopeLabelKey() 会自我死锁
// （sync.RWMutex 不可重入，同一个 goroutine 再取读锁会永久阻塞）。
func (s *Store) scopeLabelKeyLocked() string {
	return NormalizeScopeLabelKey(s.data.ScopeLabelKey)
}

func setRole(m map[string]Role, r Role) map[string]Role {
	if m == nil {
		m = make(map[string]Role)
	}
	m[r.Name] = r
	return m
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		// 文件不存在：以内置角色初始化，无用户（迁移由调用方触发）。
		s.data = DataFile{Roles: cloneBuiltinRoles()}
		s.reindexLocked()
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取用户数据文件失败: %w", err)
	}
	var df DataFile
	if err := yaml.Unmarshal(data, &df); err != nil {
		return fmt.Errorf("解析用户数据文件失败: %w", err)
	}
	// 合并内置角色：文件中的自定义角色覆盖同名（理论上不重名），内置角色始终以代码为准。
	s.data = df
	s.reindexLocked()
	// 确保内置角色存在（升级旧文件可能缺字段）。
	for _, r := range BuiltinRoles() {
		if _, ok := s.roles[r.Name]; !ok {
			s.roles[r.Name] = r
		}
	}
	return nil
}

func cloneBuiltinRoles() []Role {
	out := make([]Role, 0, len(BuiltinRoles()))
	out = append(out, BuiltinRoles()...)
	return out
}

func (s *Store) reindexLocked() {
	s.roles = make(map[string]Role, len(BuiltinRoles())+len(s.data.Roles))
	for _, r := range BuiltinRoles() {
		s.roles[r.Name] = r
	}
	for _, r := range s.data.Roles {
		// 内置角色始终以代码为准：历史版本可能在 users.yaml 中写入了与内置角色
		// 同名的自定义角色（或未来目录演进产生重名），若允许覆盖，绑定该角色的
		// 用户会以残缺权限集工作（如超级管理员丢失 system:config 被 403）。
		if _, builtin := s.roles[r.Name]; builtin {
			continue
		}
		s.roles[r.Name] = r
	}
	s.userIdx = make(map[string]int, len(s.data.Users))
	for i, u := range s.data.Users {
		s.userIdx[u.Username] = i
	}
}

// LookupRole 返回角色（含内置）。
func (s *Store) LookupRole(name string) (Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[name]
	return r, ok
}

// ListRoles 返回全部角色（内置优先）。
func (s *Store) ListRoles() []Role {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Role, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r)
	}
	return out
}

// ListUsers 返回全部用户（不含密码哈希）。
func (s *Store) ListUsers() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, len(s.data.Users))
	for i, u := range s.data.Users {
		u.PasswordHash = ""
		out[i] = u
	}
	return out
}

// GetUser 返回用户（含密码哈希，仅内部校验用）。
func (s *Store) GetUser(username string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.userIdx[username]
	if !ok {
		return User{}, false
	}
	return s.data.Users[i], true
}

// GetPrincipal 展开用户为 Principal；用户不存在或禁用返回 nil。
func (s *Store) GetPrincipal(username string) *Principal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.userIdx[username]
	if !ok {
		return nil
	}
	u := s.data.Users[i]
	if u.Status != StatusEnabled {
		return nil
	}
	return ExpandPrincipal(u, func(name string) (Role, bool) {
		r, ok := s.roles[name]
		return r, ok
	})
}

// VerifyPassword 校验用户名与密码，并返回展开的 Principal。
// 用户不存在、禁用或密码错误均返回 nil, false。
func (s *Store) VerifyPassword(username, password string) (*Principal, bool) {
	u, ok := s.GetUser(username)
	if !ok || u.Status != StatusEnabled {
		return nil, false
	}
	if !crypto.VerifyPassword(u.PasswordHash, password) {
		return nil, false
	}
	return s.GetPrincipal(username), true
}

// CreateUser 创建用户；校验用户名唯一、角色存在、密码强度。
func (s *Store) CreateUser(u User, plainPassword, operator string) error {
	if u.Status == "" {
		u.Status = StatusEnabled
	}
	if err := ValidateUser(u); err != nil {
		return err
	}
	// 业务范围的选择器要用**当前约定的标签键**校验，所以放在这里（ValidateUser 不知道该键）。
	if err := ValidateAssetScope(u.Scope.AssetMode, u.Scope.AssetLabels, s.ScopeLabelKey()); err != nil {
		return err
	}
	if err := ValidatePassword(plainPassword); err != nil {
		return err
	}
	for _, rn := range u.Roles {
		if _, ok := s.LookupRole(rn); !ok {
			return fmt.Errorf("角色 %s 不存在", rn)
		}
	}
	hash, err := crypto.HashPassword(plainPassword)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	u.CreatedBy = operator
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.userIdx[u.Username]; ok {
		return fmt.Errorf("用户名 %s 已存在", u.Username)
	}
	s.data.Users = append(s.data.Users, u)
	s.userIdx[u.Username] = len(s.data.Users) - 1
	return s.saveLocked()
}

// UpdateUser 更新用户资料（角色、范围、状态、显示名）；不修改密码。
func (s *Store) UpdateUser(username string, patch UserPatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	u := s.data.Users[i]
	if patch.DisplayName != nil {
		u.DisplayName = *patch.DisplayName
	}
	if patch.Roles != nil {
		for _, rn := range patch.Roles {
			if _, ok := s.roles[rn]; !ok {
				return fmt.Errorf("角色 %s 不存在", rn)
			}
		}
		u.Roles = patch.Roles
	}
	if patch.Scope != nil {
		u.Scope = *patch.Scope
		if err := ValidateUser(u); err != nil {
			return err
		}
		// 此处已持写锁，必须用持锁版本的键读取（见 scopeLabelKeyLocked 的死锁说明）。
		if err := ValidateAssetScope(u.Scope.AssetMode, u.Scope.AssetLabels, s.scopeLabelKeyLocked()); err != nil {
			return err
		}
	}
	if patch.Status != nil {
		u.Status = *patch.Status
	}

	// 保护最后一个启用的超级管理员不被禁用/删除。
	if u.Status == StatusDisabled && isLastSuperAdminLocked(s, username) {
		return fmt.Errorf("不能禁用最后一个启用的超级管理员")
	}
	s.data.Users[i] = u
	return s.saveLocked()
}

// isLastSuperAdminLocked 在持有写锁时判断 username 是否为最后一个启用的超级管理员。
func isLastSuperAdminLocked(s *Store, username string) bool {
	count := 0
	for _, u := range s.data.Users {
		if u.Status != StatusEnabled {
			continue
		}
		for _, rn := range u.Roles {
			if rn == RoleSuperAdmin {
				count++
				break
			}
		}
	}
	target, ok := s.userIdx[username]
	if !ok {
		return false
	}
	selfIsSuper := false
	for _, rn := range s.data.Users[target].Roles {
		if rn == RoleSuperAdmin {
			selfIsSuper = true
		}
	}
	return selfIsSuper && count <= 1
}

// DisableUser 禁用用户并使其会话失效（TokenVersion++）。
func (s *Store) DisableUser(username, operator string) error {
	return s.bumpAndSetStatus(username, StatusDisabled, operator)
}

// EnableUser 启用用户。
func (s *Store) EnableUser(username, operator string) error {
	return s.bumpAndSetStatus(username, StatusEnabled, operator)
}

func (s *Store) bumpAndSetStatus(username, status, operator string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	if status == StatusDisabled && isLastSuperAdminLocked(s, username) {
		return fmt.Errorf("不能禁用最后一个启用的超级管理员")
	}
	u := s.data.Users[i]
	u.Status = status
	u.TokenVersion++
	s.data.Users[i] = u
	_ = operator
	return s.saveLocked()
}

// DeleteUser 删除用户（保护最后一个超级管理员）。
func (s *Store) DeleteUser(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	if isLastSuperAdminLocked(s, username) {
		return fmt.Errorf("不能删除最后一个超级管理员")
	}
	s.data.Users = append(s.data.Users[:i], s.data.Users[i+1:]...)
	delete(s.userIdx, username)
	// 重建索引（下标变化）。
	s.userIdx = make(map[string]int, len(s.data.Users))
	for idx, u := range s.data.Users {
		s.userIdx[u.Username] = idx
	}
	return s.saveLocked()
}

// ResetPassword 重置/修改密码；明文不落盘、不进审计。
func (s *Store) ResetPassword(username, plainPassword string) error {
	if err := ValidatePassword(plainPassword); err != nil {
		return err
	}
	hash, err := crypto.HashPassword(plainPassword)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	u := s.data.Users[i]
	u.PasswordHash = hash
	u.TokenVersion++ // 密码变更使旧会话失效
	s.data.Users[i] = u
	return s.saveLocked()
}

// CreateRole 创建自定义角色；范围不得超过创建者自身范围。
func (s *Store) CreateRole(r Role, operator string, operatorScope Scope) error {
	r.Name = NormalizeRoleName(r.Name)
	if r.Name == "" {
		return fmt.Errorf("角色名不能为空")
	}
	if _, ok := s.LookupRole(r.Name); ok {
		return fmt.Errorf("角色 %s 已存在", r.Name)
	}
	if r.Builtin {
		return fmt.Errorf("不能创建内置角色")
	}
	// 校验权限点合法。
	all := AllPermissionKeys()
	for _, p := range r.Permissions {
		if _, ok := all[p]; !ok {
			return fmt.Errorf("权限点 %s 非法", p)
		}
	}
	if err := ValidateAssetScope(r.AssetMode, r.AssetLabels, s.ScopeLabelKey()); err != nil {
		return err
	}
	// 自定义角色范围约束：仅超级管理员可创建 global 角色；
	// 非全局操作者创建的角色范围必须是其自身范围的子集。
	if r.ScopeMode == ScopeGlobal && !operatorScope.IsGlobal() {
		return fmt.Errorf("仅全局范围用户可创建全局角色")
	}
	if r.ScopeMode == ScopeRestricted && !operatorScope.IsGlobal() {
		for _, g := range r.ScopeGroups {
			if !operatorScope.ContainsGroup(g) {
				return fmt.Errorf("自定义角色范围不得超过创建者自身范围")
			}
		}
	}
	r.CreatedBy = operator
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Roles = append(s.data.Roles, r)
	s.roles[r.Name] = r
	return s.saveLocked()
}

// UpdateRole 更新自定义角色。
func (s *Store) UpdateRole(name string, patch RolePatch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, r := range s.data.Roles {
		if r.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("自定义角色 %s 不存在", name)
	}
	r := s.data.Roles[idx]
	if patch.Description != nil {
		r.Description = *patch.Description
	}
	if patch.Permissions != nil {
		all := AllPermissionKeys()
		for _, p := range patch.Permissions {
			if _, ok := all[p]; !ok {
				return fmt.Errorf("权限点 %s 非法", p)
			}
		}
		r.Permissions = patch.Permissions
	}
	if patch.ScopeMode != nil {
		r.ScopeMode = *patch.ScopeMode
	}
	if patch.ScopeGroups != nil {
		r.ScopeGroups = patch.ScopeGroups
	}
	if patch.AssetMode != nil {
		r.AssetMode = *patch.AssetMode
	}
	if patch.AssetLabels != nil {
		r.AssetLabels = patch.AssetLabels
	}
	// 无条件校验**结果**（而不是"改了哪项校验哪项"）：业务维度的模式与选择器是一对，
	// 只改其中一个也可能组出非法组合（如把模式改成 all 却留着选择器）。
	if err := ValidateAssetScope(r.AssetMode, r.AssetLabels, s.scopeLabelKeyLocked()); err != nil {
		return err
	}
	s.data.Roles[idx] = r
	s.roles[name] = r
	return s.saveLocked()
}

// DeleteRole 删除自定义角色（须无用户绑定）。
func (s *Store) DeleteRole(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, r := range s.data.Roles {
		if r.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("自定义角色 %s 不存在", name)
	}
	if s.data.Roles[idx].Builtin {
		return fmt.Errorf("内置角色不可删除")
	}
	for _, u := range s.data.Users {
		for _, rn := range u.Roles {
			if rn == name {
				return fmt.Errorf("角色 %s 仍被用户绑定，无法删除", name)
			}
		}
	}
	s.data.Roles = append(s.data.Roles[:idx], s.data.Roles[idx+1:]...)
	delete(s.roles, name)
	return s.saveLocked()
}

// TouchLogin 更新最近登录时间与 IP（不频繁落盘，采用轻量写）。
func (s *Store) TouchLogin(username, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return
	}
	now := time.Now()
	s.data.Users[i].LastLoginAt = &now
	s.data.Users[i].LastLoginIP = ip
	_ = s.saveLocked()
}

// BumpTokenVersion 仅提升 TokenVersion 使旧会话失效，密码不变（用于注销）。
func (s *Store) BumpTokenVersion(username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.userIdx[username]
	if !ok {
		return fmt.Errorf("用户 %s 不存在", username)
	}
	u := s.data.Users[i]
	u.TokenVersion++
	s.data.Users[i] = u
	return s.saveLocked()
}

// saveLocked 在持有写锁时原子保存（调用方负责加锁）。
func (s *Store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	out, err := yaml.Marshal(s.data)
	if err != nil {
		return err
	}
	// 备份原文件。
	if _, err := os.Stat(s.path); err == nil {
		bak := s.path + ".bak"
		if data, rerr := os.ReadFile(s.path); rerr == nil {
			_ = os.WriteFile(bak, data, 0o644)
		}
	}
	if dir := filepath.Dir(s.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			slog.Warn("创建用户数据目录失败", "err", err)
		}
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// UserPatch 为用户更新请求的字段集合（指针区分「未提供」与「清空」）。
type UserPatch struct {
	DisplayName *string
	Roles       []string
	Scope       *Scope
	Status      *string
}

// RolePatch 为角色更新请求的字段集合。
type RolePatch struct {
	Description *string
	Permissions []string
	ScopeMode   *string
	ScopeGroups []string
	// AssetMode / AssetLabels 是业务标签维度。nil = 本次不改（与 ScopeGroups 同一套语义）；
	// 要把该维度关掉就显式给 AssetMode="all"（并清空选择器）。
	AssetMode   *string
	AssetLabels []AssetScope
}
