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

// Scope 描述用户/角色可访问的节点分组范围。
type Scope struct {
	// Mode 为 global 或 restricted。
	Mode string `yaml:"mode" json:"mode"`
	// Groups 在 restricted 模式下生效，为允许访问的分组 ID 列表。
	Groups []string `yaml:"groups" json:"groups"`
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

// Role 定义一组权限点与默认资源范围。
type Role struct {
	Name        string   `yaml:"name" json:"name"`
	Builtin     bool     `yaml:"builtin" json:"builtin"`
	Description string   `yaml:"description" json:"description"`
	Permissions []string `yaml:"permissions" json:"permissions"`
	ScopeMode   string   `yaml:"scope_mode" json:"scope_mode"`
	ScopeGroups []string `yaml:"scope_groups" json:"scope_groups"`
	CreatedBy   string   `yaml:"created_by,omitempty" json:"created_by,omitempty"`
}

// RoleScope 返回角色的默认资源范围。
func (r Role) RoleScope() Scope {
	return Scope{Mode: r.ScopeMode, Groups: r.ScopeGroups}
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
		{Domain: "资产", Items: []Permission{{"assets:read", "查看资产台账"}, {"assets:write", "维护资产属性"}}},
		{Domain: "安全中心", Items: []Permission{{"security:read", "查看"}, {"security:write", "操作"}, {"agent:secret:read", "查看 Agent 密钥"}}},
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
				"middleware:read", "middleware:write", "assets:read", "assets:write", "probe:read", "probe:write",
				// 集中日志（C2）：运维是排查问题的人，默认给全局运维角色；
				// 刻意不给只读/告警/安全/审计角色——日志内容可能含敏感数据，按需单独授予。
				"logs:read",
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
				"dashboard:read", "nodes:read", "groups:read", "middleware:read", "assets:read",
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
