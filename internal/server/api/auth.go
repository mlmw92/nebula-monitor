package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nebula/monitor/internal/server/auth"
	"github.com/nebula/monitor/internal/server/config"
	servercrypto "github.com/nebula/monitor/internal/server/crypto"
)

type authUserContextKey struct{}
type principalContextKey struct{}

// AuthenticatedUser 返回当前请求通过认证的用户名。
func AuthenticatedUser(r *http.Request) string {
	if v, ok := r.Context().Value(authUserContextKey{}).(string); ok {
		return v
	}
	return ""
}

// Principal 返回当前请求展开后的授权身份（含角色、权限、资源范围）。
// 未启用登录认证或匿名访问返回 nil。
func Principal(r *http.Request) *auth.Principal {
	if v, ok := r.Context().Value(principalContextKey{}).(*auth.Principal); ok {
		return v
	}
	return nil
}

// 登录失败限流：每个源 IP 在窗口内最多允许 loginLimitMax 次失败，超出返回 429。
var (
	// loginMu 保护登录限流状态的并发访问。
	loginMu sync.Mutex
	// loginFails 记录各来源 IP 的登录失败计数与窗口起始时间。
	loginFails = map[string]*loginAttempt{}
	// loginLimitMax 窗口内允许的登录失败最大次数，超出返回 429。
	loginLimitMax = 5
	// loginLimitWin 登录失败计数的滑动窗口时长。
	loginLimitWin = 5 * time.Minute
)

type loginAttempt struct {
	count int
	since time.Time
}

func loginAllowed(ip string) bool {
	loginMu.Lock()
	defer loginMu.Unlock()
	now := time.Now()
	a, ok := loginFails[ip]
	if !ok || now.Sub(a.since) > loginLimitWin {
		loginFails[ip] = &loginAttempt{count: 0, since: now}
		return true
	}
	return a.count < loginLimitMax
}

func loginFail(ip string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	if a, ok := loginFails[ip]; ok {
		a.count++
	} else {
		loginFails[ip] = &loginAttempt{count: 1, since: time.Now()}
	}
}

// clientIP 从 RemoteAddr 取出源 IP（去掉端口）。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// token 有效期
const tokenTTL = 24 * time.Hour

// 生成无状态 token：base64(username:tokenVersion:exp) + "." + hmacSig
// tokenVersion 用于用户禁用/改密后的即时失效。
func genToken(username string, tokenVersion int64, secret string) string {
	exp := time.Now().Add(tokenTTL).Unix()
	payload := username + ":" + itoa(tokenVersion) + ":" + itoa(exp)
	sig := hmacSign(secret, payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// 校验 token，返回 username 与 tokenVersion
func verifyToken(token, secret string) (string, int64, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", 0, false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(hmacSign(secret, payload)), []byte(parts[1])) {
		return "", 0, false
	}
	// payload = username:tokenVersion:exp
	first := strings.Index(payload, ":")
	last := strings.LastIndex(payload, ":")
	if first < 0 || last <= first {
		return "", 0, false
	}
	user := payload[:first]
	tv, err := atoi(payload[first+1 : last])
	if err != nil {
		return "", 0, false
	}
	exp, err := atoi(payload[last+1:])
	if err != nil || time.Now().Unix() > exp {
		return "", 0, false
	}
	return user, tv, true
}

func hmacSign(secret, data string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// 公开路径（无需登录 token）
func isPublicPath(path string) bool {
	if path == "/" || strings.HasPrefix(path, "/assets/") {
		return true
	}
	// 登录、Agent 上报（Agent 走 X-Agent-Secret 校验，不走登录 token）
	if strings.HasPrefix(path, "/api/v1/login") || strings.HasPrefix(path, "/api/v1/report") {
		return true
	}
	// Agent 安装脚本的接入鉴权预检（同样走 X-Agent-Secret，不受登录 token 影响）
	if strings.HasPrefix(path, "/api/v1/agent/check") {
		return true
	}
	if strings.HasPrefix(path, "/install/") || strings.HasPrefix(path, "/bin/") {
		return true
	}
	// WebSocket 不属于公开接口：认证开启时由本中间件校验 Cookie/Bearer Token。
	// 不能仅依赖 Origin 校验，因为 Origin 只解决跨站连接，不能证明用户已登录。
	return false
}

// AuthMiddleware 校验 token；未启用 auth 则全放行。
// authStore 显式传入（而非包级单例），避免多实例部署与测试之间的状态污染。
func AuthMiddleware(next http.Handler, authCfg config.AuthConfig, authStore *auth.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !authCfg.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 品牌配置：允许匿名只读（GET），写操作（PUT）仍需登录鉴权
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v1/ui/settings") {
			next.ServeHTTP(w, r)
			return
		}
		tok := r.Header.Get("Authorization")
		tok = strings.TrimPrefix(tok, "Bearer ")
		if tok == "" {
			if c, err := r.Cookie("nebula_token"); err == nil {
				tok = c.Value
			}
		}
		user, tv, ok := verifyToken(tok, authCfg.Secret)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "未登录或登录已过期"})
			return
		}
		// 展开授权身份（多用户模式）：校验用户启用状态与会话版本，实现即时失效。
		if authStore != nil {
			p := authStore.GetPrincipal(user)
			if p == nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "账号已禁用或不存在"})
				return
			}
			if p.TokenVersion != tv {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "会话已失效，请重新登录"})
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), principalContextKey{}, p))
			r = r.WithContext(context.WithValue(r.Context(), authUserContextKey{}, user))
		} else {
			r = r.WithContext(context.WithValue(r.Context(), authUserContextKey{}, user))
		}
		next.ServeHTTP(w, r)
	})
}

// checkPerm 校验权限点，返回 false 表示已写出响应（调用方应直接返回）。
//
// 语义要点：authStore 仅在 auth.enabled=true 时被注入（见 cmd/server/main.go），
// 因此 a.authStore == nil 即表示当前未启用登录认证——此时业务接口一律放行，
// 与开启认证前的行为保持一致（等效超级管理员），避免升级后锁死既有部署。
func (a *API) checkPerm(w http.ResponseWriter, r *http.Request, perm string) bool {
	if a.authStore == nil {
		return true
	}
	p := Principal(r)
	if p == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已失效"})
		return false
	}
	if !p.HasPermission(perm) {
		RecordPermissionDenied(a.audit, r, p.Username, perm)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权限执行该操作", "permission": perm})
		return false
	}
	return true
}

// permit 是业务接口的权限包装器：只校验权限点，不做资源范围判断。
//
// 与 authz 的差别在于「未启用登录认证」时的语义：
//   - authz（权限管理接口）：未启用多用户存储时返回 503——无认证即无权限管理；
//   - permit（业务接口）：未启用登录认证时直接放行——见 checkPerm 的说明。
func (a *API) permit(next http.HandlerFunc, perm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.checkPerm(w, r, perm) {
			return
		}
		next(w, r)
	}
}

// denyScope 写出资源范围拒绝响应（含审计），始终返回 false 便于与校验函数串用。
func (a *API) denyScope(w http.ResponseWriter, r *http.Request, perm, group string) bool {
	if p := Principal(r); p != nil {
		RecordPermissionDenied(a.audit, r, p.Username, perm)
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":      "无权访问该节点分组",
		"permission": perm,
		"group":      group,
	})
	return false
}

// checkNodeScope 校验单个节点是否落在当前用户的资源范围内；返回 false 表示已写出响应。
// 节点不存在时不在此处拦截（交由 handler 返回 404），避免用 403 泄露资源存在性。
func (a *API) checkNodeScope(w http.ResponseWriter, r *http.Request, perm, nodeName string) bool {
	p := Principal(r)
	if p == nil || p.Scope.IsGlobal() || nodeName == "" || a.nodeMgr == nil {
		return true
	}
	nd, ok := a.nodeMgr.GetNode(nodeName)
	if !ok {
		return true
	}
	if p.CanAccessGroup(nd.Group) {
		return true
	}
	return a.denyScope(w, r, perm, nd.Group)
}

// permitNode 在 permit 之上追加节点资源范围校验。
// 节点名优先取路径参数 {name}，其次取查询参数 node（如 /query/range?node=）；
// 两者都没有时视为列表类接口，范围过滤由 handler 用 auth.FilterByGroup 完成。
func (a *API) permitNode(next http.HandlerFunc, perm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.checkPerm(w, r, perm) {
			return
		}
		name := param(r, "name")
		if name == "" {
			// 部分路由用 {node} 作为路径参数（如 /security/defense/tasks/{node}）
			name = param(r, "node")
		}
		if name == "" {
			name = r.URL.Query().Get("node")
		}
		if !a.checkNodeScope(w, r, perm, name) {
			return
		}
		next(w, r)
	}
}

// authz 是 handler 级授权包装器：校验登录用户是否拥有 perm 权限点。
// 未启用多用户存储（单管理员/认证关闭）时，权限管理不可用，返回 503。
func (a *API) authz(next http.HandlerFunc, perm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "未启用登录认证，不支持多用户权限管理"})
			return
		}
		p := Principal(r)
		if p == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或会话已失效"})
			return
		}
		if !p.HasPermission(perm) {
			RecordPermissionDenied(a.audit, r, p.Username, perm)
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "无权限执行该操作", "permission": perm})
			return
		}
		next(w, r)
	}
}

// handleLogin POST /api/v1/login {username, password} -> {token}
func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求格式错误"})
		return
	}
	if !a.auth.Enabled {
		// 未启用认证，返回占位 token
		writeJSON(w, 200, map[string]interface{}{"token": "", "authEnabled": false})
		return
	}
	ip := clientIP(r)
	if !loginAllowed(ip) {
		a.recordLoginAudit(r, "", http.StatusTooManyRequests, false)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "登录失败次数过多，请稍后再试"})
		return
	}
	// 多用户模式：从用户存储校验（密码强度/状态/会话版本统一处理）。
	if a.authStore != nil {
		p, ok := a.authStore.VerifyPassword(body.Username, body.Password)
		if !ok {
			a.recordLoginAudit(r, "", http.StatusUnauthorized, false)
			loginFail(ip)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "用户名或密码错误"})
			return
		}
		a.authStore.TouchLogin(body.Username, ip)
		tok := genToken(p.Username, p.TokenVersion, a.auth.Secret)
		http.SetCookie(w, &http.Cookie{
			Name: "nebula_token", Value: tok, Path: "/", HttpOnly: true,
			MaxAge: int(tokenTTL.Seconds()),
		})
		a.recordLoginAudit(r, body.Username, http.StatusOK, true)
		writeJSON(w, 200, map[string]interface{}{"token": tok, "username": p.Username, "authEnabled": true})
		return
	}
	// 单管理员兼容模式（未启用多用户存储）：沿用 server.yaml 中 auth.username/password。
	userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(a.auth.Username)) == 1
	// 密码校验走 servercrypto.VerifyPassword：优先国密 SM3 哈希比对，
	// 对未迁移的旧明文配置自动按明文常量时间比较兜底（与启动时迁移互补）。
	passOK := servercrypto.VerifyPassword(a.auth.Password, body.Password)
	if !userOK || !passOK {
		a.recordLoginAudit(r, "", http.StatusUnauthorized, false)
		loginFail(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "用户名或密码错误"})
		return
	}
	tok := genToken(body.Username, 0, a.auth.Secret)
	// 同时设置 cookie（方便浏览器直接访问）
	http.SetCookie(w, &http.Cookie{
		Name:     "nebula_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(tokenTTL.Seconds()),
	})
	a.recordLoginAudit(r, body.Username, http.StatusOK, true)
	writeJSON(w, 200, map[string]interface{}{"token": tok, "username": body.Username, "authEnabled": true})
}

func (a *API) recordLoginAudit(r *http.Request, user string, status int, succeeded bool) {
	RecordLoginAudit(a.audit, r, user, status, succeeded)
}

// handleLogout 注销。多用户模式下使当前会话失效（TokenVersion++），前端同时清 token。
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
	if a.authStore != nil {
		if p := Principal(r); p != nil {
			_ = a.authStore.BumpTokenVersion(p.Username) // 仅 bump 版本，不重置密码
		}
	}
	http.SetCookie(w, &http.Cookie{Name: "nebula_token", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleAuthInfo 返回是否启用认证（前端用于决定是否显示登录页）
func (a *API) handleAuthInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{"authEnabled": a.auth.Enabled})
}

// changePasswordRequest 修改密码请求体。
type changePasswordRequest struct {
	OldPassword string `json:"oldPassword"`
	NewPassword string `json:"newPassword"`
}

// handleChangePassword 修改登录密码。要求已登录（authRequired 中间件保证）。
// 校验旧密码 -> 国密 SM3 哈希新密码 -> 更新内存并持久化到 server.yaml。
func (a *API) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !a.auth.Enabled {
		writeJSON(w, 400, map[string]string{"error": "未启用登录认证，无需修改密码"})
		return
	}
	var body changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, 400, map[string]string{"error": "请求体解析失败"})
		return
	}
	if body.NewPassword == "" {
		writeJSON(w, 400, map[string]string{"error": "新密码不能为空"})
		return
	}
	user := AuthenticatedUser(r)
	if a.authStore != nil {
		// 多用户模式：校验原密码后重置，重置自动 bump 会话版本使旧 token 失效。
		if _, ok := a.authStore.VerifyPassword(user, body.OldPassword); !ok {
			writeJSON(w, 401, map[string]string{"error": "原密码不正确"})
			return
		}
		if err := a.authStore.ResetPassword(user, body.NewPassword); err != nil {
			writeJSON(w, 400, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"ok": "true"})
		return
	}
	if !servercrypto.VerifyPassword(a.auth.Password, body.OldPassword) {
		writeJSON(w, 401, map[string]string{"error": "原密码不正确"})
		return
	}
	hashed, err := servercrypto.HashPassword(body.NewPassword)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "密码加密失败"})
		return
	}
	a.auth.Password = hashed
	if err := config.PatchAuthPassword(a.configPath, hashed); err != nil {
		slog.Error("持久化新密码失败", "err", err)
		writeJSON(w, 500, map[string]string{"error": "保存失败，请检查服务端配置写入权限"})
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// 简易 int 转 string（避免引入 strconv）
func itoa(i int64) string {
	return strings.TrimSpace(formatInt(i))
}

func atoi(s string) (int64, error) {
	var i int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errInvalid
		}
		i = i*10 + int64(c-'0')
	}
	return i, nil
}

// errInvalid 表示字符串转数字失败的错误实例。
var errInvalid = &invalidErr{}

type invalidErr struct{}

func (e *invalidErr) Error() string { return "invalid number" }

func formatInt(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
