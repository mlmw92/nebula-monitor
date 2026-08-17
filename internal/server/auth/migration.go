package auth

import (
	"log/slog"
	"time"

	"github.com/nebula/monitor/internal/server/crypto"
)

// MigrateSingleAdmin 将 server.yaml 中的单管理员账号（auth.username/password）
// 幂等迁移为 users.yaml 中的首个超级管理员。
//
// 规则：
//   - 若 users.yaml 已存在且含任意用户，直接跳过（不重复迁移）。
//   - 否则若 seed 用户名/密码哈希非空，则创建超级管理员用户；密码复用既有哈希，
//     若 seed 为旧明文则在此处哈希化（seedHashed=false 时）。
//   - 重复启动安全幂等：users.yaml 已存在用户时不会再次写入。
//
// 返回是否执行了迁移动作。
func MigrateSingleAdmin(s *Store, seedUsername, seedPasswordHash string, seedHashed bool) (bool, error) {
	if s == nil {
		return false, nil
	}
	// 已存在用户则不迁移。
	if len(s.ListUsers()) > 0 {
		return false, nil
	}
	if seedUsername == "" || seedPasswordHash == "" {
		return false, nil
	}
	hash := seedPasswordHash
	if !seedHashed {
		// 旧明文：哈希化后再落盘（与启动时 server.yaml 迁移互补）。
		h, err := crypto.HashPassword(seedPasswordHash)
		if err != nil {
			return false, err
		}
		hash = h
	}
	u := User{
		Username:     seedUsername,
		DisplayName:  seedUsername,
		PasswordHash: hash,
		Roles:        []string{RoleSuperAdmin},
		Scope:        Scope{Mode: ScopeGlobal},
		Status:       StatusEnabled,
		CreatedBy:    "migration",
		CreatedAt:    time.Now(),
	}
	if err := s.CreateUserDirect(u); err != nil {
		return false, err
	}
	slog.Info("已将单管理员账号迁移为超级管理员", "username", seedUsername, "file", s.path)
	return true, nil
}

// CreateUserDirect 直接写入用户（绕过唯一性之外的额外校验，供迁移使用）。
func (s *Store) CreateUserDirect(u User) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	if u.Status == "" {
		u.Status = StatusEnabled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.userIdx[u.Username]; ok {
		return nil // 幂等
	}
	s.data.Users = append(s.data.Users, u)
	s.userIdx[u.Username] = len(s.data.Users) - 1
	return s.saveLocked()
}
