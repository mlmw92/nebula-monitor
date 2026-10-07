package auth

import "sort"

// Policy 负责将用户与角色展开为可快速判断的授权上下文，并提供资源范围校验。
//
// 设计原则：业务 handler 不解析角色内部结构，只调用本包的 Principal / 辅助函数，
// 权限判断集中在统一模块，便于测试与演进。

// ExpandPrincipal 根据用户与其角色列表展开出 Principal（权限并集 + 范围并集）。
// 角色不存在时忽略；内置角色权限固定，自定义角色权限取自 roleStore。
//
// 超级管理员兜底：只要绑定内置超级管理员角色，权限恒为权限目录全量——
// 不依赖该角色在 store 中的展开结果，避免任何存储层异常（同名自定义角色
// 残留、旧文件快照缺新权限点等）导致超管被 403。
func ExpandPrincipal(u User, roleLookup func(name string) (Role, bool)) *Principal {
	perms := make(map[string]struct{})
	scopeGlobal := false
	groupSet := make(map[string]struct{})
	// 业务标签维度的合并与节点维度**同构**：任一角色不限制该维度 ⇒ 该维度对这个人不生效；
	// 否则把所有（角色 + 用户自身）的选择器并起来（维度内取并集，与节点分组并集同一口径）。
	//
	// 注意：**维度是否生效只看角色与用户自己的 AssetMode，与节点维度是否 global 无关**。
	// 两个维度是独立的：全局节点范围 + 受限业务范围 = 能看所有机器，但只看得了带该标签的资产。
	// 内置角色都不设 AssetMode（= all），因此**存量账号行为逐字不变**。
	roleUnlimited := false // 有角色声明「该维度不限」
	roleLimited := false   // 有角色声明「限定」
	assetSel := make(map[string]AssetScope)
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
		if role.Name == RoleSuperAdmin {
			for p := range AllPermissionKeys() {
				perms[p] = struct{}{}
			}
		}
		rs := role.RoleScope()
		if rs.IsGlobal() {
			scopeGlobal = true
		} else {
			for _, g := range rs.Groups {
				groupSet[g] = struct{}{}
			}
		}
		if !rs.LimitsAssets() {
			roleUnlimited = true
		} else {
			roleLimited = true
			for _, sel := range rs.AssetSelectors() {
				assetSel[sel.Key+"\x00"+sel.Value] = sel
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
	// 用户自身的业务范围（"给这个人单独收窄"的那个字段）：
	//   · 他要是自己配了限定 → **维度生效**，不可能被角色的"不限"静默抵消。
	//     否则这个字段就是个摆设，而且失败方向是"权限比配置看起来更大"。
	//   · 他没配（普通用户 scope 的 AssetMode 默认就是空）→ 不参与判断，
	//     免得普通用户的默认值把角色强加的限制关掉（那是同一个失败方向的另一面）。
	userLimited := u.Scope.LimitsAssets()
	if userLimited {
		for _, sel := range u.Scope.AssetSelectors() {
			assetSel[sel.Key+"\x00"+sel.Value] = sel
		}
	}
	// 维度是否生效：用户明确配了 → 生效；否则与节点维度同构（所有角色都限定才生效，
	// 任一角色说"不限"就等于这个人这一维不受限）。
	assetOn := userLimited || (roleLimited && !roleUnlimited)

	scope := Scope{Mode: ScopeRestricted, Groups: nil}
	if scopeGlobal {
		scope.Mode = ScopeGlobal
	} else {
		for g := range groupSet {
			scope.Groups = append(scope.Groups, g)
		}
	}
	if assetOn {
		scope.AssetMode = AssetScopeLimited
		scope.AssetLabels = sortedAssetScopes(assetSel)
	} else {
		scope.AssetMode = AssetScopeAll
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

// sortedAssetScopes 把选择器 map 摊成**有序**切片。
//
// 顺序必须稳定：它会进 Principal.Scope、进 /auth/me 的响应、也进测试断言——
// 顺序抖动会让"同一个账号两次请求拿到的范围看起来不一样"，排查时白白多一层噪声。
func sortedAssetScopes(m map[string]AssetScope) []AssetScope {
	if len(m) == 0 {
		return nil
	}
	out := make([]AssetScope, 0, len(m))
	for _, sel := range m {
		out = append(out, sel)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// AssetScopeDecision 是业务标签维度折算出的判定结果，供资产类接口下推给存储层。
//
// 三态**必须显式区分**，否则"限定了却没有选择器"会被当成"不限"——那是放大权限：
//
//	Deny=true                   → 受限但没有任何有效选择器：无任何可见资产
//	Deny=false, Selectors=nil   → 该维度不生效（不按标签过滤）
//	Deny=false, Selectors 非空  → 必须按选择器过滤（维度内任一命中即可见）
type AssetScopeDecision struct {
	Deny      bool
	Selectors []AssetScope
}

// ResolveAssetScope 把身份的业务范围折算成判定结果。
//
// p 为 nil（未启用认证/单管理员模式）时不受限——与 assetAllowedNodes / nodeInScope 对
// "无 Principal 即全局"的既有取向一致，不能在这里变成 fail-closed（那会让未启用 RBAC 的部署
// 突然看不到任何资产）。
func ResolveAssetScope(p *Principal) AssetScopeDecision {
	if p == nil || !p.Scope.LimitsAssets() {
		return AssetScopeDecision{}
	}
	return AssetScopeDecision{Deny: len(p.Scope.AssetSelectors()) == 0, Selectors: p.Scope.AssetSelectors()}
}

// AssetAllowed 判断一条资产（按它的标签）是否在身份的业务范围内。
//
// 三态与 ResolveAssetScope 一致：未限制该维度 → 恒 true；限定了但没有任何选择器 → 恒 false
// （与节点维度 restricted+空 = 无权限同构）；否则至少一个选择器命中即通过（维度内取或）。
//
// 它是**单条资产**的判定，供"取到一条资产后再兜一次"的路径使用（列表侧走 store 的
// LabelSelectors 下推，两者口径必须一致：下推管分页与总数，这里管"即使下推算错也不泄露"）。
func AssetAllowed(p *Principal, labels map[string]string) bool {
	if p == nil || !p.Scope.LimitsAssets() {
		return true
	}
	selectors := p.Scope.AssetSelectors()
	if len(selectors) == 0 {
		return false
	}
	for _, sel := range selectors {
		if labels[sel.Key] == sel.Value {
			return true
		}
	}
	return false
}

// HighRiskPermissions 为需要二次确认 + 审计的高风险权限点。
var HighRiskPermissions = map[string]struct{}{
	"system:upgrade":    {},
	"agent:secret:read": {},
	"agent:upgrade":     {},
	"security:write":    {},
	"notify:write":      {},
	"users:manage":      {},
	"roles:manage":      {},
	"audit:export":      {},
	// 资产清单导出：一次把整份台账（含 IP、责任人、标签）落盘，敏感度与审计导出一档，
	// 因此同样纳入高风险（需要二次确认 + 审计留痕）。
	"assets:export": {},
	// 资产属性的人工维护会改变运维判断所依赖的台账数据（虽然不是执行动作），
	// 按设计纳入高风险：需要二次确认 + 审计留痕。
	"assets:write": {},
	// 下行操作任务的创建（ops.exec）：这条通道能在**一批机器上执行东西**，
	// 即便首批动作都是只读查询，它也是本项目风险面最大的一类权限。
	// 注意它只是四道护栏中的一道——目标机器自己还要在 agent.yaml 里放行。
	"ops:exec": {},
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
