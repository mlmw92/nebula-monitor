package api

import (
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

	"github.com/nebula/monitor/internal/server/config"
	servercrypto "github.com/nebula/monitor/internal/server/crypto"
)

// 登录失败限流：每个源 IP 在窗口内最多允许 loginLimitMax 次失败，超出返回 429。
var (
	loginMu       sync.Mutex
	loginFails    = map[string]*loginAttempt{}
	loginLimitMax = 5
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

// 生成无状态 token：base64(username:exp) + "." + hmacSig
func genToken(username, secret string) string {
	exp := time.Now().Add(tokenTTL).Unix()
	payload := username + ":" + itoa(exp)
	sig := hmacSign(secret, payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig
}

// 校验 token，返回 username
func verifyToken(token, secret string) (string, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", false
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(hmacSign(secret, payload)), []byte(parts[1])) {
		return "", false
	}
	idx := strings.LastIndex(payload, ":")
	if idx < 0 {
		return "", false
	}
	user := payload[:idx]
	exp, err := atoi(payload[idx+1:])
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return user, true
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
	if strings.HasPrefix(path, "/ws") {
		return true
	}
	return false
}

// AuthMiddleware 校验 token；未启用 auth 则全放行
func AuthMiddleware(next http.Handler, authCfg config.AuthConfig) http.Handler {
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
		if _, ok := verifyToken(tok, authCfg.Secret); !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "未登录或登录已过期"})
			return
		}
		next.ServeHTTP(w, r)
	})
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "登录失败次数过多，请稍后再试"})
		return
	}
	userOK := subtle.ConstantTimeCompare([]byte(body.Username), []byte(a.auth.Username)) == 1
	// 密码校验走 servercrypto.VerifyPassword：优先国密 SM3 哈希比对，
	// 对未迁移的旧明文配置自动按明文常量时间比较兜底（与启动时迁移互补）。
	passOK := servercrypto.VerifyPassword(a.auth.Password, body.Password)
	if !userOK || !passOK {
		loginFail(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "用户名或密码错误"})
		return
	}
	tok := genToken(body.Username, a.auth.Secret)
	// 同时设置 cookie（方便浏览器直接访问）
	http.SetCookie(w, &http.Cookie{
		Name:     "nebula_token",
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(tokenTTL.Seconds()),
	})
	writeJSON(w, 200, map[string]interface{}{"token": tok, "username": body.Username, "authEnabled": true})
}

// handleLogout 注销（前端清 token 即可，后端无状态）
func (a *API) handleLogout(w http.ResponseWriter, r *http.Request) {
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
