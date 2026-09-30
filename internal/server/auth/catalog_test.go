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

// TestBuiltinRoles_AlertAdminCanReadNodes 告警管理员必须能读取节点与分组：
// 告警页面（分组筛选器）与规则编辑（目标节点选择）依赖这两个接口。
func TestBuiltinRoles_AlertAdminCanReadNodes(t *testing.T) {
	for _, r := range BuiltinRoles() {
		if r.Name != RoleAlertAdmin {
			continue
		}
		need := map[string]bool{"nodes:read": false, "groups:read": false}
		for _, p := range r.Permissions {
			if _, ok := need[p]; ok {
				need[p] = true
			}
		}
		for k, ok := range need {
			if !ok {
				t.Fatalf("告警管理员缺少 %s（告警页面与规则编辑依赖）", k)
			}
		}
		return
	}
	t.Fatal("缺少告警管理员角色")
}

// superAdminOnlyKeys 是**刻意只授予超级管理员**的权限点：用户与角色管理权本身不该下放，
// 否则被授予「运维管理员」的账号可以给自己加权限（自提权）。
// 新增例外必须写明理由，否则就退回"漏了也没人发现"的状态。
var superAdminOnlyKeys = map[string]bool{
	"users:manage": true,
	"roles:manage": true,
	// 系统升级会替换 Server 二进制（换来的是"升完起不来"这类不可自愈的故障），
	// 刻意只给超级管理员：它不是日常运维动作，而是"改平台自身"的动作。
	"system:upgrade": true,
}

// TestEveryCatalogKeyIsGrantedBySomeRole 目录里的每个权限点都必须至少被一个**非超级管理员**的
// 内置角色覆盖（例外见 superAdminOnlyKeys）。
//
// 为什么把这条从"手工清单"改成"遍历目录"：此前这里写的是
// `want := []string{"dashboard:write", "system:config"}`，每加一个新权限点都得记得回来补一行；
// 漏了不会报错，症状是**功能装了却没人看得到**——路由 meta 与侧边栏都按权限点门控，
// 而内置角色里根本没有它，于是只有超级管理员能用。`ops:read` / `ops:exec` 就是这样漏掉的
// （2026-09-30 在 dev-server 上实机验证时才发现）。
func TestEveryCatalogKeyIsGrantedBySomeRole(t *testing.T) {
	granted := map[string]bool{}
	for _, r := range BuiltinRoles() {
		if r.Name == RoleSuperAdmin {
			continue // 超级管理员走 allKeys() 覆盖全部，不能作为"有人能用"的证明
		}
		for _, p := range r.Permissions {
			granted[p] = true
		}
	}
	for _, d := range PermissionCatalog() {
		for _, it := range d.Items {
			if granted[it.Key] || superAdminOnlyKeys[it.Key] {
				continue
			}
			t.Errorf("权限点 %s（%s）没有被任何非超级管理员的内置角色覆盖：页面会被路由/菜单挡掉，"+
				"只有超级管理员能用（若确实只该给超级管理员，请加入 superAdminOnlyKeys 并写明理由）", it.Key, it.Description)
		}
	}
}

// TestBuiltinRolesCoverAddedKeys 新增权限点必须有内置角色可用：
// 超级管理员走 allKeys() 自动覆盖；运维管理员需显式补齐，否则新权限无角色可选。
func TestBuiltinRolesCoverAddedKeys(t *testing.T) {
	want := []string{"dashboard:write", "system:config", "ops:read", "ops:exec"}

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
