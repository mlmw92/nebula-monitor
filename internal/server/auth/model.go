// Package auth 实现多用户角色权限管理（RBAC + 资源范围）。
//
// 设计要点：
//   - 用户（User）绑定一个或多个角色（Role），并拥有资源范围（Scope）。
//   - 角色聚合一组权限点（Permission，形如 domain:action）与默认资源范围。
//   - 资源范围（Scope）按节点分组（Group）限制用户可访问的节点；
//     Mode=restricted 且 Groups 为空表示「无任何资源权限」，绝不扩大为全部资源。
//   - 内置角色（Builtin=true）不可删除、权限不可改写；可复制为自定义角色。
//
// 本包不依赖具体存储与 HTTP 框架，仅提供纯数据模型与校验规则。
package auth

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// 资源范围模式。
const (
	// ScopeGlobal 表示可访问全部资源（节点分组）。
	ScopeGlobal = "global"
	// ScopeRestricted 表示仅可访问 Scope.Groups 中列出的节点分组。
	ScopeRestricted = "restricted"
)

// 用户状态。
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// 内置角色名。
const (
	RoleSuperAdmin  = "超级管理员"
	RoleOpsAdmin    = "运维管理员"
	RoleAlertAdmin  = "告警管理员"
	RoleSecurityAdm = "安全管理员"
	RoleReadOnly    = "只读用户"
	RoleAuditor     = "审计用户"
)

// ErrInvalid 表示通用校验错误。
var ErrInvalid = errors.New("参数不合法")

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

// Scope 描述用户/角色可访问的范围：**节点分组**与**业务标签**两个维度，取交集。
type Scope struct {
	// Mode 为 global 或 restricted。
	Mode string `yaml:"mode" json:"mode"`
	// Groups 在 restricted 模式下生效，为允许访问的分组 ID 列表。
	Groups []string `yaml:"groups" json:"groups"`
	// AssetMode 是业务标签维度的模式：""（= all，该维度不生效）或 limited。
	AssetMode string `yaml:"asset_mode,omitempty" json:"asset_mode,omitempty"`
	// AssetLabels 是 limited 模式下的标签选择器（维度**内**任一命中即可见）。
	AssetLabels []AssetScope `yaml:"asset_labels,omitempty" json:"asset_labels,omitempty"`
}

// 业务范围（资产标签维度）的模式。
//
// 为什么要有显式模式，而不是"有选择器就生效、没有就不生效"：那样无法区分
// 「该维度不生效」（不看标签，只看节点分组）与「限定了但一个选择器都没配」
// （**什么资产都看不到**）——这两者的授权含义相反。节点分组维度用的是同一套形状
// （restricted + 空 = 无权限，绝不放大为全部），这里逐字同构，运维只需理解一次。
const (
	// AssetScopeAll 表示业务维度不生效。零值即此，因此**存量配置行为不变**。
	AssetScopeAll = "all"
	// AssetScopeLimited 表示仅 AssetLabels 命中的资产可见（与节点维度取交集）。
	AssetScopeLimited = "limited"
)

// DefaultScopeLabelKey 是业务范围默认使用的资产标签键。
const DefaultScopeLabelKey = "biz"

// MaxAssetScopeValueLen 是选择器值的长度上限（标签值都不长，超长多半是粘错了东西）。
const MaxAssetScopeValueLen = 64

// AssetScope 是业务范围的一个标签选择器：资产带 `Key=Value` 标签即属于该范围。
//
// **只认一个约定键**（DefaultScopeLabelKey 或部署方配置的那个）：任意标签键都能进授权范围的话，
// "随手给某台机器打个标签"会意外变成授权开关。选择器**自带键名**，所以配置改了键之后，
// 旧授权只会匹配不到任何资产（fail-closed），不会被静默解释成新键的含义。
type AssetScope struct {
	Key   string `yaml:"key" json:"key"`
	Value string `yaml:"value" json:"value"`
}

// IsGlobal 是否全局范围。
func (s Scope) IsGlobal() bool { return s.Mode == ScopeGlobal }

// ContainsGroup 判断给定分组是否在范围内。
// global 模式始终返回 true；restricted 模式需 Groups 命中。
func (s Scope) ContainsGroup(group string) bool {
	if s.Mode == ScopeGlobal {
		return true
	}
	for _, g := range s.Groups {
		if g == group {
			return true
		}
	}
	return false
}

// AssetModeNormalized 返回规范化后的业务维度模式：空值按 all 处理（兼容存量配置）。
func (s Scope) AssetModeNormalized() string {
	if s.AssetMode == "" {
		return AssetScopeAll
	}
	return s.AssetMode
}

// LimitsAssets 表示业务标签维度是否生效。
//
// **调用方必须用它区分两种"没有选择器"**：不生效（不看标签）与"限定了却没有选择器"
// （无任何可见资产）。把后者当成前者就等于放大权限——务必配 `AssetSelectors()` 一起读。
func (s Scope) LimitsAssets() bool { return s.AssetModeNormalized() == AssetScopeLimited }

// AssetSelectors 返回生效的标签选择器。
//
// 它在 "limited 但一个都没配" 时返回空切片——**那不是"不限"**，是无权限；
// 判断维度是否生效请用 LimitsAssets()。
func (s Scope) AssetSelectors() []AssetScope {
	out := make([]AssetScope, 0, len(s.AssetLabels))
	for _, sel := range s.AssetLabels {
		key, value := strings.TrimSpace(sel.Key), strings.TrimSpace(sel.Value)
		if key == "" || value == "" {
			continue // 残缺选择器由写入口拦下；这里再兜一次，绝不让它变成"通配"
		}
		out = append(out, AssetScope{Key: key, Value: value})
	}
	return out
}

// NormalizeScopeLabelKey 规范化约定的业务范围标签键（空值回落到默认键）。
func NormalizeScopeLabelKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultScopeLabelKey
	}
	return raw
}

// ValidateAssetScope 校验业务维度的取值。
//
// allowedKey 是当前约定的标签键（空表示用默认值）。规则：
//   - 模式只能是 ""（等价 all）/ all / limited；
//   - limited 下**允许一个选择器都没有**（语义是"无可见资产"，与节点维度 restricted+空 同构）；
//   - 非 limited 模式**不允许**带选择器——那是自相矛盾的配置（配了标签却声明该维度不生效），
//     静默忽略它会让"我明明配了业务范围"变成一句谎话，直接拒绝更清楚；
//   - 每个选择器的键必须等于约定键，值不得为空、不长于上限、不含换行制表符。
func ValidateAssetScope(mode string, labels []AssetScope, allowedKey string) error {
	switch mode {
	case "", AssetScopeAll, AssetScopeLimited:
	default:
		return errors.New("业务范围模式非法（只能是 all 或 limited）")
	}
	if len(labels) > 0 && mode != AssetScopeLimited {
		return errors.New("配置了业务范围标签，但模式不是 limited（要么改成 limited，要么清掉标签）")
	}
	want := NormalizeScopeLabelKey(allowedKey)
	for i, sel := range labels {
		if key := strings.TrimSpace(sel.Key); key != want {
			return fmt.Errorf("业务范围第 %d 个选择器的标签键必须是 %q（当前只认这一个约定键）", i+1, want)
		}
		value := strings.TrimSpace(sel.Value)
		if value == "" {
			return fmt.Errorf("业务范围第 %d 个选择器的标签值不能为空", i+1)
		}
		if len([]rune(value)) > MaxAssetScopeValueLen {
			return fmt.Errorf("业务范围标签值过长（上限 %d 字符）", MaxAssetScopeValueLen)
		}
		if strings.ContainsAny(value, "\r\n\t") {
			return fmt.Errorf("业务范围标签值不能包含换行或制表符")
		}
	}
	return nil
}

// ScopeCovers 判断 outer 是否覆盖 inner，用于「自定义角色/用户的范围不得超过操作者自身范围」。
//
// 返回 nil 表示覆盖。两个维度分别判：节点分组（global 覆盖一切，否则 inner 的每个分组都要在 outer 里）
// 与业务标签（outer 不生效则覆盖一切；outer 限定则 inner 必须也限定，且选择器是 outer 的子集）。
//
// 为什么必须有这条：范围是**收窄**工具，如果被授权者能创建/修改出比他自己更宽的范围，
// 那就等于他能给自己提权（节点维度此前只在创建时校验，更新路径同样要过这里）。
func ScopeCovers(outer, inner Scope) error {
	if !outer.IsGlobal() {
		for _, g := range inner.Groups {
			if !outer.ContainsGroup(g) {
				return errors.New("资源范围不得超过操作者自身的范围（节点分组）")
			}
		}
	}
	if outer.LimitsAssets() {
		if !inner.LimitsAssets() {
			return errors.New("操作者自身受限的业务范围下，不能授予不受业务限制的范围")
		}
		allowed := make(map[string]struct{}, len(outer.AssetSelectors()))
		for _, sel := range outer.AssetSelectors() {
			allowed[sel.Key+"\x00"+sel.Value] = struct{}{}
		}
		for _, sel := range inner.AssetSelectors() {
			if _, ok := allowed[sel.Key+"\x00"+sel.Value]; !ok {
				return fmt.Errorf("业务范围 %s=%s 超出操作者自身的业务范围", sel.Key, sel.Value)
			}
		}
	}
	return nil
}

// Role 定义一组权限点与默认资源范围。
type Role struct {
	Name        string   `yaml:"name" json:"name"`
	Builtin     bool     `yaml:"builtin" json:"builtin"`
	Description string   `yaml:"description" json:"description"`
	Permissions []string `yaml:"permissions" json:"permissions"`
	ScopeMode   string   `yaml:"scope_mode" json:"scope_mode"`
	ScopeGroups []string `yaml:"scope_groups" json:"scope_groups"`
	// 业务标签维度（可选）。与 Scope 的同名字段语义一致：空模式 = 不生效。
	AssetMode   string       `yaml:"asset_mode,omitempty" json:"asset_mode,omitempty"`
	AssetLabels []AssetScope `yaml:"asset_labels,omitempty" json:"asset_labels,omitempty"`
	CreatedBy   string       `yaml:"created_by,omitempty" json:"created_by,omitempty"`
}

// RoleScope 返回角色的默认资源范围。
func (r Role) RoleScope() Scope {
	return Scope{
		Mode:        r.ScopeMode,
		Groups:      r.ScopeGroups,
		AssetMode:   r.AssetMode,
		AssetLabels: r.AssetLabels,
	}
}

// User 表示一个可登录主体。
type User struct {
	Username     string     `yaml:"username" json:"username"`
	DisplayName  string     `yaml:"display_name" json:"display_name"`
	PasswordHash string     `yaml:"password_hash" json:"-"`
	Roles        []string   `yaml:"roles" json:"roles"`
	Scope        Scope      `yaml:"scope" json:"scope"`
	Status       string     `yaml:"status" json:"status"`
	TokenVersion int64      `yaml:"token_version" json:"token_version"`
	CreatedBy    string     `yaml:"created_by" json:"created_by"`
	CreatedAt    time.Time  `yaml:"created_at" json:"created_at"`
	LastLoginAt  *time.Time `yaml:"last_login_at,omitempty" json:"last_login_at,omitempty"`
	LastLoginIP  string     `yaml:"last_login_ip,omitempty" json:"last_login_ip,omitempty"`
}

// Principal 是请求上下文中的已认证身份（含展开后的权限与范围）。
type Principal struct {
	Username     string
	DisplayName  string
	Roles        []string
	Permissions  map[string]struct{} // 权限点集合（O(1) 查询）
	Scope        Scope
	TokenVersion int64
}

// HasPermission 判断是否拥有某权限点。
func (p *Principal) HasPermission(perm string) bool {
	if p == nil {
		return false
	}
	_, ok := p.Permissions[perm]
	return ok
}

// CanAccessGroup 判断能否访问某节点分组。
func (p *Principal) CanAccessGroup(group string) bool {
	if p == nil {
		return false
	}
	return p.Scope.ContainsGroup(group)
}

// ValidateUser 校验用户基本字段（不含密码强度，密码强度在创建/重置处校验）。
func ValidateUser(u User) error {
	if !usernameRe.MatchString(u.Username) {
		return errors.New("用户名仅允许字母、数字、下划线、点、连字符，长度 3-32")
	}
	if len(u.Username) < 3 || len(u.Username) > 32 {
		return errors.New("用户名长度需为 3-32")
	}
	if len(u.Roles) == 0 {
		return errors.New("用户至少需绑定一个角色")
	}
	if u.Status != StatusEnabled && u.Status != StatusDisabled {
		return errors.New("用户状态非法")
	}
	if u.Scope.Mode != ScopeGlobal && u.Scope.Mode != ScopeRestricted {
		return errors.New("资源范围模式非法")
	}
	return nil
}

// ValidatePassword 校验密码强度：至少 8 位，含两类以上字符。
func ValidatePassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("密码至少 8 位")
	}
	var hasLower, hasUpper, hasDigit, hasSpecial bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	classes := 0
	for _, v := range []bool{hasLower, hasUpper, hasDigit, hasSpecial} {
		if v {
			classes++
		}
	}
	if classes < 2 {
		return errors.New("密码需至少包含大小写字母、数字、特殊字符中的两类")
	}
	return nil
}

// permissionCatalog 为全部权限点（按域分组），供前端目录与校验使用。
type permDomain struct {
	Domain string
	Items  []Permission
}

// Permission 为一个权限点及其人类可读说明。
type Permission struct {
	Key         string `json:"key"`
	Description string `json:"description"`
}

// PermissionCatalog 返回全部权限点（按业务域分组）。
func PermissionCatalog() []permDomain {
	return []permDomain{
		{Domain: "仪表盘", Items: []Permission{{"dashboard:read", "查看概览"}, {"dashboard:write", "管理自定义仪表盘"}}},
		{Domain: "主机 / 节点", Items: []Permission{{"nodes:read", "查看主机"}, {"nodes:write", "管理主机"}}},
		{Domain: "节点分组", Items: []Permission{{"groups:read", "查看分组"}, {"groups:write", "管理分组"}}},
		{Domain: "告警", Items: []Permission{{"alerts:read", "查看规则"}, {"alerts:write", "管理规则"}}},
		{Domain: "通知", Items: []Permission{{"notify:read", "查看渠道"}, {"notify:write", "管理渠道"}}},
		{Domain: "静默 / 维护", Items: []Permission{{"silence:read", "查看"}, {"silence:write", "管理"}}},
		{Domain: "拨测", Items: []Permission{{"probe:read", "查看"}, {"probe:write", "管理"}}},
		{Domain: "报告", Items: []Permission{{"report:read", "查看"}, {"report:export", "导出"}}},
		{Domain: "数据导出", Items: []Permission{{"metrics:export", "导出指标"}}},
		{Domain: "中间件", Items: []Permission{{"middleware:read", "查看"}, {"middleware:write", "管理"}}},
		// 资产台账：读看台账与变更历史；写指人工维护资产名称与属性（采集值不会被覆盖）。
		// 配置巡检单独设权限点：读为看巡检记录与差异项，跑为触发一次巡检（只读结论、不改配置）。
		{Domain: "资产", Items: []Permission{
			{"assets:read", "查看资产台账"},
			{"assets:write", "维护资产属性"},
			// 导出是**整份台账落盘**：一次拿走全部资产与责任人，与「逐页翻看」不是一个量级的动作，
			// 因此单设权限点（与 audit:export / metrics:export 同一约定）。
			{"assets:export", "导出资产清单"},
			{"inspect:read", "查看配置巡检"},
			{"inspect:run", "执行配置巡检"},
		}},
		{Domain: "安全中心", Items: []Permission{{"security:read", "查看"}, {"security:write", "操作"}, {"agent:secret:read", "查看 Agent 密钥"}}},
		// 下行操作：读看动作目录与任务状态；执行指下发动作到指定节点（属高风险）。
		// 执行成功与否还取决于目标机器自己的 guards.ops 放行情况——权限只是四道护栏之一。
		{Domain: "节点操作", Items: []Permission{
			{"ops:read", "查看操作任务"},
			{"ops:exec", "下发操作任务（高风险）"},
		}},
		// 容器只读管理面：集群清单 / 工作负载 / Pod / 事件 / 对象详情。
		// 与 middleware:read 分开，是因为它读的是**集群内部对象**（工作负载、事件里带镜像与节点名
		// 等运行细节），与"看指标曲线"不是一个信息面；下发容器动作仍走 ops:exec。
		{Domain: "容器", Items: []Permission{{"container:read", "查看容器与工作负载"}}},
		// 日志内容可能含密码/个人信息/业务数据，因此单独设权限点：不默认给只读角色，按需授予
		{Domain: "集中日志", Items: []Permission{{"logs:read", "查看集中日志"}}},
		{Domain: "Agent", Items: []Permission{{"agent:read", "查看"}, {"agent:upgrade", "升级"}}},
		{Domain: "系统", Items: []Permission{
			{"system:upgrade", "系统升级"},
			{"system:config", "系统配置（IP 地理库 / 大屏 / 品牌）"},
			{"audit:read", "查看审计"},
			{"audit:export", "导出审计"},
			{"users:manage", "用户管理"},
			{"roles:manage", "角色管理"},
			{"roles:read", "查看角色与权限目录"},
		}},
	}
}

// AllPermissionKeys 返回全部权限点 key（用于校验角色权限合法性）。
func AllPermissionKeys() map[string]struct{} {
	m := make(map[string]struct{})
	for _, d := range PermissionCatalog() {
		for _, it := range d.Items {
			m[it.Key] = struct{}{}
		}
	}
	return m
}

// BuiltinRoles 返回内置角色定义。
func BuiltinRoles() []Role {
	return []Role{
		{
			Name: RoleSuperAdmin, Builtin: true,
			Description: "全部权限，可管理用户、角色、系统配置与审计",
			ScopeMode:   ScopeGlobal,
			Permissions: allKeys(),
		},
		{
			Name: RoleOpsAdmin, Builtin: true,
			Description: "主机、分组、Agent、中间件、拨测、报告与告警运维；不可管理权限模型",
			ScopeMode:   ScopeGlobal,
			Permissions: []string{
				"dashboard:read", "dashboard:write", "nodes:read", "nodes:write", "groups:read", "groups:write",
				"middleware:read", "middleware:write", "assets:read", "assets:write", "assets:export",
				"inspect:read", "inspect:run", "probe:read", "probe:write",
				// 集中日志（C2）：运维是排查问题的人，默认给全局运维角色；
				// 刻意不给只读/告警/安全/审计角色——日志内容可能含敏感数据，按需单独授予。
				"logs:read",
				// 下行操作：运维是执行动作的人，默认给全局运维角色（与 assets:write 同一取舍）。
				// 这不等于"能随便改机器"——目标机器自己的 guards.ops 还要再放行一次，
				// 而写动作默认是被那台机器拒绝的。
				"ops:read", "ops:exec",
				// 容器只读管理面：与 ops 同一取舍——运维是排障的人，默认给全局运维角色；
				// 刻意不给只读/告警/安全角色：它读的是集群内部对象（工作负载、事件里的镜像与节点名）。
				"container:read",
				"report:read", "report:export", "metrics:export", "agent:read", "agent:upgrade",
				"alerts:read", "roles:read", "system:config",
			},
		},
		{
			Name: RoleAlertAdmin, Builtin: true,
			Description: "告警规则、通知、静默、维护窗口",
			ScopeMode:   ScopeGlobal,
			Permissions: []string{
				"dashboard:read", "alerts:read", "alerts:write",
				"notify:read", "notify:write", "silence:read", "silence:write",
				// 告警页面与规则编辑依赖节点/分组名称（分组筛选器、规则目标选择）
				"nodes:read", "groups:read",
				"roles:read",
			},
		},
		{
			Name: RoleSecurityAdm, Builtin: true,
			Description: "安全中心、入侵防御与审计查看",
			ScopeMode:   ScopeGlobal,
			Permissions: []string{
				"dashboard:read", "security:read", "security:write", "agent:read",
				"agent:secret:read", "audit:read", "audit:export", "roles:read",
			},
		},
		{
			Name: RoleReadOnly, Builtin: true,
			Description: "仪表盘、主机、中间件、告警与报告只读",
			ScopeMode:   ScopeRestricted,
			Permissions: []string{
				"dashboard:read", "nodes:read", "groups:read", "middleware:read", "assets:read", "inspect:read",
				"alerts:read", "report:read", "roles:read",
			},
		},
		{
			Name: RoleAuditor, Builtin: true,
			Description: "仅查看与导出审计记录",
			ScopeMode:   ScopeGlobal,
			Permissions: []string{"audit:read", "audit:export", "roles:read"},
		},
	}
}

func allKeys() []string {
	keys := make([]string, 0)
	for k := range AllPermissionKeys() {
		keys = append(keys, k)
	}
	return keys
}

// NormalizeRoleName 规范化角色名（去除首尾空格）。
func NormalizeRoleName(name string) string { return strings.TrimSpace(name) }
