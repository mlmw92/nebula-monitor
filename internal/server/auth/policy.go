package auth

// Policy 负责将用户与角色展开为可快速判断的授权上下文，并提供资源范围校验。
//
// 设计原则：业务 handler 不解析角色内部结构，只调用本包的 Principal / 辅助函数，
// 权限判断集中在统一模块，便于测试与演进。

// ExpandPrincipal 根据用户与其角色列表展开出 Principal（权限并集 + 范围并集）。
// 角色不存在时忽略；内置角色权限固定，自定义角色权限取自 roleStore。
func ExpandPrincipal(u User, roleLookup func(name string) (Role, bool)) *Principal {
	perms := make(map[string]struct{})
	scopeGlobal := false
	groupSet := make(map[string]struct{})
	roleNames := make([]string, 0, len(u.Roles))

	for _, rn := range u.Roles {
		roleNames = append(roleNames, rn)
		role, ok := roleLookup(rn)
		if !ok {
			continue
		}
		for _, p := range role.Permissions {
			perms[p] = struct{}{}
		}
		rs := role.RoleScope()
		if rs.IsGlobal() {
			scopeGlobal = true
		} else {
			for _, g := range rs.Groups {
				groupSet[g] = struct{}{}
			}
		}
	}

	// 用户自身 Scope 进一步收窄/扩大：若任一角色为 global 则用户为 global；
	// 否则取「角色范围 ∪ 用户范围」，但用户范围不得超过其角色允许范围（已由创建约束保证）。
	if !scopeGlobal {
		if u.Scope.IsGlobal() {
			// 仅当具备全局角色时才允许；此处用户显式 global 但角色无 global 理论上不应出现，
			// 为健壮性仍按角色并集处理，不自动提升为 global。
			for _, g := range u.Scope.Groups {
				groupSet[g] = struct{}{}
			}
		} else {
			for _, g := range u.Scope.Groups {
				groupSet[g] = struct{}{}
			}
		}
	}

	scope := Scope{Mode: ScopeRestricted, Groups: nil}
	if scopeGlobal {
		scope.Mode = ScopeGlobal
	} else {
		for g := range groupSet {
			scope.Groups = append(scope.Groups, g)
		}
	}

	return &Principal{
		Username:     u.Username,
		DisplayName:  u.DisplayName,
		Roles:        roleNames,
		Permissions:  perms,
		Scope:        scope,
		TokenVersion: u.TokenVersion,
	}
}

// HighRiskPermissions 为需要二次确认 + 审计的高风险权限点。
var HighRiskPermissions = map[string]struct{}{
	"system:upgrade":     {},
	"agent:secret:read":  {},
	"agent:upgrade":      {},
	"security:write":     {},
	"notify:write":       {},
	"users:manage":       {},
	"roles:manage":       {},
	"audit:export":       {},
}

// IsHighRisk 判断权限点是否属于高风险操作。
func IsHighRisk(perm string) bool {
	_, ok := HighRiskPermissions[perm]
	return ok
}

// FilterGroups 返回 groups 中用户可访问的子集（服务端范围过滤）。
func FilterGroups(p *Principal, groups []string) []string {
	if p == nil {
		return nil
	}
	if p.Scope.IsGlobal() {
		return groups
	}
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if p.Scope.ContainsGroup(g) {
			out = append(out, g)
		}
	}
	return out
}

// FilterByGroup 对带分组标签的项做范围过滤。
// items 为任意类型，groupOf 返回其分组；返回可见项。
func FilterByGroup[T any](p *Principal, items []T, groupOf func(T) string) []T {
	if p == nil {
		return nil
	}
	if p.Scope.IsGlobal() {
		return items
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		if p.Scope.ContainsGroup(groupOf(it)) {
			out = append(out, it)
		}
	}
	return out
}

// CheckBatchGroups 批量校验分组范围，返回被拒绝的分组列表。
func CheckBatchGroups(p *Principal, groups []string) []string {
	if p == nil {
		return groups
	}
	denied := make([]string, 0)
	for _, g := range groups {
		if !p.CanAccessGroup(g) {
			denied = append(denied, g)
		}
	}
	return denied
}
