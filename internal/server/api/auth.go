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

// isPublicPath 判断该请求是否无需登录 token。
//
// 参数是**请求**而不是路径，因为「Agent 上行」与「浏览器接口」可能共用同一路径：
// `/api/v1/logs` 的 POST 是 Agent 上行（走 X-Agent-Secret），GET 是浏览器检索（走登录 + logs:read）。
//
// 这一点必须按方法区分：本函数为 true 时中间件**直接放行且不解析 token**，请求里没有 Principal，
// 于是 permit 会因 p == nil 直接 401（浏览器永远查不了），依赖 Principal 的资源范围过滤也会静默失效。
// 早期版本按「路径前缀」放行，正是踩了这个坑。
func isPublicPath(r *http.Request) bool {
	path := r.URL.Path
	if path == "/" || strings.HasPrefix(path, "/assets/") {
		return true
	}
	// 登录接口，以及 Agent→Server 的上行接口（**仅 POST**：Agent 没有登录会话，走 X-Agent-Secret）。
	// 新增任何 Agent 上行接口都要加到这里；漏加的后果是「启用登录认证后该接口 401」，
	// 而本机直连测试通常没开登录认证，因此只在生产才暴露。
	if strings.HasPrefix(path, "/api/v1/login") {
		return true
	}
	if r.Method == http.MethodPost && (path == "/api/v1/report" || path == "/api/v1/logs") {
		return true
	}
	// 对外状态页（C3）：免登录是它的设计目的。**只放行 GET**，
	// 与 /api/v1/logs 同理——同一路径的其它方法不应被顺带放行。
	if r.Method == http.MethodGet && path == "/api/v1/status" {
		return true
	}
	// Agent 安装脚本的接入鉴权预检（同样走 X-Agent-Secret，不受登录 token 影响）
	if strings.HasPrefix(path, "/api/v1/agent/check") {
		return true
	}
	if strings.HasPrefix(path, "/install/") || strings.HasPrefix(path, "/bin/") {
		return true
	}
	// 健康探针：探针通常无法携带登录令牌，且仅暴露「存活 / 就绪」这类非敏感信息。
	if path == "/healthz" || path == "/readyz" {
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
		if isPublicPath(r) {
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

// checkNodeTarget 校验「查询参数」形式的节点目标（?node=、?hostname=、query/range 等的
// labels.node），fail-closed：受限身份下未注册节点（nodeGroup 为空）与已注册但范围外节点
// 一律 403（denyScope，写审计）。
//
// 与 checkNodeScope 的边界必须分清楚：checkNodeScope 服务于**路径参数**目标（{name}/{node}，
// 如 /nodes/{name}、/security/defense/status/{node}），这类路由在节点不存在时应交给 handler
// 返回 404，用 403 拦截反而会向受限用户泄露「资源是否存在」；而查询参数目标的 handler
// 通常没有 404 分支（不带 node 时是列表/跨节点语义，带了未注册 node 只会静默返回空结果），
// 必须在授权层就 fail-closed，否则「200 空结果 vs 403」本身就成了一个判别节点是否已注册的
// oracle。调用方按参数来源选择：路径参数用 checkNodeScope，查询参数用 checkNodeTarget。
func (a *API) checkNodeTarget(w http.ResponseWriter, r *http.Request, perm, name string) bool {
	if name == "" {
		return true
	}
	p := Principal(r)
	if p == nil || p.Scope.IsGlobal() {
		return true
	}
	group := a.nodeGroup(name)
	if group == "" || !p.CanAccessGroup(group) {
		return a.denyScope(w, r, perm, group)
	}
	return true
}

// checkNodeScope 校验单个节点是否落在当前用户的资源范围内；返回 false 表示已写出响应。
// 节点不存在时不在此处拦截，但「交由 handler 返回 404」的前提只对**确有 404 分支**的路径
// 参数路由成立（如 /nodes/{name}、/analysis/hosts/{name}）：这些路由未注册节点会被 handler
// 判定为 404，用 403 拦截反而泄露资源存在性。而 DELETE /nodes/{name}、
// /security/defense/status|tasks/{node} 三条路径参数路由对未注册节点返回 200，因此
// 「200 vs 403」仍构成一个节点名存在性探测面（无数据泄露，属后续跟进项）。
// 仅用于**路径参数**形式的节点目标；查询参数目标见 checkNodeTarget。
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
//
// 节点目标可能同时来自路径参数 {name} / {node} 与查询参数 node / hostname：
//   - 设计 §3 要求「带多个节点候选参数而值矛盾时拒绝请求，不用优先级选择绕过检查」，
//     因此先对所有候选做一致性判定——任意两个非空候选值不一致即 400（同值不算冲突），
//     这一步在路径参数分派之前完成，`/nodes/web-01?node=db-01` 之类不会被路径参数静默吞掉；
//   - 路径参数命中时是「单节点详情」语义，未注册节点对**确有 404 分支**的路由
//     （如 /nodes/{name}、/analysis/hosts/{name}）交给 handler 返回 404（checkNodeScope）；
//     但 DELETE /nodes/{name}、/security/defense/status|tasks/{node} 这三条路径参数路由
//     对未注册节点返回 200，存在 200 vs 403 的节点名存在性探测面（无数据泄露，属后续跟进项）；
//   - 只有查询参数目标时（如 /alerts?node=/?hostname=），handler 没有 404 分支，必须
//     fail-closed（checkNodeTarget），否则「200 空结果 vs 403」会泄露节点是否已注册；
//   - 无法解析出任何目标（如列表类接口）时范围过滤交给 handler 完成。
//
// 查询参数 node / hostname 互为别名，Get 只取首个值，与 handler 读取方式一致
// （多值重复参数不在此单独判定冲突，由 handler 侧同样取首个值，目标必然一致）。
func (a *API) permitNode(next http.HandlerFunc, perm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.checkPerm(w, r, perm) {
			return
		}
		// 路径参数候选：{name} 优先于 {node}（同一路由不会两者都有）。
		pathName := param(r, "name")
		if pathName == "" {
			pathName = param(r, "node")
		}
		// 查询参数候选：node / hostname 互为别名。
		q := r.URL.Query()
		queryName := q.Get("node")
		if hostname := q.Get("hostname"); hostname != "" {
			if queryName != "" && queryName != hostname {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点参数冲突"})
				return
			}
			queryName = hostname
		}
		// 路径参数与查询参数之间同样不得矛盾。
		if pathName != "" && queryName != "" && pathName != queryName {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点参数冲突"})
			return
		}
		if pathName != "" {
			if !a.checkNodeScope(w, r, perm, pathName) {
				return
			}
			next(w, r)
			return
		}
		if !a.checkNodeTarget(w, r, perm, queryName) {
			return
		}
		next(w, r)
	}
}

// permitHostname 是以 hostname 为唯一查询目标的业务接口（进程/监听端口/防火墙等）的权限包装器。
//
// 这些接口的实际查询目标就是 hostname，且没有可退化的「列表类接口」语义——因此必须要求
// hostname 非空，并交给 checkNodeTarget 做 fail-closed 校验（受限身份访问未注册节点直接拒绝，
// 不能像路径参数路由那样把「节点不存在」交给 handler 返回 404，那会让受限用户绕过资源范围
// 探测到未注册节点数据）。全局身份与未启用认证维持原有放行行为。
func (a *API) permitHostname(next http.HandlerFunc, perm string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.checkPerm(w, r, perm) {
			return
		}
		q := r.URL.Query()
		// Get 只取首个值：包装器与 handler 同用 Query().Get，重复参数时两者取到的目标必然一致，
		// 无需为 ?hostname=a&hostname=b 这类情况单独判定冲突。
		hostname := q.Get("hostname")
		if node := q.Get("node"); node != "" && node != hostname {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "节点参数冲突"})
			return
		}
		if hostname == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "hostname 不能为空"})
			return
		}
		if !a.checkNodeTarget(w, r, perm, hostname) {
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
