package api

import (
	"encoding/json"
	"net/http"

	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/audit"
)

// handleMe 返回当前登录用户的授权信息（角色、权限、资源范围）。
func (a *API) handleMe(w http.ResponseWriter, r *http.Request) {
	if a.authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未启用登录认证"})
		return
	}
	p := Principal(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已失效"})
		return
	}
	perms := make([]string, 0, len(p.Permissions))
	for k := range p.Permissions {
		perms = append(perms, k)
	}
	writeJSON(w, 200, map[string]interface{}{
		"username":     p.Username,
		"displayName":  p.DisplayName,
		"roles":        p.Roles,
		"permissions":  perms,
		"scope":        p.Scope,
	})
}

// handleUpdateMe 当前用户自助更新自身资料（仅允许修改昵称 displayName），
// 不涉及角色、范围、状态或密码（这些需 users:manage 由管理员操作）。登录即可调用。
func (a *API) handleUpdateMe(w http.ResponseWriter, r *http.Request) {
	if a.authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未启用登录认证"})
		return
	}
	p := Principal(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已失效"})
		return
	}
	var body struct {
		DisplayName *string `json:"displayName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	if body.DisplayName == nil {
		writeJSON(w, 400, map[string]string{"error": "未提供任何可修改字段"})
		return
	}
	if len([]rune(*body.DisplayName)) > 64 {
		writeJSON(w, 400, map[string]string{"error": "昵称过长（上限 64 字符）"})
		return
	}
	if err := a.authStore.UpdateUser(p.Username, auth.UserPatch{DisplayName: body.DisplayName}); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, p.Username, "self.update", p.Username)
	writeJSON(w, 200, map[string]interface{}{"ok": "true", "displayName": *body.DisplayName})
}

// handleListUsers 列出全部用户（不含密码哈希）。
func (a *API) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users := a.authStore.ListUsers()
	writeJSON(w, 200, map[string]interface{}{"users": users})
}

// handleGetUser 返回单个用户详情。
func (a *API) handleGetUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	u, ok := a.authStore.GetUser(name)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "用户不存在"})
		return
	}
	u.PasswordHash = ""
	writeJSON(w, 200, u)
}

// createUserRequest 创建用户请求体。
type createUserRequest struct {
	Username     string      `json:"username"`
	DisplayName  string      `json:"displayName"`
	Password     string      `json:"password"`
	Roles        []string    `json:"roles"`
	Scope        auth.Scope  `json:"scope"`
	Status       string      `json:"status"`
}

// handleCreateUser 创建用户。
func (a *API) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var body createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	if body.Username == "" || body.Password == "" {
		writeJSON(w, 400, map[string]string{"error": "用户名与密码不能为空"})
		return
	}
	if len(body.Roles) == 0 {
		writeJSON(w, 400, map[string]string{"error": "至少绑定一个角色"})
		return
	}
	operator := AuthenticatedUser(r)
	u := auth.User{
		Username:    body.Username,
		DisplayName: body.DisplayName,
		Roles:       body.Roles,
		Scope:       body.Scope,
		Status:      body.Status,
	}
	if err := a.authStore.CreateUser(u, body.Password, operator); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, operator, "user.create", body.Username)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// updateUserRequest 更新用户请求体（部分字段）。
type updateUserRequest struct {
	DisplayName *string     `json:"displayName"`
	Roles       []string    `json:"roles"`
	Scope       *auth.Scope `json:"scope"`
	Status      *string     `json:"status"`
}

// handleUpdateUser 更新用户资料。
func (a *API) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	var body updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	patch := auth.UserPatch{
		DisplayName: body.DisplayName,
		Roles:       body.Roles,
		Scope:       body.Scope,
		Status:      body.Status,
	}
	if err := a.authStore.UpdateUser(name, patch); err != nil {
		code := 400
		if contains(err.Error(), "最后") {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "user.update", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleResetUserPassword 重置用户密码（管理员）/ 修改自身密码（需旧密码，前端另走 change-password）。
func (a *API) handleResetUserPassword(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	var body struct {
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.NewPassword == "" {
		writeJSON(w, 400, map[string]string{"error": "新密码不能为空"})
		return
	}
	if err := a.authStore.ResetPassword(name, body.NewPassword); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "user.reset_password", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleDisableUser 禁用用户（会话失效）。
func (a *API) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	if err := a.authStore.DisableUser(name, AuthenticatedUser(r)); err != nil {
		code := 400
		if contains(err.Error(), "最后") {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "user.disable", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleEnableUser 启用用户。
func (a *API) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	if err := a.authStore.EnableUser(name, AuthenticatedUser(r)); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "user.enable", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleDeleteUser 删除用户。
func (a *API) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("username")
	if err := a.authStore.DeleteUser(name); err != nil {
		code := 400
		if contains(err.Error(), "最后") || contains(err.Error(), "绑定") {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "user.delete", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleListRoles 列出全部角色（内置优先）。
func (a *API) handleListRoles(w http.ResponseWriter, r *http.Request) {
	roles := a.authStore.ListRoles()
	// 计算各角色关联用户数。
	counts := make(map[string]int)
	for _, u := range a.authStore.ListUsers() {
		for _, rn := range u.Roles {
			counts[rn]++
		}
	}
	type roleView struct {
		auth.Role
		UserCount int `json:"userCount"`
	}
	out := make([]roleView, 0, len(roles))
	for _, rl := range roles {
		out = append(out, roleView{Role: rl, UserCount: counts[rl.Name]})
	}
	writeJSON(w, 200, map[string]interface{}{"roles": out})
}

// handleGetRole 返回角色详情（权限矩阵 + 范围 + 关联用户数）。
func (a *API) handleGetRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	rl, ok := a.authStore.LookupRole(name)
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "角色不存在"})
		return
	}
	count := 0
	for _, u := range a.authStore.ListUsers() {
		for _, rn := range u.Roles {
			if rn == name {
				count++
				break
			}
		}
	}
	writeJSON(w, 200, map[string]interface{}{"role": rl, "userCount": count})
}

// createRoleRequest 创建角色请求体。
type createRoleRequest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Permissions []string   `json:"permissions"`
	ScopeMode   string     `json:"scopeMode"`
	ScopeGroups []string   `json:"scopeGroups"`
}

// handleCreateRole 创建自定义角色。
func (a *API) handleCreateRole(w http.ResponseWriter, r *http.Request) {
	var body createRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	p := Principal(r)
	operatorScope := auth.Scope{}
	if p != nil {
		operatorScope = p.Scope
	}
	rl := auth.Role{
		Name:        body.Name,
		Description: body.Description,
		Permissions: body.Permissions,
		ScopeMode:   body.ScopeMode,
		ScopeGroups: body.ScopeGroups,
	}
	if err := a.authStore.CreateRole(rl, AuthenticatedUser(r), operatorScope); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "role.create", body.Name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// updateRoleRequest 更新角色请求体。
type updateRoleRequest struct {
	Description  *string   `json:"description"`
	Permissions  []string  `json:"permissions"`
	ScopeMode    *string   `json:"scopeMode"`
	ScopeGroups  []string  `json:"scopeGroups"`
}

// handleUpdateRole 更新自定义角色。
func (a *API) handleUpdateRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body updateRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	patch := auth.RolePatch{
		Description: body.Description,
		Permissions: body.Permissions,
		ScopeMode:   body.ScopeMode,
		ScopeGroups: body.ScopeGroups,
	}
	if err := a.authStore.UpdateRole(name, patch); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "role.update", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleDeleteRole 删除自定义角色。
func (a *API) handleDeleteRole(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := a.authStore.DeleteRole(name); err != nil {
		code := 400
		if contains(err.Error(), "绑定") || contains(err.Error(), "内置") {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	a.recordAuthAudit(r, AuthenticatedUser(r), "role.delete", name)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handlePermissionCatalog 返回全部权限点（按业务域分组）。
func (a *API) handlePermissionCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"domains": auth.PermissionCatalog()})
}

// recordAuthAudit 记录权限模型相关管理操作。
func (a *API) recordAuthAudit(r *http.Request, operator, action, target string) {
	if a.audit == nil {
		return
	}
	_ = a.audit.Record(audit.Event{
		User:      operator,
		Method:    r.Method,
		Path:      r.URL.Path,
		Status:    http.StatusOK,
		RemoteIP:  audit.ClientIP(r),
		Succeeded: true,
		Category:  "auth",
		Action:    action,
		Detail:    "target=" + target,
	})
}

// contains 简单子串判断。
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOfSub(s, sub) >= 0)
}

func indexOfSub(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
