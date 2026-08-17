package auth

import (
	"os"
	"path/filepath"
	"testing"
)

// newTempStore 创建临时 users.yaml 的 Store（含内置角色）。
func newTempStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

func TestBuiltinRolesPresent(t *testing.T) {
	s, _ := newTempStore(t)
	roles := s.ListRoles()
	if len(roles) < 6 {
		t.Fatalf("内置角色不足 6 个: %d", len(roles))
	}
	if _, ok := s.LookupRole(RoleSuperAdmin); !ok {
		t.Fatal("缺少超级管理员角色")
	}
}

func TestCreateUserAndVerify(t *testing.T) {
	s, _ := newTempStore(t)
	// 受限自定义角色：仅 nodes:read，范围 g1
	if err := s.CreateRole(Role{Name: "viewer_g1", Permissions: []string{"nodes:read"}, ScopeMode: ScopeRestricted, ScopeGroups: []string{"g1"}}, "admin", Scope{Mode: ScopeGlobal}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	u := User{Username: "ops1", DisplayName: "运维一", Roles: []string{"viewer_g1"}, Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}, Status: StatusEnabled}
	if err := s.CreateUser(u, "Passw0rd!", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	p, ok := s.VerifyPassword("ops1", "Passw0rd!")
	if !ok || p == nil {
		t.Fatal("校验密码失败")
	}
	if !p.HasPermission("nodes:read") {
		t.Error("应拥有 nodes:read")
	}
	if p.HasPermission("nodes:write") {
		t.Error("受限角色不应拥有 nodes:write")
	}
	if !p.CanAccessGroup("g1") {
		t.Error("应可访问 g1")
	}
	if p.CanAccessGroup("g2") {
		t.Error("受限用户不应访问 g2")
	}
	_, ok = s.VerifyPassword("ops1", "wrong")
	if ok {
		t.Error("错误密码不应通过")
	}
}

func TestDisabledUserDenied(t *testing.T) {
	s, _ := newTempStore(t)
	if err := s.CreateUser(User{Username: "usr", Roles: []string{RoleReadOnly}, Scope: Scope{Mode: ScopeGlobal}}, "Passw0rd!", "admin"); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Logf("users after create: %+v", s.ListUsers())
	if err := s.DisableUser("usr", "admin"); err != nil {
		t.Fatalf("DisableUser: %v", err)
	}
	if _, ok := s.VerifyPassword("usr", "Passw0rd!"); ok {
		t.Error("禁用用户不应通过校验")
	}
}

func TestEmptyScopeMeansNoAccess(t *testing.T) {
	s, _ := newTempStore(t)
	_ = s.CreateUser(User{Username: "empt", Roles: []string{RoleReadOnly}, Scope: Scope{Mode: ScopeRestricted, Groups: nil}}, "Passw0rd!", "admin")
	p, _ := s.VerifyPassword("empty", "Passw0rd!")
	if p.CanAccessGroup("any") {
		t.Error("空范围不应授予任何分组访问")
	}
}

func TestLastSuperAdminProtected(t *testing.T) {
	s, _ := newTempStore(t)
	_ = s.CreateUser(User{Username: "sup", Roles: []string{RoleSuperAdmin}, Scope: Scope{Mode: ScopeGlobal}}, "Passw0rd!", "admin")
	if err := s.DisableUser("sup", "admin"); err == nil {
		t.Error("应禁止禁用最后一个超级管理员")
	}
	if err := s.DeleteUser("sup"); err == nil {
		t.Error("应禁止删除最后一个超级管理员")
	}
}

func TestCustomRoleScopeConstrained(t *testing.T) {
	s, _ := newTempStore(t)
	// 受限操作者创建全局角色应失败
	_ = s.CreateUser(User{Username: "lim", Roles: []string{RoleOpsAdmin}, Scope: Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}}, "Passw0rd!", "admin")
	// 模拟操作者范围
	if err := s.CreateRole(Role{Name: "custom", Permissions: []string{"nodes:read"}, ScopeMode: ScopeGlobal}, "lim", Scope{Mode: ScopeRestricted, Groups: []string{"g1"}}); err == nil {
		t.Error("受限操作者不应能创建全局角色")
	}
	// 超管可创建
	if err := s.CreateRole(Role{Name: "custom", Permissions: []string{"nodes:read"}, ScopeMode: ScopeGlobal}, "admin", Scope{Mode: ScopeGlobal}); err != nil {
		t.Fatalf("超管应可创建全局角色: %v", err)
	}
	if err := s.DeleteRole("custom"); err != nil {
		t.Fatalf("删除自定义角色: %v", err)
	}
}

func TestPasswordResetBumpsVersion(t *testing.T) {
	s, _ := newTempStore(t)
	_ = s.CreateUser(User{Username: "usr", Roles: []string{RoleReadOnly}, Scope: Scope{Mode: ScopeGlobal}}, "Passw0rd!", "admin")
	if err := s.ResetPassword("usr", "NewPass9!"); err != nil {
		t.Fatalf("ResetPassword: %v", err)
	}
	if _, ok := s.VerifyPassword("usr", "Passw0rd!"); ok {
		t.Error("旧密码在重置后应失效")
	}
	if _, ok := s.VerifyPassword("usr", "NewPass9!"); !ok {
		t.Error("新密码应通过")
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "users.yaml")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	// 首次迁移
	if _, err := MigrateSingleAdmin(s, "admin", "admin", false); err != nil {
		t.Fatalf("迁移: %v", err)
	}
	// 二次迁移应幂等（不报错，不重复）
	if _, err := MigrateSingleAdmin(s, "admin", "admin", false); err != nil {
		t.Fatalf("二次迁移: %v", err)
	}
	if _, ok := s.VerifyPassword("admin", "admin"); !ok {
		t.Fatal("迁移后管理员密码应可用")
	}
	// 文件应存在
	if _, err := os.Stat(path); err != nil {
		t.Fatal("users.yaml 应已写入")
	}
}
