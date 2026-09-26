package auth

import "testing"

// TestPermissionCatalogContainsAddedKeys 新增权限点必须出现在目录中（目录是 /api/v1/permissions/catalog 的唯一来源）。
func TestPermissionCatalogContainsAddedKeys(t *testing.T) {
	keys := AllPermissionKeys()
	for _, k := range []string{"dashboard:write", "system:config"} {
		if _, ok := keys[k]; !ok {
			t.Fatalf("权限点目录缺少 %s", k)
		}
	}
}

// TestPermissionCatalogKeysUnique 目录内不得出现重复权限点（重复会让前端勾选状态产生歧义）。
func TestPermissionCatalogKeysUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range PermissionCatalog() {
		for _, it := range d.Items {
			if seen[it.Key] {
				t.Fatalf("权限点重复: %s", it.Key)
			}
			seen[it.Key] = true
			if it.Description == "" {
				t.Fatalf("权限点 %s 缺少说明", it.Key)
			}
		}
	}
}

// TestBuiltinRolesUseValidKeys 内置角色的权限点必须都在目录内，避免拼写错误导致权限静默失效。
func TestBuiltinRolesUseValidKeys(t *testing.T) {
	keys := AllPermissionKeys()
	for _, r := range BuiltinRoles() {
		if len(r.Permissions) == 0 {
			t.Fatalf("内置角色 %s 未授予任何权限点", r.Name)
		}
		for _, p := range r.Permissions {
			if _, ok := keys[p]; !ok {
				t.Fatalf("内置角色 %s 引用了目录中不存在的权限点: %s", r.Name, p)
			}
		}
	}
}

// TestBuiltinRolesCoverAddedKeys 新增权限点必须有内置角色可用：
// 超级管理员走 allKeys() 自动覆盖；运维管理员需显式补齐，否则新权限无角色可选。
func TestBuiltinRolesCoverAddedKeys(t *testing.T) {
	want := []string{"dashboard:write", "system:config"}

	byName := map[string]Role{}
	for _, r := range BuiltinRoles() {
		byName[r.Name] = r
	}

	super, ok := byName[RoleSuperAdmin]
	if !ok {
		t.Fatal("缺少超级管理员角色")
	}
	ops, ok := byName[RoleOpsAdmin]
	if !ok {
		t.Fatal("缺少运维管理员角色")
	}

	has := func(r Role, perm string) bool {
		for _, p := range r.Permissions {
			if p == perm {
				return true
			}
		}
		return false
	}
	for _, perm := range want {
		if !has(super, perm) {
			t.Errorf("超级管理员缺少新增权限点 %s", perm)
		}
		if !has(ops, perm) {
			t.Errorf("运维管理员缺少新增权限点 %s", perm)
		}
	}
}
